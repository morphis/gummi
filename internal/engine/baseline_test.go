package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// BaselineChecks runs the artifact's checks on the fresh worktree and
// persists the outcomes, so verify can later tell pre-existing failures
// from regressions.
func TestBaselineChecksPersists(t *testing.T) {
	ws, store, wt := newRepo(t)
	e := New(Config{
		Agent: &agent.Fake{}, Store: store, Worktrees: wt, Workspace: ws,
		Model: "m", MaxActive: 1, Permission: agent.PermissionAllowAll,
	})
	t.Cleanup(func() { e.Close() })

	ctx := context.Background()
	f := feature(1, "baseline me", domain.StagePlan)
	if err := store.CreateFeature(ctx, &f); err != nil {
		t.Fatal(err)
	}
	withWorktree(t, wt, f)
	writeSpecChecks(t, wt, f, "- name: pass-check\n  cmd: \"true\"\n- name: fail-check\n  cmd: \"echo boom; exit 3\"\n")

	results, err := e.BaselineChecks(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %+v, want 2", results)
	}

	rows, err := store.CheckBaseline(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("baseline rows = %+v, want 2", rows)
	}
	// ORDER BY name: fail-check, pass-check
	if rows[0].Name != "fail-check" || rows[0].OK || rows[0].ExitCode != 3 || !strings.Contains(rows[0].Output, "boom") {
		t.Errorf("fail-check row = %+v", rows[0])
	}
	if rows[1].Name != "pass-check" || !rows[1].OK {
		t.Errorf("pass-check row = %+v", rows[1])
	}
}

// Guarded mode never auto-runs artifact-carried commands — the baseline
// is skipped, not taken.
func TestBaselineChecksGuardedNoop(t *testing.T) {
	ws, store, wt := newRepo(t)
	e := New(Config{
		Agent: &agent.Fake{}, Store: store, Worktrees: wt, Workspace: ws,
		Model: "m", MaxActive: 1, Permission: agent.PermissionGuarded,
	})
	t.Cleanup(func() { e.Close() })

	ctx := context.Background()
	f := feature(1, "guarded", domain.StagePlan)
	if err := store.CreateFeature(ctx, &f); err != nil {
		t.Fatal(err)
	}
	withWorktree(t, wt, f)
	writeSpecChecks(t, wt, f, "- name: pass-check\n  cmd: \"true\"\n")

	results, err := e.BaselineChecks(ctx, f)
	if err != nil || results != nil {
		t.Fatalf("guarded baseline = %+v (err %v), want nil/nil", results, err)
	}
	if rows, _ := store.CheckBaseline(ctx, f.ID); len(rows) != 0 {
		t.Errorf("guarded mode persisted a baseline: %+v", rows)
	}
}

// A malformed gummi-checks block is a defect the baseline pass reports,
// not an empty block to shrug at.
func TestBaselineChecksMalformedYAMLErrors(t *testing.T) {
	ws, store, wt := newRepo(t)
	e := New(Config{
		Agent: &agent.Fake{}, Store: store, Worktrees: wt, Workspace: ws,
		Model: "m", MaxActive: 1, Permission: agent.PermissionAllowAll,
	})
	t.Cleanup(func() { e.Close() })

	ctx := context.Background()
	f := feature(1, "malformed", domain.StagePlan)
	if err := store.CreateFeature(ctx, &f); err != nil {
		t.Fatal(err)
	}
	withWorktree(t, wt, f)
	writeSpecChecks(t, wt, f, "\t: not yaml [\n")

	if _, err := e.BaselineChecks(ctx, f); err == nil {
		t.Fatal("malformed block did not error")
	}
}
