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
	"github.com/morphis/gummi/internal/rounds"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/verify"
	"github.com/morphis/gummi/internal/workflow"
	"github.com/morphis/gummi/internal/worktree"
)

// featureRow is one board entry: the stored feature plus the bits of
// derived filesystem state the board displays.
type featureRow struct {
	F           domain.Feature
	HasWorktree bool
	Landed      bool // branch has merged into main; worktree is cleanup-ready
	History     []state.TransitionRecord
	StageSpend  []state.StageSpend // per-stage/model spend rollup (forward-only)
	// gate blockers (DESIGN §6.1), snapshotted at load so the dashboard's
	// next block can explain why g would bounce without doing IO per frame
	OpenSpecQs       int // open user %% threads in the artifact
	OpenDiffComments int // unresolved diff annotations
	// Undrafted names the required section(s) the departing stage left
	// blank — the artifact half of the same gate, resolved through the
	// engine's own predicate so the panel names the blocker the crossing
	// would be refused for.
	Undrafted     []string
	BaselineFails int // gummi-checks already failing on the fresh branch
	// DepBlocked is whether the Advance gate would block this card on an
	// unmet direct dependency at its coding-stage entry — a load-time
	// snapshot resolved against the live dependency store (never a
	// persisted flag, so it cannot go stale and diverge from the gate).
	DepBlocked bool
	// Exited reports that the card's CURRENT stage has already finished a
	// run — a stage_exit event for it sits in the log, newer than the
	// transition that entered the stage — and ExitVerdict is what that
	// run concluded. Both are read from the log at load, so they survive
	// the process that ran the stage: a card whose verify finished an
	// hour ago must still present as finished after a restart, or the
	// stop offers "run verify" to a reader looking at two completed
	// verify receipts (nextsteps.go's finished predicate).
	Exited      bool
	ExitVerdict reviewVerdict
	// Foreign is the live session another gummi process is running on this
	// card (a headless run/resume, a second board), resolved at load from
	// the card's live file. The board cannot drive a card someone else
	// owns, so the row badges it and the card actions withhold everything
	// that would fight the other process — it can still be watched.
	Foreign      state.ForeignDrive
	DrivenAbroad bool
	// AutopilotDriving is whether an autopilot period is open on this card
	// right now — a load-time snapshot, recomputed every load from the
	// event log via autopilotStretches, never persisted.
	AutopilotDriving bool
	// Events is the card's event log (state.CardEvent, card_events table),
	// populated for the SELECTED card only, lazily, once the card page
	// opens or the selection changes on it (Shell.loadCardEvents). Every
	// other row's Events is nil — loading every card's log on each board
	// refresh would be unbounded IO, which is exactly what the row
	// snapshot above exists to avoid.
	Events []state.CardEvent
}

// rowsMsg delivers a fresh load of the board content.
type rowsMsg struct {
	rows []featureRow
	err  error
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
	// restore, when non-empty, is a composer line that was never
	// delivered and belongs back in the input. A refused turn (the
	// backend was still streaming the previous one) must not cost the
	// user their sentence: the composer clears optimistically when the
	// line is handed off, so the one path that can fail after that hands
	// it back here.
	restore string
}

// boardOpenedMsg carries the result of engine.OpenBoard — boardthread.go's
// ensureBoardSession dispatches it in a command because spawning the
// backend can take seconds, so it must not block Update.
type boardOpenedMsg struct {
	session *engine.BoardSession
	err     error
}

// blockersMsg carries one card's recomputed gate blockers back into its
// row. See Shell.refreshBlockers.
type blockersMsg struct {
	id               domain.FeatureID
	openSpecQs       int
	openDiffComments int
	undrafted        []string
}

// refreshBlockers recomputes the three gate-blocker fields for one card
// and folds them back into its row.
//
// These fields decide what the card page SAYS about the gate — the
// narration's first sentence, the decision block's recommended action,
// the chip — and until this existed they were only ever computed by
// loadRows, which runs on a handful of engine events and on none of the
// things that actually change them. The card page therefore lied in both
// directions, from the same stale snapshot: it insisted "the spec still
// has Chosen approach and Implementation notes blank, and the gate stays
// shut" over two fully written sections, offering only to re-run the
// stage that had just written them (an unbounded loop, since the re-run
// changes nothing the page is reading); and, with a comment freshly
// added, it still said "verify passed — the branch is ready to land" and
// recommended landing, which the engine then refused. Advance() was
// right every time — it re-reads the artifact itself — so the fix is to
// stop the page's copy from drifting away from it.
//
// One card, not all of them: this runs on artifact loads and on every
// turn end, where walking the whole board would be a file read and a
// query per card for one card's change.
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
		return blockersMsg{
			id:               id,
			openSpecQs:       m.openQuestionsBlockingGate(f),
			openDiffComments: m.openDiffCommentsBlockingGate(ctx, id),
			undrafted:        m.undraftedGate(f),
		}
	}
}

