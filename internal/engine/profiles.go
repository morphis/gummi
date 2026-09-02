package engine

import (
	"sort"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/config"
)

// resolveRole picks the role config and backend name for a feature's
// profile and role. It falls back to the engine's single-model config
// (M1/M2 behavior) when profiles are absent or don't cover the
// profile/role, so a repo without profiles.yaml still works. An empty
// backend means "use the engine's default backend" — agentFor resolves
// that.
func (e *Engine) resolveRole(profileName string, role agent.Role) (config.RoleConfig, string) {
	if rc, ok := e.lookupRole(profileName, role); ok {
		return rc, rc.Backend
	}
	return config.RoleConfig{Model: e.cfg.Model}, ""
}

// lookupRole reports whether profileName's profile (or the default one)
// declares role, and its config if so. It is resolveRole's first half,
// split out because "the role is declared" and "the role resolved to
// something" are different questions and only the caller knows which one
// it is asking. resolveRole conflates them deliberately — an undeclared
// role there means the single-model fallback, which is the right answer
// for a stage role that every profile is expected to cover. A role no
// profile is expected to declare at all needs to tell the two apart.
func (e *Engine) lookupRole(profileName string, role agent.Role) (config.RoleConfig, bool) {
	prof, ok := e.cfg.Profiles.Profiles[profileName]
	if !ok {
		if def := e.cfg.Profiles.Default; def != "" {
			prof, ok = e.cfg.Profiles.Profiles[def]
		}
	}
	if !ok {
		return config.RoleConfig{}, false
	}
	rc, ok := prof[string(role)]
	return rc, ok
}

// resolveBoardRole picks a board session's model and backend as ONE
// decision, which is the whole point of it not being resolveRole.
//
// resolveRole's fallback returns the engine's single default model with
// an EMPTY backend, on the reasonable assumption that a profile covers
// every stage role and so the fallback is only ever reached by a repo
// with no profiles.yaml at all. RoleBoard breaks that assumption: no
// profile declares it (none existed when profiles.yaml was written, and
// requiring one would make the board tab fail on every existing
// workspace), so the fallback is the NORMAL path here, not the edge —
// and it hands back a model from one source and, via agentFor(""), a
// backend from another. A workspace whose default model is gpt-5 and
// whose default agent is claude then gets a claude session told to drive
// gpt-5, which that adapter refuses outright at session start. Model and
// backend have to travel together or they disagree.
//
// So: the board role if a profile has bothered to declare one, else the
// architect's — the closest analogue, being the role that reasons about
// the work rather than editing it, and paired by construction. Failing
// both, nothing at all: an empty model lets the default backend's own
// CLI pick whatever it normally would, which is always something that
// backend can actually drive. That is strictly better than naming a
// model chosen with no idea of who would run it.
func (e *Engine) resolveBoardRole(profileName string) (config.RoleConfig, string) {
	for _, role := range []agent.Role{agent.RoleBoard, agent.RoleArchitect} {
		if rc, ok := e.lookupRole(profileName, role); ok {
			return rc, rc.Backend
		}
	}
	return config.RoleConfig{}, ""
}

// resolveConsultRole picks a consult session's model and backend as one
// decision, exactly the reasoning resolveBoardRole's own doc comment
// gives for RoleBoard: no profile declares RoleConsult (it did not exist
// when profiles.yaml was written), so the fallback — the architect's role,
// the closest analogue for reasoning about a card's work rather than
// editing it — is the normal path here, not the edge. Failing that,
// nothing at all: an empty model lets the card's own profile-resolved
// backend pick whatever it normally would.
func (e *Engine) resolveConsultRole(profileName string) (config.RoleConfig, string) {
	for _, role := range []agent.Role{agent.RoleConsult, agent.RoleArchitect} {
		if rc, ok := e.lookupRole(profileName, role); ok {
			return rc, rc.Backend
		}
	}
	return config.RoleConfig{}, ""
}

