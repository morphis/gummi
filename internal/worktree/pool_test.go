package worktree

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

// twoRepoPool builds a pool with the workspace root as the default repo and
// two nested named repos.
func twoRepoPool(t *testing.T) (*Pool, string, string, string) {
	t.Helper()
	ws := newRepo(t)
	repoA := filepath.Join(ws, "git", "a")
	repoB := filepath.Join(ws, "git", "b")
	for _, r := range []string{repoA, repoB} {
		if err := os.MkdirAll(r, 0o750); err != nil {
			t.Fatal(err)
		}
		mustGit(t, r, "init", "-q", "-b", "main")
		mustGit(t, r, "config", "user.name", "test")
		mustGit(t, r, "config", "user.email", "t@e.invalid")
		writeFile(t, r, "README.md", "hello\n")
		mustGit(t, r, "add", ".")
		mustGit(t, r, "commit", "-q", "-m", "initial")
	}
	p, err := NewPool(ctx, ws, ws, []NamedRepo{{Name: "a", Root: repoA}, {Name: "b", Root: repoB}}, &memForkStore{}, false)
	if err != nil {
		t.Fatal(err)
	}
	return p, ws, repoA, repoB
}

// TestPoolManagerForDefault: a card with no repo uses the default manager.
func TestPoolManagerForDefault(t *testing.T) {
	p, ws, _, _ := twoRepoPool(t)
	m, err := p.ManagerFor(ctx, feature(1, "default card"))
	if err != nil {
		t.Fatal(err)
	}
	if m.RepoRoot() != ws {
		t.Errorf("default manager repo root = %q, want workspace %q", m.RepoRoot(), ws)
	}
	if p.Root() != ws {
		t.Errorf("pool root = %q, want workspace %q", p.Root(), ws)
	}
}

// TestPoolTwoRootsDistinct: cards in different named repos resolve to
// distinct managers, each bound to its own repo root.
func TestPoolTwoRootsDistinct(t *testing.T) {
	p, _, repoA, repoB := twoRepoPool(t)
	fA := feature(1, "in a")
	fA.Repo = "a"
	fB := feature(2, "in b")
	fB.Repo = "b"

	ma, err := p.ManagerFor(ctx, fA)
	if err != nil {
		t.Fatal(err)
	}
	mb, err := p.ManagerFor(ctx, fB)
	if err != nil {
		t.Fatal(err)
	}
	if ma == mb {
		t.Fatal("two repos resolved to the same manager")
	}
	if ma.RepoRoot() != repoA || mb.RepoRoot() != repoB {
		t.Errorf("roots = (%q, %q), want (%q, %q)", ma.RepoRoot(), mb.RepoRoot(), repoA, repoB)
	}
	// the cache returns the same instance on repeat resolution
	if again, err := p.ManagerFor(ctx, fA); err != nil || again != ma {
		t.Fatalf("second resolution did not return the cached manager (err %v)", err)
	}
}

// TestPoolUnconfiguredRepoErrors: a stored-but-unconfigured repo name is a
// resolution-time error, never a silent fallback.
func TestPoolUnconfiguredRepoErrors(t *testing.T) {
	p, _, _, _ := twoRepoPool(t)
	f := feature(1, "lost card")
	f.Repo = "gone"
	if _, err := p.ManagerFor(ctx, f); err == nil {
		t.Fatal("expected an error for an unconfigured repo")
	} else if err.Error() != "repository \"gone\" is not configured; add it to `repos:` in .gummi/config.yaml, or recreate the card against a configured repository" {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestPoolKnown: the empty name is always known (the default); named repos
// are known only when configured.
func TestPoolKnown(t *testing.T) {
	p, _, _, _ := twoRepoPool(t)
	if !p.Known("") || !p.Known("a") || !p.Known("b") {
		t.Error("default and configured names should be known")
	}
	if p.Known("nope") {
		t.Error("an unconfigured name should not be known")
	}
	if got := fmt.Sprint(p.Names()); got != "[a b]" {
		t.Errorf("Names = %s, want [a b]", got)
	}
	if p.DefaultName() != "" {
		t.Errorf("DefaultName = %q, want empty", p.DefaultName())
	}
}

// TestPoolConcurrent: ManagerFor is safe under concurrent access — the same
// root always resolves to the same manager even when many goroutines touch
// it at once.
func TestPoolConcurrent(t *testing.T) {
	p, _, _, _ := twoRepoPool(t)
	features := []*domain.Feature{
		{Repo: ""}, {Repo: "a"}, {Repo: "b"}, {Repo: "a"}, {Repo: "b"},
	}
	var wg sync.WaitGroup
	errs := make(chan error, 100)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			f := features[i%len(features)]
			m, err := p.ManagerFor(ctx, f)
			if err != nil {
				errs <- err
			} else if m == nil {
				errs <- fmt.Errorf("nil manager for repo %q", f.Repo)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent ManagerFor: %v", err)
	}
}

// TestPoolReposOnlyNoDefault: a repos:-only pool (empty defaultRoot) does not
// eagerly construct a default manager and reports no default: pool creation
// succeeds even when the workspace root is not a git repo, the empty name is
// unknown, and the failure to pick a repo surfaces only at the point a card
// needs it.
func TestPoolReposOnlyNoDefault(t *testing.T) {
	ctx := context.Background()
	ws := t.TempDir() // ws is deliberately NOT a git repo (multi-repo parent)
	repoA := filepath.Join(ws, "git", "a")
	if err := os.MkdirAll(repoA, 0o750); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repoA, "init", "-q", "-b", "main")

	p, err := NewPool(ctx, ws, "", []NamedRepo{{Name: "a", Root: repoA}}, &memForkStore{}, false)
	if err != nil {
		t.Fatalf("repos:-only pool with a non-git workspace root should build: %v", err)
	}
	if p.Known("") {
		t.Error("empty name should not be known with no default configured")
	}
	if !p.Known("a") {
		t.Error("configured named repo should be known")
	}
	// naming a configured repo resolves fine; the default does not exist.
	m, err := p.ManagerForName(ctx, "a")
	if err != nil {
		t.Fatalf("ManagerForName(a): %v", err)
	}
	if m.RepoRoot() != repoA {
		t.Errorf("repo root = %q, want %q", m.RepoRoot(), repoA)
	}
	if _, err := p.ManagerForName(ctx, ""); err == nil {
		t.Fatal("expected a no-default error for the empty name")
	} else if !strings.Contains(err.Error(), "no default repository configured") {
		t.Errorf("unexpected no-default error: %v", err)
	}
}