// loadRows reads all features, their histories, and worktree presence.
func (m *Shell) loadRows() tea.Msg {
	ctx := context.Background()
	feats, err := m.store.ListFeatures(ctx)
	if err != nil {
		return rowsMsg{err: err}
	}
	rows := make([]featureRow, 0, len(feats))
	for _, f := range feats {
		row := featureRow{F: f}
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
				row.Landed = m.canHaveLanded(ctx, &f)
			}
		}
		row.OpenSpecQs = m.openQuestionsBlockingGate(f)
		row.OpenDiffComments = m.openDiffCommentsBlockingGate(ctx, f.ID)
		row.Undrafted = m.undraftedGate(f)
		if bl, err := m.store.CheckBaseline(ctx, f.ID); err == nil {
			for _, r := range bl {
				if !r.OK {
					row.BaselineFails++
				}
			}
		}
		row.DepBlocked = len(m.dependencyBlockers(ctx, f.ID)) > 0
		row.Foreign, row.DrivenAbroad = state.ForeignDriver(m.ws, f.ID)
		if events, err := m.store.Events(ctx, f.ID); err == nil {
			row.AutopilotDriving = autopilotDriving(liveStretches(f, events, m.ws))
			row.ExitVerdict, row.Exited = stageExited(events, row.History, f.Stage)
		}
		rows = append(rows, row)
	}
	return rowsMsg{rows: rows}
}

// dependencyBlockers reports the direct dependencies that would block the
// card at its coding-stage entry — the read-only gate the board badge
// mirrors. It resolves the same engine handle advanceStage shares (reuse a
// wired engine, else a transient agent-less one that closes here), so a
// static board derives the badge against the live store. An empty result on
// any error keeps a failed read from wedging the badge.
func (m *Shell) dependencyBlockers(ctx context.Context, id domain.FeatureID) []engine.BlockingDep {
	eng := m.engine
	if eng == nil {
		eng = engine.New(engine.Config{Store: m.store, Pool: m.wt, Workspace: m.ws})
		defer func() { _ = eng.Close() }()
	}
	deps, err := eng.DependencyBlockers(ctx, id)
	if err != nil {
		return nil
	}
	return deps
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
func (m *Shell) canHaveLanded(ctx context.Context, f *domain.Feature) bool {
	ahead, err := m.wt.BranchAhead(ctx, f)
	if err != nil || !ahead {
		return false
	}
	landed, err := m.wt.Landed(ctx, f)
	return err == nil && landed
}

// formResult is what the new-card dialog hands the shell: everything a
// mint needs, for any kind. The description's first line is the card's
// title; the lines past it seed the artifact (cardmint decides how, per
// kind). After names dependency edges written once the card exists;
// Start opens the autopilot dialog on it; FromPicker sends the person
// back to the browse picker instead of the board.
type formResult struct {
	Kind        domain.Kind // "" reads as feature
	Desc        string
	Profile     string
	Envelope    *int // nil = the shell's default envelope
	Repo        string
	Severity    domain.Severity
	ExternalRef string
	Source      string // "manual", "github"
	Discussion  string // an imported issue's comments
	After       []domain.FeatureID
	Start       bool
	FromPicker  bool
}

// cardCreatedMsg is createCard's success: the shell reloads rows, keeps
// the repo as the next dialog's preselect, and does what the result
// asked for next (autopilot dialog, back to the picker).
type cardCreatedMsg struct {
	f          domain.Feature
	start      bool
	fromPicker bool
	warn       string // a dependency edge that could not be written
}

// createCard mints a card of any kind through cardmint.Mint — the same
// recipe `gummi run`, `bugs new` and the workspace MCP endpoint use — and
// then records its dependency edges. The old feature/bug/research
// create paths were three hand-rolled copies of that recipe; this is the
// one TUI caller now.
func (m *Shell) createCard(res formResult) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		kind := res.Kind
		if !kind.Valid() {
			kind = domain.KindFeature
		}
		env := m.envelope
		if res.Envelope != nil {
			env = *res.Envelope
		}
		f, err := cardmint.Mint(ctx, m.store, m.ws, cardmint.Input{
			Kind: kind, Description: res.Desc, Profile: res.Profile, Envelope: env,
			Repo: res.Repo, RequireRepo: m.requireRepo,
			ExternalRef: res.ExternalRef, Severity: res.Severity, Source: res.Source,
			Discussion: res.Discussion,
		})
		if err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		var warn []string
		for _, dep := range res.After {
			if err := m.store.AddDependency(ctx, f.ID, dep); err != nil {
				warn = append(warn, sanitize(err.Error()))
			}
		}
		return cardCreatedMsg{f: f, start: res.Start, fromPicker: res.FromPicker, warn: strings.Join(warn, "; ")}
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
	return fmt.Errorf("repository %q is not configured; add it to `repos:` in .gummi/config.yaml", name)
}