// agentFor returns the Agent for the given backend name. An empty name,
// or an unknown backend, resolves to the engine's default agent (the
// entry stored under the "" key in cfg.Agents). Returns nil when the
// engine has no agents at all — a construction-time misconfiguration the
// callers already guard against with their own nil checks.
func (e *Engine) agentFor(backend string) agent.Agent {
	if backend != "" {
		if a, ok := e.cfg.Agents[backend]; ok {
			return a
		}
	}
	return e.cfg.Agents[""]
}

// defaultAgent returns the engine's default backend, or nil when none is
// configured. It exists because a handful of engine-owned sessions
// (discovery, ingest, estimate) don't run under a profile role and use
// the default directly.
func (e *Engine) defaultAgent() agent.Agent { return e.agentFor("") }

// BoardProfile is one profile entry in the board's inline profile
// picker: the name a user picks, and the backend/model resolveBoardRole
// actually resolves it to — not the raw role config, since a profile
// that never declared a board role at all borrows the architect's (see
// resolveBoardRole's doc comment), and the picker needs to show what
// will really run when it's picked, not what the yaml literally says
// under "board:".
type BoardProfile struct {
	Name    string
	Backend string
	Model   string
}

// BoardProfiles lists every declared profile for the board's picker, in
// config.Profiles.Names order (the declared default first, the rest
// sorted). It reuses Names rather than re-deriving that ordering here —
// a duplicate sort is a second place for the new-feature form's ordering
// rule and the board picker's to quietly disagree the next time one of
// them changes. Each entry's Backend/Model comes from resolveBoardRole,
// not the profile map directly, for the reason BoardProfile's own
// comment gives. An empty Backend coming back from resolveBoardRole is
// reported empty, not papered over with a placeholder string — wording
// "use the engine's default" is a UI decision, not this package's to
// make.
//
// Nil-safe: an engine with no profiles.yaml has an empty
// cfg.Profiles.Profiles; Names() then returns nil and so does this.
func (e *Engine) BoardProfiles() []BoardProfile {
	names := e.cfg.Profiles.Names()
	if len(names) == 0 {
		return nil
	}
	out := make([]BoardProfile, 0, len(names))
	for _, name := range names {
		rc, backend := e.resolveBoardRole(name)
		out = append(out, BoardProfile{Name: name, Backend: backend, Model: rc.Model})
	}
	return out
}

// KnownModel is one model value found somewhere in profiles.yaml, plus
// every "<profile> · <role>" pairing that named it — a memory aid for
// the board's model picker, not a registry. Uses is what lets the picker
// explain WHY a value is offered instead of presenting it out of
// nowhere.
type KnownModel struct {
	Model string
	Uses  []string
}

// KnownModels harvests every distinct model named anywhere in
// cfg.Profiles.Profiles, sorted, each paired with its (also sorted)
// uses — deterministic order twice over, so the picker's list doesn't
// reshuffle between opens on nothing but Go's map iteration order.
//
// There is deliberately no hardcoded model registry behind this. gummi
// has no fixed notion of which model names are valid for any backend —
// RoleConfig.Model is an opaque string forwarded verbatim everywhere
// else in this codebase — and a baked-in list would go stale the week a
// provider ships something, leaving a picker that offers only names
// nobody wants. The only models worth surfacing are the ones this
// workspace has already asked a backend to run.
//
// Nil-safe: an engine with no profiles.yaml has an empty
// cfg.Profiles.Profiles and this returns nil.
func (e *Engine) KnownModels() []KnownModel {
	uses := map[string][]string{}
	for profile, roles := range e.cfg.Profiles.Profiles {
		for role, rc := range roles {
			if rc.Model == "" {
				continue
			}
			uses[rc.Model] = append(uses[rc.Model], profile+" · "+role)
		}
	}
	if len(uses) == 0 {
		return nil
	}
	models := make([]string, 0, len(uses))
	for m := range uses {
		models = append(models, m)
	}
	sort.Strings(models)
	out := make([]KnownModel, 0, len(models))
	for _, m := range models {
		u := uses[m]
		sort.Strings(u)
		out = append(out, KnownModel{Model: m, Uses: u})
	}
	return out
}
