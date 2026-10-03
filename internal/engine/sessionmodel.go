package engine

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/domain"
)

// A session — a freeform card — runs on the agent and model the person
// chose for it, not on a profile role (DESIGN §19.8). Everything here is
// the engine's half of that: which role a session resolves to, starting a
// backend the board did not launch, and switching a live session to
// another model with its conversation intact. A card in the workflow never
// reaches any of it: its stages take their agents from its profile, and
// domain.Feature.Validate refuses a stage card that names a session model.

// SessionBackends is every backend name a session may be pointed at. It is
// the adapters startAdapter in cmd/gummi knows how to build; whether one is
// installed on this host is a separate question (agentcli.Detect) that the
// board asks when it offers the list.
var SessionBackends = []string{"claude", "codex", "copilot", "opencode", "pi", "headless"}

// ErrSessionBusy refuses a model switch while the session is mid-turn:
// the turn in flight belongs to the model that started it, and stopping
// its backend under it would lose the reply.
var ErrSessionBusy = errors.New("the session is mid-turn; stop the turn or wait for it before switching models")

// sessionRole is a freeform card's role config and backend: the agent and
// model its person chose when it names them, else the profile's
// implementer exactly as every freeform card resolved before a session
// could choose.
func (e *Engine) sessionRole(f domain.Feature) (config.RoleConfig, string) {
	if f.SessionBackend != "" || f.SessionModel != "" {
		return config.RoleConfig{Model: f.SessionModel, Backend: f.SessionBackend}, f.SessionBackend
	}
	return e.resolveRole(f.Profile, agent.RoleImplementer)
}

// SessionModel reports the backend and model a freeform card's session
// runs on, resolved the way its next spawn will resolve them. An empty
// backend is the board's default agent, named here so the page can show
// what actually runs.
func (e *Engine) SessionModel(f domain.Feature) (backend, model string) {
	rc, b := e.sessionRole(f)
	if b == "" {
		if a := e.agentFor(""); a != nil {
			b = a.Name()
		}
	}
	return b, rc.Model
}

// sessionAgent returns the agent a session runs on, starting it when the
// board did not. The profiles decide which backends a board starts, and a
// session that asks for one no profile names (a person picking codex on a
// board whose profiles only use claude) is what StartAgent exists for.
// Unlike agentFor it never falls back to the default for a name it does
// not know: a session that asked for codex and silently got claude would
// be running on a model nobody chose.
func (e *Engine) sessionAgent(backend string) (agent.Agent, error) {
	if backend == "" {
		if a := e.agentFor(""); a != nil {
			return a, nil
		}
		return nil, errors.New("no agent configured")
	}
	if a, ok := e.cfg.Agents[backend]; ok {
		return a, nil
	}
	e.startedMu.Lock()
	defer e.startedMu.Unlock()
	if a, ok := e.started[backend]; ok {
		return a, nil
	}
	if e.cfg.StartAgent == nil {
		return nil, fmt.Errorf("this board did not start %s and cannot start one", backend)
	}
	a, err := e.cfg.StartAgent(backend)
	if err != nil {
		return nil, fmt.Errorf("starting %s: %w", backend, err)
	}
	if e.started == nil {
		e.started = map[string]agent.Agent{}
	}
	e.started[backend] = a
	return a, nil
}

// knownAgent is sessionAgent without the start: the agent for backend if
// this engine already has one, for readers (a restore, a rate lookup) that
// must not launch a process as a side effect of asking.
func (e *Engine) knownAgent(backend string) agent.Agent {
	if backend == "" {
		return e.agentFor("")
	}
	if a, ok := e.cfg.Agents[backend]; ok {
		return a
	}
	e.startedMu.Lock()
	defer e.startedMu.Unlock()
	return e.started[backend]
}

// closeStartedAgents closes the backends sessionAgent started. The ones in
// cfg.Agents belong to whoever built the engine and are closed there.
func (e *Engine) closeStartedAgents() {
	e.startedMu.Lock()
	started := e.started
	e.started = nil
	e.startedMu.Unlock()
	for _, a := range started {
		_ = a.Close()
	}
}

// CheckSessionModel refuses a backend/model pair a session could not run,
// before anything is stored: an unknown backend, a backend that needs a
// model and got none, and the model ids the claude CLI refuses at session
// start. The adapters would refuse them too, but only when the next turn
// spawned — after the person had typed a message into a session that
// could not answer it.
func CheckSessionModel(backend, model string) error {
	model = strings.TrimSpace(model)
	known := false
	for _, b := range SessionBackends {
		if b == backend {
			known = true
			break
		}
	}
	if !known {
		return fmt.Errorf("unknown agent %q (one of %s)", backend, strings.Join(SessionBackends, ", "))
	}
	switch backend {
	case "opencode", "pi":
		if model == "" {
			return fmt.Errorf("%s needs a model spelled provider/model", backend)
		}
	case "codex":
		if model == "" {
			return errors.New("codex needs a model")
		}
	case "claude":
		if foreign, provider := agent.ForeignModel(model); foreign {
			return fmt.Errorf("the claude CLI only runs Anthropic models, and %s is from %s", model, provider)
		}
		if suggest, bad := agent.ClaudeModelIDHint(model); bad {
			return fmt.Errorf("the claude CLI spells %s as %s", model, suggest)
		}
	}
	return nil
}

