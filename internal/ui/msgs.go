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
// status bar.
type noticeMsg struct {
	text  string
	isErr bool
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
				if landed, err := m.wt.Landed(ctx, &f); err == nil {
					row.Landed = landed
				}
			}
		}
		row.OpenSpecQs = m.openQuestionsBlockingGate(ctx, f)
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

// formResult carries the new-feature form's fields. The description
// the user types is the feature's title; everything richer lives in
// the spec, which the brainstorm stage develops.
type formResult struct {
	Title   string
	Profile string
	Skip    domain.SkipFlags
}

// createFeature mints a number and persists a new feature in todo.
func (m *Shell) createFeature(res formResult) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		// the form takes one free-text line; a long line becomes a concise
		// card title with the full text kept as the one-liner (the card body),
		// so the title slot isn't the whole description.
		title, oneLiner := domain.SplitDescription(res.Title)
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
		f := domain.Feature{
			ID: id, Num: num, Title: title, OneLiner: oneLiner,
			Slug: slug, Stage: workflow.Initial(domain.KindFeature), Skip: res.Skip,
			Profile: res.Profile, Budget: domain.Budget{Envelope: m.envelope},
			CreatedAt: now, UpdatedAt: now,
		}
		if err := m.store.CreateFeature(ctx, &f); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		return noticeMsg{text: fmt.Sprintf("%s created", id)}
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
		f := domain.Feature{
			ID: id, Num: num, Kind: domain.KindBug, Title: res.Title, OneLiner: res.OneLiner,
			Slug: slug, Stage: workflow.Initial(domain.KindBug), Skip: res.Skip,
			Profile: res.Profile, Budget: domain.Budget{Envelope: m.envelope},
			CreatedAt: now, UpdatedAt: now,
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
		return noticeMsg{text: fmt.Sprintf("%s created", id)}
	}
}

// advanceStage moves the feature along its primary forward edge. When
// the feature leaves Spec (spec approval, DESIGN §10.11) its worktree
// and branch are created first.
func (m *Shell) advanceStage(id domain.FeatureID) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		f, err := m.store.GetFeature(ctx, id)
		if err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		var estimate string // set at spec approval by plan-time estimation
		nexts := workflow.Next(f.Kind, f.Stage, f.Skip)
		if len(nexts) == 0 {
			return noticeMsg{text: fmt.Sprintf("%s is done — nothing to advance", id)}
		}
		// prefer the skip edge when the flag opts the item out of the
		// intermediate stage, otherwise take the primary edge.
		next := nexts[len(nexts)-1]
		if f.Stage == domain.StageReview || f.Stage == domain.StageVerify {
			// the last edge out of review/verify is a rerun (→ implement/fix),
			// a bounce, not a forward move; g always goes forward.
			next = nexts[0]
		}
		// unresolved user %% annotations block every human gate, not just
		// spec approval — g re-gates only once they resolve (DESIGN §6.1).
		if n := m.openQuestionsBlockingGate(ctx, f); n > 0 {
			surface := "spec"
			if f.Kind == domain.KindBug {
				surface = "report"
			}
			return noticeMsg{
				text:  fmt.Sprintf("%s: %d open question(s) block approval — resolve them or press R in the %s view", id, n, surface),
				isErr: true,
			}
		}
		// so do unresolved diff annotations, the gate's other backend
		if n := m.openDiffCommentsBlockingGate(ctx, id); n > 0 {
			return noticeMsg{
				text:  fmt.Sprintf("%s: %d open diff comment(s) block approval — resolve them (x) or press R in the diff view", id, n),
				isErr: true,
			}
		}
		// Advancing out of Verify is the user's "this feature is done"
		// decision: the branch lands on main as one squash commit before the
		// record moves to Done. The merge flow (user-written message → merge)
		// finishes the transition itself. A branch that already landed — or
		// is already gone (merged and cleaned up outside gummi) — skips
		// straight to the transition.
		if next == domain.StageDone {
			if exists, err := m.wt.BranchExists(ctx, &f); err != nil {
				return noticeMsg{text: sanitize(err.Error()), isErr: true}
			} else if exists {
				if landed, err := m.wt.Landed(ctx, &f); err != nil {
					return noticeMsg{text: sanitize(err.Error()), isErr: true}
				} else if !landed {
					return mergeThenDoneMsg{f: f}
				}
			}
		}
		// Crossing from the design phase (todo / interactive) into the first
		// worktree stage is the approval gate: it creates the worktree and
		// commits the artifact (spec or bug report) to the branch. Bounces
		// (review/verify → work stage) already have a worktree, so this
		// fires exactly once, whichever design stage is being left.
		enteringWorktree := next != domain.StageTodo && !workflow.Interactive(next)
		existed := true
		if enteringWorktree {
			var err error
			if existed, err = m.wt.Exists(ctx, &f); err != nil {
				return noticeMsg{text: err.Error(), isErr: true}
			}
		}
		if enteringWorktree && !existed {
			if _, err := m.wt.Create(ctx, &f); err != nil {
				return noticeMsg{text: err.Error(), isErr: true}
			}
			// approval commits the artifact to the branch and retires the
			// draft (DESIGN §10.11)
			if err := m.migrateDraft(ctx, &f); err != nil {
				return noticeMsg{text: err.Error(), isErr: true}
			}
			// plan-time estimation is feature-specific (spec approval): size
			// the spend-plan envelope from what completed features cost,
			// before budgeted autonomous work begins (DESIGN §5.1).
			if f.Stage == domain.StageSpec {
				estimate = m.estimateEnvelope(ctx, &f)
			}
		}
		fromSpec := f.Stage == domain.StageSpec
		if _, err := m.store.Transition(ctx, id, next, "user"); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		m.dropSession(id) // the old stage's session is stale now
		note := fmt.Sprintf("%s → %s", id, next) + estimate
		// Approval kicks off the background one-shot passes: check
		// discovery whenever a fresh worktree was created (both kinds),
		// and the scribe envelope pass on spec approval in estimation
		// mode only (an explicit GUMMI_ENVELOPE wins).
		discover := enteringWorktree && !existed
		est := fromSpec && m.envelope == 0
		if discover || est {
			return worktreeEnteredMsg{id: id, note: note, discover: discover, estimate: est}
		}
		return noticeMsg{text: note}
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
		return noticeMsg{text: fmt.Sprintf("%s: scribe sized the envelope at %d credits", id, blended)}
	}
}

