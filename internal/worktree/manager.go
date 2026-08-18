package worktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// ForkPointStore is the subset of the state store the manager needs to
// persist and read a feature's recorded fork-point SHA. The concrete
// *state.Store satisfies it; tests swap in an in-memory stub.
type ForkPointStore interface {
	// ForkPoint reads a feature's recorded fork-point SHA; the empty string
	// when the worktree predates drift detection (the lazy-backfill sentinel).
	ForkPoint(ctx context.Context, id domain.FeatureID) (string, error)
	// SetForkPoint stamps the SHA. It refuses to overwrite a non-empty value
	// (stamped-once invariant, reported via state.ErrForkPointStamped), so
	// Create and the lazy backfill cannot race a stored SHA into being
	// overwritten.
	SetForkPoint(ctx context.Context, id domain.FeatureID, sha string) error
	// ReanchorForkPoint overwrites a feature's recorded fork-point SHA
	// unconditionally — the single explicit re-stamp, distinct from the
	// stamped-once SetForkPoint, reached only through the manager's re-anchor
	// operation after it has verified main's HEAD is in the branch's history.
	ReanchorForkPoint(ctx context.Context, id domain.FeatureID, sha string) error
	// ClearForkPoint resets a feature's recorded fork-point SHA to the empty
	// backfill sentinel, so the next Create on the row re-anchors the fork to
	// main's then-current head.
	ClearForkPoint(ctx context.Context, id domain.FeatureID) error
}

// Manager creates and tends the per-feature git worktrees nested under
// <root>/.gummi/worktrees. Every git invocation uses argument arrays;
// every feature-derived input is re-validated and every target path is
// verified to stay inside the worktrees directory before any git
// command runs.
type Manager struct {
	// repo is the absolute physical path of the managed git repository
	// (the main checkout). All git commands run here.
	repo string
	// wsRoot is the absolute physical path of the workspace root, where
	// .gummi lives. Worktrees are nested under wsRoot/.gummi/worktrees,
	// which is outside the repo tree in the nested layout.
	wsRoot string

	// forkStore persists each worktree's recorded fork-point SHA, the
	// anchor diff-based stages check against for drift.
	forkStore ForkPointStore

	// mainMu serializes gummi-initiated mutations of the main checkout
	// (squash merges).
	mainMu sync.Mutex
}

// NewManager binds a manager to the workspace rooted at ws and the git
// repository rooted at repo. It verifies repo really is the top level of a
// git working tree; ws may equal repo (sibling layout) or contain it
// (nested layout).
func NewManager(ctx context.Context, ws, repo string, fs ForkPointStore) (*Manager, error) {
	absWs, err := filepath.Abs(ws)
	if err != nil {
		return nil, err
	}
	absRepo, err := filepath.Abs(repo)
	if err != nil {
		return nil, err
	}
	// --show-toplevel runs against the repo root and must equal the repo
	// root's physical path — the equality check that once constrained the
	// workspace root now constrains the repo root.
	top, err := runGit(ctx, absRepo, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("%s is not inside a git repository: %w", absRepo, err)
	}
	realRepo, err := filepath.EvalSymlinks(absRepo)
	if err != nil {
		return nil, err
	}
	realTop, err := filepath.EvalSymlinks(top)
	if err != nil {
		return nil, err
	}
	if realRepo != realTop {
		return nil, fmt.Errorf("%s is not the repository root (top level is %s); gummi must run from the main checkout", absRepo, top)
	}
	realWs, err := filepath.EvalSymlinks(absWs)
	if err != nil {
		return nil, err
	}
	// Keep the physical paths: git prints physical paths (e.g. in
	// worktree list), so all later comparisons must share the namespace.
	return &Manager{repo: realRepo, wsRoot: realWs, forkStore: fs}, nil
}

// Root returns the absolute workspace root the manager is bound to — the
// base .gummi-relative paths (WorktreePath/ArtifactPath) join onto.
func (m *Manager) Root() string { return m.wsRoot }

// RepoRoot returns the absolute git repository root the manager's git
// commands run against — the base for agent workdirs and the main checkout.
func (m *Manager) RepoRoot() string { return m.repo }

