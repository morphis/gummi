package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/cardmint"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/rmtree"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/worktree"
)

// featureRow is one board entry: the stored feature plus the bits of
// derived filesystem state the board displays.
type featureRow struct {
	F domain.Feature
	// AgentPublish is whether a person allowed the session to publish its
	// own branch (DESIGN §22.14), read at load for every card at once.
	AgentPublish bool
	// Workflows is whether the session is offered specs and workflow runs
	// (its own switch, else the workspace's), and WorkflowsSet whether the
	// switch is its own; read at load for every card at once.
	Workflows, WorkflowsSet bool
	// OpenRun is the session's open workflow run (running or paused), read
	// at load; it holds the session's landing (Engine.RunHolds). Nil when
	// none is open.
	OpenRun *domain.Run
	// StepAsks is the question a step of OpenRun is asking the person
	// (its agent waits inside ask_user), read at load; "" when no step
	// asks. The step's card is hidden, so the session's row is where a
	// person learns the run waits on them.
	StepAsks string
	// DecidingRun is the session's run with an open decision for the
	// person (Engine.WorkflowDecision), read at load; nil when none waits.
	DecidingRun *domain.Run
	// Proposal is DecidingRun's proposal or revised plan as data
	// (Engine.WorkflowProposal), read at load for the picker to draw:
	// it asks git whether the tree is dirty. Nil for any other decision.
	Proposal *engine.WorkflowProposal
	// DispatchedBy is the agent whose run minted the session, empty for
	// one no agent dispatched; Dispatched counts an agent's open children.
	DispatchedBy domain.FeatureID
	Dispatched   int
	HasWorktree  bool
	Landed       bool // branch has merged into main; worktree is cleanup-ready
	// Ahead: the branch holds commits of its own (worktree.BranchAhead),
	// read at load with Landed
	Ahead      bool
	History    []state.TransitionRecord
	StageSpend []state.StageSpend // per-stage/model spend rollup (forward-only)
	// BaseBranch is what f actually lands on, resolved once at load
	// (loadRows) through Shell.baseBranch — the same map lookup shell.go
	// resolves at attach — so a pure function holding only this row, not a
	// *Shell, can still say the trunk's real name instead of asserting
	// "main" (REVIEW-ux-drive-2026-09-10-round2.md §3.4). Empty on a row
	// nothing has populated it for (a synthetic test row, a not-yet-loaded
	// card); baseBranch() below is what every reader actually calls, and it
	// carries the same worktree.DefaultBaseBranchName fallback Shell.baseBranch
	// does, so a sentence built from it is never missing a word.
	BaseBranch       string
	OpenDiffComments int // unresolved diff annotations
	// Repos are the repositories a session spans, its own first; nil for
	// a card with nothing attached (DESIGN §27).
	Repos []engine.RepoView
	// Foreign is the live session another gummi process is running on this
	// card (a headless run/resume, a second board), resolved at load from
	// the card's live file. The board cannot drive a card someone else
	// owns, so the row badges it and the card actions withhold everything
	// that would fight the other process — it can still be watched.
	Foreign      state.ForeignDrive
	DrivenAbroad bool
	// Events is the card's event log (state.CardEvent, card_events table),
	// populated for the SELECTED card only, lazily, once the card page
	// opens or the selection changes on it (Shell.loadCardEvents). Every
	// other row's Events is nil — loading every card's log on each board
	// refresh would be unbounded IO, which is exactly what the row
	// snapshot above exists to avoid.
	Events []state.CardEvent
	// Drift is the card's fork drift as its worktree stands at load — its
	// base no longer carries the commit it forked from — nil when there is
	// none or no worktree. Every stage session refuses a drifted card
	// before it starts, so a stop on one has exactly one answer that can
	// work (the rebase), and the answer set has to know that without
	// reading it back out of an error sentence.
	Drift *worktree.ForkDriftError
	// HasSessionSpec is whether a session's spec file exists on disk,
	// stat'd once at load (loadRows) the same way HasWorktree is — so
	// cardHasSessionSpec (cardtabs.go), which gates the spec tab, alt+s
	// and the action inventory's "open document" entry, costs the render
	// path nothing.
	HasSessionSpec bool
}

// baseBranch names the branch r's card lands on, for prose that only has
// the row in hand (cardactions.go's action inventory, built by a plain
// function with no *Shell). Mirrors Shell.baseBranch's own fallback so
// the two never say something different for the same card.
func (r featureRow) baseBranch() string {
	if r.BaseBranch != "" {
		return r.BaseBranch
	}
	return worktree.DefaultBaseBranchName
}

// watchOnly reports whether this board may watch the card but not steer
// it, because another gummi process is already driving it (DrivenAbroad).
// Every place that withholds a verb, a key, a decision or the composer
// asks this; watchDriver names who to defer to when saying so.
func (r featureRow) watchOnly() bool { return r.DrivenAbroad }

// watchDriver names the driver watchOnly is deferring to, for the one
// sentence each refusal owes the reader. Empty when the row is this
// board's to drive.
func (r featureRow) watchDriver() string {
	if r.DrivenAbroad {
		return fmt.Sprintf("pid %d", r.Foreign.PID)
	}
	return ""
}