// duplicateFeature mints a fresh card from an existing one: same title,
// one-liner, kind, skip flags, profile, and budget envelope, starting
// over in todo with nothing spent. The original stays untouched — the
// copy is how a feature restarts from scratch without rewinding the
// workflow or losing the original's history and cost record. Nothing
// else carries over: the external ref stays on the original (re-ingest
// dedupe resolves items by ref, which must stay unambiguous) and the
// copy has no artifacts — a blank template is seeded when it enters
// design (spec.EnsureDraft). The shared slug is safe: branch, worktree,
// and artifact paths are all keyed by ID.
func (m *Shell) duplicateFeature(id domain.FeatureID) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		src, err := m.store.GetFeature(ctx, id)
		if err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
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
			Slug: src.Slug, Stage: workflow.Initial(),
			Profile: src.Profile, Budget: domain.Budget{Envelope: src.Budget.Envelope},
			CreatedAt: now, UpdatedAt: now,
		}
		if err := m.store.CreateFeature(ctx, &f); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		return noticeMsg{text: fmt.Sprintf("%s created — fresh copy of %s", newID, id), reload: true}
	}
}

// advanceStage moves the feature along its primary forward edge as the
// user — advanceStageAs(id, "user"). Every hand-driven call site (the
// board's own g, the spec view's approve) goes through this name
// unchanged; autopilot's own crossing (autopilot.go) calls advanceStageAs
// directly with state.ActorAutopilot instead.
func (m *Shell) advanceStage(id domain.FeatureID) tea.Cmd {
	return m.advanceStageAs(id, "user")
}

// autopilotGateBlockedMsg reports that actor state.ActorAutopilot's own
// attempt to cross a design gate (advanceStageAs) found the gate
// blocked — an open %%/diff thread, an unmet dependency, or the
// document floor. Autopilot cannot resolve any of those itself, so the
// card must park exactly as if autopilot had never tried the crossing;
// the Update handler (shell.go) raises the attention item that the
// event which tried autopilot first skipped in favor of the attempt.
// It carries plain text rather than an AdvanceStatus because every
// blocked branch below already renders the right explanation once, for
// both actors — the handler adds no second rendering of its own: it
// parks with this text and re-words the standing gate decision with it,
// so the card's waiting-on-you record names the blocker Advance named
// instead of the crossing's inviting wording (shell.go's
// rewordGateDecision).
type autopilotGateBlockedMsg struct {
	id   domain.FeatureID
	text string
}

// autopilotContinueMsg follows a crossing autopilot made itself onto an
// autonomous stage. The card now sits at a stage with nothing running,
// which is decisionIdle — the last kind in §10.17's table, and the one
// that makes the rest of the table mean anything: crossing a gate and
// then sitting at the stage behind it would leave "it runs to a verified
// branch on its own" false, and would leave `gates` promising that
// design gates cross themselves while the card stopped anyway, one stage
// further along.
//
// It is sent only for autopilot's OWN crossing. An idle card the board
// merely finds that way — restored at startup, parked by a quit — is
// never started down this path, because nothing resumes itself after a
// quit without being asked (§10.17, and the reason quitresume.go's
// dialog exists at all).
type autopilotContinueMsg struct {
	id   domain.FeatureID
	to   domain.Stage
	note string
}

