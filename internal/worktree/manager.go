package worktree

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/morphia/gummi/internal/domain"
)

// Manager creates and tends the per-feature git worktrees nested under
// <root>/.gummi/worktrees. Every git invocation uses argument arrays;
// every feature-derived input is re-validated and every target path is
// verified to stay inside the worktrees directory before any git
// command runs.
type Manager struct {
	root string // absolute physical path of the main checkout
}

// NewManager binds a manager to the repo rooted at root. It verifies
// root really is the top level of a git working tree.
func NewManager(ctx context.Context, root string) (*Manager, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	top, err := runGit(ctx, abs, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("%s is not inside a git repository: %w", abs, err)
	}
	// Resolve both through symlinks before comparing: git prints the
	// physical path.
	realAbs, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}
	realTop, err := filepath.EvalSymlinks(top)
	if err != nil {
		return nil, err
	}
	if realAbs != realTop {
		return nil, fmt.Errorf("%s is not the repository root (top level is %s); gummi must run from the main checkout", abs, top)
	}
	// Keep the physical path: git prints physical paths (e.g. in
	// worktree list), so all later comparisons must share the namespace.
	return &Manager{root: realAbs}, nil
}

// Root returns the absolute repo root the manager is bound to.
func (m *Manager) Root() string { return m.root }

// worktreesDir is the directory all feature worktrees must live in.
func (m *Manager) worktreesDir() string {
	return filepath.Join(m.root, ".gummi", "worktrees")
}

// featurePaths validates the feature and derives its absolute worktree
// path, refusing anything that would escape .gummi/worktrees. This is
// the single chokepoint every operation goes through.
func (m *Manager) featurePaths(f *domain.Feature) (wtPath, branch string, err error) {
	if err := f.Validate(); err != nil {
		return "", "", fmt.Errorf("refusing worktree operation: %w", err)
	}
	base := m.worktreesDir()
	wtPath = filepath.Clean(filepath.Join(base, string(f.ID)))
	if filepath.Dir(wtPath) != base {
		return "", "", fmt.Errorf("refusing worktree operation: %s escapes %s", wtPath, base)
	}
	// A hostile repo can commit .gummi or .gummi/worktrees as a symlink
	// pointing outside the checkout; writing through it would escape
	// the repo, which the lexical check above cannot see.
	for _, p := range []string{filepath.Join(m.root, ".gummi"), base} {
		fi, err := os.Lstat(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue // not created yet — fine
			}
			return "", "", err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return "", "", fmt.Errorf("refusing worktree operation: %s is a symlink", p)
		}
	}
	return wtPath, f.BranchName(), nil
}

// Path returns the absolute worktree path for a (valid) feature.
func (m *Manager) Path(f *domain.Feature) (string, error) {
	p, _, err := m.featurePaths(f)
	return p, err
}

// Exists reports whether the feature's worktree directory is present.
func (m *Manager) Exists(ctx context.Context, f *domain.Feature) (bool, error) {
	p, _, err := m.featurePaths(f)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(p); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// requireWorktree returns the worktree path, erroring clearly when the
// directory is absent (git's own "cannot change to directory" is
// opaque).
func (m *Manager) requireWorktree(f *domain.Feature) (string, error) {
	p, _, err := m.featurePaths(f)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(p); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("feature %s has no worktree at %s", f.ID, p)
		}
		return "", err
	}
	return p, nil
}

// Create adds the feature's worktree at .gummi/worktrees/FD-NNN on a
// new branch gummi/FD-NNN-slug based at the main checkout's HEAD.
func (m *Manager) Create(ctx context.Context, f *domain.Feature) (string, error) {
	p, branch, err := m.featurePaths(f)
	if err != nil {
		return "", err
	}
	if _, err := runGit(ctx, m.root, "rev-parse", "--verify", "HEAD"); err != nil {
		return "", fmt.Errorf("repository has no commits yet; commit something before creating a feature worktree: %w", err)
	}
	if _, err := os.Stat(p); err == nil {
		return "", fmt.Errorf("worktree path %s already exists", p)
	}
	if ok, err := gitOK(ctx, m.root, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err != nil {
		return "", err
	} else if ok {
		return "", fmt.Errorf("branch %s already exists (leftover from an earlier worktree?); delete it first: git branch -D %s", branch, branch)
	}
	if err := os.MkdirAll(m.worktreesDir(), 0o750); err != nil {
		return "", err
	}
	if _, err := runGit(ctx, m.root, "worktree", "add", "-b", branch, "--", p); err != nil {
		return "", err
	}
	return p, nil
}

// Remove deletes the feature's worktree. A dirty worktree is refused
// unless force is set; the branch itself is left alone (see
// DeleteBranch).
func (m *Manager) Remove(ctx context.Context, f *domain.Feature, force bool) error {
	p, _, err := m.featurePaths(f)
	if err != nil {
		return err
	}
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, "--", p)
	if _, err := runGit(ctx, m.root, args...); err != nil {
		return err
	}
	return nil
}