// rowsMsg delivers a fresh load of the board content.
type rowsMsg struct {
	rows []featureRow
	// stacks is the per-card stack annotation, keyed by card id and
	// empty for an unstacked one. Derived in loadRows for the same
	// reason every other git-derived column is.
	stacks map[domain.FeatureID]stackRow
	// children are the sessions' hidden child cards (a workflow step's),
	// which are no row of the board: the web face opens a step's thread
	// by its card (DESIGN §25.10).
	children map[domain.FeatureID]domain.Feature
	err      error
	// seq numbers the load by when it started (Shell.nextRowsSeq), so a
	// load that returns after a newer one is dropped rather than applied
	// over it. 0 is never judged stale.
	seq uint64
	// changes is the store's own-write counter as the load began: the
	// rows hold every write up to it (freshness.go).
	changes int64
}

// noticeMsg surfaces a transient outcome (success or failure) in the
// status bar. reload is set only by a command that mutated row-rendered
// state (membership, stage, worktree/branch, envelope/budget, gate-blocker
// counts); a routine status notice (queued, paused, a non-mutating error)
// leaves it false so it never triggers a board reload.
type noticeMsg struct {
	text   string
	isErr  bool
	reload bool
	// id names the feature an error notice is about, so clearTransientNotice
	// can scope its keep-on-error exemption to "still on that feature's
	// surface" instead of keeping it for the rest of the process. Only
	// meaningful when isErr is set; a non-error notice is always transient
	// regardless of id.
	id domain.FeatureID
	// clearInbox, when non-empty, names a feature whose needs-attention
	// entry is removed on receipt of this notice — the outcome-driven
	// counterpart to pre-dispatch removal (see the key handler). It is
	// set only on the success returns of the board actions; error and
	// gate-blocked returns leave it empty so the attention item survives
	// until the thing is actually attended to.
	clearInbox domain.FeatureID
	// reseedInbox, with clearInbox, reads that card's open decisions back
	// once the item is gone. An answer settles its own question and
	// nothing else: a stop that stood behind the question — a budget
	// stop raised while it was up — is still the card's, and the one item
	// the queue keeps per card was the only place the board held it.
	reseedInbox bool
	// restore, when non-empty, is a composer line that was never
	// delivered and belongs back in the input. A refused turn (the
	// backend was still streaming the previous one) must not cost the
	// user their sentence: the composer clears optimistically when the
	// line is handed off, so the one path that can fail after that hands
	// it back here.
	restore string
	// web is the notice as the web face shows it, when text names a
	// terminal key the page has no such key for ("x resolves one"): the
	// same sentence with the act named instead. Empty means text reads the
	// same on both faces (webText).
	web string
}

// webText is the notice in the words the web face shows.
func (n noticeMsg) webText() string {
	if n.web != "" {
		return n.web
	}
	return n.text
}

// blockersMsg carries one card's recounted open diff comments back into
// its row. See Shell.refreshBlockers.
type blockersMsg struct {
	id               domain.FeatureID
	openDiffComments int
}

// refreshBlockers recounts one card's open diff comments and folds the
// count back into its row: they hold its landing, and the card page says
// so. One card, not all of them: this runs on every turn end, where
// walking the whole board would be a query per card for one card's
// change.
// RefreshBlockers re-reads one card's unresolved diff comments into its
// row. A web write
// to a card's documents is made off the loop, so nothing the loop sees
// would recount them: without this a comment added from the page would
// not reach the decision it rides on until something else reloaded the
// board.
func (m *Shell) RefreshBlockers(id string) tea.Cmd { return m.refreshBlockers(webID(id)) }

// WebPublished reloads the board after the page published a card: the act
// ran off the loop and wrote the card's PR link and its thread, which the
// rows and the open card's history are read from.
func (m *Shell) WebPublished(id string) tea.Cmd {
	if !m.attached() {
		return nil
	}
	return tea.Batch(m.loadRows, m.reloadOpenCardEvents(m.selectedID()), m.refreshBlockers(webID(id)))
}

func (m *Shell) refreshBlockers(id domain.FeatureID) tea.Cmd {
	if !m.attached() {
		return nil
	}
	return func() tea.Msg {
		ctx := context.Background()
		f, err := m.store.GetFeature(ctx, id)
		if err != nil {
			// a card that cannot be read keeps the counts it has: a failed
			// read must never be reported as "no blockers", which would
			// wave a gate through.
			return nil
		}
		return blockersMsg{id: id, openDiffComments: m.openDiffComments(f.ID)}
	}
}

