package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/atomicfile"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
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
	BaselineFails    int // gummi-checks already failing on the fresh branch
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
	// clearInbox, when non-empty, names a feature whose needs-attention
	// entry is removed on receipt of this notice — the outcome-driven
	// counterpart to pre-dispatch removal (see the key handler). It is
	// set only on the success returns of the board actions; error and
	// gate-blocked returns leave it empty so the attention item survives
	// until the thing is actually attended to.
	clearInbox domain.FeatureID
}

// chatAttachedMsg carries the result of an interactive Attach that ran in
// a command (backend spawn is slow); the Update loop opens the pane.
type chatAttachedMsg struct {
	feature domain.Feature
	session *engine.Session
	err     error
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
		if bl, err := m.store.CheckBaseline(ctx, f.ID); err == nil {
			for _, r := range bl {
				if !r.OK {
					row.BaselineFails++
				}
			}
		}
		rows = append(rows, row)
	}
	return rowsMsg{rows: rows}
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

// formResult carries the new-feature form's fields. The description's
// first line is the feature's title; the lines past it are seeded into
// the spec, which the brainstorm stage develops.
type formResult struct {
	Desc     string
	Profile  string
	Skip     domain.SkipFlags
	Envelope *int
}

// createFeature mints a number and persists a new feature in todo,
// seeding the spec draft with the description when it runs past one
// line — the brainstorm stage picks the Problem section up from there.
func (m *Shell) createFeature(res formResult) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		// the first line becomes a concise card title with its full text
		// kept as the one-liner (the card body), so the title slot isn't
		// the whole description.
		title, oneLiner, seed := domain.SplitFreeform(res.Desc)
		slug, err := domain.Slugify(title)
		if err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		num, err := m.store.MintFeatureNum(ctx, m.ws.SeqFile())
		if err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		id, err := domain.NewFeatureID(num)
		if err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		now := m.now()
		env := m.envelope
		if res.Envelope != nil {
			env = *res.Envelope
		}
		f := domain.Feature{
			ID: id, Num: num, Title: title, OneLiner: oneLiner,
			Slug: slug, Stage: workflow.Initial(domain.KindFeature), Skip: res.Skip,
			Profile: res.Profile, Budget: domain.Budget{Envelope: env},
			CreatedAt: now, UpdatedAt: now,
		}
		// Seed the draft first (so the description survives), then persist —
		// a persisted feature with no draft would be reseeded blank. A
		// title-sized description seeds nothing: the blank template's
		// prompts do more for brainstorm than an echoed title would.
		if seed != "" {
			draft := filepath.Join(m.ws.DraftsDir(), spec.DraftFilename(&f))
			content := spec.SeededTemplate(&f, domain.DraftSeed{Problem: seed}, domain.DraftProvenance{})
			if err := os.MkdirAll(m.ws.DraftsDir(), 0o750); err != nil {
				return noticeMsg{text: err.Error(), isErr: true}
			}
			if err := atomicfile.Write(draft, []byte(content), 0o600); err != nil {
				return noticeMsg{text: err.Error(), isErr: true}
			}
		}
		if err := m.store.CreateFeature(ctx, &f); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		return noticeMsg{text: fmt.Sprintf("%s created", id), reload: true}
	}
}

// bugFormResult carries the new-bug form's fields. Like a feature, the
// title is the card title; the symptoms are seeded into the report and
// triage develops the rest.
type bugFormResult struct {
	Title    string
	OneLiner string
	Severity domain.Severity
	Profile  string
	Skip     domain.SkipFlags
	Envelope *int
}

