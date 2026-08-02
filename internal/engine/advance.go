package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/workflow"
)

// AdvanceStatus classifies what Advance did (or why it could not move the
// item forward), so every driver — the TUI and the headless run loop —
// routes the outcome without re-deriving the state machine. Genuine
// infrastructure failures (store, git, promotion) are returned as errors
// instead; a Status describes a normal-flow gate outcome.
type AdvanceStatus int

const (
	// StatusAdvanced: the item transitioned to Result.To.
	StatusAdvanced AdvanceStatus = iota
	// StatusNoop: the item is terminal — nothing to advance.
	StatusNoop
	// StatusBlockedQuestions: unresolved user %% threads block the gate.
	StatusBlockedQuestions
	// StatusBlockedDiff: unresolved diff annotations block the gate.
	StatusBlockedDiff
	// StatusNeedsMerge: the verify→done gate, where the branch is ahead of
	// main and awaits a squash-merge decision. Advance never merges — the
	// caller lands the branch (the TUI collects a commit message; the
	// headless driver stops at the verified branch, DESIGN §10.6/§12).
	StatusNeedsMerge
)

// AdvanceResult reports the outcome of one Advance call. Only the fields
// relevant to Status are populated.
type AdvanceResult struct {
	// Feature is the item after the call: transitioned on StatusAdvanced,
	// otherwise the loaded (unchanged) record.
	Feature domain.Feature
	From    domain.Stage // the stage the item started in
	To      domain.Stage // the target stage (the intended forward edge)
	Status  AdvanceStatus
	// Blockers is the open-thread / open-comment count for the Blocked*
	// statuses.
	Blockers int
	// EnteredWorktree reports that this call created the item's worktree
	// (the design→work approval gate), so the caller can kick off the
	// one-shot check-discovery + baseline passes over the fresh branch.
	EnteredWorktree bool
	// EstimatedCredits / EstimateSamples record the historical-median
	// envelope estimate applied at spec approval (0 when none was). The
	// engine leaves the follow-on scribe estimate — an agent pass — to the
	// caller's policy: whether to run it (the TUI gates it on its default
	// envelope) is not a floor concern, so From is enough to key it on.
	EstimatedCredits int
	EstimateSamples  int
}

// Advance moves a feature along its primary forward edge — the engine-level
// quality floor both drivers share (DESIGN §8, the extraction of the TUI's
// former advanceStage). It owns the gate mechanics: blocker checks
// (unresolved %% threads and diff annotations block every human gate,
// §6.1), worktree creation and artifact promotion at the design→work
// approval gate (§10.11), plan-time envelope estimation at spec approval
// (§5.1), and the recorded store.Transition under actor. It never merges:
// the verify→done gate reports StatusNeedsMerge for the caller to land.
//
// Infrastructure failures surface as errors; a blocked/terminal/merge gate
// is a Status with no error, so a driver can style a notice or map a typed
// exit from the same return.
func (e *Engine) Advance(ctx context.Context, id domain.FeatureID, actor string) (AdvanceResult, error) {
	f, err := e.cfg.Store.GetFeature(ctx, id)
	if err != nil {
		return AdvanceResult{}, err
	}
	res := AdvanceResult{Feature: f, From: f.Stage}

	nexts := workflow.Next(f.Kind, f.Stage, f.Skip)
	if len(nexts) == 0 {
		res.Status = StatusNoop
		return res, nil
	}
	// prefer the skip edge when the flag opts the item out of the
	// intermediate stage, otherwise take the primary edge.
	next := nexts[len(nexts)-1]
	if f.Stage == domain.StageReview || f.Stage == domain.StageVerify {
		// the last edge out of review/verify is a rerun (→ implement/fix),
		// a bounce, not a forward move; Advance always goes forward.
		next = nexts[0]
	}
	res.To = next

	// unresolved user %% annotations block every human gate, not just spec
	// approval — the gate re-opens only once they resolve (DESIGN §6.1).
	if n := e.openQuestionsBlockingGate(f); n > 0 {
		res.Status, res.Blockers = StatusBlockedQuestions, n
		return res, nil
	}
	// so do unresolved diff annotations, the gate's other backend.
	if n := e.openDiffCommentsBlockingGate(ctx, id); n > 0 {
		res.Status, res.Blockers = StatusBlockedDiff, n
		return res, nil
	}

	// Advancing out of Verify is the "this feature is done" decision: the
	// branch lands on main as one squash commit before the record moves to
	// Done. Advance never merges — it reports that a landing is owed. A
	// branch that already landed, is already gone, or never got any commits
	// of its own (nothing to land — the artifact lives in the workspace,
	// not on the branch) skips straight to the transition.
	if next == domain.StageDone {
		if exists, err := e.cfg.Worktrees.BranchExists(ctx, &f); err != nil {
			return res, err
		} else if exists {
			if landed, err := e.cfg.Worktrees.Landed(ctx, &f); err != nil {
				return res, err
			} else if !landed {
				if ahead, err := e.cfg.Worktrees.BranchAhead(ctx, &f); err != nil {
					return res, err
				} else if ahead {
					res.Status = StatusNeedsMerge
					return res, nil
				}
			}
		}
	}

	// Crossing from the design phase (todo / interactive) into the first
	// worktree stage is the approval gate: it creates the worktree and
	// promotes the artifact (spec or bug report) to its workspace home.
	// Bounces (review/verify → work stage) already have a worktree, so this
	// fires exactly once, whichever design stage is being left.
	enteringWorktree := next != domain.StageTodo && !workflow.Interactive(next)
	existed := true
	if enteringWorktree {
		if existed, err = e.cfg.Worktrees.Exists(ctx, &f); err != nil {
			return res, err
		}
	}
	if enteringWorktree && !existed {
		res.EnteredWorktree = true
		if _, err := e.cfg.Worktrees.Create(ctx, &f); err != nil {
			return res, err
		}
		// approval promotes the draft to the artifact's workspace home
		// (DESIGN §10.11)
		if err := e.promoteDraft(&f); err != nil {
			return res, err
		}
		// plan-time estimation is feature-specific (spec approval): size the
		// spend-plan envelope from what completed features cost, before
		// budgeted autonomous work begins (DESIGN §5.1).
		if f.Stage == domain.StageSpec {
			res.EstimatedCredits, res.EstimateSamples = e.estimateEnvelope(ctx, &f)
		}
	}

	if _, err := e.cfg.Store.Transition(ctx, id, next, actor); err != nil {
		return res, err
	}
	e.Drop(id) // the old stage's session is stale now
	res.Feature = f
	res.Feature.Stage = next
	res.Status = StatusAdvanced
	return res, nil
}