// loadRows reads all features, their histories, and worktree presence.
func (m *Shell) loadRows() tea.Msg {
	ctx := context.Background()
	seq := m.nextRowsSeq()
	changes, _ := m.store.Changes(ctx)
	feats, err := m.store.ListFeatures(ctx)
	if err != nil {
		return rowsMsg{err: err}
	}
	// The open sessions' branches, resolved from the features this load is
	// already holding rather than from m.rows: the rows being built here
	// are not on the Shell yet.
	openSessions := map[domain.FeatureID]string{}
	for _, f := range feats {
		if f.IsFreeform() && !f.MainCheckout && f.Stage != domain.StageDone {
			openSessions[f.ID] = f.BranchName()
		}
	}
	dispatches, err := m.store.DispatchesByCard(ctx)
	if err != nil {
		dispatches = nil // the badges are a nicety; a failed read draws none
	}
	openChildren := map[domain.FeatureID]int{}
	for _, f := range feats {
		if d, ok := dispatches[f.ID]; ok && f.Stage != domain.StageDone {
			openChildren[d.Agent]++
		}
	}
	publishers, err := m.store.AgentPublishers(ctx)
	if err != nil {
		publishers = nil // the menu's label is all it feeds; a failed read says off
	}
	switches, err := m.store.SessionWorkflowOverrides(ctx)
	if err != nil {
		switches = nil
	}
	workflowsDefault := m.engine != nil && m.engine.WorkflowsDefault()
	rows := make([]featureRow, 0, len(feats))
	children := map[domain.FeatureID]domain.Feature{}
	for _, f := range feats {
		// a workflow step's card, and an old crew's, are never on the
		// board: their session is their face (DESIGN §25.6)
		if f.IsChild() {
			children[f.ID] = f
			continue
		}
		// goals are retired: a legacy goal card is not on the board
		if f.Kind == domain.KindGoal {
			continue
		}
		row := featureRow{F: f, BaseBranch: m.repoBaseBranch(f), Dispatched: openChildren[f.ID], AgentPublish: publishers[f.ID]}
		row.Workflows = workflowsDefault
		if on, set := switches[f.ID]; set {
			row.Workflows, row.WorkflowsSet = on, true
		}
		if d, ok := dispatches[f.ID]; ok {
			row.DispatchedBy = d.Agent
		}
		if f.IsFreeform() && f.Stage != domain.StageDone {
			row.OpenRun, _ = m.store.OpenRun(ctx, f.ID)
			row.StepAsks = m.stepAsks(ctx, f.ID, row.OpenRun)
			if m.engine != nil {
				row.DecidingRun, _ = m.engine.WorkflowDecision(ctx, f.ID)
				if d := row.DecidingRun; d != nil && d.Pending != nil &&
					(d.Pending.Kind == domain.RunDecideProposal || d.Pending.Kind == domain.RunDecidePlan) {
					if p, err := m.engine.WorkflowProposal(ctx, f.ID, d.N); err == nil {
						row.Proposal = &p
					}
				}
			}
		}
		if f.Base != "" && (f.StackID == "" || f.StackPos == 0) {
			// a card cut from a chosen base forks from it and lands on
			// it (§18.2), so the words on its verbs name that branch. A
			// stacked card above the bottom ignores its own choice.
			row.BaseBranch = f.Base
		}
		if name := openSessions[f.ParentID]; name != "" {
			// a card a freeform session created lands on that session's
			// branch while it is open (worktree.Pool.ManagerFor)
			row.BaseBranch = name
		}
		if hist, err := m.store.History(ctx, f.ID); err == nil {
			row.History = hist
		}
		if bd, err := m.store.StageBreakdown(ctx, f.ID); err == nil {
			row.StageSpend = bd
		}
		if ok, err := m.wt.Exists(ctx, &f); err == nil {
			row.HasWorktree = ok
			// a branch that has merged into main no longer needs its
			// worktree — flag it so the board can offer cleanup.
			if ok {
				row.Ahead, row.Landed = m.canHaveLanded(ctx, &f)
				row.Drift = m.stoppedDrift(ctx, &f, row.Landed)
			}
		}
		row.OpenDiffComments = m.openDiffComments(f.ID)
		if m.engine != nil && spansRepos(f) {
			row.Repos = m.engine.RepoViews(ctx, f)
		}
		row.Foreign, row.DrivenAbroad = state.ForeignDriver(m.ws, f.ID)
		if hasSessionSpec(f) {
			if path, ok := m.store.SessionSpecFile(ctx, m.wt.Root(), f.ID, 1); ok {
				if _, err := os.Stat(path); err == nil {
					row.HasSessionSpec = true
				}
			}
		}
		rows = append(rows, row)
	}
	// Stack annotations are derived here, on the command goroutine,
	// because they ask git (is this card stale? has it landed?) and the
	// render path may not. Keyed by card, so cardLine is a map lookup.
	return rowsMsg{rows: rows, children: children, stacks: m.stackRowsForFeatures(ctx, feats), seq: seq, changes: changes}
}

// stoppedDrift is a card's fork drift, asked only of a card stopped on a
// person — the one whose answer set is on screen and has to know that
// every run will be refused. The rest of the board pays nothing for it:
// loadRows runs on every refresh, and its git spawns are budgeted
// (TestReloadGitCount). A card that meets its drift by running raises a
// failure, and that failure reloads the rows (handleEngineEvent), so it
// is asked then. Read-only: Drift never backfills a fork the way the
// refusing check does, so loading the board stamps nothing.
func (m *Shell) stoppedDrift(ctx context.Context, f *domain.Feature, landed bool) *worktree.ForkDriftError {
	if landed {
		return nil
	}
	if _, ok := m.inbox.get(f.ID); !ok {
		return nil
	}
	d, err := m.wt.Drift(ctx, f)
	if err != nil {
		return nil
	}
	return d
}