// blockedMsg maps one blocked Advance outcome to the message the calling
// actor should see: a human gets the plain error notice advanceStage has
// always returned; autopilot — which only attempted the crossing because
// autopilotCrossGate (autopilot.go) had already decided the mode and the
// edge allow it — gets autopilotGateBlockedMsg instead, so Update parks
// the card rather than just flashing an error nobody is watching for.
func blockedMsg(actor string, id domain.FeatureID, text string) tea.Msg {
	if actor == state.ActorAutopilot {
		return autopilotGateBlockedMsg{id: id, text: text}
	}
	return noticeMsg{text: text, isErr: true}
}

// advanceStageAs is advanceStage's actor-parameterized form: it runs the
// engine's shared advance floor (engine.Advance) — the same gate
// mechanics the headless driver uses, so the quality floor can't fork
// between the two — and maps the typed result back to the board's
// notices and follow-on commands. The blocker checks, worktree creation,
// artifact promotion, plan-time estimation, and the recorded transition
// all live in the engine now (DESIGN §10.11, §6.1, §5.1); this wrapper's
// only actor-specific behavior is where a blocked outcome goes
// (blockedMsg above) — a successful crossing is identical either way,
// including which actor Store.Transition records on the gate event.
func (m *Shell) advanceStageAs(id domain.FeatureID, actor string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		// A static board (no coding agent) still advances: engine.Advance
		// touches only the store, worktrees, and workspace, so a transient
		// agent-less engine runs the same floor and closes. When an engine
		// is wired, its Advance also drops the stale stage session.
		eng := m.engine
		if eng == nil {
			eng = engine.New(engine.Config{Store: m.store, Pool: m.wt, Workspace: m.ws})
			defer func() { _ = eng.Close() }()
		}
		res, err := eng.Advance(ctx, id, actor)
		if err != nil {
			// actor-aware like the blocked statuses below, and for the same
			// reason: the caller that tried this crossing skipped its own
			// raiseAttention on the strength of the attempt, so an error
			// that only became a notice would leave the card with an open
			// decision row, nothing in the needs-you queue, and no sign
			// anything had gone wrong until the next restart re-seeded it.
			return blockedMsg(actor, id, sanitize(err.Error()))
		}
		switch res.Status {
		case engine.StatusNoop:
			return noticeMsg{text: fmt.Sprintf("%s is done — nothing to advance", id), clearInbox: id}
		case engine.StatusBlockedQuestions:
			// unresolved user %% annotations block every human gate — g
			// re-gates only once they resolve (DESIGN §6.1).
			surface := "spec"
			if res.Feature.Kind == domain.KindBug {
				surface = "report"
			}
			text := fmt.Sprintf("%s: %d open question(s) block approval — resolve them or press R in the %s view", id, res.Blockers, surface)
			return blockedMsg(actor, id, text)
		case engine.StatusBlockedDiff:
			text := fmt.Sprintf("%s: %d open diff comment(s) block approval — resolve them (x) or press R in the diff view", id, res.Blockers)
			return blockedMsg(actor, id, text)
		case engine.StatusBlockedOmission:
			return blockedMsg(actor, id, res.Reason)
		case engine.StatusBlockedUndrafted:
			text := fmt.Sprintf("%s: %s wrote nothing in %s — the gate stays shut until the section is drafted", id, res.From, strings.Join(res.Undrafted, ", "))
			return blockedMsg(actor, id, text)
		case engine.StatusBlockedDependency:
			names := make([]string, 0, len(res.BlockingDeps))
			for _, d := range res.BlockingDeps {
				names = append(names, d.String())
			}
			text := fmt.Sprintf("%s: blocked by unmet dependency %s — land it before this card can start coding", id, strings.Join(names, ", "))
			return blockedMsg(actor, id, text)
		case engine.StatusBlockedDocument:
			// the deterministic citation/coverage floor (internal/verifydoc)
			// failed — the document stays at verify rather than reaching done
			// on a broken citation or an unmapped brief question.
			rep := res.DocumentReport
			text := fmt.Sprintf("%s: document floor failed — %d open thread(s), %d broken citation(s), %d unmapped question(s)",
				id, rep.OpenThreads, len(rep.Citations), len(rep.Coverage))
			return blockedMsg(actor, id, text)
		case engine.StatusNeedsMerge:
			// verify→done is the user's "this feature is done" decision: the
			// merge flow (user-written message → squash merge) finishes the
			// transition to Done itself. Autopilot never reaches this branch
			// — autopilotForward excludes verify, because landing on main
			// stays a keypress (DESIGN §10.17) — so it is never worth a
			// blockedMsg-style fork; a mergeThenDoneMsg is the right answer
			// for whichever actor somehow got here.
			return mergeThenDoneMsg{f: res.Feature}
		}
		// StatusAdvanced: show the transition notice, then kick off the
		// background one-shot passes — check discovery whenever a fresh
		// worktree was created (both kinds), and the scribe envelope pass on
		// spec approval in estimation mode only (an explicit GUMMI_ENVELOPE
		// wins, so the UI default gates it here, not the engine).
		note := fmt.Sprintf("%s → %s", id, res.To) + res.EstimateNotice()
		discover := res.EnteredWorktree
		est := res.From == domain.StagePlan && m.envelope == 0
		continueTo := domain.Stage("")
		if actor == state.ActorAutopilot && autonomousStage(res.To) {
			continueTo = res.To
		}
		if discover || est {
			// the crossing entered a worktree, so the background one-shot
			// passes go first — but the continuation rides along rather
			// than being dropped here. Entering a worktree is exactly what
			// a spec approval does, which made this the branch autopilot's
			// own handover took, and it used to end the story: the gate
			// crossed and nothing behind it ever started.
			return worktreeEnteredMsg{id: id, note: note, discover: discover, estimate: est, continueTo: continueTo}
		}
		if continueTo != "" {
			return autopilotContinueMsg{id: id, to: continueTo, note: note}
		}
		return noticeMsg{text: note, reload: true, clearInbox: id}
	}
}

