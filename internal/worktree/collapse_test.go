package worktree

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

var shaRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

// checkpointedFeature creates a feature worktree with three checkpoint
// commits beyond base (the repo's initial HEAD, the resolved collapse base
// in every test that doesn't need a different one) and returns the manager,
// feature, worktree path, and base sha.
func checkpointedFeature(t *testing.T, root string) (m *Manager, f *domain.Feature, wtPath, base string) {
	t.Helper()
	m = newManager(t, root)
	f = feature(9, "Three checkpoints")
	base = mustGit(t, root, "rev-parse", "HEAD")
	p, err := m.Create(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		writeFile(t, p, fmt.Sprintf("cp%d.txt", i), fmt.Sprintf("checkpoint %d\n", i))
		mustGit(t, p, "add", ".")
		mustGit(t, p, "commit", "-q", "-m", fmt.Sprintf("FD-009: checkpoint %d", i))
	}
	return m, f, p, base
}

// TestCollapse proves the golden path: Collapse rewrites the branch to one
// commit off base, preserves the tree exactly, and returns the new sha.
func TestCollapse(t *testing.T) {
	root := newRepo(t)
	m, f, p, base := checkpointedFeature(t, root)
	preTree := mustGit(t, p, "rev-parse", "HEAD^{tree}")

	sha, err := m.Collapse(ctx, f, "feat(x): collapsed", base)
	if err != nil {
		t.Fatal(err)
	}
	if !shaRe.MatchString(sha) {
		t.Errorf("sha = %q, want a 40-hex sha", sha)
	}
	if got := mustGit(t, p, "rev-parse", "HEAD"); got != sha {
		t.Errorf("returned sha %q != branch HEAD %q", sha, got)
	}
	if n := mustGit(t, root, "rev-list", "--count", base+".."+f.BranchName()); n != "1" {
		t.Errorf("commits beyond base = %s, want 1", n)
	}
	if got := mustGit(t, p, "rev-parse", "HEAD^{tree}"); got != preTree {
		t.Errorf("tree after collapse = %s, want %s (content-preservation broken)", got, preTree)
	}
	if got := mustGit(t, p, "log", "-1", "--format=%s"); got != "feat(x): collapsed" {
		t.Errorf("subject = %q, want %q", got, "feat(x): collapsed")
	}
}

// TestCollapseNoOp proves that collapsing an already-one-commit branch with
// a matching subject is a no-op: it returns "" and touches nothing.
func TestCollapseNoOp(t *testing.T) {
	root := newRepo(t)
	m, f, p, base := checkpointedFeature(t, root)

	if _, err := m.Collapse(ctx, f, "feat(x): collapsed", base); err != nil {
		t.Fatal(err)
	}
	tip := mustGit(t, p, "rev-parse", "HEAD")

	sha, err := m.Collapse(ctx, f, "feat(x): collapsed", base)
	if err != nil {
		t.Fatal(err)
	}
	if sha != "" {
		t.Errorf("no-op sha = %q, want empty", sha)
	}
	if got := mustGit(t, p, "rev-parse", "HEAD"); got != tip {
		t.Errorf("branch moved on no-op: %s -> %s", tip, got)
	}
}

