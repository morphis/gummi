package engine

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
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
	e := New(Config{Agent: ag, Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1, Permission: agent.PermissionAllowAll})
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
	e := New(Config{Agent: ag, Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1, Permission: agent.PermissionGuarded})
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
