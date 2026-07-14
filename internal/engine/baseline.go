package engine

import (
	"context"
	"os"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/verify"
)

// BaselineChecks runs the artifact's gummi-checks once on the fresh
// worktree — right after discovery writes them, before any feature
// changes — and persists the outcomes as the feature's baseline. Verify
// later diffs its live results against it, so a command that was
// already failing (or malformed) surfaces at approval, when the
// architect can still fix the block, instead of masquerading as the
// feature's fault six stages later.
//
// Guarded mode never auto-runs artifact commands (the block is
// agent-written and not yet human-gated; auto-executing it is only
// acceptable where the sandbox is the boundary — the same rule as
// runSpecChecks), so it returns (nil, nil) there. The baseline is
// best-effort: its absence degrades Verify to unlabeled failures.
func (e *Engine) BaselineChecks(ctx context.Context, f domain.Feature) ([]verify.Result, error) {
	if e.cfg.Permission == agent.PermissionGuarded {
		return nil, nil
	}
	workDir, specPath, err := e.locate(ctx, f)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(specPath)
	if err != nil {
		return nil, err
	}
	checks, _, err := spec.ParseChecks(string(raw))
	if err != nil {
		return nil, err // malformed block: the caller flags it to the user
	}
	if len(checks) == 0 {
		return nil, nil
	}
	runCtx, cancel := context.WithTimeout(ctx, verifyStageTimeout)
	defer cancel()
	results := verify.Run(runCtx, workDir, checks)

	baseline := make([]state.CheckResult, 0, len(results))
	now := time.Now().UTC()
	for _, r := range results {
		baseline = append(baseline, state.CheckResult{
			Name: r.Name, Cmd: r.Cmd, OK: r.OK,
			ExitCode: r.ExitCode, Output: r.Output, RanAt: now,
		})
	}
	if err := e.cfg.Store.SetCheckBaseline(ctx, f.ID, baseline); err != nil {
		return nil, err
	}
	return results, nil
}
