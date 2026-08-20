package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/worktree"
)

// writeSpecChecks drops a spec carrying a gummi-checks block into the
// feature's worktree, where the Verify stage reads it.
func writeSpecChecks(t *testing.T, wt *worktree.Manager, f domain.Feature, checksYAML string) {
	t.Helper()
	p := filepath.Join(wt.Root(), f.WorktreePath(), f.ArtifactPath())
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	content := "# " + string(f.ID) + "\n\n## Verification plan\n\n```gummi-checks\n" + checksYAML + "```\n"
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyStageRunsChecksGummiSide(t *testing.T) {
	ws, store, wt := newRepo(t)

	var mu sync.Mutex
	var got string
	ag := &agent.Fake{Responder: func(_ agent.SessionOpts, msg string) []agent.Event {
		mu.Lock()
		got = msg
		mu.Unlock()
		return []agent.Event{{Kind: agent.EventMessage, Text: "recorded"}, {Kind: agent.EventIdle}}
	}}
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1, Permission: agent.PermissionAllowAll})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "verify me", domain.StageVerify)
	withWorktree(t, wt, f)
	// a passing and a failing check in the spec's gummi-checks block
	writeSpecChecks(t, wt, f, "- name: pass-check\n  cmd: \"true\"\n- name: fail-check\n  cmd: \"echo boom; exit 3\"\n")
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-001", StateDone)

	mu.Lock()
	defer mu.Unlock()
	for _, want := range []string{"gummi already ran", "pass-check: pass", "fail-check: FAIL", "boom"} {
		if !strings.Contains(got, want) {
			t.Errorf("verify kickoff missing %q:\n%s", want, got)
		}
	}
	// the outcomes are also in the activity feed
	acts := strings.Join(e.Get("FD-001").Snapshot().Activity, "\n")
	if !strings.Contains(acts, "check pass-check: pass") || !strings.Contains(acts, "check fail-check: FAIL") {
		t.Errorf("check outcomes not in activity:\n%s", acts)
	}
}

// kickoffAfterVerify runs the Verify stage against the given checks
// block and returns the kickoff message the verify agent received.
func kickoffAfterVerify(t *testing.T, seed func(store *state.Store, f domain.Feature), checksYAML string) string {
	t.Helper()
	ws, store, wt := newRepo(t)
	var mu sync.Mutex
	var got string
	ag := &agent.Fake{Responder: func(_ agent.SessionOpts, msg string) []agent.Event {
		mu.Lock()
		got = msg
		mu.Unlock()
		return []agent.Event{{Kind: agent.EventMessage, Text: "recorded"}, {Kind: agent.EventIdle}}
	}}
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1, Permission: agent.PermissionAllowAll})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "verify me", domain.StageVerify)
	if seed != nil {
		seed(store, f)
	}
	withWorktree(t, wt, f)
	writeSpecChecks(t, wt, f, checksYAML)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-001", StateDone)
	// Done flips before the checkpoint commit runs (settle follows
	// finishRunning), so returning at Done lets TempDir cleanup race the
	// git write. The spec file guarantees a dirty worktree, so the
	// checkpoint always leaves an activity line to wait for.
	waitActivity(t, e, "FD-001", "worktree committed", "checkpoint commit failed")
	mu.Lock()
	defer mu.Unlock()
	return got
}

// A check that already failed at baseline (same command) is labeled
// pre-existing, and the kickoff tells the agent only regressions count.
func TestVerifyKickoffLabelsPreexistingFail(t *testing.T) {
	got := kickoffAfterVerify(t, func(store *state.Store, f domain.Feature) {
		ctx := context.Background()
		if err := store.CreateFeature(ctx, &f); err != nil {
			t.Fatal(err)
		}
		if err := store.SetCheckBaseline(ctx, f.ID, []state.CheckResult{
			{Name: "fail-check", Cmd: "echo boom; exit 3", OK: false, ExitCode: 3, RanAt: time.Now()},
		}); err != nil {
			t.Fatal(err)
		}
	}, "- name: fail-check\n  cmd: \"echo boom; exit 3\"\n")

	if !strings.Contains(got, "fail-check: FAIL (pre-existing, exit 3)") {
		t.Errorf("kickoff missing the pre-existing label:\n%s", got)
	}
	if !strings.Contains(got, "only regressions count") {
		t.Errorf("kickoff missing the only-regressions rule:\n%s", got)
	}
}

// A check that passed at baseline and fails now is a regression: the
// plain FAIL label, no pre-existing softening.
func TestVerifyKickoffRegressionWhenBaselinePassed(t *testing.T) {
	got := kickoffAfterVerify(t, func(store *state.Store, f domain.Feature) {
		ctx := context.Background()
		if err := store.CreateFeature(ctx, &f); err != nil {
			t.Fatal(err)
		}
		if err := store.SetCheckBaseline(ctx, f.ID, []state.CheckResult{
			{Name: "fail-check", Cmd: "echo boom; exit 3", OK: true, RanAt: time.Now()},
		}); err != nil {
			t.Fatal(err)
		}
	}, "- name: fail-check\n  cmd: \"echo boom; exit 3\"\n")

	if !strings.Contains(got, "fail-check: FAIL (exit 3)") || strings.Contains(got, "pre-existing") {
		t.Errorf("regression must carry the plain FAIL label:\n%s", got)
	}
}

