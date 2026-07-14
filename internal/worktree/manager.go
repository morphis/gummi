package worktree

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/morphis/gummi/internal/atomicfile"
	"github.com/morphis/gummi/internal/domain"
)

// Manager creates and tends the per-feature git worktrees nested under
// <root>/.gummi/worktrees. Every git invocation uses argument arrays;
// every feature-derived input is re-validated and every target path is
// verified to stay inside the worktrees directory before any git
// command runs.
type Manager struct {
	root string // absolute physical path of the main checkout

	// mainMu serializes gummi-initiated mutations of the main checkout
	// (squash merges, escape reverts) and guards mainGen, the sanctioned
	// mutation counter the escape check reads (see primary.go).
	mainMu  sync.Mutex
	mainGen uint64
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
	// The lexical check above is not enough: a hostile repo can commit an
	// intermediate path component (e.g. .gummi/specs) as a symlink, which
	// `worktree add` checks out, so the resolved destination escapes the
	// worktree. Verify the deepest existing ancestor resolves back inside
	// the worktree before creating or writing anything through it.
	if err := ensureContained(p, dest); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return err
	}
	// Atomic write: a rename replaces a symlink sitting at dest rather than
	// following it to write outside the repo (the case where git would
	// otherwise stage nothing and this would falsely report success), and it
	// never leaves a torn file.
	if err := atomicfile.Write(dest, []byte(content), 0o600); err != nil {
		return err
	}
	// force past the repo-wide .gummi exclusion (EnsureGummiExcluded):
	// artifacts are the one .gummi content gummi itself commits, so the
	// spec travels with its branch while agents' bulk adds skip .gummi.
	if _, err := runGit(ctx, p, "add", "-f", "--", dest); err != nil {
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

// CommitAll stages everything in the feature's worktree — tracked edits
// and new files alike — and commits it to the feature branch with
// message, reporting whether a commit was made (a clean worktree is a
// no-op). This is the checkpoint behind gummi-owned commits: agent work
// is committed as stages complete, and the branch later lands on main as
// a single squash commit, so checkpoint granularity never reaches main's
// history.
func (m *Manager) CommitAll(ctx context.Context, f *domain.Feature, message string) (bool, error) {
	if strings.TrimSpace(message) == "" {
		return false, fmt.Errorf("refusing checkpoint commit for %s: empty message", f.ID)
	}
	p, err := m.requireWorktree(f)
	if err != nil {
		return false, err
	}
	if _, err := runGit(ctx, p, "add", "-A"); err != nil {
		return false, err
	}
	staged, err := runGit(ctx, p, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(staged) == "" {
		return false, nil
	}
	if _, err := runGit(ctx, p, "commit", "-m", message); err != nil {
		return false, err
	}
	return true, nil
}

// ensureContained verifies that dest, once existing symlinks are resolved,
// still lives inside root. It resolves the deepest already-existing
// ancestor of dest (any symlinked component in the chain is dereferenced
// there) and checks its real path against root's real path. This catches a
// committed symlink anywhere along the path that would divert the write
// outside the worktree.
func ensureContained(root, dest string) error {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	anc := dest
	for {
		if _, err := os.Lstat(anc); err == nil {
			break
		}
		parent := filepath.Dir(anc)
		if parent == anc {
			break
		}
		anc = parent
	}
	realAnc, err := filepath.EvalSymlinks(anc)
	if err != nil {
		return err
	}
	if realAnc != realRoot && !strings.HasPrefix(realAnc, realRoot+string(filepath.Separator)) {
		return fmt.Errorf("refusing to write %s: resolves outside worktree %s", dest, root)
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

// DeleteLandedBranch removes the feature's branch after its work landed
// on main. It tries git's own -d safety first; when git refuses ("not
// fully merged" — the squash-merge case, where the branch's commits are
// not ancestors of main even though their content is in) it re-verifies
// via the merge-tree content check and only then force-deletes. That
// content check is stronger than git's ancestor test, so nothing
// unlanded can slip through the -D.
func (m *Manager) DeleteLandedBranch(ctx context.Context, f *domain.Feature) error {
	_, branch, err := m.featurePaths(f)
	if err != nil {
		return err
	}
	_, derr := runGit(ctx, m.root, "branch", "-d", "--", branch)
	if derr == nil {
		return nil
	}
	landed, err := m.squashLanded(ctx, branch)
	if err != nil || !landed {
		return derr
	}
	_, err = runGit(ctx, m.root, "branch", "-D", "--", branch)
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

// TrackedDirty reports whether the worktree has uncommitted changes to
// tracked files (staged or unstaged), ignoring untracked files. This is the
// signal that force-removing the worktree would lose real work: untracked
// files are the disposable build artifacts a landed-branch cleanup is meant
// to discard, but modified tracked files are rework that isn't in main.
func (m *Manager) TrackedDirty(ctx context.Context, f *domain.Feature) (bool, error) {
	p, err := m.requireWorktree(f)
	if err != nil {
		return false, err
	}
	out, err := runGit(ctx, p, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// MainTrackedDirty reports uncommitted changes to tracked files (staged
// or unstaged) in the main checkout. A squash merge commits into main,
// so anything already modified there would be swept into the merge
// commit. Untracked files are ignored: git itself refuses a merge that
// would overwrite one. .gummi is also ignored: its index state is
// gummi's own machinery (notably the staged deletions EnsureGummiExcluded
// leaves after untracking a once-committed .gummi) and must never
// deadlock a land.
func (m *Manager) MainTrackedDirty(ctx context.Context) (bool, error) {
	out, err := runGit(ctx, m.root, "status", "--porcelain", "--untracked-files=no", "--", ":(exclude).gummi")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// Landed reports whether the feature branch has merged into main, by
// either of two routes:
//
//   - Regular / fast-forward merge: the branch tip is an ancestor of the
//     main checkout's HEAD and has moved past that HEAD (the second clause
//     excludes a fresh branch sitting at HEAD, a trivial ancestor).
//   - Squash merge: the branch has its own commits, but every change it
//     makes is already present in main — merging it would be a no-op — so
//     its work has landed even though its commits aren't ancestors.
//
// A branch merged by fast-forward while main had no other activity (HEAD
// == branch tip) still reads as not-yet-landed until main next advances.
func (m *Manager) Landed(ctx context.Context, f *domain.Feature) (bool, error) {
	_, branch, err := m.featurePaths(f)
	if err != nil {
		return false, err
	}
	anc, err := gitOK(ctx, m.root, "merge-base", "--is-ancestor", branch, "HEAD")
	if err != nil {
		return false, err
	}
	branchTip, err := runGit(ctx, m.root, "rev-parse", branch)
	if err != nil {
		return false, err
	}
	head, err := runGit(ctx, m.root, "rev-parse", "HEAD")
	if err != nil {
		return false, err
	}
	if anc {
		return branchTip != head, nil
	}
	return m.squashLanded(ctx, branch)
}

// squashLanded reports whether branch has its own commits whose changes
// are all already in main — i.e. a squash-merge landed it. It merges the
// branch against main in memory (no working-tree touch) and checks the
// result tree is identical to main's: if merging adds nothing, the work
// is in. Any merge-tree failure (conflict, or a git too old for
// --write-tree) reads as not-landed, the safe default.
func (m *Manager) squashLanded(ctx context.Context, branch string) (bool, error) {
	base, err := runGit(ctx, m.root, "merge-base", "HEAD", branch)
	if err != nil {
		return false, err
	}
	n, err := runGit(ctx, m.root, "rev-list", "--count", base+".."+branch)
	if err != nil {
		return false, err
	}
	if n == "0" { // no commits of its own — a fresh/empty branch, not landed
		return false, nil
	}
	merged, err := runGit(ctx, m.root, "merge-tree", "--write-tree", "HEAD", branch)
	if err != nil {
		return false, nil // conflict or unsupported: treat as not landed
	}
	mainTree, err := runGit(ctx, m.root, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return false, err
	}
	// --write-tree prints the merged tree oid on the first line.
	if i := strings.IndexByte(merged, '\n'); i >= 0 {
		merged = merged[:i]
	}
	return merged == mainTree, nil
}

// RebaseConflictError reports that a rebase stopped on conflicts and was
// aborted (the worktree is left clean, on its original tip). Files lists
// the paths that conflicted, so the UI can tell the user what to resolve.
type RebaseConflictError struct {
	Files []string
}

func (e *RebaseConflictError) Error() string {
	if len(e.Files) == 0 {
		return "rebase hit conflicts and was aborted (worktree clean)"
	}
	return "rebase conflicts in " + strings.Join(e.Files, ", ") + " — aborted, worktree clean"
}

// RebaseOnMain rebases the feature branch onto the main checkout's
// current HEAD, inside the feature's worktree. When a started rebase
// stops on conflicts it is aborted so the worktree is never left
// mid-rebase, and a *RebaseConflictError naming the conflicted files is
// returned; when the rebase could not start at all (e.g. dirty worktree)
// the original error is returned untouched.
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
		// capture what conflicted before we abort and lose the state
		conflicts := m.conflictedFiles(ctx, p)
		if _, abortErr := runGit(ctx, p, "rebase", "--abort"); abortErr != nil {
			return fmt.Errorf("rebase failed AND abort failed, worktree %s needs manual attention: %w (abort: %v)", p, err, abortErr)
		}
		return &RebaseConflictError{Files: conflicts}
	}
	return nil
}

// MainHead returns the main checkout's current HEAD commit id — the
// commit RebaseOnMain rebases onto, exposed so an agent-driven rebase
// can be pointed at the exact same target.
func (m *Manager) MainHead(ctx context.Context) (string, error) {
	return runGit(ctx, m.root, "rev-parse", "HEAD")
}

// RebaseInProgress reports whether the feature's worktree has a rebase
// in flight (stopped on conflicts, or otherwise unfinished).
func (m *Manager) RebaseInProgress(ctx context.Context, f *domain.Feature) (bool, error) {
	p, err := m.requireWorktree(f)
	if err != nil {
		return false, err
	}
	return m.rebaseInProgress(ctx, p), nil
}

// AbortRebase aborts an in-flight rebase in the feature's worktree,
// restoring it to its pre-rebase tip; with none in flight it is a no-op
// and reports false. This is the safety net behind the agent-driven
// rebase: whatever a session leaves mid-rebase is aborted, so a worktree
// is never at rest mid-rebase.
func (m *Manager) AbortRebase(ctx context.Context, f *domain.Feature) (bool, error) {
	p, err := m.requireWorktree(f)
	if err != nil {
		return false, err
	}
	if !m.rebaseInProgress(ctx, p) {
		return false, nil
	}
	if _, err := runGit(ctx, p, "rebase", "--abort"); err != nil {
		return false, err
	}
	return true, nil
}

// RebasedOnMain reports whether the feature branch's history now
// includes the main checkout's HEAD — the success test for a completed
// rebase. A conflicted, aborted, or never-started rebase leaves main's
// HEAD outside the branch (assuming main has moved since the branch was
// cut; a branch already at main's HEAD trivially passes).
func (m *Manager) RebasedOnMain(ctx context.Context, f *domain.Feature) (bool, error) {
	p, err := m.requireWorktree(f)
	if err != nil {
		return false, err
	}
	mainHead, err := m.MainHead(ctx)
	if err != nil {
		return false, err
	}
	return gitOK(ctx, p, "merge-base", "--is-ancestor", mainHead, "HEAD")
}

// conflictedFiles lists the unmerged paths in wt (empty on any error, so
// callers still get a useful conflict error even if the list is missing).
func (m *Manager) conflictedFiles(ctx context.Context, wt string) []string {
	out, err := runGit(ctx, wt, "diff", "--name-only", "--diff-filter=U")
	if err != nil || out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// MergeConflictError reports that a squash merge stopped on conflicts
// and was undone (the main checkout is left clean, at its original
// HEAD). Files lists the paths that conflicted.
type MergeConflictError struct {
	Files []string
}

func (e *MergeConflictError) Error() string {
	if len(e.Files) == 0 {
		return "squash merge hit conflicts and was undone (main checkout clean)"
	}
	return "squash merge conflicts in " + strings.Join(e.Files, ", ") + " — undone, main checkout clean"
}

// SquashMerge lands the feature branch on the main checkout as a single
// squash commit carrying message. It refuses when main has tracked
// changes (they would be swept into the commit), when the branch has no
// commits of its own, or when its content is already in main. A
// conflicted merge is undone with reset --merge — a squash merge writes
// no MERGE_HEAD, so merge --abort cannot — and reported as a
// *MergeConflictError; main is left clean on every path short of a
// failed reset.
func (m *Manager) SquashMerge(ctx context.Context, f *domain.Feature, message string) error {
	if strings.TrimSpace(message) == "" {
		return fmt.Errorf("refusing squash merge of %s: empty commit message", f.ID)
	}
	// A land is a sanctioned main-checkout mutation: take the mutation
	// lock for its duration and bump the generation up front, so an escape
	// check overlapping the merge reads a moved generation and never
	// misattributes (or reverts) the landing commit (see primary.go).
	m.mainMu.Lock()
	defer m.mainMu.Unlock()
	m.mainGen++
	_, branch, err := m.featurePaths(f)
	if err != nil {
		return err
	}
	if ok, err := gitOK(ctx, m.root, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("feature %s has no branch %s", f.ID, branch)
	}
	if dirty, err := m.MainTrackedDirty(ctx); err != nil {
		return err
	} else if dirty {
		return fmt.Errorf("main checkout has uncommitted changes — commit or stash them before merging")
	}
	base, err := runGit(ctx, m.root, "merge-base", "HEAD", branch)
	if err != nil {
		return err
	}
	if n, err := runGit(ctx, m.root, "rev-list", "--count", base+".."+branch); err != nil {
		return err
	} else if n == "0" {
		return fmt.Errorf("branch %s has no commits to merge", branch)
	}
	if _, err := runGit(ctx, m.root, "merge", "--squash", branch); err != nil {
		// capture what conflicted before the reset wipes the state
		conflicts := m.conflictedFiles(ctx, m.root)
		if _, resetErr := runGit(ctx, m.root, "reset", "--merge"); resetErr != nil {
			return fmt.Errorf("squash merge failed AND reset failed, main checkout needs manual attention: %w (reset: %v)", err, resetErr)
		}
		if len(conflicts) > 0 {
			return &MergeConflictError{Files: conflicts}
		}
		return err
	}
	// "Already up to date" stages nothing: the branch's content is in
	// main already, i.e. it landed some other way.
	if clean, err := gitOK(ctx, m.root, "diff", "--cached", "--quiet"); err != nil {
		return err
	} else if clean {
		return fmt.Errorf("nothing to merge — %s already landed on main", branch)
	}
	if _, err := runGit(ctx, m.root, "commit", "-m", message); err != nil {
		if _, resetErr := runGit(ctx, m.root, "reset", "--merge"); resetErr != nil {
			return fmt.Errorf("squash commit failed AND reset failed, main checkout needs manual attention: %w (reset: %v)", err, resetErr)
		}
		return err
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