// estimateEnvelope sizes a feature's spend-plan envelope from the median
// spend of previously completed features and persists it, returning a
// notice suffix describing the estimate. It only fills an *unset*
// envelope (0), so an explicit GUMMI_ENVELOPE default a user chose is
// respected, not silently replaced. Empty when the envelope is already
// set, when there's no history to learn from, or on any error —
// estimation is best-effort and never blocks the transition.
func (m *Shell) estimateEnvelope(ctx context.Context, f *domain.Feature) string {
	if f.Budget.Envelope != 0 {
		return "" // an explicit envelope wins over estimation
	}
	feats, err := m.store.ListFeatures(ctx)
	if err != nil {
		return ""
	}
	var hist []domain.Spend
	for _, x := range feats {
		if x.ID != f.ID && x.Stage == domain.StageDone {
			hist = append(hist, x.Spend)
		}
	}
	env, n := domain.EstimateEnvelope(hist)
	if n == 0 || env <= 0 {
		return ""
	}
	f.Budget.Envelope = int(env)
	if err := m.store.UpdateFeature(ctx, f); err != nil {
		return ""
	}
	// n is the number of past features that metered spend (zero-spend
	// completions are not samples).
	return fmt.Sprintf(" · envelope estimated at %d credits from %d metered feature(s)", int(env), n)
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
		if dirty, err := m.wt.Dirty(ctx, &f); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		} else if dirty {
			return noticeMsg{text: string(f.ID) + ": worktree has uncommitted changes — commit them before rebasing", isErr: true}
		}
		if err := m.wt.RebaseOnMain(ctx, &f); err != nil {
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
		return noticeMsg{text: string(f.ID) + " rebased onto main"}
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
		return noticeMsg{text: string(f.ID) + " cleaned up — worktree and merged branch removed"}
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

// migrateDraft moves the artifact draft (spec or bug report) into the
// item's worktree as a committed file. An item that never had a draft
// gets one from its template — the branch always carries its artifact.
// Idempotent, keyed on git tracking (a merely-present uncommitted file is
// not migrated).
func (m *Shell) migrateDraft(ctx context.Context, f *domain.Feature) error {
	artifact := f.ArtifactPath()
	draftPath := filepath.Join(m.ws.DraftsDir(), spec.DraftFilename(f))
	if tracked, err := m.wt.FileCommitted(ctx, f, artifact); err != nil {
		return err
	} else if tracked {
		return os.RemoveAll(draftPath)
	}
	// content preference: the draft; else a stray uncommitted worktree
	// copy (e.g. a crashed earlier migration); else a fresh template
	wtArtifact := filepath.Join(m.wt.Root(), f.WorktreePath(), artifact)
	content := spec.Template(f)
	commitKind := "spec"
	if f.Kind == domain.KindBug {
		content = spec.BugTemplate(f)
		commitKind = "bug"
	}
	if raw, err := os.ReadFile(draftPath); err == nil {
		content = string(raw)
	} else if !os.IsNotExist(err) {
		return err
	} else if raw, err := os.ReadFile(wtArtifact); err == nil {
		content = string(raw)
	}
	if err := m.wt.CommitFile(ctx, f, artifact, content,
		fmt.Sprintf("docs(%s): %s %s", commitKind, f.ID, f.Title)); err != nil {
		return err
	}
	return os.RemoveAll(draftPath)
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
		return noticeMsg{text: fmt.Sprintf("%s bounced back to %s", id, back)}
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
		m.dropSession(id)
		return noticeMsg{text: fmt.Sprintf("%s deleted", id)}
	}
}