// worktreeEnteredMsg is emitted when an approval gate moves a feature
// into its first worktree stage, so the shell can kick off the
// background passes over the now-committed artifact.
type worktreeEnteredMsg struct {
	id       domain.FeatureID
	note     string
	discover bool // run check auto-discovery
	estimate bool // run the scribe envelope pass
	// continueTo is the autonomous stage autopilot's own crossing opened
	// and must now start, or "" for a crossing nobody is continuing. It
	// rides this message because entering a worktree is what a spec
	// approval does, so the handover's own crossing lands here rather
	// than on autopilotContinueMsg — and dropping it here left the gate
	// crossed with nothing behind it running.
	continueTo domain.Stage
}

// checksDiscoveredMsg follows the check auto-discovery pass, whether or
// not it wrote anything: the shell chains the baseline run off it, and
// a hand-authored block (discovery no-ops) deserves a baseline too.
type checksDiscoveredMsg struct {
	id domain.FeatureID
	n  int // checks discovered; 0 when the block pre-existed or discovery failed
}

// baselineDoneMsg carries the baseline run's outcome back to the shell.
type baselineDoneMsg struct {
	id      domain.FeatureID
	results []verify.Result
	err     error // malformed block or run/persist failure
}

// scribeEstimateDoneMsg follows the envelope-estimate pass, whether or
// not it changed anything: the shell needs to hear back on every exit
// path, not just the success one, so the card's in-flight scribe count
// always settles. blended is the new envelope value on success, 0 on
// every early-out (store lookup failure, engine error or non-positive
// estimate, an unchanged blend, or a persist failure) — none of those
// distinguish from each other, only from a real change.
type scribeEstimateDoneMsg struct {
	id      domain.FeatureID
	blended int
}

// discoverChecks runs a one-shot scribe pass that surveys the fresh
// worktree and records the repo's build/test/lint commands in the
// artifact's Verification section as a gummi-checks block (skipped when
// a block is already there). Best-effort: on failure the block stays
// absent and the Verify agent discovers the commands itself. Always
// resolves to checksDiscoveredMsg so the baseline run chains behind it.
func (m *Shell) discoverChecks(id domain.FeatureID) tea.Cmd {
	if m.engine == nil {
		return nil
	}
	m.scribing[id]++
	return func() tea.Msg {
		ctx := context.Background()
		f, err := m.store.GetFeature(ctx, id)
		if err != nil {
			return checksDiscoveredMsg{id: id}
		}
		checks, err := m.engine.DiscoverChecks(ctx, f)
		if err != nil {
			return checksDiscoveredMsg{id: id}
		}
		return checksDiscoveredMsg{id: id, n: len(checks)}
	}
}

