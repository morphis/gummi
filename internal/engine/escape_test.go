package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/worktree"
)

// escGit runs git in dir for escape tests. It reports failures with
// t.Errorf (not Fatalf): it is called from Fake responder goroutines,
// where FailNow must not be used.
func escGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.CommandContext(context.Background(), "git",
		append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Errorf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func escWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Errorf("write %s: %v", path, err)
	}
}

// workspaceAt wires gummi's workspace, store, and worktree manager into
// an existing repo root (newRepo's tail, for tests that must know the
// root before the engine exists).
func workspaceAt(t *testing.T, root string) (state.Workspace, *state.Store, *worktree.Manager) {
	t.Helper()
	ws, err := state.Init(root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	wt, err := worktree.NewManager(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	return ws, store, wt
}

// escapeEngine builds an engine whose fake agent runs `escape` against
// the main checkout mid-turn, and starts an implement run for FD-001.
func escapeEngine(t *testing.T, root string, escape func(wt *worktree.Manager)) *Engine {
	t.Helper()
	ws, store, wt := workspaceAt(t, root)
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		escape(wt)
		return []agent.Event{
			{Kind: agent.EventMessage, Text: "done"},
			{Kind: agent.EventIdle},
		}
	}}
	e := New(Config{Agent: ag, Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })
	f := feature(1, "impl", domain.StageImplement)
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestEscapeCommitOnMainReverted(t *testing.T) {
	root := gitRepo(t)
	base := escGit(t, root, "rev-parse", "HEAD")
	e := escapeEngine(t, root, func(*worktree.Manager) {
		escWrite(t, filepath.Join(root, "rogue.txt"), "rogue\n")
		escGit(t, root, "add", "rogue.txt")
		escGit(t, root, "commit", "-q", "-m", "rogue")
	})

	ev := waitFor(t, e, EventEscape)
	if ev.Err == nil || !strings.Contains(ev.Err.Error(), "reverted") ||
		strings.Contains(ev.Err.Error(), "not auto-reverted") {
		t.Fatalf("escape event should report a revert: %v", ev.Err)
	}
	waitState(t, e, "FD-001", StatePaused)
	if head := escGit(t, root, "rev-parse", "HEAD"); head != base {
		t.Errorf("main HEAD not restored: %s want %s", head, base)
	}
	if _, err := os.Stat(filepath.Join(root, "rogue.txt")); !os.IsNotExist(err) {
		t.Error("the escape commit's file survived")
	}
	if snap := e.Get("FD-001").Snapshot(); snap.Err == nil {
		t.Error("run did not fail")
	}
}

func TestEscapeUntrackedFileRemoved(t *testing.T) {
	root := gitRepo(t)
	e := escapeEngine(t, root, func(*worktree.Manager) {
		escWrite(t, filepath.Join(root, "dropping.txt"), "junk\n")
	})

	ev := waitFor(t, e, EventEscape)
	if ev.Err == nil || !strings.Contains(ev.Err.Error(), "reverted") {
		t.Fatalf("escape event should report a revert: %v", ev.Err)
	}
	waitState(t, e, "FD-001", StatePaused)
	if _, err := os.Stat(filepath.Join(root, "dropping.txt")); !os.IsNotExist(err) {
		t.Error("the agent's untracked file survived")
	}
}

func TestEscapeWithUserWorkNotReverted(t *testing.T) {
	root := gitRepo(t)
	// the user's uncommitted work, present before the turn
	escWrite(t, filepath.Join(root, "README.md"), "user edit in flight\n")
	e := escapeEngine(t, root, func(*worktree.Manager) {
		escWrite(t, filepath.Join(root, "rogue.txt"), "rogue\n")
		escGit(t, root, "add", "rogue.txt")
		escGit(t, root, "commit", "-q", "-m", "rogue")
	})

	ev := waitFor(t, e, EventEscape)
	if ev.Err == nil || !strings.Contains(ev.Err.Error(), "not auto-reverted") {
		t.Fatalf("ambiguous escape must fail loudly without a revert: %v", ev.Err)
	}
	waitState(t, e, "FD-001", StatePaused)
	// nothing was destroyed: the escape commit is still inspectable and
	// the user's edit is intact
	if _, err := os.Stat(filepath.Join(root, "rogue.txt")); err != nil {
		t.Error("escape commit was reverted despite user work in the checkout")
	}
	b, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil || string(b) != "user edit in flight\n" {
		t.Errorf("user work was destroyed: %q %v", b, err)
	}
}

func TestSanctionedMainMutationNotFlagged(t *testing.T) {
	root := gitRepo(t)
	// a gummi-sanctioned main mutation mid-turn (the shape of a land):
	// commit on main, generation bumped — the escape check must stand down.
	e := escapeEngine(t, root, func(wt *worktree.Manager) {
		escWrite(t, filepath.Join(root, "landed.txt"), "landed\n")
		escGit(t, root, "add", "landed.txt")
		escGit(t, root, "commit", "-q", "-m", "land")
		wt.BumpMainGen()
	})

	waitState(t, e, "FD-001", StateDone)
	if snap := e.Get("FD-001").Snapshot(); snap.Err != nil {
		t.Errorf("sanctioned mutation flagged as escape: %v", snap.Err)
	}
	if _, err := os.Stat(filepath.Join(root, "landed.txt")); err != nil {
		t.Error("sanctioned commit was reverted")
	}
}

// gitRepo creates a bare-bones repo like newRepo but returns only the
// root, letting the test wire the workspace afterwards via newRepoAt.
func gitRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.name", "t"},
		{"config", "user.email", "t@e.invalid"},
	} {
		escGit(t, root, args...)
	}
	escWrite(t, filepath.Join(root, "README.md"), "x\n")
	escGit(t, root, "add", ".")
	escGit(t, root, "commit", "-q", "-m", "init")
	return root
}
