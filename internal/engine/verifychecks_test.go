package engine

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
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

// A live (non-pre-existing) gummi-check failure must floor the verdict to
// blocked, even when the verify agent's own reply claims a pass — gummi's
// machine judgement must outrank the agent's prose (BG-040).
func TestVerifyLiveCheckFailureFloorsVerdict(t *testing.T) {
	ws, store, wt := newRepo(t)

	ag := &agent.Fake{Responder: func(_ agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{{Kind: agent.EventMessage, Text: "Repo checks clean; verification plan satisfied. VERDICT: pass"}, {Kind: agent.EventIdle}}
	}}
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1, Permission: agent.PermissionAllowAll})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "verify me", domain.StageVerify)
	withWorktree(t, wt, f)
	writeSpecChecks(t, wt, f, "- name: fail-check\n  cmd: \"echo boom; exit 3\"\n")
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-001", StateDone)

	snap := e.Get("FD-001").Snapshot()
	if snap.VerdictFloor != "blocked" {
		t.Errorf("VerdictFloor = %q, want %q: a live check failure (fail-check: FAIL) never floored the verdict", snap.VerdictFloor, "blocked")
	}
	if !strings.Contains(snap.VerdictFloorReason, "fail-check") {
		t.Errorf("VerdictFloorReason = %q, want it to name the failed check", snap.VerdictFloorReason)
	}
}

// A check that already failed at baseline is pre-existing, not a
// regression this feature caused — it must not floor the verdict.
func TestVerifyPreexistingCheckFailureDoesNotFloorVerdict(t *testing.T) {
	ws, store, wt := newRepo(t)
	ctx := context.Background()

	ag := &agent.Fake{Responder: func(_ agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{{Kind: agent.EventMessage, Text: "Pre-existing failure only. VERDICT: pass"}, {Kind: agent.EventIdle}}
	}}
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1, Permission: agent.PermissionAllowAll})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "verify me", domain.StageVerify)
	if err := store.CreateFeature(ctx, &f); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCheckBaseline(ctx, f.ID, []state.CheckResult{
		{Name: "fail-check", Cmd: "echo boom; exit 3", OK: false, ExitCode: 3, RanAt: time.Now()},
	}); err != nil {
		t.Fatal(err)
	}
	withWorktree(t, wt, f)
	writeSpecChecks(t, wt, f, "- name: fail-check\n  cmd: \"echo boom; exit 3\"\n")
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-001", StateDone)

	snap := e.Get("FD-001").Snapshot()
	if snap.VerdictFloor == "blocked" {
		t.Errorf("VerdictFloor = %q, want unset: a pre-existing check failure must not floor the verdict", snap.VerdictFloor)
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

// reverifyOmissionBug sets up a bug parked at verify with a clean-present
// env prerequisite, zero [env:] tags, no waiver, and a passing gummi-check.
// It returns the engine, store, and feature for Reverify assertions.
func reverifyOmissionBug(t *testing.T, checkCmd string) (*Engine, *state.Store, domain.Feature) {
	t.Helper()
	ws, store, wt := newRepo(t)
	if err := os.WriteFile(ws.ConfigFile(), []byte("env:\n  docker:\n    probe: \"true\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	e := New(Config{Store: store, Worktrees: wt, Workspace: ws})
	t.Cleanup(func() { e.Close() })

	f := bugFeature("reverify omission gate")
	f.Stage = domain.StageVerify
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	withWorktree(t, wt, f)

	checks := "```gummi-checks\n- name: verify-check\n  cmd: " + strconv.Quote(checkCmd) + "\n```\n"
	writeBugSpec(t, wt, f, "Run local unit tests only.\n\n"+checks)

	return e, store, f
}

func TestReverify_OmissionGate_Blocked(t *testing.T) {
	ctx := context.Background()
	e, store, f := reverifyOmissionBug(t, "true")

	res, err := e.Reverify(ctx, f.ID, "user")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != ReverifyBlocked {
		t.Fatalf("status=%d, want ReverifyBlocked", res.Status)
	}
	if res.Advance.Status != StatusBlockedOmission {
		t.Fatalf("advance status=%d, want StatusBlockedOmission", res.Advance.Status)
	}
	if res.Advance.Reason == "" {
		t.Fatal("advance reason empty")
	}
	if got, _ := store.GetFeature(ctx, f.ID); !got.VerifiedAt.IsZero() {
		t.Fatalf("verified_at stamped on reverify omission block: %v", got.VerifiedAt)
	}
}

func TestReverify_RegressionStillFails(t *testing.T) {
	ctx := context.Background()
	e, store, f := reverifyOmissionBug(t, "echo boom; exit 3")

	res, err := e.Reverify(ctx, f.ID, "user")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != ReverifyFailed {
		t.Fatalf("status=%d, want ReverifyFailed", res.Status)
	}
	if len(res.Failed) == 0 {
		t.Fatal("expected failing check names, got none")
	}
	if got, _ := store.GetFeature(ctx, f.ID); !got.VerifiedAt.IsZero() {
		t.Fatalf("verified_at stamped on reverify failure: %v", got.VerifiedAt)
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
