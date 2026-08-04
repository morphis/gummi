package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/verify"
)

// ReverifyStatus classifies a cheap verify re-attach (Reverify).
type ReverifyStatus int

const (
	// ReverifyFinalized: the branch's acceptance checks passed and the
	// verify gate was finalized — verified_at is stamped and Advance
	// reports the branch ready to land (or transitioned to Done). Result
	// carries that AdvanceResult.
	ReverifyFinalized ReverifyStatus = iota
	// ReverifyFailed: a live (non-pre-existing) acceptance check still
	// fails on the branch. Result.Failed names them; nothing was stamped.
	ReverifyFailed
	// ReverifyUnavailable: a cheap re-attach does not apply here (the
	// feature is not parked at verify, guarded mode, no gummi-checks, or no
	// worktree). Result.Reason explains; the caller falls back to `resume`.
	ReverifyUnavailable
)

// ReverifyResult reports the outcome of a Reverify call.
type ReverifyResult struct {
	Status  ReverifyStatus
	Feature domain.Feature
	Reason  string        // ReverifyUnavailable: why not
	Failed  []string      // ReverifyFailed: names of the live-failing checks
	Ran     int           // number of checks executed
	Advance AdvanceResult // ReverifyFinalized: the finalize outcome
}

// Reverify re-runs a feature's gummi-side acceptance checks on its existing
// branch and, if they pass (only regressions count — pre-existing baseline
// failures don't), finalizes the verify gate through the shared floor
// (engine.Advance stamps verified_at and reports the branch ready to land).
// It runs no agent pass: it is the cheap re-attach for a run whose verify
// already passed but whose card lost its finalize to a crash in the tail,
// so `resume` (which redoes the whole verify stage) is avoidable.
//
// It never merges and never re-runs the agent write-up — a genuinely
// unfinished verify should go through `resume` instead, which is why
// Reverify only applies to a feature parked at the verify stage and refuses
// (ReverifyUnavailable) everywhere a cheap re-attach can't be trusted.
func (e *Engine) Reverify(ctx context.Context, id domain.FeatureID, actor string) (ReverifyResult, error) {
	f, err := e.cfg.Store.GetFeature(ctx, id)
	if err != nil {
		return ReverifyResult{}, err
	}
	res := ReverifyResult{Feature: f}

	unavailable := func(reason string) (ReverifyResult, error) {
		res.Status = ReverifyUnavailable
		res.Reason = reason
		return res, nil
	}

	if f.Stage != domain.StageVerify {
		return unavailable(fmt.Sprintf(
			"re-attach only finalizes a feature parked at the verify stage; %s is at %q — resume it instead", id, f.Stage))
	}
	// Guarded mode never auto-runs the acceptance checks (the agent verifies
	// instead), so there is nothing cheap to re-run here.
	if e.cfg.Permission == agent.PermissionGuarded {
		return unavailable("re-attach runs the acceptance checks itself, which guarded mode does not allow — resume to let the agent verify")
	}

	// the spec's fixed gummi-checks are what a cheap re-attach re-runs.
	path := e.artifactFile(&f)
	if path == "" {
		return unavailable("the feature's spec could not be located — resume to let the agent verify")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return unavailable(fmt.Sprintf("reading the spec failed (%v) — resume to let the agent verify", err))
	}
	checks, _, _ := spec.ParseChecks(string(raw))
	if len(checks) == 0 {
		return unavailable("the spec lists no gummi-checks to re-run — resume to let the agent verify")
	}

	if exists, err := e.cfg.Worktrees.Exists(ctx, &f); err != nil {
		return res, err
	} else if !exists {
		return unavailable("the feature's worktree is gone — resume to recreate it and verify")
	}

	// run the checks in the worktree, bounded like the Verify stage's own
	// gummi-side run.
	workDir := filepath.Join(e.cfg.Worktrees.Root(), f.WorktreePath())
	rctx, cancel := context.WithTimeout(ctx, verifyStageTimeout)
	defer cancel()
	results := verify.Run(rctx, workDir, checks)
	res.Ran = len(results)

	// classify against the approval-time baseline: a failure the branch was
	// born with (same command, already failing) is pre-existing and does not
	// count against this feature — only regressions do. No baseline degrades
	// to treating every failure as live.
	baseline := map[string]state.CheckResult{}
	if rows, err := e.cfg.Store.CheckBaseline(ctx, id); err == nil {
		for _, r := range rows {
			baseline[r.Name] = r
		}
	}
	for _, r := range results {
		if r.OK {
			continue
		}
		if base, ok := baseline[r.Name]; ok && base.Cmd == r.Cmd && !base.OK {
			continue // pre-existing failure, not this feature's regression
		}
		res.Failed = append(res.Failed, r.Name)
	}
	if len(res.Failed) > 0 {
		res.Status = ReverifyFailed
		return res, nil
	}

	// all live checks pass — finalize via the shared floor, which stamps
	// verified_at (once) and reports StatusNeedsMerge for a branch that is
	// ahead, or transitions to Done when there is nothing to land.
	adv, err := e.Advance(ctx, id, actor)
	if err != nil {
		return res, err
	}
	res.Advance = adv
	res.Feature = adv.Feature
	res.Status = ReverifyFinalized
	return res, nil
}