// canHaveLanded reports whether a card's branch could plausibly have
// merged into main, gating the expensive Landed walk behind a cheap
// precondition. A branch with no commits of its own ahead of the fork
// cannot be landed: a squash merge needs the branch's own commits, and a
// merged-then-advanced branch (an ancestor with main moved past it) never
// arises from gummi's own squash-merge lands. For such a card Landed is
// skipped and the row reads not-landed — a best-effort hint, re-checked
// by the c/m handlers at run time, so a stale-negative only withholds the
// cleanup prompt until the next reload. False on any git error, matching
// the swallow-errors contract of the inline Landed call it replaces.
//
// It answers whether the branch has commits of its own too (ahead), which
// it asks anyway: the row keeps it for what the board says about an idle
// session, at no further git spawn.
func (m *Shell) canHaveLanded(ctx context.Context, f *domain.Feature) (ahead, landed bool) {
	ahead, err := m.wt.BranchAhead(ctx, f)
	if err != nil || !ahead {
		return false, false
	}
	landed, err = m.wt.Landed(ctx, f)
	return true, err == nil && landed
}

// formResult is what the new-card dialog hands the shell: everything a
// mint needs. The description's first line is the card's title, and the
// whole of it is the session's opening turn.
type formResult struct {
	Desc string
	// Name is the title the person gave the session before it started,
	// "" for the one its description's first line gives.
	Name        string
	Profile     string
	Envelope    *int // nil = the shell's default envelope
	Repo        string
	ExternalRef string // an imported issue
	// Base is the branch the card forks from, "" for the checkout's HEAD.
	Base string
	// StackOnto names a card to stack this one on top of, creating the
	// stack if that card is not in one yet. StackInto joins an existing
	// stack at the top. Both empty is a standalone card.
	StackOnto domain.FeatureID
	StackInto domain.StackID
	// SessionBackend and SessionModel are the agent and model a freeform
	// card's session runs on when the person picked them (DESIGN §19.8);
	// both empty is the profile's implementer. Only the web face's session
	// draft sets them today.
	SessionBackend string
	SessionModel   string
	// MainCheckout is the freeform kind's "runs in" choice: a session that
	// works in the repository's main checkout instead of a worktree of its
	// own — no branch cut, no worktree created (DESIGN §19).
	MainCheckout bool
	// Home is the third "runs in" choice: an agent, working in a home of
	// its own and in no repository (domain.Feature.Home).
	Home bool
}

// cardCreatedMsg is createCard's success: the shell reloads rows, keeps
// the repo as the next dialog's preselect.
type cardCreatedMsg struct {
	f domain.Feature
	// stack is the stack the new card joined, empty when standalone. The
	// shell ticks it so the card's branch is cut on the right base.
	stack domain.StackID
	warn  string // a stack placement that could not be made
	// opening is the text a freeform card was created with, verbatim: its
	// session's first turn (FreeformSession.KickoffWith), which the card
	// itself only keeps as a line-sized title and one-liner.
	opening string
	// open asks the shell to land on the new card once the reload that
	// puts it on the board arrives (shell.go's openOnLoad). Set by the
	// follow-up, which is minted from the page of the card it came out
	// of: the reader typed what is wrong with finished work, and the
	// card holding that sentence is the one they are now on. A card
	// created from the board's own form leaves it false — the form was
	// opened from the board and answers back to it.
	open bool
}

// createCard mints a session through cardmint.Mint — the same recipe
// `gummi run` uses — and then places it in its stack.
func (m *Shell) createCard(res formResult) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		env := m.envelope
		if res.Envelope != nil {
			env = *res.Envelope
		}
		repo := res.Repo
		if res.Home {
			// an agent is in no repository, whatever the form's row held
			repo = ""
		}
		f, err := cardmint.Mint(ctx, m.store, m.ws, cardmint.Input{
			Description: res.Desc, Title: res.Name, Profile: res.Profile, Envelope: env,
			Repo: repo, RequireRepo: m.requireRepo, Base: res.Base,
			ExternalRef:    res.ExternalRef,
			SessionBackend: res.SessionBackend, SessionModel: res.SessionModel,
			MainCheckout: res.MainCheckout, Home: res.Home,
		})
		if err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		var warn []string
		// Stacking happens after the mint, and a failure to stack is a
		// warning rather than a rollback: the card is real, described and
		// numbered, and throwing it away because a chain could not be
		// formed would lose the reader's words. They can stack it after.
		stacked := domain.StackID("")
		if res.StackOnto != "" || res.StackInto != "" {
			id, serr := m.stackNewCard(ctx, f, res.StackOnto, res.StackInto)
			if serr != nil {
				warn = append(warn, sanitize(serr.Error()))
			} else {
				stacked = id
			}
		}
		created := cardCreatedMsg{
			f:     f,
			stack: stacked, warn: strings.Join(warn, "; "),
		}
		created.opening = res.Desc
		return created
	}
}

