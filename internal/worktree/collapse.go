package worktree

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/morphis/gummi/internal/domain"
)

// Sentinel refusals Collapse returns before any git mutation, each naming a
// distinct precondition so a caller (the CLI, the TUI) can surface a
// targeted message instead of a raw git failure.
var (
	// ErrDirtyWorktree marks a worktree with uncommitted changes — collapsing
	// would silently fold them into the rewritten commit.
	ErrDirtyWorktree = errors.New("worktree has uncommitted changes")
	// ErrRebaseInProgress marks a worktree with a rebase, merge, or
	// cherry-pick left in flight.
	ErrRebaseInProgress = errors.New("a rebase, merge, or cherry-pick is in progress")
	// ErrNoCommitsBeyondBase marks a branch already sitting at (or behind)
	// the resolved base — there is nothing to collapse.
	ErrNoCommitsBeyondBase = errors.New("branch has no commits beyond the base")
	// ErrBaseNotAncestor marks a resolved base that is not in the branch's
	// history — resetting onto it would not be content-preserving.
	ErrBaseNotAncestor = errors.New("base is not an ancestor of the branch")
)

// CollapseInvariantError reports that a Collapse mutation broke the
// tree(HEAD_before) == tree(HEAD_after) invariant. The branch has already
// been restored to BeforeSHA by the time this is returned.
type CollapseInvariantError struct {
	BeforeSHA  string
	BeforeTree string
	AfterTree  string
}

func (e *CollapseInvariantError) Error() string {
	return fmt.Sprintf("collapse changed the branch's content (tree %s before, %s after) — restored the branch to %s",
		e.BeforeTree, e.AfterTree, e.BeforeSHA)
}

// worktreeDirty reports whether the git working tree at worktreePath has any
// uncommitted changes (staged, unstaged, or untracked) — the general-purpose
// version of MainTrackedDirty/TrackedDirty, scoped to any working tree rather
// than a specific feature's.
func worktreeDirty(ctx context.Context, worktreePath string) (bool, error) {
	out, err := runGit(ctx, worktreePath, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return out != "", nil
}

// firstLine returns s up to (not including) its first newline.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// Collapse rewrites the feature's branch, in place, to a single commit
// carrying message on top of baseSHA — the primitive behind `gummi squash`
// and (via a refactor) SquashMerge's own branch-collapsing step. It never
// touches the main checkout.
//
// Preconditions (checked before any mutation, each surfaced as a distinct
// sentinel): the worktree must be clean, no rebase/merge/cherry-pick may be
// in flight, baseSHA must be an ancestor of the branch's HEAD, and the
// branch must carry at least one commit beyond baseSHA.
//
// No-op: if the branch already sits exactly one commit past baseSHA and
// that commit's subject line already matches message's, Collapse returns
// ("", nil) without touching the branch.
//
// Invariant: the collapsed branch's tree is byte-identical to the
// pre-collapse tree. If a mutation (e.g. a commit hook) breaks that, the
// branch is restored to its pre-collapse tip and a *CollapseInvariantError
// is returned.
func (m *Manager) Collapse(ctx context.Context, f *domain.Feature, message, baseSHA string) (string, error) {
	if strings.TrimSpace(message) == "" {
		return "", fmt.Errorf("refusing collapse of %s: empty commit message", f.ID)
	}
	wtPath, branch, err := m.featurePaths(f)
	if err != nil {
		return "", err
	}

	// No-op detection runs first: an already-collapsed branch with a
	// matching subject needs no mutation, dirty tree or not.
	n, err := runGit(ctx, wtPath, "rev-list", "--count", baseSHA+"..HEAD")
	if err != nil {
		return "", err
	}
	if n == "1" {
		subject, err := runGit(ctx, wtPath, "log", "-1", "--format=%s", "HEAD")
		if err != nil {
			return "", err
		}
		if subject == firstLine(message) {
			return "", nil
		}
	}

	if m.rebaseInProgress(ctx, wtPath) {
		return "", fmt.Errorf("%s: %w", f.ID, ErrRebaseInProgress)
	}
	for _, head := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD"} {
		if inProgress, err := gitOK(ctx, wtPath, "rev-parse", "--verify", "--quiet", head); err != nil {
			return "", err
		} else if inProgress {
			return "", fmt.Errorf("%s: %w", f.ID, ErrRebaseInProgress)
		}
	}
	if dirty, err := worktreeDirty(ctx, wtPath); err != nil {
		return "", err
	} else if dirty {
		return "", fmt.Errorf("%s: %w", f.ID, ErrDirtyWorktree)
	}
	if ancestor, err := gitOK(ctx, wtPath, "merge-base", "--is-ancestor", baseSHA, "HEAD"); err != nil {
		return "", err
	} else if !ancestor {
		return "", fmt.Errorf("%s: %w", f.ID, ErrBaseNotAncestor)
	}
	if n == "0" {
		return "", fmt.Errorf("%s: %w", f.ID, ErrNoCommitsBeyondBase)
	}

	preSHA, err := runGit(ctx, wtPath, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	preTree, err := runGit(ctx, wtPath, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return "", err
	}

	restore := func() {
		_, _ = runGit(ctx, wtPath, "update-ref", "refs/heads/"+branch, preSHA)
		_, _ = runGit(ctx, wtPath, "reset", "--hard", preSHA)
	}

	if _, err := runGit(ctx, wtPath, "reset", "--soft", baseSHA); err != nil {
		return "", err
	}
	if _, err := runGit(ctx, wtPath, "commit", "-m", message); err != nil {
		restore()
		return "", err
	}
	afterTree, err := runGit(ctx, wtPath, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return "", err
	}
	if afterTree != preTree {
		restore()
		return "", &CollapseInvariantError{BeforeSHA: preSHA, BeforeTree: preTree, AfterTree: afterTree}
	}
	return runGit(ctx, wtPath, "rev-parse", "HEAD")
}