// baselineChecks runs the artifact's gummi-checks once on the fresh
// worktree and persists the outcomes as the feature's baseline, so a
// malformed or already-failing command surfaces now — at approval,
// while the architect can still fix the block — instead of reading as
// the feature's fault at verify.
func (m *Shell) baselineChecks(id domain.FeatureID) tea.Cmd {
	if m.engine == nil {
		return nil
	}
	return func() tea.Msg {
		ctx := context.Background()
		f, err := m.store.GetFeature(ctx, id)
		if err != nil {
			return baselineDoneMsg{id: id, err: err}
		}
		results, err := m.engine.BaselineChecks(ctx, f)
		return baselineDoneMsg{id: id, results: results, err: err}
	}
}

// scribeEstimate runs a scribe-agent pass over the approved spec and, if
// it returns a usable number, blends it with the historical estimate and
// updates the envelope (DESIGN §5.1). Best-effort: any failure or an
// unparseable reply leaves the envelope as the historical estimate.
func (m *Shell) scribeEstimate(id domain.FeatureID) tea.Cmd {
	if m.engine == nil {
		return nil
	}
	m.scribing[id]++
	return func() tea.Msg {
		ctx := context.Background()
		f, err := m.store.GetFeature(ctx, id)
		if err != nil {
			return scribeEstimateDoneMsg{id: id}
		}
		scribe, err := m.engine.Estimate(ctx, f)
		if err != nil || scribe <= 0 {
			return scribeEstimateDoneMsg{id: id}
		}
		blended := int(domain.BlendEstimate(float64(f.Budget.Envelope), scribe))
		// a user-chosen GUMMI_ENVELOPE is a floor: the blend may raise it
		// for an expensive-looking feature, never silently undercut it
		if m.envelope > 0 && blended < m.envelope {
			blended = m.envelope
		}
		if blended == f.Budget.Envelope {
			return scribeEstimateDoneMsg{id: id}
		}
		f.Budget.Envelope = blended
		if err := m.store.UpdateFeature(ctx, &f); err != nil {
			return scribeEstimateDoneMsg{id: id}
		}
		return scribeEstimateDoneMsg{id: id, blended: blended}
	}
}

// rebaseFeature rebases a feature's branch onto main from the TUI
// (DESIGN §9 M4). It refuses a dirty worktree (so nothing uncommitted is
// risked), and when the rebase can't apply cleanly (it self-aborts,
// leaving the worktree untouched) it offers the agent hand-off — or,
// with no engine, reports the conflicted files to resolve by hand.
func (m *Shell) rebaseFeature(f domain.Feature) tea.Cmd {
	return m.cardLocked(f.ID, func() tea.Msg {
		ctx := context.Background()
		if ok, err := m.wt.Exists(ctx, &f); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		} else if !ok {
			return noticeMsg{text: noWorktreeYet(f), isErr: true}
		}
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
				return noticeMsg{text: string(f.ID) + ": worktree has uncommitted changes — commit them before rebasing", isErr: true}
			}
		}
		if autostash {
			if err := m.wt.RebaseOnMainAutostash(ctx, &f); err != nil {
				return noticeMsg{text: sanitize(err.Error()), isErr: true}
			}
		} else if err := m.wt.RebaseOnMain(ctx, &f); err != nil {
			var ce *worktree.RebaseConflictError
			if errors.As(err, &ce) {
				if m.engine != nil {
					return rebaseConflictMsg{f: f, files: ce.Files}
				}
				// ce carries git-derived file names; sanitize like every
				// other notice before it reaches the terminal.
				return noticeMsg{text: sanitize(string(f.ID) + ": " + ce.Error() + " — resolve on the branch, then retry"), isErr: true}
			}
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		// Re-anchor the recorded fork to main's HEAD after the rebase, so a
		// drifted feature is cleared in the same gesture and a fresh one does
		// not go stale on the next innocent rewrite of main.
		if err := m.wt.ReanchorOnMain(ctx, &f); err != nil {
			return noticeMsg{text: sanitize(fmt.Sprintf("%s: rebased but fork not re-anchored: %v", f.ID, err)), isErr: true}
		}
		return noticeMsg{text: string(f.ID) + " rebased onto main", reload: true}
	})
}