// SwitchSessionModel moves a freeform card's session to another agent and
// model. The choice is stored on the card, so it survives a restart and is
// what the next spawn resolves; a live backend is stopped, and the next
// turn respawns on the new model with the conversation replayed to it
// (freeformReplayHint) rather than resumed, since a conversation id belongs
// to the backend and model that kept it.
//
// It refuses a card in the workflow (its stages take their agents from its
// profile), a closed card, a pair CheckSessionModel refuses, and a session
// that is mid-turn (ErrSessionBusy).
func (e *Engine) SwitchSessionModel(ctx context.Context, id domain.FeatureID, backend, model string) error {
	model = strings.TrimSpace(model)
	if err := CheckSessionModel(backend, model); err != nil {
		return err
	}
	e.freeformMu.Lock()
	defer e.freeformMu.Unlock()
	f, err := e.feature(ctx, id)
	if err != nil {
		return err
	}
	if !f.IsFreeform() {
		return fmt.Errorf("%s is a %s card: its stages take their agents from its profile", f.ID, f.Kind)
	}
	if f.Stage == domain.StageDone {
		return fmt.Errorf("%s is closed", f.ID)
	}
	if f.SessionBackend == backend && f.SessionModel == model {
		return nil
	}
	ff := e.Freeform(id)
	if ff != nil && ff.Busy() {
		return ErrSessionBusy
	}
	f.SessionBackend, f.SessionModel = backend, model
	if err := e.cfg.Store.UpdateFeature(ctx, &f); err != nil {
		return err
	}
	if ff == nil {
		return nil
	}
	rc, b := e.sessionRole(f)
	ff.mu.Lock()
	ff.rc, ff.backend = rc, b
	sess := ff.sess
	ff.mu.Unlock()
	if sess == nil {
		return nil
	}
	// The conversation id the old backend kept is not the new one's to
	// resume; dropping it is what makes the next spawn replay instead.
	sess.setAgentSessionID("")
	sess.appendSystem(fmt.Sprintf("Switched to %s on %s. The conversation so far goes with it.", modelLabel(model), backend))
	if sess.Live() {
		// Stopping the backend loses nothing: what it wrote stays in the
		// worktree for the next one, and settle saves the conversation.
		ff.settle()
		sess.setState(StateDone)
		sess.stop()
		ff.dropLock()
	} else {
		e.persist(sess)
	}
	return nil
}

func modelLabel(model string) string {
	if model == "" {
		return "the agent's default model"
	}
	return model
}

// SessionSuggestions is the model ids the workspace's profiles run on each
// backend, sorted, for a session's picker to offer. There is deliberately
// no registry of every model an agent can run: the
// ids a workspace already uses are the ones worth offering, and any other
// may be typed. A role that names no backend runs on the default agent and
// is filed under its name.
func (e *Engine) SessionSuggestions() map[string][]string {
	def := ""
	if a := e.agentFor(""); a != nil {
		def = a.Name()
	}
	seen := map[string]map[string]bool{}
	for _, prof := range e.currentProfiles().Profiles {
		for _, rc := range prof {
			b := rc.Backend
			if b == "" {
				b = def
			}
			if b == "" || rc.Model == "" {
				continue
			}
			if seen[b] == nil {
				seen[b] = map[string]bool{}
			}
			seen[b][rc.Model] = true
		}
	}
	out := make(map[string][]string, len(seen))
	for b, ms := range seen {
		for m := range ms {
			out[b] = append(out[b], m)
		}
		sort.Strings(out[b])
	}
	return out
}

// HasAgent reports whether this engine already holds a backend named name,
// started with the board or since, for a session.
func (e *Engine) HasAgent(name string) bool {
	return name != "" && e.knownAgent(name) != nil
}

// SessionModelRule says whether backend refuses to start without a model
// id, how it spells one, and a pattern a typed id must match (an
// ECMAScript regular expression, case-insensitive, for the page; empty
// takes any id), so the picker offers no pair CheckSessionModel would
// refuse. The pattern is a courtesy, not the check: CheckSessionModel is.
func SessionModelRule(backend string) (needsModel bool, hint, pattern string) {
	switch backend {
	case "opencode":
		return true, "provider/model, e.g. anthropic/claude-sonnet-5-5", `^[^/\s]+/\S+$`
	case "pi":
		return true, "provider/id", `^[^/\s]+/\S+$`
	case "codex":
		return true, "", ""
	case "claude":
		var alts []string
		for _, p := range agent.ForeignModelPrefixes() {
			alts = append(alts, regexp.QuoteMeta(p))
		}
		return false, "versions with dashes, e.g. claude-haiku-4-5; empty is the CLI's default", `^(?!(` + strings.Join(alts, "|") + `))(?!\S*\d\.\d)`
	}
	return false, "", ""
}
