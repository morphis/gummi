package worktree

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// openTestStore opens a throwaway state.Store for a collapse-base test.
func openTestStore(t *testing.T) *state.Store {
	t.Helper()
	s, err := state.OpenStore(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// fakeOriginMain points refs/remotes/origin/main at sha, so a test repo with
// no real remote can still exercise the "no parent" collapse-base arm.
func fakeOriginMain(t *testing.T, root, sha string) {
	t.Helper()
	mustGit(t, root, "update-ref", "refs/remotes/origin/main", sha)
}

// dependentFeature builds a feature parked before its coding stage, so
// AddDependency's late-attachment guard doesn't refuse the edge these tests
// need to set up.
func dependentFeature(num int, title string) *domain.Feature {
	f := feature(num, title)
	f.Stage = domain.StageSpec
	return f
}

func TestResolveCollapseBase(t *testing.T) {
	t.Run("no parent resolves to origin/main", func(t *testing.T) {
		root := newRepo(t)
		mainHead := mustGit(t, root, "rev-parse", "HEAD")
		fakeOriginMain(t, root, mainHead)
		m := newManager(t, root)
		store := openTestStore(t)
		f := feature(9, "Child card")
		if err := store.CreateFeature(ctx, f); err != nil {
			t.Fatal(err)
		}
		if _, err := m.Create(ctx, f); err != nil {
			t.Fatal(err)
		}

		base, err := ResolveCollapseBase(ctx, store, m, f)
		if err != nil {
			t.Fatal(err)
		}
		if base != mainHead {
			t.Errorf("base = %s, want origin/main %s", base, mainHead)
		}
	})

	t.Run("one parent resolves to its branch tip", func(t *testing.T) {
		root := newRepo(t)
		fakeOriginMain(t, root, mustGit(t, root, "rev-parse", "HEAD"))
		m := newManager(t, root)
		store := openTestStore(t)

		parent := feature(1, "Parent card")
		if err := store.CreateFeature(ctx, parent); err != nil {
			t.Fatal(err)
		}
		pPath, err := m.Create(ctx, parent)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, pPath, "parent.txt", "parent work\n")
		mustGit(t, pPath, "add", ".")
		mustGit(t, pPath, "commit", "-q", "-m", "parent work")
		parentTip := mustGit(t, root, "rev-parse", parent.BranchName())

		child := dependentFeature(2, "Child card")
		if err := store.CreateFeature(ctx, child); err != nil {
			t.Fatal(err)
		}
		if err := store.AddDependency(ctx, child.ID, parent.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := m.Create(ctx, child); err != nil {
			t.Fatal(err)
		}

		base, err := ResolveCollapseBase(ctx, store, m, child)
		if err != nil {
			t.Fatal(err)
		}
		if base != parentTip {
			t.Errorf("base = %s, want parent tip %s", base, parentTip)
		}
	})

	t.Run("two or more parents is a hard error", func(t *testing.T) {
		root := newRepo(t)
		fakeOriginMain(t, root, mustGit(t, root, "rev-parse", "HEAD"))
		m := newManager(t, root)
		store := openTestStore(t)

		p1, p2 := feature(1, "Parent one"), feature(2, "Parent two")
		if err := store.CreateFeature(ctx, p1); err != nil {
			t.Fatal(err)
		}
		if err := store.CreateFeature(ctx, p2); err != nil {
			t.Fatal(err)
		}
		child := dependentFeature(3, "Child card")
		if err := store.CreateFeature(ctx, child); err != nil {
			t.Fatal(err)
		}
		if err := store.AddDependency(ctx, child.ID, p1.ID); err != nil {
			t.Fatal(err)
		}
		if err := store.AddDependency(ctx, child.ID, p2.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := m.Create(ctx, child); err != nil {
			t.Fatal(err)
		}

		_, err := ResolveCollapseBase(ctx, store, m, child)
		if err == nil || !strings.Contains(err.Error(), "2 dependencies") {
			t.Fatalf("err = %v, want a 2-dependencies error", err)
		}
	})

	t.Run("unresolvable parent branch is a hard error, not a fallback", func(t *testing.T) {
		root := newRepo(t)
		fakeOriginMain(t, root, mustGit(t, root, "rev-parse", "HEAD"))
		m := newManager(t, root)
		store := openTestStore(t)

		parent := feature(1, "Parent card")
		if err := store.CreateFeature(ctx, parent); err != nil {
			t.Fatal(err)
		}
		// Parent has never had a worktree/branch created for it.
		child := dependentFeature(2, "Child card")
		if err := store.CreateFeature(ctx, child); err != nil {
			t.Fatal(err)
		}
		if err := store.AddDependency(ctx, child.ID, parent.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := m.Create(ctx, child); err != nil {
			t.Fatal(err)
		}

		_, err := ResolveCollapseBase(ctx, store, m, child)
		if err == nil {
			t.Fatal("unresolvable parent branch accepted")
		}
	})
}