// cleanupLanded removes a landed feature's worktree and branch, keeping
// the feature record (it stays on the board as a done entry). It
// re-checks Landed at run time so a stale board row can't trigger a
// cleanup of unmerged work (DESIGN §9 M4, §10 landed-branch detection).
func (m *Shell) cleanupLanded(f domain.Feature) tea.Cmd {
	return m.cardLocked(f.ID, func() tea.Msg {
		ctx := context.Background()
		landed, err := m.wt.Landed(ctx, &f)
		if err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		if !landed {
			return noticeMsg{text: string(f.ID) + " hasn't landed on main yet — nothing to clean up", isErr: true}
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
				return noticeMsg{text: string(f.ID) + " has uncommitted changes on its branch — commit or discard them before cleanup", isErr: true}
			}
			// force: only disposable untracked artifacts remain now, and a
			// non-force remove would abort on them. The confirm dialog spells
			// this out.
			if err := m.wt.Remove(ctx, &f, true); err != nil {
				return noticeMsg{text: sanitize(err.Error()), isErr: true}
			}
		}
		// A research card runs in a scratch tree rather than a branch
		// worktree. Disposable by construction, and absent is success, so
		// this runs unconditionally for every kind.
		if err := m.wt.RemoveScratch(ctx, &f); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		if ok, err := m.wt.BranchExists(ctx, &f); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		} else if ok {
			// git's own merged-check (-d) still backstops regular merges;
			// for a squash merge, whose commits aren't ancestors of main,
			// the delete re-verifies with the merge-tree content check —
			// stronger than the ancestor test — before forcing.
			if err := m.wt.DeleteLandedBranch(ctx, &f); err != nil {
				return noticeMsg{text: sanitize(err.Error()), isErr: true}
			}
		}
		return noticeMsg{text: string(f.ID) + " cleaned up — worktree and merged branch removed", reload: true}
	})
}