// requireRepo is cardmint's repository check for this workspace: a name
// the pool knows, or the default when the pool has one. A shell with no
// pool (a scaffold) has one implicit repository and refuses nothing.
func (m *Shell) requireRepo(name string) error {
	if m.wt == nil || m.wt.Known(name) {
		return nil
	}
	if name == "" {
		return errors.New(repoUnchosenErr)
	}
	if err := m.wt.ClashError(name); err != nil {
		return err
	}
	return fmt.Errorf("repository %q is not configured; add it to `repos:` in .gummi/config.yaml", name)
}

// duplicateFeature mints a fresh card from an existing one: same title,
// one-liner, kind, skip flags, profile, and budget envelope, and the same
// identity set the source was minted with — its repo, base, and severity.
// A card with an empty repo is one mint refuses in a repos:-only
// workspace, so a copy that dropped it would be born un-routable and
// fail at its first git operation (a delete's worktree probe included).
// The branch scheme is stamped to the current default like every fresh
// card: a copy is never adopted and never resurrects the scheme the
// source was minted under. The original stays untouched — the copy is
// how a feature restarts from scratch without rewinding the workflow or
// losing the original's history and cost record. Nothing else carries
// over: the external ref stays on the original (re-ingest dedupe
// resolves items by ref, which must stay unambiguous), and the adopted
// branch, stack position, goal membership, spend, session backend/model,
// main-checkout, and gate approval all stay unset (an agent's copy alone
// keeps its mark: it is an agent, in a fresh home), as does the copy's
// record of work — it has no artifacts, and a blank template is seeded
// when it enters design (spec.EnsureDraft). The shared slug is safe:
// branch, worktree, and artifact paths are all keyed by ID.
func (m *Shell) duplicateFeature(id domain.FeatureID) tea.Cmd {
	name := m.cardName(id)
	return func() tea.Msg {
		ctx := context.Background()
		src, err := m.store.GetFeature(ctx, id)
		if err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		if !src.IsFreeform() {
			return noticeMsg{text: name + " is a retired workflow card — it is not copied", isErr: true}
		}
		num, err := m.store.MintFeatureNum(ctx, m.ws.SeqFile())
		if err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		newID, err := domain.NewID(src.Kind, num)
		if err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		now := m.now()
		f := domain.Feature{
			ID: newID, Num: num, Kind: src.Kind, Title: src.Title, OneLiner: src.OneLiner,
			Slug: src.Slug, Stage: domain.StageOpen,
			Profile: src.Profile, Budget: domain.Budget{Envelope: src.Budget.Envelope},
			Repo: src.Repo, Base: src.Base,
			// an agent's copy is an agent, in a fresh home of its own
			MainCheckout: src.Home, Home: src.Home,
			BranchScheme: domain.DefaultBranchScheme,
			CreatedAt:    now, UpdatedAt: now,
		}
		if err := m.store.CreateFeature(ctx, &f); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		return noticeMsg{text: fmt.Sprintf("%s created — fresh copy of %s", newID.Ref(), name), reload: true}
	}
}

// humanActor is who a hand-driven act is recorded as: the person a web
// intent carries (state.PersonActor), or the terminal's "user". It is read
// on the Update goroutine, when the act is set up — a command that runs
// later captures it rather than asking again.
func (m *Shell) humanActor() string {
	if m.intent != nil && m.intent.in.actor != "" {
		return m.intent.in.actor
	}
	if m.webActor != "" {
		return m.webActor
	}
	return state.ActorUser
}

// withEngine runs fn against the board's engine, standing a transient
// agent-less one up when the board has none.
//
// A static board (no coding agent) still crosses gates: engine.Advance
// touches only the store, worktrees, and workspace, so a throwaway engine
// runs the same floor and closes. When an engine IS wired, its Advance
// also drops the stale stage session, which the transient one cannot —
// hence the preference rather than always building one.
func (m *Shell) withEngine(fn func(*engine.Engine) tea.Msg) tea.Msg {
	eng := m.engine
	if eng == nil {
		eng = engine.New(engine.Config{Store: m.store, Pool: m.wt, Workspace: m.ws})
		defer func() { _ = eng.Close() }()
	}
	return fn(eng)
}

// rebaseFeature rebases a feature's branch onto main from the TUI
// (DESIGN §9 M4). It refuses a dirty worktree (so nothing uncommitted is
// risked), and when the rebase can't apply cleanly (it self-aborts,
// leaving the worktree untouched) it offers the agent hand-off — or,
// with no engine, reports the conflicted files to resolve by hand.
func (m *Shell) rebaseFeature(f domain.Feature) tea.Cmd {
	// the rebase is the answer to a landing's conflicts: once it is under
	// way the decision stops leading with it
	delete(m.landConflicts, f.ID)
	return m.rebaseFeatureLocked(f)
}

