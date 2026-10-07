package ui

import (
	"context"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/agentcli"
	"github.com/morphis/gummi/internal/cardmint"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/webapi"
)

// A session is a freeform card that runs on the agent and model its person
// picked (DESIGN §19.8). This file is the Shell's half: what the web face's
// model picker offers, which pair a card's session runs on, and the switch.

// sessionRecentMax bounds the picker's Recent list: it is a shortcut to the
// few pairs in use, not a history.
const sessionRecentMax = 5

// agentInstalled reports whether this host can start the agent named: its
// CLI resolves on PATH, or — headless having no CLI of its own — a command
// line is configured for it. A var so a test can decide without a PATH.
var agentInstalled = func(name string) bool {
	if bin, ok := agentcli.Binary(name); ok {
		_, err := exec.LookPath(bin)
		return err == nil
	}
	if name == "headless" {
		return strings.TrimSpace(os.Getenv("GUMMI_AGENT_CMD")) != ""
	}
	return false
}

// webSessionModels is what a session's model picker offers: every agent a
// session can run on with the models the workspace's profiles already run
// there, the pairs sessions on this board run on now, and what a new
// session gets when nothing is picked. The agent's own catalog — the
// models it says it provides, asked live — is merged into those rows by
// Bridge.Form off the loop; a probe must not run on it.
func (m *Shell) webSessionModels() webapi.SessionModels {
	out := webapi.SessionModels{Agents: []webapi.SessionAgent{}, Recent: []webapi.SessionModel{}}
	var suggest map[string][]string
	if m.engine != nil {
		suggest = m.engine.SessionSuggestions()
		b, model := m.engine.SessionModel(domain.Feature{Kind: domain.KindFreeform, Profile: m.defaultProfile()})
		out.Default = webapi.SessionModel{Backend: b, Model: model}
	}
	for _, name := range engine.SessionBackends {
		needs, hint, pattern := engine.SessionModelRule(name)
		caps, _ := agent.CapabilitiesFor(name)
		out.Agents = append(out.Agents, webapi.SessionAgent{
			Name:       name,
			Installed:  (m.engine != nil && m.engine.HasAgent(name)) || agentInstalled(name),
			Models:     append([]string{}, suggest[name]...),
			NeedsModel: needs, Hint: hint, Pattern: pattern,
			Images: caps.Images,
		})
	}
	var named []domain.Feature
	for _, r := range m.rows {
		if r.F.IsFreeform() && (r.F.SessionBackend != "" || r.F.SessionModel != "") {
			named = append(named, r.F)
		}
	}
	slices.SortStableFunc(named, func(a, b domain.Feature) int { return b.UpdatedAt.Compare(a.UpdatedAt) })
	for _, f := range named {
		p := webapi.SessionModel{Backend: f.SessionBackend, Model: f.SessionModel}
		if !slices.Contains(out.Recent, p) {
			out.Recent = append(out.Recent, p)
		}
		if len(out.Recent) == sessionRecentMax {
			break
		}
	}
	return out
}

// catalogWait is how long a read of the picker's catalog waits for an
// agent's own list of models before it goes without it: long enough for an
// adapter that answers from memory, short of a CLI started cold.
const catalogWait = 1500 * time.Millisecond

// mergeSessionCatalog replaces each installed agent's inline suggestions
// with the merged view (SessionModelChoices) — the agent's own catalog
// where it can say one, plus the workspace's profile ids. The merge asks
// a backend and possibly runs its CLI, so it happens off the loop; the
// loop builds the same rows inline from SessionSuggestions alone. Every
// endpoint that ships the picker's catalog runs this one merge, so no
// face can see a catalog the others do not. The agents are asked at
// once: each may run its own CLI, and a first read uncached would
// otherwise wait for every one of them in turn. Nor does a read wait long
// for any of them: an agent whose catalog is not in by catalogWait shows
// the workspace's profile ids for now, while its probe goes on and caches
// what it finds for the next read.
func mergeSessionCatalog(ctx context.Context, eng *engine.Engine, sm *webapi.SessionModels) {
	if eng == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, catalogWait)
	defer cancel()
	var wg sync.WaitGroup
	for i := range sm.Agents {
		a := &sm.Agents[i]
		if !a.Installed {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.Models = eng.SessionModelChoices(ctx, a.Name)
			if a.Models == nil {
				// the contract spells an empty list as [], not null
				a.Models = []string{}
			}
		}()
	}
	wg.Wait()
}

// defaultProfile is the profile a card minted with none resolves under:
// the declared default, which is also what the new-card form preselects.
func (m *Shell) defaultProfile() string {
	if len(m.profileNames) > 0 {
		return m.profileNames[0]
	}
	return ""
}