// dropSession ends and forgets a feature's engine session and clears
// any needs-attention item for it.
func (m *Shell) dropSession(id domain.FeatureID) {
	if m.engine != nil {
		m.engine.Drop(id)
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
	return spec.Promote(
		filepath.Join(m.wt.Root(), f.ArtifactPath()),
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
	for _, p := range []string{
		filepath.Join(m.wt.Root(), f.ArtifactPath()),
		filepath.Join(m.ws.DraftsDir(), spec.DraftFilename(f)),
		filepath.Join(m.wt.Root(), f.WorktreePath(), f.ArtifactPath()),
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// undraftedGate names the required section(s) the departing stage left
// blank — the same predicate engine.Advance applies when the gate refuses
// (engine.UndraftedGateSections), read live from the artifact so the
// decision panel can name the blocker before the crossing is attempted
// and the critique loop can send the stage's writer back instead of
// re-raising a gate approving cannot cross. Nil for an edge that owes no
// sections — a research card's stages among them.
func (m *Shell) undraftedGate(f domain.Feature) []string {
	path := m.artifactFile(&f)
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return engine.UndraftedGateSections(f.Kind, f.Stage, forwardEdge(f), string(raw))
}

// bounceStage sends a feature back for rework. Implement and Verify
// bounce via the domain transition into Plan/Implement (the rerun
// edges) — the edges into Implement and Plan are normally forward moves
// belonging to g, so only those two stages take them backward. Plan
// bounces without leaving the stage: it sits *before* the work stage,
// so there is no stage to transition back to — instead it resets the
// critique loop's round counter and re-runs the same in-stage replan
// onPlanDone's automatic path already uses, giving the human a fresh
// capped budget instead of g's silent override of an unresolved
// "changes needed" verdict. Only legal once the loop has actually
// escalated — see the attnGate check below; a plan that hasn't hit that
// gate has nothing to bounce.

// note is the prose the composer aimed at the bounce: for
// Implement/Verify it rides the reborn stage's kickoff when that run
// starts (shell.go's bounceNotes); for Plan there is no later kickoff
// to catch it, so it is appended to the replan kickoff directly. Empty
// for the plain b key either way.
//
// bounceStage itself runs on the Update goroutine (every call site
// invokes it directly from a key/decision handler, never from inside a
// tea.Cmd), so the store read that decides which branch applies, and
// every write this function makes to Shell's own unguarded fields
// (m.rounds via setRound, m.bounceNotes), happen here, synchronously,
// before any tea.Cmd is returned — matching how onPlanDone resets the
// same round counter. Only the engine/store calls that do real I/O
// (RunWith, Transition) are deferred into the returned cmd, which runs
// later on its own goroutine.
func (m *Shell) bounceStage(id domain.FeatureID, note string) tea.Cmd {
	ctx := context.Background()
	f, err := m.store.GetFeature(ctx, id)
	if err != nil {
		return func() tea.Msg { return noticeMsg{text: err.Error(), isErr: true} }
	}
	if f.Stage == domain.StagePlan {
		it, ok := m.inbox.get(id)
		if !ok || it.Kind != attnGate || !it.Escalated {
			text := fmt.Sprintf("%s plan has no escalated gate to bounce", id)
			return func() tea.Msg { return noticeMsg{text: text, isErr: true} }
		}
		if err := rounds.Reset(ctx, m.roundStore, id, domain.RoundKindPlan); err != nil {
			return func() tea.Msg { return noticeMsg{text: err.Error(), isErr: true} }
		}
		m.setRound(id, domain.RoundKindPlan, 0)
		m.dropSession(id)
		kickoff := replanNote
		if note != "" {
			kickoff += "\n\n" + note
		}
		return func() tea.Msg {
			if err := m.engine.RunWith(f, kickoff); err != nil {
				return noticeMsg{text: err.Error(), isErr: true}
			}
			text := fmt.Sprintf("%s sent back for another plan round", id)
			if note != "" {
				text += " — your line rides the kickoff"
			}
			return noticeMsg{text: text, reload: true, clearInbox: id}
		}
	}
	// each stage takes its own rerun edge backward: verify rewinds the
	// work stage, implement rewinds the plan that produced it. The
	// escalated-plan branch above re-runs in place and is not an edge.
	back, ok := workflow.RerunTarget(f.Stage)
	if !ok {
		text := fmt.Sprintf("%s is in %s — only plan/implement/verify can bounce back", id, f.Stage)
		return func() tea.Msg { return noticeMsg{text: text, isErr: true} }
	}
	if note != "" {
		if m.bounceNotes == nil {
			m.bounceNotes = map[domain.FeatureID]string{}
		}
		m.bounceNotes[id] = note
	}
	m.dropSession(id)
	return func() tea.Msg {
		if _, err := m.store.Transition(ctx, id, back, "user"); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		text := fmt.Sprintf("%s bounced back to %s", id, back)
		if note != "" {
			text += " — your line rides the next run's kickoff"
		}
		return noticeMsg{text: text, reload: true, clearInbox: id}
	}
}

// deleteFeature removes worktree, branch, and record.
func (m *Shell) deleteFeature(id domain.FeatureID) tea.Cmd {
	return m.cardLocked(id, func() tea.Msg {
		ctx := context.Background()
		f, err := m.store.GetFeature(ctx, id)
		if err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		if ok, err := m.wt.Exists(ctx, &f); err == nil && ok {
			if err := m.wt.Remove(ctx, &f, true); err != nil {
				return noticeMsg{text: err.Error(), isErr: true}
			}
		}
		// ...and the scratch tree, which is the only tree a research card
		// ever has.
		if err := m.wt.RemoveScratch(ctx, &f); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		// a feature that never left Spec has no branch — only delete
		// one that exists
		if ok, err := m.wt.BranchExists(ctx, &f); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		} else if ok {
			if err := m.wt.DeleteBranch(ctx, &f, true); err != nil {
				return noticeMsg{text: err.Error(), isErr: true}
			}
		}
		if err := m.store.DeleteFeature(ctx, id); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		// the artifact and its draft are workspace files keyed to the
		// record — they go with it (best effort: an orphan is only clutter)
		_ = os.RemoveAll(filepath.Join(m.wt.Root(), f.ArtifactPath()))
		_ = os.RemoveAll(filepath.Join(m.ws.DraftsDir(), spec.DraftFilename(&f)))
		// the live-stream mirror is keyed to the record too; without this
		// a deleted card's last session would linger as a watchable file
		// (harmless — its owner is gone — but clutter all the same).
		_ = os.Remove(m.ws.LiveFile(id))
		m.dropSession(id)
		return noticeMsg{text: fmt.Sprintf("%s deleted", id), reload: true}
	})
}