// rebasedMsg is a rebase that went through: its notice, and whether it
// cleared the card's fork drift.
type rebasedMsg struct {
	id     domain.FeatureID
	notice noticeMsg
	// cleared: the card was drifted before this rebase and is not now
	cleared bool
}

func (m *Shell) rebaseFeatureLocked(f domain.Feature) tea.Cmd {
	by, onto := m.humanActor(), m.baseBranch(f)
	return m.cardLocked(f.ID, func() tea.Msg {
		ctx := context.Background()
		if ok, err := m.wt.Exists(ctx, &f); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		} else if !ok {
			return noticeMsg{text: noWorktreeYet(f), isErr: true}
		}
		drifted, _ := m.wt.Drift(ctx, &f)
		// a rebase stranded mid-flight (a crash, a killed agent session)
		// blocks any new rebase and reads as dirty; abort it first so r
		// always recovers the worktree before retrying.
		if _, err := m.wt.AbortRebase(ctx, &f); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		var autostash bool
		if dirty, err := m.wt.Dirty(ctx, &f); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		} else if dirty {
			// A dirty worktree is normally refused (nothing uncommitted is
			// risked), but a drifted one would otherwise deadlock: CommitAll
			// refuses under drift, so the operator could neither commit nor
			// rebase. When drift is the cause, --autostash carries the work
			// across the rebase and restores it — never silently discarding
			// it. A non-drifted dirty worktree keeps the safe refusal.
			if err := m.wt.AssertNoForkDrift(ctx, &f); err != nil {
				autostash = true
			} else {
				return noticeMsg{text: f.Name() + ": worktree has uncommitted changes — commit them before rebasing", isErr: true}
			}
		}
		rebase := m.wt.RebaseOnMain
		if autostash {
			rebase = m.wt.RebaseOnMainAutostash
		}
		if err := rebase(ctx, &f); err != nil {
			// The dirty (autostash) path used to stop here with a bare
			// notice: a drifted card carrying uncommitted work — which it
			// could not checkpoint — was never offered the agent, the one
			// thing that can sort the two histories and that work out.
			var ce *worktree.RebaseConflictError
			if errors.As(err, &ce) {
				if m.engine != nil {
					return rebaseConflictMsg{f: f, files: ce.Files, reason: ce.Reason, dirty: autostash}
				}
				// ce carries git-derived file names; sanitize like every
				// other notice before it reaches the terminal.
				return noticeMsg{text: sanitize(f.Name() + ": " + ce.Error() + " — resolve on the branch, then retry"), isErr: true}
			}
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		// Re-anchor the recorded fork to main's HEAD after the rebase, so a
		// drifted feature is cleared in the same gesture and a fresh one does
		// not go stale on the next innocent rewrite of main.
		if err := m.wt.ReanchorOnMain(ctx, &f); err != nil {
			return noticeMsg{text: sanitize(fmt.Sprintf("%s: rebased but fork not re-anchored: %v", f.Name(), err)), isErr: true}
		}
		// the branch moved under everyone reading the card: its thread
		// says so, and who moved it
		m.logRebase(f.ID, state.RebasePayload{Onto: onto, By: by})
		return rebasedMsg{
			id: f.ID, cleared: drifted != nil,
			notice: noticeMsg{text: f.Name() + " rebased onto " + onto, reload: true},
		}
	})
}

// cleanupLanded removes a card's worktree and branch once it has no more
// use for them, keeping the feature record (it stays on the board as a
// done entry): a card whose branch landed, or one that is closed. A
// closed card's branch goes with it unless it is the only copy of its
// work (worktree.Manager.Spent) — the rule `gummi clean` applies. It
// re-checks at run time so a stale board row can't trigger a cleanup of
// unmerged work (DESIGN §9 M4, §10 landed-branch detection).
func (m *Shell) cleanupLanded(f domain.Feature) tea.Cmd {
	return m.cardLocked(f.ID, func() tea.Msg { return m.tidy(context.Background(), f) })
}