// webSessionOf is the agent and model a card's session runs on, as its next
// turn will resolve them; nil for a card in the workflow.
func (m *Shell) webSessionOf(f domain.Feature) *webapi.SessionModel {
	if !f.IsFreeform() || m.engine == nil {
		return nil
	}
	b, model := m.engine.SessionModel(f)
	return &webapi.SessionModel{Backend: b, Model: model}
}

// checkSessionPick refuses a backend/model pair before anything is minted
// or switched: a pair the engine refuses, and an agent this host cannot
// start. Empty is no pick at all, which is always fine.
func (m *Shell) checkSessionPick(backend, model string) string {
	if backend == "" && strings.TrimSpace(model) == "" {
		return ""
	}
	if backend == "" {
		return "say which agent runs " + strings.TrimSpace(model)
	}
	if err := engine.CheckSessionModel(backend, model); err != nil {
		return err.Error()
	}
	if (m.engine == nil || !m.engine.HasAgent(backend)) && !agentInstalled(backend) {
		return backend + " is not installed on this host"
	}
	return ""
}

// switchSessionModel moves a card's session to another agent and model,
// off the loop: a live backend is stopped first, its worktree left as it is.
func (m *Shell) switchSessionModel(id domain.FeatureID, backend, model string) tea.Cmd {
	eng := m.engine
	return func() tea.Msg {
		if err := eng.SwitchSessionModel(context.Background(), id, backend, model); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true, id: id}
		}
		label := strings.TrimSpace(model)
		if label == "" {
			label = backend + "'s default model"
		} else {
			label += " · " + backend
		}
		return sessionSwitchedMsg{id: id, text: string(id) + " now runs on " + label}
	}
}

// commitSession commits a session's worktree with the person's message, off
// the loop. The rows reload so the branch's new head is what the board shows.
func (m *Shell) commitSession(id domain.FeatureID, message string) tea.Cmd {
	eng := m.engine
	return func() tea.Msg {
		committed, err := eng.CommitFreeform(context.Background(), id, message)
		if err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true, id: id}
		}
		if !committed {
			return noticeMsg{text: string(id) + " has nothing to commit", id: id}
		}
		return noticeMsg{text: string(id) + ": committed", reload: true, id: id}
	}
}

// sessionSwitchedMsg settles a model switch: the rows reload, since the
// card row is what carries the pair, and the notice says what runs now.
type sessionSwitchedMsg struct {
	id   domain.FeatureID
	text string
}

