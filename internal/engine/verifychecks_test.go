package engine

import (
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

func TestVerifyStageRunsChecksGummiSide(t *testing.T) {
	ws, store, wt := newRepo(t)
	// a passing and a failing check
	cfg := "checks:\n  - name: pass-check\n    cmd: \"true\"\n  - name: fail-check\n    cmd: \"echo boom; exit 3\"\n"
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
	e := New(Config{Agent: ag, Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1, Permission: agent.PermissionAllowAll})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "verify me", domain.StageVerify)
	withWorktree(t, wt, f)
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

func TestVerifyStageGuardedSkipsGummiSide(t *testing.T) {
	ws, store, wt := newRepo(t)
	cfg := "checks:\n  - name: pass-check\n    cmd: \"true\"\n"
	if err := os.WriteFile(ws.ConfigFile(), []byte(cfg), 0o600); err != nil {
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
	// guarded mode: gummi does not auto-run repo commands; the agent does
	e := New(Config{Agent: ag, Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1, Permission: agent.PermissionGuarded})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "verify me", domain.StageVerify)
	withWorktree(t, wt, f)
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