// createBug mints a BG number and persists a new bug in todo, seeding its
// report with the one-liner and severity so nothing the user typed is
// lost (triage fills reproduction and root cause).
func (m *Shell) createBug(res bugFormResult) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		slug, err := domain.Slugify(res.Title)
		if err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		num, err := m.store.MintFeatureNum(ctx, m.ws.SeqFile())
		if err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		id, err := domain.NewID(domain.KindBug, num)
		if err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		now := m.now()
		env := m.envelope
		if res.Envelope != nil {
			env = *res.Envelope
		}
		f := domain.Feature{
			ID: id, Num: num, Kind: domain.KindBug, Title: res.Title, OneLiner: res.OneLiner,
			Slug: slug, Stage: workflow.Initial(domain.KindBug), Skip: res.Skip,
			Profile: res.Profile, Budget: domain.Budget{Envelope: env},
			Severity: res.Severity, CreatedAt: now, UpdatedAt: now,
		}
		// Seed the report draft first (so severity/one-liner survive), then
		// persist — a persisted bug with no draft would be reseeded blank.
		draft := filepath.Join(m.ws.DraftsDir(), spec.DraftFilename(&f))
		content := spec.SeededBugTemplate(&f, domain.BugReport{}, domain.BugProvenance{Source: "manual"}, res.Severity)
		if err := os.MkdirAll(m.ws.DraftsDir(), 0o750); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		if err := atomicfile.Write(draft, []byte(content), 0o600); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		if err := m.store.CreateFeature(ctx, &f); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		return noticeMsg{text: fmt.Sprintf("%s created", id), reload: true}
	}
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
			Slug: src.Slug, Stage: workflow.Initial(src.Kind), Skip: src.Skip,
			Profile: src.Profile, Budget: domain.Budget{Envelope: src.Budget.Envelope},
			CreatedAt: now, UpdatedAt: now,
		}
		if err := m.store.CreateFeature(ctx, &f); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		return noticeMsg{text: fmt.Sprintf("%s created — fresh copy of %s", newID, id), reload: true}
	}
}

// routeViaPlan restores the Plan stage on a feature created with it
// skipped (the quick route, or an explicit plan skip): the escalation
// path when the spec reveals the work is bigger than the route assumed.
// Loosening a skip only ever adds a stage back, so it is safe after
// creation — the reverse (skipping a stage mid-flight) never is.
// Clearing Quick with it keeps the flag invariant (quick implies both
// skips): the card simply becomes a skip-brainstorm feature, and a
// fresh spec session picks up the standard convergence contract.
func (m *Shell) routeViaPlan(id domain.FeatureID) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		f, err := m.store.GetFeature(ctx, id)
		if err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		if f.Kind == domain.KindBug {
			return noticeMsg{text: fmt.Sprintf("%s: plan is a feature stage — bugs route triage → diagnose → fix", id), isErr: true}
		}
		if !f.Skip.Plan {
			return noticeMsg{text: fmt.Sprintf("%s already routes through plan", id), isErr: true}
		}
		// past Spec the plan stage is already behind the feature; there is
		// nothing left to restore it in front of.
		if f.Stage != domain.StageTodo && f.Stage != domain.StageBrainstorm && f.Stage != domain.StageSpec {
			return noticeMsg{text: fmt.Sprintf("%s is in %s — the plan stage is already behind it", id, f.Stage), isErr: true}
		}
		f.Skip.Plan = false
		f.Skip.Quick = false
		if err := m.store.UpdateFeature(ctx, &f); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		return noticeMsg{text: fmt.Sprintf("%s will route through plan — spec approval now leads there", id), reload: true}
	}
}

