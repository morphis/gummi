package ui

// Stacks on the board. A stack is a chain of cards whose branches fork
// from one another; the board's part is to make one with a single key,
// show the chain, and tick the engine when something that matters
// happened — the same division of labour goals have (goalloop.go): the
// engine decides, the board notices.
//
// Nothing here can stop a card from running. A stack orders landing and
// nothing else, so the board never renders a member as blocked by its
// neighbours.

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/stack"
)

// stackPollInterval re-ticks stacks with nothing else to wake them. A
// stack changes when a card's branch moves, and the board hears about
// that through the engine's events — so this is a backstop for the
// changes it cannot hear, chiefly a person running git by hand or a
// PR merging and main being pulled.
const stackPollInterval = 20 * time.Second

// stackNewCard puts a freshly minted card into a stack: on top of onto
// (creating the stack when onto is not in one yet), or at the top of an
// existing stack. It returns the stack the card ended up in.
//
// Creating the stack here — at the moment a second card is stacked on a
// first — is the whole of the setup. There is no "new stack" dialog to
// find, because a stack of one card is not a thing anybody wants and a
// stack's name only becomes obvious once there is a card to name it
// after.
func (m *Shell) stackNewCard(ctx context.Context, f domain.Feature, onto domain.FeatureID, into domain.StackID) (domain.StackID, error) {
	if into != "" {
		if err := m.store.AddToStack(ctx, into, f.ID, -1); err != nil {
			return "", err
		}
		return into, nil
	}
	base, err := m.store.GetFeature(ctx, onto)
	if err != nil {
		return "", err
	}
	id := base.StackID
	if id == "" {
		// The stack takes its name from the card at the bottom, which is
		// the only name that means anything to the reader at this point.
		st, serr := m.store.StartStack(ctx, base.ID, "", time.Now())
		if serr != nil {
			return "", serr
		}
		id = st.ID
	}
	// Directly above the card it was stacked on, not at the top of the
	// stack: "stack this on FD-101" means exactly that, even when other
	// cards already sit above FD-101.
	if err := m.store.AddToStack(ctx, id, f.ID, base.StackPos+1); err != nil {
		return "", err
	}
	return id, nil
}

// queueStackTick asks for a stack to be ticked once this update
// finishes. Coalesced, so a burst of events over one stack is one tick —
// the shape drainGoalTicks has, for the same reason.
func (m *Shell) queueStackTick(id domain.StackID) {
	if id == "" {
		return
	}
	if m.stackTickQueue == nil {
		m.stackTickQueue = map[domain.StackID]bool{}
	}
	m.stackTickQueue[id] = true
}

// drainStackTicks turns the queued ticks into commands.
func (m *Shell) drainStackTicks() tea.Cmd {
	if len(m.stackTickQueue) == 0 || m.engine == nil {
		m.stackTickQueue = nil
		return nil
	}
	var cmds []tea.Cmd
	for id := range m.stackTickQueue {
		cmds = append(cmds, m.stackTick(id))
	}
	m.stackTickQueue = nil
	return tea.Batch(cmds...)
}

// stackTickMsg carries one tick's outcome back to the update loop.
type stackTickMsg struct {
	res engine.StackTickResult
	err error
}

// stackTick runs one step of a stack in the background.
func (m *Shell) stackTick(id domain.StackID) tea.Cmd {
	eng := m.engine
	if eng == nil {
		return nil
	}
	return func() tea.Msg {
		res, err := eng.StackTick(context.Background(), id)
		return stackTickMsg{res: res, err: err}
	}
}