// estimateEnvelope sizes a feature's spend-plan envelope from the median
// spend of previously completed features and persists it, returning the
// applied estimate and the number of metered samples behind it. It only
// fills an *unset* envelope (0), so an explicit envelope a user chose is
// respected, not silently replaced. Returns (0, 0) when the envelope is
// already set, when there's no history to learn from, or on any error —
// estimation is best-effort and never blocks the transition.
func (e *Engine) estimateEnvelope(ctx context.Context, f *domain.Feature) (credits, samples int) {
	if f.Budget.Envelope != 0 {
		return 0, 0 // an explicit envelope wins over estimation
	}
	feats, err := e.cfg.Store.ListFeatures(ctx)
	if err != nil {
		return 0, 0
	}
	var hist []domain.Spend
	for _, x := range feats {
		if x.ID != f.ID && x.Stage == domain.StageDone {
			hist = append(hist, x.Spend)
		}
	}
	env, n := domain.EstimateEnvelope(hist)
	if n == 0 || env <= 0 {
		return 0, 0
	}
	f.Budget.Envelope = int(env)
	if err := e.cfg.Store.UpdateFeature(ctx, f); err != nil {
		return 0, 0
	}
	// n is the number of past features that metered spend (zero-spend
	// completions are not samples).
	return int(env), n
}

// EstimateNotice formats the human-readable suffix a driver appends to the
// spec-approval transition notice when a historical estimate was applied.
// Empty when none was, so it composes cleanly onto any notice.
func (r AdvanceResult) EstimateNotice() string {
	if r.EstimatedCredits <= 0 {
		return ""
	}
	return fmt.Sprintf(" · envelope estimated at %d credits from %d metered feature(s)",
		r.EstimatedCredits, r.EstimateSamples)
}

// promoteDraft promotes the artifact draft (spec or bug report) to its
// workspace home under .gummi/specs|bugs in the main checkout. The artifact
// is gummi workspace content: it never enters the worktree and is never
// committed. An item that never had a draft gets a fresh template — the
// artifact always exists from approval on.
func (e *Engine) promoteDraft(f *domain.Feature) error {
	root := e.cfg.Worktrees.Root()
	return spec.Promote(
		filepath.Join(root, f.ArtifactPath()),
		filepath.Join(e.cfg.Workspace.DraftsDir(), spec.DraftFilename(f)),
		filepath.Join(root, f.WorktreePath(), f.ArtifactPath()),
		f,
	)
}

// artifactFile resolves where an item's design artifact lives right now:
// its workspace home once promoted, the draft before then, or the worktree
// copy of an item mid-flight from the committed-artifact era. Empty when
// none exists yet.
func (e *Engine) artifactFile(f *domain.Feature) string {
	root := e.cfg.Worktrees.Root()
	for _, p := range []string{
		filepath.Join(root, f.ArtifactPath()),
		filepath.Join(e.cfg.Workspace.DraftsDir(), spec.DraftFilename(f)),
		filepath.Join(root, f.WorktreePath(), f.ArtifactPath()),
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// openQuestionsBlockingGate returns the number of open, USER-authored `%%`
// annotations in an item's artifact (DESIGN §6.1: unresolved annotations
// block the gate). It reads wherever the artifact lives right now; zero for
// a missing or unreadable artifact, so a failed read never wedges the gate.
func (e *Engine) openQuestionsBlockingGate(f domain.Feature) int {
	path := e.artifactFile(&f)
	if path == "" {
		return 0
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return len(spec.Parse(string(raw)).UserOpenThreads())
}

// openDiffCommentsBlockingGate returns the number of unresolved diff
// annotations on an item — the diff-backend half of §6.1's gate check.
// Zero on any store error: like an unreadable artifact, a failed read never
// wedges the gate shut.
func (e *Engine) openDiffCommentsBlockingGate(ctx context.Context, id domain.FeatureID) int {
	anns, err := e.cfg.Store.ListDiffAnnotations(ctx, id)
	if err != nil {
		return 0
	}
	n := 0
	for _, a := range anns {
		if !a.Resolved {
			n++
		}
	}
	return n
}