// advanceStage moves the feature along its primary forward edge by
// running the engine's shared advance floor (engine.Advance) — the same
// gate mechanics the headless driver uses, so the quality floor can't
// fork between the two. This wrapper only maps the typed result back to
// the board's notices and follow-on commands; the blocker checks,
// worktree creation, artifact promotion, plan-time estimation, and the
// recorded transition all live in the engine now (DESIGN §10.11, §6.1,
// §5.1). When the feature leaves Spec its worktree and branch are created
// there first.
func (m *Shell) advanceStage(id domain.FeatureID) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		// A static board (no coding agent) still advances: engine.Advance
		// touches only the store, worktrees, and workspace, so a transient
		// agent-less engine runs the same floor and closes. When an engine
		// is wired, its Advance also drops the stale stage session.
		eng := m.engine
		if eng == nil {
			eng = engine.New(engine.Config{Store: m.store, Worktrees: m.wt, Workspace: m.ws})
			defer func() { _ = eng.Close() }()
		}
		res, err := eng.Advance(ctx, id, "user")
		if err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
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
			return noticeMsg{
				text:  fmt.Sprintf("%s: %d open question(s) block approval — resolve them or press R in the %s view", id, res.Blockers, surface),
				isErr: true,
			}
		case engine.StatusBlockedDiff:
			return noticeMsg{
				text:  fmt.Sprintf("%s: %d open diff comment(s) block approval — resolve them (x) or press R in the diff view", id, res.Blockers),
				isErr: true,
			}
		case engine.StatusNeedsMerge:
			// verify→done is the user's "this feature is done" decision: the
			// merge flow (user-written message → squash merge) finishes the
			// transition to Done itself.
			return mergeThenDoneMsg{f: res.Feature}
		}
		// StatusAdvanced: show the transition notice, then kick off the
		// background one-shot passes — check discovery whenever a fresh
		// worktree was created (both kinds), and the scribe envelope pass on
		// spec approval in estimation mode only (an explicit GUMMI_ENVELOPE
		// wins, so the UI default gates it here, not the engine).
		note := fmt.Sprintf("%s → %s", id, res.To) + res.EstimateNotice()
		discover := res.EnteredWorktree
		est := res.From == domain.StageSpec && m.envelope == 0
		if discover || est {
			return worktreeEnteredMsg{id: id, note: note, discover: discover, estimate: est}
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
	return func() tea.Msg {
		ctx := context.Background()
		f, err := m.store.GetFeature(ctx, id)
		if err != nil {
			return nil
		}
		scribe, err := m.engine.Estimate(ctx, f)
		if err != nil || scribe <= 0 {
			return nil
		}
		blended := int(domain.BlendEstimate(float64(f.Budget.Envelope), scribe))
		// a user-chosen GUMMI_ENVELOPE is a floor: the blend may raise it
		// for an expensive-looking feature, never silently undercut it
		if m.envelope > 0 && blended < m.envelope {
			blended = m.envelope
		}
		if blended == f.Budget.Envelope {
			return nil
		}
		f.Budget.Envelope = blended
		if err := m.store.UpdateFeature(ctx, &f); err != nil {
			return nil
		}
		return noticeMsg{text: fmt.Sprintf("%s: scribe sized the envelope at %d credits", id, blended), reload: true}
	}
}

// rebaseFeature rebases a feature's branch onto main from the TUI
// (DESIGN §9 M4). It refuses a dirty worktree (so nothing uncommitted is
// risked), and when the rebase can't apply cleanly (it self-aborts,
// leaving the worktree untouched) it offers the agent hand-off — or,
// with no engine, reports the conflicted files to resolve by hand.
func (m *Shell) rebaseFeature(f domain.Feature) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if ok, err := m.wt.Exists(ctx, &f); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		} else if !ok {
			return noticeMsg{text: string(f.ID) + " has no worktree yet (created at spec approval)", isErr: true}
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
	}
}

// cleanupLanded removes a landed feature's worktree and branch, keeping
// the feature record (it stays on the board as a done entry). It
// re-checks Landed at run time so a stale board row can't trigger a
// cleanup of unmerged work (DESIGN §9 M4, §10 landed-branch detection).
func (m *Shell) cleanupLanded(f domain.Feature) tea.Cmd {
	return func() tea.Msg {
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
	}
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

// bounceStage sends a review/verify feature back to implement (the
// rerun edge). Only those two stages bounce: from anywhere else the
// edge into Implement is a forward move and belongs to g.
func (m *Shell) bounceStage(id domain.FeatureID) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		f, err := m.store.GetFeature(ctx, id)
		if err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		if f.Stage != domain.StageReview && f.Stage != domain.StageVerify {
			return noticeMsg{text: fmt.Sprintf("%s is in %s — only review/verify can bounce back", id, f.Stage), isErr: true}
		}
		back := workflow.WorkStage(f.Kind)
		if _, err := m.store.Transition(ctx, id, back, "user"); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		m.dropSession(id)
		return noticeMsg{text: fmt.Sprintf("%s bounced back to %s", id, back), reload: true, clearInbox: id}
	}
}

// deleteFeature removes worktree, branch, and record.
func (m *Shell) deleteFeature(id domain.FeatureID) tea.Cmd {
	return func() tea.Msg {
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
		m.dropSession(id)
		return noticeMsg{text: fmt.Sprintf("%s deleted", id), reload: true}
	}
}