// onStackTick handles a finished tick: report a conflict through the
// rebase hand-off the package already has, come back round while there
// is more to do, and say what a finished walk moved.
//
// Each replay step is silent — the board's own marker says a card is
// being replayed while it happens, and a notice per step would be a
// stream of things nobody asked for. The walk as a whole is not: every
// branch it moved now differs from its remote, and gummi prints the push
// that needs and never runs it (§18.5). So the tick that finds the stack
// settled after a walk says, once, which cards moved and the push lines
// for them — the same lines `gummi stack restack` prints.
func (m *Shell) onStackTick(msg stackTickMsg) tea.Cmd {
	if msg.err != nil {
		return func() tea.Msg {
			return noticeMsg{text: sanitize("stack: " + msg.err.Error()), isErr: true}
		}
	}
	if ce := msg.res.Conflict; ce != nil {
		id := msg.res.Restacked
		f, err := m.store.GetFeature(context.Background(), id)
		if err != nil {
			return nil
		}
		// The same hand-off a manual rebase gets: an agent session costs
		// credits, so it never starts without a yes.
		return func() tea.Msg { return rebaseConflictMsg{f: f, files: ce.Files} }
	}
	if msg.res.Again {
		id := msg.res.Stack
		return subscription(tea.Tick(400*time.Millisecond, func(time.Time) tea.Msg {
			return stackTickDueMsg{stack: id}
		}))
	}
	if w := msg.res.Settled; w != nil && len(w.Cards) > 0 {
		text := stackReplayNotice(msg.res.Stack, *w)
		return func() tea.Msg { return noticeMsg{text: text, reload: true} }
	}
	if msg.res.Restacked != "" {
		// A replay changed a branch, so the board's git-derived columns
		// (diffstat, landed, ahead) are stale.
		return func() tea.Msg { return noticeMsg{reload: true} }
	}
	return nil
}

// stackReplayNotice is what a finished replay walk says: the cards that
// moved and the push each branch now needs, one per line — the shell
// shows a notice of several lines in the band above the status bar, so
// every push line is on screen and not only the sentence before them.
func stackReplayNotice(id domain.StackID, w engine.StackReplay) string {
	ids := make([]string, 0, len(w.Cards))
	for _, c := range w.Cards {
		ids = append(ids, string(c))
	}
	onto, them := " onto their new base", "them"
	if len(ids) == 1 {
		onto, them = " onto its new base", "it"
	}
	text := "stack " + string(id) + ": replayed " + strings.Join(ids, ", ") + onto
	var push []string
	for _, p := range w.Push {
		if p != "" {
			push = append(push, p)
		}
	}
	if len(push) == 0 {
		return text
	}
	text += " — gummi never pushes; push " + them + " yourself:"
	for _, p := range push {
		text += "\n  " + p
	}
	return text
}

// stackTickDueMsg asks for another tick after a short pause, so a walk
// of several members does not spin the update loop.
type stackTickDueMsg struct{ stack domain.StackID }

// stackRows is the per-card stack annotation the board renders: the
// position, what the card forks from, and whether it is being replayed.
type stackRow struct {
	// ID and Name are the stack the card is in.
	ID   domain.StackID
	Name string
	// Pos is the card's 1-based place, for "2 of 4".
	Pos, Of int
	// Below names the card this one forks from, empty at the bottom.
	Below domain.FeatureID
	// Base is the branch the bottom card forks from.
	Base string
	// Stale marks a card sitting on commits that have moved — it is
	// queued for a replay, or being replayed now.
	Stale bool
	// Landed marks a member whose work has reached the base.
	Landed bool
}

// stackRowsForFeatures builds the annotation for every member of every
// stack the given cards belong to. One snapshot per stack, so a board
// with three stacks asks the engine three times rather than once per row.
func (m *Shell) stackRowsForFeatures(ctx context.Context, feats []domain.Feature) map[domain.FeatureID]stackRow {
	if m.store == nil || m.wt == nil {
		return nil
	}
	eng, release := m.stackEngine()
	if eng == nil {
		return nil
	}
	defer release()
	seen := map[domain.StackID]bool{}
	out := map[domain.FeatureID]stackRow{}
	for _, f := range feats {
		id := f.StackID
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		view, err := eng.StackSnapshot(ctx, id)
		if err != nil {
			continue
		}
		snap := view.Snapshot
		for _, mem := range snap.Members {
			// The declared predecessor, not the currently-resolvable one:
			// a card whose neighbour has not cut its branch yet will
			// still fork from it, and the board should say the chain
			// rather than this instant's git state.
			below, _ := stack.BelowDeclared(snap, mem.ID)
			out[mem.ID] = stackRow{
				ID: snap.ID, Name: snap.Name,
				Pos: mem.Pos + 1, Of: len(snap.Members),
				Below: below, Base: snap.Base,
				Stale: mem.Stale, Landed: mem.Landed,
			}
		}
	}
	return out
}