// tidy is cleanupLanded's act, for a caller that already holds the card's
// lock: the clean-up itself, and a closing that takes the card's worktree
// and branch with it (closeOut).
func (m *Shell) tidy(ctx context.Context, f domain.Feature) noticeMsg {
	closed := f.Stage == domain.StageDone
	hasBranch, err := m.wt.BranchExists(ctx, &f)
	if err != nil {
		return noticeMsg{text: sanitize(err.Error()), isErr: true}
	}
	var landed bool
	if hasBranch || !closed {
		landed, err = m.wt.Landed(ctx, &f)
	}
	// fork drift is a refusal for an open card; a closed one is read
	// by Spent, which does not need the base to still carry the landing
	var drift *worktree.ForkDriftError
	if err != nil && (!closed || !errors.As(err, &drift)) {
		return noticeMsg{text: sanitize(err.Error()), isErr: true}
	}
	if !landed && !closed {
		return noticeMsg{text: f.Name() + " hasn't landed on " + m.baseBranch(f) + " yet — nothing to clean up", isErr: true}
	}
	spent := landed
	if !landed {
		if spent, err = m.wt.Spent(ctx, &f); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
	}
	m.dropSession(f.ID)
	if ok, err := m.wt.Exists(ctx, &f); err != nil {
		return noticeMsg{text: sanitize(err.Error()), isErr: true}
	} else if ok {
		// A "landed" branch can still be topologically indistinguishable
		// from a fresh one that merely fell behind an advancing main, and
		// a bounced-back feature may hold uncommitted rework the merged
		// history doesn't contain. Refuse the force-remove when tracked
		// files are modified — that's real work not in main — so cleanup
		// only ever discards untracked build artifacts.
		if dirty, err := m.wt.TrackedDirty(ctx, &f); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		} else if dirty {
			return noticeMsg{text: f.Name() + " has uncommitted changes on its branch — commit or discard them before cleanup", isErr: true}
		}
		// where the branch is the only copy of the work, so is a file
		// the worktree holds that no commit does — untracked included
		if !spent {
			if dirty, err := m.wt.Dirty(ctx, &f); err != nil {
				return noticeMsg{text: sanitize(err.Error()), isErr: true}
			} else if dirty {
				return noticeMsg{text: f.Name() + " was neither landed nor pushed and its worktree holds uncommitted work — commit or discard it before cleanup", isErr: true}
			}
		}
		// force: only disposable untracked artifacts remain now, and a
		// non-force remove would abort on them. The confirm dialog spells
		// this out.
		if err := m.wt.Remove(ctx, &f, true); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
	}
	// An adopted branch was never gummi's to delete, and a branch that
	// is the only copy of its work is the one thing here that could be
	// lost: both stay, and the notice says which.
	kept := f.Adopted() || !spent
	if hasBranch && !kept {
		// git's own merged-check (-d) still backstops regular merges;
		// for a squash merge, whose commits aren't ancestors of main,
		// the delete re-verifies with the merge-tree content check —
		// stronger than the ancestor test — before forcing. A closed
		// card's delete re-reads Spent instead.
		del := m.wt.DeleteLandedBranch
		if !landed {
			del = m.wt.DeleteClosedBranch
		}
		if err := del(ctx, &f); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
	}
	text := f.Name() + " cleaned up — worktree and merged branch removed"
	switch {
	case !spent:
		text = f.Name() + " cleaned up — worktree removed · " + f.BranchName() + " kept, it is the only copy of its work"
	case !landed:
		text = f.Name() + " cleaned up — worktree and branch removed"
	}
	return noticeMsg{text: text, reload: true}
}

// closeOut is what closing a card does next, under the lock the closing
// holds: it takes the card's worktree and branch with it, unless the
// person asked to keep them or the card is one a closing leaves standing
// (worktree.Pool.GoesWithClose). It answers the words the closing's notice
// ends on, and "" where everything is left as it was — the closing is what
// was asked for, and a clean-up that failed is said without undoing it.
func (m *Shell) closeOut(ctx context.Context, id domain.FeatureID, keep bool) string {
	f, err := m.store.GetFeature(ctx, id)
	if err != nil || !m.wt.GoesWithClose(ctx, &f) {
		return ""
	}
	if keep {
		return "worktree and branch kept"
	}
	if n := m.tidy(ctx, f); n.isErr {
		return "its worktree and branch are still here: " + n.text
	}
	if f.Adopted() {
		return "worktree removed"
	}
	return "worktree and branch removed"
}

// dropSession ends and forgets a feature's engine session and clears
// any needs-attention item for it.
func (m *Shell) dropSession(id domain.FeatureID) {
	if m.engine != nil {
		m.engine.Drop(id)
		// A freeform card's session holds its worktree and its card lock for
		// as long as the conversation lives, so the moment the card ends —
		// landed, handed off, deleted — is the moment to close it: Close
		// commits whatever the last turn left, stops the backend and drops
		// the lock. Leaving it open would keep a writer pointed at a branch
		// that has just gone to main.
		if ff := m.engine.Freeform(id); ff != nil {
			_ = ff.Close()
		}
	}
	if m.inbox != nil {
		m.inbox.remove(id)
	}
}

// migrateDraft promotes the artifact draft (spec or bug report) to its
// workspace home under .gummi/specs|bugs in the main checkout. The
// artifact is gummi workspace content: it never enters the worktree and
// is never committed. An item that never had a draft gets a fresh
// template — the artifact always exists from approval on.
func (m *Shell) migrateDraft(f *domain.Feature) error {
	// A card with no artifact has nothing to promote, and the bare join
	// would name the workspace root as the destination (domain.
	// Feature.ArtifactFile has the whole story).
	home, ok := f.ArtifactFile(m.wt.Root())
	if !ok {
		return nil
	}
	return spec.Promote(
		home,
		filepath.Join(m.ws.DraftsDir(), spec.DraftFilename(f)),
		filepath.Join(m.wt.Root(), f.WorktreePath(), f.ArtifactPath()),
		f,
	)
}