// worktreesDir is the directory all feature worktrees must live in.
func (m *Manager) worktreesDir() string {
	return filepath.Join(m.wsRoot, ".gummi", "worktrees")
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
	for _, p := range []string{filepath.Join(m.wsRoot, ".gummi"), base} {
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
	if _, err := runGit(ctx, m.repo, "rev-parse", "--verify", "HEAD"); err != nil {
		return "", fmt.Errorf("repository has no commits yet; commit something before creating a feature worktree: %w", err)
	}
	if _, err := os.Stat(p); err == nil {
		return "", fmt.Errorf("worktree path %s already exists", p)
	}
	if ok, err := gitOK(ctx, m.repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err != nil {
		return "", err
	} else if ok {
		return "", fmt.Errorf("branch %s already exists (leftover from an earlier worktree?); delete it first: git branch -D %s", branch, branch)
	}
	if err := os.MkdirAll(m.worktreesDir(), 0o750); err != nil {
		return "", err
	}
	if _, err := runGit(ctx, m.repo, "worktree", "add", "-b", branch, "--", p); err != nil {
		return "", err
	}
	// The checkout tracks whatever HEAD carries, including .gummi content
	// the launch untracking only removed from main's index. Untrack it
	// here too, or agent adds in this worktree sweep .gummi churn in.
	if err := untrackGummiInWorktree(ctx, m.wsRoot, m.repo, p); err != nil {
		if _, rmErr := runGit(ctx, m.repo, "worktree", "remove", "--force", "--", p); rmErr == nil {
			_, _ = runGit(ctx, m.repo, "branch", "-D", "--", branch)
		}
		return "", fmt.Errorf("untracking .gummi in new worktree: %w", err)
	}
	// Record the fork point — merge-base(main HEAD, branch) at creation —
	// so diff-based stages can later detect if main is rewound past it.
	// This happens after the untrack succeeds, so a rolled-back creation
	// never leaves a stored SHA pointing at a nonexistent worktree.
	recorded, err := runGit(ctx, m.repo, "merge-base", "HEAD", branch)
	if err != nil {
		if _, rmErr := runGit(ctx, m.repo, "worktree", "remove", "--force", "--", p); rmErr == nil {
			_, _ = runGit(ctx, m.repo, "branch", "-D", "--", branch)
		}
		return "", fmt.Errorf("recording fork point for %s: %w", f.ID, err)
	}
	// Stamp the fresh fork. A recreated worktree reuses the feature row, so
	// a still-recorded SHA from a prior incarnation is tolerated (the row is
	// never overwritten — stamped once), matching Remove/DeleteBranch, which
	// clear it; only a genuine write error rolls the creation back.
	if err := m.forkStore.SetForkPoint(ctx, f.ID, recorded); err != nil && !errors.Is(err, state.ErrForkPointStamped) {
		if _, rmErr := runGit(ctx, m.repo, "worktree", "remove", "--force", "--", p); rmErr == nil {
			_, _ = runGit(ctx, m.repo, "branch", "-D", "--", branch)
		}
		return "", fmt.Errorf("recording fork point for %s: %w", f.ID, err)
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
	if _, err := runGit(ctx, m.repo, args...); err != nil {
		return err
	}
	// The worktree is gone, so its recorded fork is meaningless. Clear it so
	// a recreate re-anchors to main's then-current head instead of keeping a
	// stale SHA that would trip the drift guard forever.
	return m.forkStore.ClearForkPoint(ctx, f.ID)
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
	// Refuse before staging anything: if main was rewound past the recorded
	// fork, a checkpoint would stack on a branch whose base is no longer
	// coherent with main. Returning early leaves the branch tip byte-identical
	// and hands the operator a ForkDriftError naming the remedy.
	if err := m.AssertNoForkDrift(ctx, f); err != nil {
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

// BranchExists reports whether the feature's branch ref exists.
func (m *Manager) BranchExists(ctx context.Context, f *domain.Feature) (bool, error) {
	_, branch, err := m.featurePaths(f)
	if err != nil {
		return false, err
	}
	return gitOK(ctx, m.repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
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
	if _, err := runGit(ctx, m.repo, "branch", flag, "--", branch); err != nil {
		return err
	}
	// The branch is gone; a later Create makes a fresh branch from main's
	// current head, so the recorded fork no longer describes it — clear it to
	// let that recreate re-anchor without tripping the drift guard.
	return m.forkStore.ClearForkPoint(ctx, f.ID)
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
	_, derr := runGit(ctx, m.repo, "branch", "-d", "--", branch)
	if derr == nil {
		return nil
	}
	landed, err := m.squashLanded(ctx, branch)
	if err != nil || !landed {
		return derr
	}
	_, err = runGit(ctx, m.repo, "branch", "-D", "--", branch)
	return err
}

// BranchAhead reports whether the feature branch carries commits of its
// own beyond where it forked from the main checkout — i.e. the stage has
// committed work on the branch. Used to tell a budget park that stopped
// with work committed (nothing lost) from one that stopped mid-edit.
func (m *Manager) BranchAhead(ctx context.Context, f *domain.Feature) (bool, error) {
	_, branch, err := m.featurePaths(f)
	if err != nil {
		return false, err
	}
	base, err := runGit(ctx, m.repo, "merge-base", "HEAD", branch)
	if err != nil {
		return false, err
	}
	n, err := runGit(ctx, m.repo, "rev-list", "--count", base+".."+branch)
	if err != nil {
		return false, err
	}
	return n != "0", nil
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
	out, err := runGit(ctx, m.repo, "status", "--porcelain", "--untracked-files=no", "--", ":(exclude).gummi")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// MainDirtyPaths returns the sorted set of paths dirty under the main
// checkout: tracked edits (staged or unstaged) plus new non-ignored
// untracked files, produced from the NUL-terminated porcelain stream so
// path names (renames, odd characters) survive intact. .gummi is
// excluded — its index state is gummi's own machinery. Rename and copy
// records yield the destination path only; the origin path is consumed
// and discarded. This is the tripwire's raw signal: on a clean→dirty
// transition the caller aborts the run naming the newly-dirty paths.
func (m *Manager) MainDirtyPaths(ctx context.Context) ([]string, error) {
	out, err := runGitRaw(ctx, m.repo, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--", ":(exclude).gummi")
	if err != nil {
		return nil, err
	}
	set := map[string]struct{}{}
	fields := []byte(out)
	for i := 0; i+3 < len(fields); {
		// porcelain v1 -z: "XY path\0"; a rename/copy is "XY dest\0origin\0".
		if fields[i+2] != ' ' {
			break // malformed; stop rather than mis-parse
		}
		status := fields[i : i+2]
		j := bytes.IndexByte(fields[i+3:], 0)
		if j < 0 {
			j = len(fields) - (i + 3) // unterminated tail: take the rest
		}
		path := string(fields[i+3 : i+3+j])
		i += 3 + j + 1
		if path != "" {
			set[path] = struct{}{}
		}
		// a rename/copy record carries a second NUL-terminated field (the
		// origin); consume and discard it so the origin isn't emitted as a
		// separate dirty entry.
		if len(status) == 2 && (status[0] == 'R' || status[0] == 'C') {
			if k := bytes.IndexByte(fields[i:], 0); k >= 0 {
				i += k + 1
			} else {
				i = len(fields)
			}
		}
	}
	paths := make([]string, 0, len(set))
	for p := range set {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths, nil
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
	// A rewrite of main (a rewind or rebase past the recorded fork) makes
	// the is-ancestor and squash-landed routes disagree: the branch reads
	// as unmerged by ancestry yet content-landed by squash detection. Refuse
	// the ambiguous result instead — the caller hears a ForkDriftError and
	// can recreate the worktree from current main rather than trust a bool
	// that hides the failure.
	if err := m.AssertNoForkDrift(ctx, f); err != nil {
		return false, err
	}
	_, branch, err := m.featurePaths(f)
	if err != nil {
		return false, err
	}
	anc, err := gitOK(ctx, m.repo, "merge-base", "--is-ancestor", branch, "HEAD")
	if err != nil {
		return false, err
	}
	branchTip, err := runGit(ctx, m.repo, "rev-parse", branch)
	if err != nil {
		return false, err
	}
	head, err := runGit(ctx, m.repo, "rev-parse", "HEAD")
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
	base, err := runGit(ctx, m.repo, "merge-base", "HEAD", branch)
	if err != nil {
		return false, err
	}
	n, err := runGit(ctx, m.repo, "rev-list", "--count", base+".."+branch)
	if err != nil {
		return false, err
	}
	if n == "0" { // no commits of its own — a fresh/empty branch, not landed
		return false, nil
	}
	merged, err := runGit(ctx, m.repo, "merge-tree", "--write-tree", "HEAD", branch)
	if err != nil {
		return false, nil // conflict or unsupported: treat as not landed
	}
	mainTree, err := runGit(ctx, m.repo, "rev-parse", "HEAD^{tree}")
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
	return m.rebaseOnMain(ctx, f, false)
}

// RebaseOnMainAutostash rebases onto main like RebaseOnMain, but passes
// --autostash so a drifted worktree's uncommitted edits are stashed for the
// rebase and restored after, in one gesture. It is scoped to the drifted
// case: the ordinary, non-drifted rebase keeps refusing a dirty worktree
// (the safe default), so uncommitted work is never silently discarded.
func (m *Manager) RebaseOnMainAutostash(ctx context.Context, f *domain.Feature) error {
	return m.rebaseOnMain(ctx, f, true)
}

// rebaseOnMain is the shared engine behind RebaseOnMain and
// RebaseOnMainAutostash; autostash selects whether the rebase carries
// uncommitted work across (--autostash) or refuses to start on a dirty
// worktree.
func (m *Manager) rebaseOnMain(ctx context.Context, f *domain.Feature, autostash bool) error {
	p, err := m.requireWorktree(f)
	if err != nil {
		return err
	}
	mainHead, err := runGit(ctx, m.repo, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	args := []string{"rebase"}
	if autostash {
		args = append(args, "--autostash")
	}
	args = append(args, mainHead)
	if _, err := runGit(ctx, p, args...); err != nil {
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

// ReanchorOnMain re-stamps the feature's recorded fork point to main's
// current HEAD — the recovery that clears fork drift. It is guarded by
// RebasedOnMain: main's HEAD must already be in the branch's history, so the
// merge base IS main's HEAD and the post-rebase diff can only be the
// feature's own commits. When the guard fails the feature is still drifted,
// and its current ForkDriftError is returned so the operator keeps the
// remedies. Idempotent: re-anchoring to the same HEAD twice is a no-op.
func (m *Manager) ReanchorOnMain(ctx context.Context, f *domain.Feature) error {
	mainHead, err := m.MainHead(ctx)
	if err != nil {
		return err
	}
	rebased, err := m.RebasedOnMain(ctx, f)
	if err != nil {
		return err
	}
	if !rebased {
		return m.AssertNoForkDrift(ctx, f)
	}
	if err := m.forkStore.ReanchorForkPoint(ctx, f.ID, mainHead); err != nil {
		return err
	}
	return nil
}

// MainHead returns the main checkout's current HEAD commit id — the
// commit RebaseOnMain rebases onto, exposed so an agent-driven rebase
// can be pointed at the exact same target.
func (m *Manager) MainHead(ctx context.Context) (string, error) {
	return runGit(ctx, m.repo, "rev-parse", "HEAD")
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
// squash commit carrying message, returning the new commit's sha. It
// refuses when main has tracked changes (they would be swept into the
// commit), when the branch has no commits of its own, or when its content
// is already in main. A conflicted merge is undone with reset --merge — a
// squash merge writes no MERGE_HEAD, so merge --abort cannot — and
// reported as a *MergeConflictError; main is left clean on every path
// short of a failed reset. The returned sha is non-empty exactly when a
// squash commit was created; every failure path returns ("", err).
func (m *Manager) SquashMerge(ctx context.Context, f *domain.Feature, message string) (string, error) {
	if strings.TrimSpace(message) == "" {
		return "", fmt.Errorf("refusing squash merge of %s: empty commit message", f.ID)
	}
	m.mainMu.Lock()
	defer m.mainMu.Unlock()
	_, branch, err := m.featurePaths(f)
	if err != nil {
		return "", err
	}
	if ok, err := gitOK(ctx, m.repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err != nil {
		return "", err
	} else if !ok {
		return "", fmt.Errorf("feature %s has no branch %s", f.ID, branch)
	}
	if dirty, err := m.MainTrackedDirty(ctx); err != nil {
		return "", err
	} else if dirty {
		return "", fmt.Errorf("main checkout has uncommitted changes — commit or stash them before merging")
	}
	base, err := runGit(ctx, m.repo, "merge-base", "HEAD", branch)
	if err != nil {
		// merge-base gives up when main and branch share no common
		// ancestor — the signature of a rewind that took main to a
		// disconnected history. That is drift, so run the guard first so a
		// real rewind surfaces as the typed ForkDriftError; anything else
		// propagates the merge-base failure.
		if derr := m.AssertNoForkDrift(ctx, f); derr != nil {
			return "", derr
		}
		return "", err
	}
	if err := m.assertNoForkDriftAgainstBase(ctx, f, base); err != nil {
		return "", err
	}
	if n, err := runGit(ctx, m.repo, "rev-list", "--count", base+".."+branch); err != nil {
		return "", err
	} else if n == "0" {
		return "", fmt.Errorf("branch %s has no commits to merge", branch)
	}
	if _, err := runGit(ctx, m.repo, "merge", "--squash", branch); err != nil {
		// capture what conflicted before the reset wipes the state
		conflicts := m.conflictedFiles(ctx, m.repo)
		if _, resetErr := runGit(ctx, m.repo, "reset", "--merge"); resetErr != nil {
			return "", fmt.Errorf("squash merge failed AND reset failed, main checkout needs manual attention: %w (reset: %v)", err, resetErr)
		}
		if len(conflicts) > 0 {
			return "", &MergeConflictError{Files: conflicts}
		}
		return "", err
	}
	// "Already up to date" stages nothing: the branch's content is in
	// main already, i.e. it landed some other way.
	if clean, err := gitOK(ctx, m.repo, "diff", "--cached", "--quiet"); err != nil {
		return "", err
	} else if clean {
		return "", fmt.Errorf("nothing to merge — %s already landed on main", branch)
	}
	if _, err := runGit(ctx, m.repo, "commit", "-m", message); err != nil {
		if _, resetErr := runGit(ctx, m.repo, "reset", "--merge"); resetErr != nil {
			return "", fmt.Errorf("squash commit failed AND reset failed, main checkout needs manual attention: %w (reset: %v)", err, resetErr)
		}
		return "", err
	}
	// the mainMu lock serializes main mutations, so HEAD is still the
	// squash commit we just created — its sha is the landed commit.
	return runGit(ctx, m.repo, "rev-parse", "HEAD")
}

// ForkDriftRemedy is the single recovery phrase quoted verbatim by both
// ForkDriftError.Error() and the doctor remediation line, so the two never
// drift apart: pressing r rebases the branch onto main and re-anchors the
// fork, and if main was rewound (not just rebased) restoring it from its
// reflog undoes the accidental rewind.
const ForkDriftRemedy = "press r in the board to rebase onto main and re-anchor this work item to it, or if main was accidentally rewound restore it from its reflog"

// ForkDriftError reports that the feature's recorded fork point is no
// longer an ancestor of main's current HEAD — i.e. main was rewound past
// where the worktree's branch originally forked, so a diff-based stage
// would present commits the feature did not author as its own. Recorded and
// MainHead are the two commit SHAs. FeatureID and Branch identify the
// work item the drift stranding. Fields are populated only at the single
// construction site in AssertNoForkDrift.
type ForkDriftError struct {
	// FeatureID is the drifted work item.
	FeatureID domain.FeatureID
	// Branch is the work item's git branch.
	Branch string
	// Recorded is the fork point stamped at worktree-creation time.
	Recorded string
	// MainHead is main's current HEAD — the commit main now points at.
	MainHead string
}

func (e *ForkDriftError) Error() string {
	return fmt.Sprintf("%s (%s): fork drift — recorded fork %s is no longer in main's history; main now points at %s (likely a rebase, amend, or reset on main). %s",
		e.FeatureID, e.Branch, e.Recorded, e.MainHead, ForkDriftRemedy)
}

// AssertNoForkDrift refuses a diff-based operation when the feature's
// recorded fork point is no longer an ancestor of main's current HEAD —
// main was rewound backward past where the branch forked, so the live
// merge-base has slid with it and would re-introduce already-merged
// commits as the feature's own. Forward advances of main leave the stored
// SHA as an ancestor and pass. A worktree with no recorded fork point
// (created before drift detection existed) is lazily anchored to
// merge-base(main, branch) as it reads now, with a one-line note.
func (m *Manager) AssertNoForkDrift(ctx context.Context, f *domain.Feature) error {
	recorded, err := m.forkStore.ForkPoint(ctx, f.ID)
	if err != nil {
		return err
	}
	// Lazy backfill: anchor the recorded fork from the current merge-base
	// for worktrees predating drift detection. Drift already suffered by
	// them is unreconstructable; detection starts from here.
	if recorded == "" {
		recorded, err = runGit(ctx, m.repo, "merge-base", "HEAD", f.BranchName())
		if err != nil {
			return err
		}
		if err := m.forkStore.SetForkPoint(ctx, f.ID, recorded); err != nil {
			// A concurrent create or backfill (or a store row that cannot hold
			// a fork point — no feature row — under the stamped-once guard)
			// means we cannot persist our anchor. Adopt whatever the store has
			// when it can say so; otherwise fall back to the merge-base we just
			// computed so the check still runs rather than hard-failing on a
			// store that cannot backfill.
			if !errors.Is(err, state.ErrForkPointStamped) {
				return err
			}
			if cur, rerr := m.forkStore.ForkPoint(ctx, f.ID); rerr == nil && cur != "" {
				recorded = cur
			}
		} else {
			log.Printf("drift detection for %s starts from %s", f.ID, recorded)
		}
	}
	// Check ancestry directly against the main checkout's HEAD ref. The
	// pass-through path costs a single git invocation: the recorded SHA is
	// accepted as-is by --is-ancestor, so no separate HEAD resolution is
	// needed unless drift is actually detected (where the resolved SHA is
	// required for the error message). We deliberately do NOT fold this
	// into a caller's own merge-base computation: drift is defined against
	// main HEAD, not the live merge-base, so reusing the latter would flag
	// a legitimate branch rebase as drift.
	ok, err := gitOK(ctx, m.repo, "merge-base", "--is-ancestor", recorded, "HEAD")
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	mainHead, err := runGit(ctx, m.repo, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	return &ForkDriftError{FeatureID: f.ID, Branch: f.BranchName(), Recorded: recorded, MainHead: mainHead}
}

// assertNoForkDriftAgainstBase is AssertNoForkDrift with the live
// merge-base already computed. If that merge-base still equals the
// recorded fork point, drift is impossible by construction — a merge-base
// is a common ancestor of main HEAD, hence an ancestor of it — so no
// ancestry git call is needed. Otherwise it defers to the full check.
// This is the merge path's zero-extra-call case: the common pass where
// main has not rewound past the fork reuses the merge-base SquashMerge
// already computed instead of spawning a second git invocation.
func (m *Manager) assertNoForkDriftAgainstBase(ctx context.Context, f *domain.Feature, base string) error {
	recorded, err := m.forkStore.ForkPoint(ctx, f.ID)
	if err != nil {
		return err
	}
	if recorded != "" && base == recorded {
		return nil
	}
	return m.AssertNoForkDrift(ctx, f)
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
	if err := m.AssertNoForkDrift(ctx, f); err != nil {
		return "", err
	}
	mainHead, err := runGit(ctx, m.repo, "rev-parse", "HEAD")
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
	out, err := runGit(ctx, m.repo, "worktree", "list", "--porcelain")
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