// A baseline row for an edited command says nothing about the new one:
// the old run's failure must not soften a live failure.
func TestVerifyKickoffChangedCmdIgnoresBaseline(t *testing.T) {
	got := kickoffAfterVerify(t, func(store *state.Store, f domain.Feature) {
		ctx := context.Background()
		if err := store.CreateFeature(ctx, &f); err != nil {
			t.Fatal(err)
		}
		if err := store.SetCheckBaseline(ctx, f.ID, []state.CheckResult{
			{Name: "fail-check", Cmd: "some old command", OK: false, ExitCode: 1, RanAt: time.Now()},
		}); err != nil {
			t.Fatal(err)
		}
	}, "- name: fail-check\n  cmd: \"echo boom; exit 3\"\n")

	if !strings.Contains(got, "fail-check: FAIL (exit 3)") || strings.Contains(got, "pre-existing") {
		t.Errorf("edited command must not inherit the old baseline:\n%s", got)
	}
}

func TestVerifyStageGuardedSkipsGummiSide(t *testing.T) {
	ws, store, wt := newRepo(t)
	var mu sync.Mutex
	var got string
	ag := &agent.Fake{Responder: func(_ agent.SessionOpts, msg string) []agent.Event {
		mu.Lock()
		got = msg
		mu.Unlock()
		return []agent.Event{{Kind: agent.EventIdle}}
	}}
	// guarded mode: gummi does not auto-run the spec's commands; the agent does
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1, Permission: agent.PermissionGuarded})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "verify me", domain.StageVerify)
	withWorktree(t, wt, f)
	writeSpecChecks(t, wt, f, "- name: pass-check\n  cmd: \"true\"\n")
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-001", StateDone)

	mu.Lock()
	defer mu.Unlock()
	if strings.Contains(got, "gummi already ran") {
		t.Errorf("guarded mode should not run checks gummi-side:\n%s", got)
	}
}

func TestVerifyKickoffRunsEnvProbes(t *testing.T) {
	ws, store, wt := newRepo(t)
	// write operator-configured env prerequisites into the workspace root
	cfg := `env:
  present-thing:
    probe: "true"
    describe: a present prerequisite
  absent-thing:
    probe: "false"
    describe: an absent prerequisite
`
	if err := os.WriteFile(ws.ConfigFile(), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var got string
	ag := &agent.Fake{Responder: func(_ agent.SessionOpts, msg string) []agent.Event {
		mu.Lock()
		got = msg
		mu.Unlock()
		return []agent.Event{{Kind: agent.EventMessage, Text: "recorded"}, {Kind: agent.EventIdle}}
	}}
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1, Permission: agent.PermissionAllowAll})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "verify me", domain.StageVerify)
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-001", StateDone)

	mu.Lock()
	defer mu.Unlock()
	for _, want := range []string{"Environment prerequisites probed", "present-thing: PRESENT", "absent-thing: ABSENT", "a present prerequisite", "an absent prerequisite"} {
		if !strings.Contains(got, want) {
			t.Errorf("verify kickoff missing %q:\n%s", want, got)
		}
	}

	snap := e.Get("FD-001").Snapshot()
	if len(snap.EnvProbes) != 2 {
		t.Fatalf("got %d env probes, want 2", len(snap.EnvProbes))
	}
	names := []string{snap.EnvProbes[0].Name, snap.EnvProbes[1].Name}
	if names[0] != "absent-thing" || names[1] != "present-thing" {
		t.Errorf("probe order = %v, want [absent-thing present-thing]", names)
	}
}

func TestVerifyKickoffRunsEnvProbesInGuardedMode(t *testing.T) {
	ws, store, wt := newRepo(t)
	if err := os.WriteFile(ws.ConfigFile(), []byte("env:\n  ok:\n    probe: \"true\"\n    describe: present\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var got string
	ag := &agent.Fake{Responder: func(_ agent.SessionOpts, msg string) []agent.Event {
		mu.Lock()
		got = msg
		mu.Unlock()
		return []agent.Event{{Kind: agent.EventIdle}}
	}}
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1, Permission: agent.PermissionGuarded})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "verify me", domain.StageVerify)
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-001", StateDone)

	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(got, "Environment prerequisites probed") {
		t.Errorf("guarded mode should still run env probes:\n%s", got)
	}
	if strings.Contains(got, "gummi already ran") {
		t.Errorf("guarded mode should not run spec checks gummi-side:\n%s", got)
	}
}