// stackTag is the board row's stack marker: where the card sits and
// what it forks from, plus a replay marker when it is stale.
func stackTag(r stackRow) string {
	from := "main"
	if r.Base != "" {
		from = r.Base
	}
	if r.Below != "" {
		from = string(r.Below)
	}
	out := fmt.Sprintf("%d/%d ← %s", r.Pos, r.Of, from)
	switch {
	case r.Landed:
		out += " · landed"
	case r.Stale:
		// "↻" and not a word, because this clears itself: the reader is
		// being told what is happening, not asked to do anything.
		out += " · ↻ replaying"
	}
	return out
}

// updateStack answers the stack messages before the main switch sees
// them, the way updateGoal does for goals.
func (m *Shell) updateStack(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case stackTickMsg:
		return m.onStackTick(msg), true
	case stackTickDueMsg:
		return m.stackTick(msg.stack), true
	case stackPollMsg:
		// Wake every stack the board is showing, then arm the next poll.
		// Distinct stacks only: one tick per stack, however many of its
		// members are on screen.
		seen := map[domain.StackID]bool{}
		for _, r := range m.rows {
			if r.F.StackID != "" && !seen[r.F.StackID] {
				seen[r.F.StackID] = true
				m.queueStackTick(r.F.StackID)
			}
		}
		return stackPoll(), true
	}
	return nil, false
}

// stackPollMsg is the periodic wake for changes the board cannot hear
// about through engine events.
type stackPollMsg struct{}

// stackPoll arms the next poll.
func stackPoll() tea.Cmd {
	return subscription(tea.Tick(stackPollInterval, func(time.Time) tea.Msg { return stackPollMsg{} }))
}

// openStackForm opens the new-card dialog preset to stack the card onto
// the selected one. It returns nil when there is nothing to stack on, so
// the key falls through to its own refusal rather than opening a dialog
// that cannot do what it says.
func (m *Shell) openStackForm() tea.Cmd {
	r, ok := m.selected()
	if !ok {
		return func() tea.Msg { return noticeMsg{text: "select a card to stack the new one on", isErr: true} }
	}
	f := r.F
	// The kinds with no branch have nothing for a card to fork from.
	switch f.Kind {
	case domain.KindResearch:
		return func() tea.Msg {
			return noticeMsg{text: string(f.ID) + " is a research card — it has no branch to stack on", isErr: true}
		}
	case domain.KindGoal:
		return func() tea.Msg {
			return noticeMsg{text: string(f.ID) + " is a goal — its own cards already share one branch", isErr: true}
		}
	}
	if f.GoalID != "" {
		return func() tea.Msg {
			return noticeMsg{text: string(f.ID) + " belongs to " + string(f.GoalID) + " — a goal's cards share its branch rather than stacking", isErr: true}
		}
	}
	form := m.openCardForm(domain.CardType{Kind: domain.KindFeature})
	form.stackOn(f.ID, f.Title, "")
	// The new card must live in the same repository as the card it forks
	// from, so the repo row is settled rather than asked.
	form.repo.selectName(f.Repo)
	m.Overlay.Push(form)
	return nil
}

// stackEngine resolves the engine handle the stack surfaces need: the
// wired one, else a transient agent-less one the caller must release.
//
// It is the same fallback the dependency badge uses (msgs.go's
// dependencyBlockers), and for the same reason: a stack's shape is a
// store-and-git fact, so a board with no usable agent — which still
// creates cards, cuts worktrees and crosses gates — must still show the
// chain and still refuse an out-of-order landing.
func (m *Shell) stackEngine() (*engine.Engine, func()) {
	if m.store == nil || m.wt == nil {
		return nil, func() {}
	}
	if m.engine != nil {
		return m.engine, func() {}
	}
	eng := engine.New(engine.Config{Store: m.store, Pool: m.wt, Workspace: m.ws})
	// Without this the transient engine resolves every card to the
	// checkout's HEAD, and a stacked card would be measured against the
	// wrong base — reported stale when it is not, or the reverse.
	m.wt.SetBaseLookup(eng.StackBaseFor)
	return eng, func() { _ = eng.Close() }
}