// artifactFile resolves where the item's design artifact lives right
// now: its workspace home once promoted, the draft before then, or the
// worktree copy of an item mid-flight from the committed-artifact era.
// Empty when none exists yet.
func (m *Shell) artifactFile(f *domain.Feature) string {
	return artifactFileIn(m.wt.Root(), m.ws.DraftsDir(), f)
}

// artifactFileIn is artifactFile for a workspace root and drafts
// directory in hand, for a reader running off the Update loop.
func artifactFileIn(root, drafts string, f *domain.Feature) string {
	home, ok := f.ArtifactFile(root)
	if !ok {
		// No artifact, so no candidate: without this the bare join below
		// named the workspace ROOT, which Stat happily confirms exists —
		// and every reader of this function then had a directory where it
		// expected a document.
		return ""
	}
	for _, p := range []string{
		home,
		filepath.Join(drafts, spec.DraftFilename(f)),
		filepath.Join(root, f.WorktreePath(), f.ArtifactPath()),
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// deleteFeature removes worktree, branch, and record.
func (m *Shell) deleteFeature(id domain.FeatureID) tea.Cmd {
	return m.cardLocked(id, func() tea.Msg {
		ctx := context.Background()
		f, err := m.store.GetFeature(ctx, id)
		if err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		if err := m.deleteCard(ctx, &f); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		return noticeMsg{text: fmt.Sprintf("%s deleted", f.Name()), reload: true}
	})
}

// deleteCardTrees removes what a deleted card held on disk in its
// repository: its worktree, its scratch tree and its branch.
func (m *Shell) deleteCardTrees(ctx context.Context, f *domain.Feature) error {
	// a card whose repository was dropped from `repos:` after it was
	// minted has nothing on disk left to reach, so the record
	// and workspace files below are all that is left to remove, rather
	// than refusing the delete outright.
	if ok, err := m.wt.Exists(ctx, f); err != nil {
		if !errors.Is(err, worktree.ErrRepoNotConfigured) {
			return err
		}
	} else if ok {
		if err := m.wt.Remove(ctx, f, true); err != nil {
			return err
		}
	}
	// a feature that never left Spec has no branch — only delete
	// one that exists. An adopted card's branch is skipped outright: the
	// card record goes, the inherited work stays (DESIGN §10 D22).
	if ok, err := m.wt.BranchExists(ctx, f); err != nil {
		if !errors.Is(err, worktree.ErrRepoNotConfigured) {
			return err
		}
	} else if ok && !f.Adopted() {
		if err := m.wt.DeleteBranch(ctx, f, true); err != nil {
			return err
		}
	}
	return nil
}

// deleteCard takes one card off disk and out of the store: its worktree,
// its branch, its record, and the workspace files keyed to it.
func (m *Shell) deleteCard(ctx context.Context, f *domain.Feature) error {
	// an agent has no tree and no branch, and its home is kept: there is
	// nothing on disk to take, and asking would make the home of an agent
	// that never ran
	if !f.Home {
		if err := m.deleteCardTrees(ctx, f); err != nil {
			return err
		}
	}
	if m.engine != nil {
		// the hidden cards of its runs' steps, and their directories
		m.engine.ForgetRuns(ctx, f.ID)
	}
	if err := m.store.DeleteFeature(ctx, f.ID); err != nil {
		return err
	}
	// the artifact and its draft are workspace files keyed to the
	// record — they go with it (best effort: an orphan is only clutter).
	//
	// Through ArtifactFile, never a bare join: a freeform card has no
	// artifact, and joining its empty path onto the root made this line
	// os.RemoveAll(workspace) — it deleted the repository.
	if artifact, ok := f.ArtifactFile(m.wt.Root()); ok {
		_ = os.RemoveAll(artifact)
	}
	_ = os.RemoveAll(filepath.Join(m.ws.DraftsDir(), spec.DraftFilename(f)))
	// the live-stream mirror is keyed to the record too; without this
	// a deleted card's last session would linger as a watchable file
	// (harmless — its owner is gone — but clutter all the same).
	_ = os.Remove(m.ws.LiveFile(f.ID))
	// the card's scratch-files dir is keyed to the record the same way.
	// Best effort: an orphan is only clutter. Through rmtree: an agent's
	// build can leave a read-only Go module cache in it.
	_ = rmtree.RemoveAll(m.ws.ScratchFilesDir(f.ID))
	m.dropSession(f.ID)
	return nil
}

// stepAsks is the question a step of the session's open run asks its
// person: the first live step whose agent waits inside ask_user. "" when
// none does. It asks the step's session rather than the step's stored
// state, which the run's tick marks asking only after the question is up.
func (m *Shell) stepAsks(ctx context.Context, session domain.FeatureID, run *domain.Run) string {
	if run == nil || m.engine == nil {
		return ""
	}
	steps, err := m.store.RunStepsLite(ctx, session, run.N)
	if err != nil {
		return ""
	}
	for _, st := range steps {
		if !st.State.Live() || st.Card == "" {
			continue
		}
		if ff := m.engine.Freeform(st.Card); ff != nil {
			if s := ff.Session(); s != nil {
				if ask := s.PendingAsk(); ask != nil {
					return ask.Question
				}
			}
		}
	}
	return ""
}
