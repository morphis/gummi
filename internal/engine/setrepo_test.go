package engine

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/worktree"
)

// twoRepoEngine builds an engine over a pool with named repos "a" and "b"
// and no default, matching the multi-repo workspace fixtures used by the TUI
// repo-picker tests.
func twoRepoEngine(t *testing.T) *Engine {
	t.Helper()
	wsRoot := t.TempDir()
	wsRoot, err := filepath.EvalSymlinks(wsRoot)
	if err != nil {
		t.Fatal(err)
	}
	git := func(root string, args ...string) {
		t.Helper()
		if out, err := exec.CommandContext(context.Background(), "git",
			append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	init := func(root string) {
		git(root, "init", "-q", "-b", "main")
		git(root, "config", "user.name", "t")
		git(root, "config", "user.email", "t@e.invalid")
		if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		git(root, "add", ".")
		git(root, "commit", "-q", "-m", "init")
	}
	init(wsRoot)
	repoA := filepath.Join(wsRoot, "git", "a")
	repoB := filepath.Join(wsRoot, "git", "b")
	for _, r := range []string{repoA, repoB} {
		if err := os.MkdirAll(r, 0o750); err != nil {
			t.Fatal(err)
		}
		init(r)
	}

	ws, err := state.Init(wsRoot, wsRoot)
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	pool, err := worktree.NewPool(context.Background(), ws.Root, "",
		[]worktree.NamedRepo{{Name: "a", Root: repoA}, {Name: "b", Root: repoB}}, store, false)
	if err != nil {
		t.Fatal(err)
	}
	e := New(Config{Agents: singleAgent(agent.NewFake("x")), Store: store, Pool: pool, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })
	return e
}

func TestSetRepoPersists(t *testing.T) {
	e := twoRepoEngine(t)
	ctx := context.Background()
	f := feature(1, "x", domain.StageTodo)
	if err := e.cfg.Store.CreateFeature(ctx, &f); err != nil {
		t.Fatal(err)
	}

	updated, err := e.SetRepo(ctx, f.ID, "a")
	if err != nil {
		t.Fatalf("SetRepo: %v", err)
	}
	if updated.Repo != "a" {
		t.Errorf("returned repo = %q, want %q", updated.Repo, "a")
	}
	reloaded, err := e.cfg.Store.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Repo != "a" {
		t.Errorf("reloaded repo = %q, want %q", reloaded.Repo, "a")
	}
}

func TestSetRepoNoop(t *testing.T) {
	e := twoRepoEngine(t)
	ctx := context.Background()
	f := feature(1, "x", domain.StageTodo)
	f.Repo = "a"
	if err := e.cfg.Store.CreateFeature(ctx, &f); err != nil {
		t.Fatal(err)
	}

	before, _ := e.cfg.Store.GetFeature(ctx, f.ID)
	updated, err := e.SetRepo(ctx, f.ID, "a")
	if err != nil {
		t.Fatalf("SetRepo: %v", err)
	}
	if updated.Repo != "a" {
		t.Errorf("returned repo = %q, want %q", updated.Repo, "a")
	}
	after, _ := e.cfg.Store.GetFeature(ctx, f.ID)
	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Error("no-op SetRepo mutated UpdatedAt")
	}
}

func TestSetRepoRejectsUnknownRepo(t *testing.T) {
	e := twoRepoEngine(t)
	ctx := context.Background()
	f := feature(1, "x", domain.StageTodo)
	if err := e.cfg.Store.CreateFeature(ctx, &f); err != nil {
		t.Fatal(err)
	}

	_, err := e.SetRepo(ctx, f.ID, "nope")
	if err == nil {
		t.Fatal("expected error for unknown repo")
	}
	reloaded, _ := e.cfg.Store.GetFeature(ctx, f.ID)
	if reloaded.Repo != "" {
		t.Errorf("repo changed despite error: %q", reloaded.Repo)
	}
}

func TestSetRepoLockedAfterWorktree(t *testing.T) {
	ws, store, wt := newRepo(t)
	e := New(Config{
		Agents: singleAgent(agent.NewFake("x")), Store: store, Worktrees: wt, Workspace: ws,
		Model: "m", MaxActive: 1,
	})
	t.Cleanup(func() { e.Close() })
	ctx := context.Background()
	f := feature(1, "x", domain.StageImplement)
	if err := e.cfg.Store.CreateFeature(ctx, &f); err != nil {
		t.Fatal(err)
	}

	_, err := e.SetRepo(ctx, f.ID, "")
	if !errors.Is(err, ErrRepoLocked) {
		t.Fatalf("error = %v, want ErrRepoLocked", err)
	}
	reloaded, _ := e.cfg.Store.GetFeature(ctx, f.ID)
	if reloaded.Repo != "" {
		t.Errorf("repo changed despite lock: %q", reloaded.Repo)
	}
}