// TestCollapseRefuses proves each refusal guard fires with its targeted
// sentinel and leaves the branch ref untouched.
func TestCollapseRefuses(t *testing.T) {
	t.Run("dirty worktree", func(t *testing.T) {
		root := newRepo(t)
		m, f, p, base := checkpointedFeature(t, root)
		tip := mustGit(t, p, "rev-parse", "HEAD")
		writeFile(t, p, "cp1.txt", "uncommitted edit\n")

		_, err := m.Collapse(ctx, f, "feat(x): collapsed", base)
		if !errors.Is(err, ErrDirtyWorktree) {
			t.Fatalf("err = %v, want ErrDirtyWorktree", err)
		}
		if got := mustGit(t, p, "rev-parse", "HEAD"); got != tip {
			t.Errorf("branch moved on refusal: %s -> %s", tip, got)
		}
	})

	t.Run("rebase in progress", func(t *testing.T) {
		root := newRepo(t)
		m, f, p, base := checkpointedFeature(t, root)
		// diverge base and branch on the same file so a rebase conflicts and
		// leaves rebase state on disk instead of completing cleanly.
		writeFile(t, p, "cp1.txt", "branch side\n")
		mustGit(t, p, "commit", "-qam", "branch edits cp1")
		writeFile(t, root, "cp1.txt", "main side\n")
		mustGit(t, root, "add", ".")
		mustGit(t, root, "commit", "-q", "-m", "main edits cp1")
		newBase := mustGit(t, root, "rev-parse", "HEAD")
		if _, err := runGit(ctx, p, "rebase", newBase); err == nil {
			t.Fatal("expected the rebase to conflict")
		}
		tip := mustGit(t, p, "rev-parse", "HEAD")

		_, err := m.Collapse(ctx, f, "feat(x): collapsed", base)
		if !errors.Is(err, ErrRebaseInProgress) {
			t.Fatalf("err = %v, want ErrRebaseInProgress", err)
		}
		if got := mustGit(t, p, "rev-parse", "HEAD"); got != tip {
			t.Errorf("branch moved on refusal: %s -> %s", tip, got)
		}
		// leave the worktree usable for t.TempDir's cleanup
		_, _ = runGit(ctx, p, "rebase", "--abort")
	})

	t.Run("no commits beyond base", func(t *testing.T) {
		root := newRepo(t)
		m, f, p, _ := checkpointedFeature(t, root)
		tip := mustGit(t, p, "rev-parse", "HEAD")

		// base == the branch's own tip: trivially an ancestor, zero commits
		// beyond it.
		_, err := m.Collapse(ctx, f, "feat(x): collapsed", tip)
		if !errors.Is(err, ErrNoCommitsBeyondBase) {
			t.Fatalf("err = %v, want ErrNoCommitsBeyondBase", err)
		}
		if got := mustGit(t, p, "rev-parse", "HEAD"); got != tip {
			t.Errorf("branch moved on refusal: %s -> %s", tip, got)
		}
	})

	t.Run("base not ancestor", func(t *testing.T) {
		root := newRepo(t)
		m, f, p, _ := checkpointedFeature(t, root)
		tip := mustGit(t, p, "rev-parse", "HEAD")

		// a second, unrelated feature branch forked from the same main
		// diverges from f's branch — its tip is not an ancestor of f's HEAD.
		other := feature(10, "Unrelated branch")
		op, err := m.Create(ctx, other)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, op, "other.txt", "unrelated work\n")
		mustGit(t, op, "add", ".")
		mustGit(t, op, "commit", "-q", "-m", "FD-010: unrelated work")
		unrelated := mustGit(t, op, "rev-parse", "HEAD")

		_, err = m.Collapse(ctx, f, "feat(x): collapsed", unrelated)
		if !errors.Is(err, ErrBaseNotAncestor) {
			t.Fatalf("err = %v, want ErrBaseNotAncestor", err)
		}
		if got := mustGit(t, p, "rev-parse", "HEAD"); got != tip {
			t.Errorf("branch moved on refusal: %s -> %s", tip, got)
		}
	})
}

// TestCollapseInvariantRestore proves that when a mutation (a pre-commit
// hook, here — a prepare-commit-msg hook runs too late to affect the tree
// git actually writes) breaks the tree(HEAD_before) == tree(HEAD_after)
// invariant, Collapse restores the branch to its pre-collapse tip and
// returns a typed error.
func TestCollapseInvariantRestore(t *testing.T) {
	root := newRepo(t)
	m, f, p, base := checkpointedFeature(t, root)
	preSHA := mustGit(t, p, "rev-parse", "HEAD")
	preCount := mustGit(t, root, "rev-list", "--count", base+".."+f.BranchName())

	hookDir := filepath.Join(t.TempDir(), "hooks")
	if err := os.MkdirAll(hookDir, 0o750); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(hookDir, "pre-commit")
	script := "#!/bin/sh\necho corruption >> cp1.txt\ngit add cp1.txt\n"
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	mustGit(t, p, "config", "core.hooksPath", hookDir)

	_, err := m.Collapse(ctx, f, "feat(x): collapsed", base)
	var ie *CollapseInvariantError
	if !errors.As(err, &ie) {
		t.Fatalf("err = %v, want *CollapseInvariantError", err)
	}
	if got := mustGit(t, p, "rev-parse", "HEAD"); got != preSHA {
		t.Errorf("HEAD not restored: got %s, want %s", got, preSHA)
	}
	if got := mustGit(t, root, "rev-list", "--count", base+".."+f.BranchName()); got != preCount {
		t.Errorf("commit count after restore = %s, want %s", got, preCount)
	}
}