// CommitFile writes content to relPath inside the feature's worktree
// and commits it to the feature branch. relPath is verified to stay
// inside the worktree.
func (m *Manager) CommitFile(ctx context.Context, f *domain.Feature, relPath, content, message string) error {
	p, err := m.requireWorktree(f)
	if err != nil {
		return err
	}
	dest := filepath.Clean(filepath.Join(p, relPath))
	if dest != p && !strings.HasPrefix(dest, p+string(filepath.Separator)) {
		return fmt.Errorf("refusing to write %s: escapes worktree %s", relPath, p)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return err
	}
	if err := os.WriteFile(dest, []byte(content), 0o600); err != nil {
		return err
	}
	if _, err := runGit(ctx, p, "add", "--", dest); err != nil {
		return err
	}
	// idempotent: identical content means nothing staged, nothing to do
	status, err := runGit(ctx, p, "status", "--porcelain", "--", dest)
	if err != nil {
		return err
	}
	if status == "" {
		return nil
	}
	if _, err := runGit(ctx, p, "commit", "-m", message, "--", dest); err != nil {
		return err
	}
	return nil
}

// FileCommitted reports whether relPath inside the feature's worktree
// is tracked by git (i.e. has been committed or staged).
func (m *Manager) FileCommitted(ctx context.Context, f *domain.Feature, relPath string) (bool, error) {
	p, err := m.requireWorktree(f)
	if err != nil {
		return false, err
	}
	return gitOK(ctx, p, "ls-files", "--error-unmatch", "--", filepath.Join(p, relPath))
}

// BranchExists reports whether the feature's branch ref exists.
func (m *Manager) BranchExists(ctx context.Context, f *domain.Feature) (bool, error) {
	_, branch, err := m.featurePaths(f)
	if err != nil {
		return false, err
	}
	return gitOK(ctx, m.root, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
}

// DeleteBranch removes the feature's branch. Without force it refuses
// branches that are not fully merged into HEAD (git -d semantics).
func (m *Manager) DeleteBranch(ctx context.Context, f *domain.Feature, force bool) error {
	_, branch, err := m.featurePaths(f)
	if err != nil {
		return err
	}
	flag := "-d"
	if force {
		flag = "-D"
	}
	_, err = runGit(ctx, m.root, "branch", flag, "--", branch)
	return err
}

// Dirty reports whether the feature's worktree has uncommitted changes
// (staged, unstaged, or untracked).
func (m *Manager) Dirty(ctx context.Context, f *domain.Feature) (bool, error) {
	p, err := m.requireWorktree(f)
	if err != nil {
		return false, err
	}
	out, err := runGit(ctx, p, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return out != "", nil
}

// Landed reports whether the feature branch's tip is an ancestor of
// the main checkout's HEAD — i.e. the branch has been merged (or
// fast-forwarded) into main. Squash-merges are not detected; that
// refinement is scheduled for M4.
func (m *Manager) Landed(ctx context.Context, f *domain.Feature) (bool, error) {
	_, branch, err := m.featurePaths(f)
	if err != nil {
		return false, err
	}
	return gitOK(ctx, m.root, "merge-base", "--is-ancestor", branch, "HEAD")
}

// RebaseOnMain rebases the feature branch onto the main checkout's
// current HEAD, inside the feature's worktree. When a started rebase
// stops on conflicts it is aborted so the worktree is never left
// mid-rebase; when the rebase could not start at all (e.g. dirty
// worktree) the original error is returned untouched.
func (m *Manager) RebaseOnMain(ctx context.Context, f *domain.Feature) error {
	p, err := m.requireWorktree(f)
	if err != nil {
		return err
	}
	mainHead, err := runGit(ctx, m.root, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if _, err := runGit(ctx, p, "rebase", mainHead); err != nil {
		if !m.rebaseInProgress(ctx, p) {
			return fmt.Errorf("rebase of %s did not start: %w", f.ID, err)
		}
		if _, abortErr := runGit(ctx, p, "rebase", "--abort"); abortErr != nil {
			return fmt.Errorf("rebase failed AND abort failed, worktree %s needs manual attention: %w (abort: %v)", p, err, abortErr)
		}
		return fmt.Errorf("rebase aborted cleanly after conflict: %w", err)
	}
	return nil
}

// Diff returns the unified diff of the feature branch against the point
// it forked from main: the merge base to the worktree (so both committed
// branch work and uncommitted edits show, without main's later commits
// appearing as spurious reversals). Empty when nothing changed.
func (m *Manager) Diff(ctx context.Context, f *domain.Feature) (string, error) {
	p, err := m.requireWorktree(f)
	if err != nil {
		return "", err
	}
	mainHead, err := runGit(ctx, m.root, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	base, err := runGit(ctx, p, "merge-base", mainHead, "HEAD")
	if err != nil {
		return "", err
	}
	return runGit(ctx, p, "diff", base)
}

// rebaseInProgress reports whether wt has rebase state on disk.
func (m *Manager) rebaseInProgress(ctx context.Context, wt string) bool {
	for _, dir := range []string{"rebase-merge", "rebase-apply"} {
		out, err := runGit(ctx, wt, "rev-parse", "--git-path", dir)
		if err != nil {
			continue
		}
		if !filepath.IsAbs(out) {
			out = filepath.Join(wt, out)
		}
		if _, err := os.Stat(out); err == nil {
			return true
		}
	}
	return false
}

// List returns the feature-worktree paths git currently knows about
// under .gummi/worktrees (not the whole repo's worktrees).
func (m *Manager) List(ctx context.Context) ([]string, error) {
	out, err := runGit(ctx, m.root, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	prefix := m.worktreesDir() + string(filepath.Separator)
	var paths []string
	for line := range strings.Lines(out) {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "worktree "); ok && strings.HasPrefix(rest, prefix) {
			paths = append(paths, rest)
		}
	}
	return paths, nil
}