// specFromSession ends a session and continues its work as a feature card
// (DESIGN §19.8, "write a spec"). The session is handed off — its branch
// kept, and refused while its worktree holds work nobody committed, since
// nothing here commits it — and a feature is minted with the brief
// the writespec dialog handed in: the person's edited text, fetched once
// when the dialog opened (the session's own words, or the assembled draft
// when no live session could answer), never re-derived here at mint. The
// branch is cut from the session's tip, so the plan stage starts from the
// work rather than from main, and its plan stage runs at once.
//
// The feature does not adopt the session's branch. That would make one
// branch two cards', and deleting the closed session would take the
// spec's work with it; a branch of its own is gummi's to rebase, land and
// clean exactly like any feature's.
func (m *Shell) specFromSession(f domain.Feature, title, brief, profile string, envelope int) tea.Cmd {
	eng, pool, actor := m.engine, m.wt, m.humanActor()
	return func() tea.Msg {
		ctx := context.Background()
		if _, diffOpen, _, err := eng.GateBlockers(ctx, f.ID); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true, id: f.ID}
		} else if diffOpen > 0 {
			them := "them"
			if diffOpen == 1 {
				them = "it"
			}
			return noticeMsg{text: string(f.ID) + " has " + itoa(diffOpen) + " open diff comment" + plural(diffOpen) + " — send or resolve " + them + " before writing a spec from it", isErr: true, id: f.ID}
		}
		// a turn in flight is still writing into the worktree the next
		// check reads, and closing the session below would cut it off
		if ff := eng.Freeform(f.ID); ff != nil && ff.Busy() {
			return noticeMsg{text: string(f.ID) + " is mid-turn; write the spec once this turn ends", isErr: true, id: f.ID}
		}
		// the spec continues from the session's last commit, and nothing
		// here commits for it: loose work would be left behind
		if err := eng.WritespecRefusal(ctx, f.ID); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true, id: f.ID}
		}
		// the session's backend and lock go first: the hand-off commits the
		// worktree, and nothing may still be writing into it
		if ff := eng.Freeform(f.ID); ff != nil {
			_ = ff.Close()
		}
		release, err := m.locks.Acquire(f.ID)
		if err != nil {
			return noticeMsg{text: cardLockedNotice(f.ID, err), isErr: true, id: f.ID}
		}
		res, err := eng.HandOff(ctx, f.ID, actor)
		release()
		if err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true, id: f.ID}
		}
		if res.Status != engine.StatusAdvanced {
			return noticeMsg{text: string(f.ID) + " could not be handed off to a spec", isErr: true, id: f.ID}
		}
		head, err := pool.Head(ctx, &f)
		if err != nil {
			return noticeMsg{text: string(f.ID) + " was handed off, but its branch could not be read: " + sanitize(err.Error()), isErr: true, id: f.ID}
		}
		spec, err := cardmint.Mint(ctx, m.store, m.ws, cardmint.Input{
			Kind: domain.KindFeature, Description: specBrief(f, title, head, brief),
			Profile: profile, Envelope: envelope, Repo: f.Repo, RequireRepo: m.requireRepo, Base: f.Base,
			Source: "manual",
		})
		if err != nil {
			return noticeMsg{text: string(f.ID) + " was handed off, but the spec was not created: " + sanitize(err.Error()), isErr: true, id: f.ID}
		}
		if _, err := pool.CreateFrom(ctx, &spec, head); err != nil {
			// a spec card whose branch was not cut from the session would
			// start its plan from the base, as if the work never happened
			_ = m.store.DeleteFeature(ctx, spec.ID)
			return noticeMsg{text: string(f.ID) + " was handed off, but the spec's branch could not be cut from " + f.BranchName() + ": " + sanitize(err.Error()), isErr: true, id: f.ID}
		}
		// the session's work now lands through the spec, so the session
		// must not land it a second way (domain.Feature.MayLandAfterAll)
		if err := m.store.SetContinuedAs(ctx, f.ID, spec.ID); err != nil {
			return noticeMsg{text: string(spec.ID) + " was created, but " + string(f.ID) + " could not record it: " + sanitize(err.Error()), isErr: true, id: f.ID}
		}
		// into the plan stage, where the architect is what runs: todo runs
		// no agent, and a spec written from a session has nothing to wait
		// for in a backlog
		adv, err := eng.Advance(ctx, spec.ID, actor)
		if err != nil || adv.Status != engine.StatusAdvanced {
			eng.NoteClosedFreeform(f.ID, "Continued as the spec "+string(spec.ID)+" ("+spec.Title+").")
			return cardCreatedMsg{f: spec, open: true}
		}
		eng.NoteClosedFreeform(f.ID, "Continued as the spec "+string(adv.Feature.ID)+" ("+adv.Feature.Title+"), on its own branch "+adv.Feature.BranchName()+" cut from this one.")
		return cardCreatedMsg{f: adv.Feature, open: true, run: true}
	}
}

// specBrief is the spec card's description: its title, where its work came
// from, and the handoff brief the writespec dialog handed in — the brief
// the session wrote, or the assembled draft, as the person edited it.
func specBrief(f domain.Feature, title, head, brief string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		title = f.Title
	}
	short := head
	if len(short) > 7 {
		short = short[:7]
	}
	var b strings.Builder
	b.WriteString(title)
	if f.MainCheckout {
		// The session ran in the main checkout and left its work uncommitted
		// there; the branch this spec walks starts from the checkout's HEAD
		// and carries none of the loose work — the architect has to know
		// which half of the checkout it is standing on.
		b.WriteString("\n\nContinued from the session " + string(f.ID) + " (" + f.Title + "). That session ran in the main checkout and left its work uncommitted there; this card's branch is cut from the checkout's HEAD at " + short + " and carries none of the loose work: read the checkout before designing, and plan what remains rather than what is done.")
	} else {
		b.WriteString("\n\nContinued from the session " + string(f.ID) + " (" + f.Title + "). Its work so far is already on this card's branch, cut from " + f.BranchName() + " at " + short + ": read the diff before designing, and plan what remains rather than what is done.")
	}
	if strings.TrimSpace(brief) != "" {
		b.WriteString("\n\n" + strings.TrimSpace(brief))
	}
	return b.String()
}

// WarmModelCatalogs starts asking every agent a session could run on here
// for its own catalog, so the first form or picker that needs one finds it
// cached rather than waiting while each agent's CLI starts to answer.
func (b *Bridge) WarmModelCatalogs() {
	eng := b.shell.engine
	if eng == nil {
		return
	}
	var names []string
	for _, name := range engine.SessionBackends {
		if eng.HasAgent(name) || agentInstalled(name) {
			names = append(names, name)
		}
	}
	eng.WarmModelCatalogs(names...)
}
