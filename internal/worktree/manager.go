package worktree

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
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
	// LandedSHA reads a feature's recorded landed-commit SHA — the squash
	// commit SquashMerge created when the branch actually landed on main.
	// Empty when gummi has never squash-merged this feature's branch.
	LandedSHA(ctx context.Context, id domain.FeatureID) (string, error)
	// SetLandedSHA stamps the landed-commit SHA. SquashMerge calls it
	// itself right after creating the commit, so every caller gets the
	// record for free instead of having to remember to persist the sha
	// it returns.
	SetLandedSHA(ctx context.Context, id domain.FeatureID, sha string) error
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

	// baseLookup resolves the revision a card forks from and lands on.
	// Nil means "the managed checkout's HEAD" for every card, which is
	// what this package did before bases were selectable — so a manager
	// built without one behaves exactly as it always has, and every test
	// fixture keeps working untouched.
	//
	// It is a callback rather than a wider signature on twenty methods
	// for the same reason forkStore is one: resolving a card's base needs
	// the store (its stack's members, in the stacked case), and this
	// package must not import it.
	baseLookup BaseLookup

	// mainMu serializes gummi-initiated mutations of the main checkout
	// (squash merges).
	mainMu sync.Mutex
}

// BaseLookup resolves the git revision a card's work forks from and lands
// on — a branch name, or "" to mean the managed checkout's HEAD.
//
// A card with no chosen base and no stack returns "". A card with a
// chosen base returns that branch. A stacked card above the bottom
// returns the branch of the card below it, which is what chains a stack.
type BaseLookup func(ctx context.Context, f *domain.Feature) (string, error)

// SetBaseLookup installs the base resolver. Called once at pool
// construction; a nil lookup leaves every card on the checkout's HEAD.
func (m *Manager) SetBaseLookup(l BaseLookup) { m.baseLookup = l }

// baseRev is THE chokepoint for "the revision this card forks from and
// lands on" — what this package used to spell as a bare "HEAD" against
// m.repo in twenty places.
//
// The distinction that matters when reading this file: a "HEAD" passed to
// runGit with m.repo as the directory meant *the trunk*, and every one of
// those is now this call. A "HEAD" passed with a worktree path as the
// directory means *the card's own branch tip* — a completely different
// fact that happens to share the token — and those are untouched. Mixing
// the two up is the one way to break this seam, so they never appear in
// the same call.
//
// Resolution failures fall back to "HEAD" rather than erroring: a base
// gummi cannot resolve is a reason to behave as it did before bases
// existed, never a reason to refuse an operation git itself would accept.
func (m *Manager) baseRev(ctx context.Context, f *domain.Feature) string {
	if m.baseLookup == nil || f == nil {
		return "HEAD"
	}
	base, err := m.baseLookup(ctx, f)
	if err != nil || strings.TrimSpace(base) == "" {
		return "HEAD"
	}
	// The branch must actually exist here, or every merge-base against it
	// fails and the card looks drifted when it is merely pointed at a
	// branch this checkout has not got.
	if _, err := runGit(ctx, m.repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+base); err != nil {
		return "HEAD"
	}
	return base
}

// BaseRevFor reports the revision f forks from, as baseRev resolves it.
// Exported for the callers that need to name it in a sentence or pass it
// to a rebase.
func (m *Manager) BaseRevFor(ctx context.Context, f *domain.Feature) string {
	return m.baseRev(ctx, f)
}

// ListBranches returns the repository's local branch names, for the
// pickers that let a person choose a base. Branch listing did not exist
// in this package before: nothing needed to enumerate refs, because the
// only base was whatever was checked out.
func (m *Manager) ListBranches(ctx context.Context) ([]string, error) {
	out, err := runGit(ctx, m.repo, "for-each-ref", "--format=%(refname:short)", "refs/heads")
	if err != nil {
		return nil, fmt.Errorf("listing branches in %s: %w", m.repo, err)
	}
	if strings.TrimSpace(out) == "" {
		return nil, nil
	}
	return strings.Split(strings.TrimSpace(out), "\n"), nil
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

// DefaultBaseBranchName is what the UI says when the checkout's branch
// cannot be read — a detached HEAD, or a repository with no commits yet.
// It is the name the copy used to hardcode everywhere, so nothing reads
// worse than it did before this existed.
const DefaultBaseBranchName = "main"

// BaseBranch names the branch the main checkout currently has out — the
// branch every card's work lands on.
//
// It names what the CHECKOUT has out, which is a different question from
// what a given card forks from: that is baseRev, and a card may name a
// branch this checkout does not currently have out. This is the answer
// for the sentences that tell a human what the repository is sitting on
// — so a repo on master reads "master" instead of asserting "main" — and
// for SquashMerge's refusal to land a card onto a branch other than the
// one it forked from.
//
// It was DECORATIVE before bases were selectable, when forking, landing
// and drift all leaned on this checkout's HEAD and no branch name was
// ever passed to git. It is load-bearing now only in that refusal.
//
// Unreadable HEAD reports DefaultBaseBranchName rather than an error:
// a name the UI cannot get is a copy problem, never a reason to refuse a
// merge that git itself would accept.
func (m *Manager) BaseBranch(ctx context.Context) string {
	out, err := runGit(ctx, m.repo, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return DefaultBaseBranchName
	}
	name := strings.TrimSpace(out)
	// "HEAD" is what --abbrev-ref reports for a detached checkout: a SHA
	// is not a name a sentence can use.
	if name == "" || name == "HEAD" {
		return DefaultBaseBranchName
	}
	return name
}

// worktreesDir is the directory all feature worktrees must live in.
func (m *Manager) worktreesDir() string {
	return filepath.Join(m.wsRoot, ".gummi", "worktrees")
}

// cardTreePath validates the feature and derives its absolute checkout
// path under base, refusing anything that would escape base. This is the
// single chokepoint every operation goes through — both the card's branch
// worktree (.gummi/worktrees) and its scratch tree (.gummi/scratch).
func (m *Manager) cardTreePath(base string, f *domain.Feature) (string, error) {
	return m.cardTreePathNamed(base, f, string(f.ID))
}

// cardTreePathNamed is cardTreePath for a tree whose directory is not
// simply the card's id. A goal has one tree per repository it touches
// (goal.go), and only the one in its home repo is named after the card;
// the rest carry the repo as a suffix. The escape check is the same one,
// which is the point of routing them through here: a directory name is a
// directory name, however it was spelled.
func (m *Manager) cardTreePathNamed(base string, f *domain.Feature, name string) (string, error) {
	if err := f.Validate(); err != nil {
		return "", fmt.Errorf("refusing worktree operation: %w", err)
	}
	if name == "" {
		return "", fmt.Errorf("refusing worktree operation: %s has no tree name", f.ID)
	}
	p := filepath.Clean(filepath.Join(base, name))
	if filepath.Dir(p) != base {
		return "", fmt.Errorf("refusing worktree operation: %s escapes %s", p, base)
	}
	// A hostile repo can commit .gummi or the tree's parent as a symlink
	// pointing outside the checkout; writing through it would escape
	// the repo, which the lexical check above cannot see.
	for _, dir := range []string{filepath.Join(m.wsRoot, ".gummi"), base} {
		fi, err := os.Lstat(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue // not created yet — fine
			}
			return "", err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("refusing worktree operation: %s is a symlink", dir)
		}
	}
	return p, nil
}

// featurePaths derives the feature's absolute branch-worktree path and
// branch name through cardTreePath's chokepoint.
func (m *Manager) featurePaths(f *domain.Feature) (wtPath, branch string, err error) {
	wtPath, err = m.cardTreePath(m.worktreesDir(), f)
	if err != nil {
		return "", "", err
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

// ErrNoWorktree marks a requireWorktree failure caused by the worktree
// directory being absent — as opposed to a transient git error — so a
// caller like checkpoint can tell "nothing to commit, leftovers are
// still on disk" apart from "there is no disk to leave anything on".
var ErrNoWorktree = errors.New("no worktree")

// requireWorktree returns the worktree path, erroring clearly when the
// directory is absent (git's own "cannot change to directory" is
// opaque). The error wraps ErrNoWorktree so callers can distinguish
// total loss from other failure modes.
func (m *Manager) requireWorktree(f *domain.Feature) (string, error) {
	p, _, err := m.featurePaths(f)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(p); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("feature %s has no worktree at %s: %w", f.ID, p, ErrNoWorktree)
		}
		return "", err
	}
	return p, nil
}

// Create adds the feature's worktree at .gummi/worktrees/FD-NNN on a new
// branch forked from the revision the card names — its chosen base, the
// branch of the card below it in its stack, or the main checkout's HEAD
// when it names neither (which is what every card did before bases were
// selectable).
func (m *Manager) Create(ctx context.Context, f *domain.Feature) (string, error) {
	p, branch, err := m.featurePaths(f)
	if err != nil {
		return "", err
	}
	base := m.baseRev(ctx, f)
	if _, err := runGit(ctx, m.repo, "rev-parse", "--verify", "HEAD"); err != nil {
		return "", fmt.Errorf("repository has no commits yet; commit something before creating a feature worktree: %w", err)
	}
	if _, err := os.Stat(p); err == nil {
		return "", fmt.Errorf("worktree path %s already exists", p)
	}
	if ok, err := gitOK(ctx, m.repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err != nil {
		return "", err
	} else if ok {
		// Two causes now, and the message names both: a leftover from an
		// earlier worktree of this same card, or another card whose title
		// slugified to the same label — the branch spelling carries no
		// card id, so that is a real collision rather than a curiosity.
		// The mint-time BranchTaken check catches the second for cards
		// gummi created; this is the backstop for a ref that arrived by
		// any other route.
		return "", fmt.Errorf("branch %s already exists — a leftover from an earlier worktree of %s (delete it: git branch -D %s), or another card's branch with the same label", branch, f.ID, branch)
	}
	if err := os.MkdirAll(m.worktreesDir(), 0o750); err != nil {
		return "", err
	}
	// The base is named explicitly rather than left implicit: `worktree
	// add -b` without a commit-ish forks from whatever HEAD carries, which
	// is right only for a card that named no base.
	add := func() error {
		_, err := runGit(ctx, m.repo, "worktree", "add", "-b", branch, "--", p, base)
		return err
	}
	if err := add(); err != nil {
		// A directory removed out of band leaves admin metadata registered
		// at that path, and git refuses a fresh checkout there for as long
		// as it stays — so the next card to be handed this path inherits a
		// refusal it cannot clear by retrying. Prune and retry once, on the
		// failure path only, for the same reason EnsureScratch does: this
		// is the common creation path and a prune on it is work for a case
		// that almost never holds. The os.Stat guard above already
		// established the directory is absent, so the prune can only drop
		// registrations whose checkout is gone — never a live one.
		if _, perr := runGit(ctx, m.repo, "worktree", "prune"); perr != nil {
			return "", err
		}
		// `worktree add -b` creates the branch before it fails on the path,
		// so the attempt that just failed can have left the ref behind and
		// the retry would trip over it ("a branch named X already exists").
		// The guard above established the branch did not exist when Create
		// began, so a ref here came from that attempt and is ours to drop —
		// and it is dropped after the prune, since a stale registration
		// pins the branch against deletion. Deleting it can only lose the
		// empty fork the failed add just made.
		if ok, gerr := gitOK(ctx, m.repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); gerr == nil && ok {
			if _, derr := runGit(ctx, m.repo, "branch", "-D", "--", branch); derr != nil {
				return "", err
			}
		}
		if err := add(); err != nil {
			return "", err
		}
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
	recorded, err := runGit(ctx, m.repo, "merge-base", base, branch)
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

// ForkPoint returns the feature's recorded fork-point SHA, or the empty
// string if none is stamped yet (a worktree that predates drift
// detection, or one that never had a worktree at all).
func (m *Manager) ForkPoint(ctx context.Context, f *domain.Feature) (string, error) {
	return m.forkStore.ForkPoint(ctx, f.ID)
}

// Recreate rebuilds a feature's worktree directory after it vanished out
// from under an active branch — e.g. an environment/filesystem glitch
// that removed .gummi/worktrees/<ID> without going through git, unlike a
// clean Remove. It reattaches the existing branch when git still knows
// about it (the common case: only the working directory was lost), or
// falls back to a fresh Create when the branch is gone too. Either way
// the feature's already-stamped fork point is left untouched — Create's
// own stamp is a no-op once one exists (state.ErrForkPointStamped).
func (m *Manager) Recreate(ctx context.Context, f *domain.Feature) (string, error) {
	p, branch, err := m.featurePaths(f)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(p); err == nil {
		return "", fmt.Errorf("worktree path %s already exists", p)
	}
	// Stale admin metadata left behind by the vanished directory would
	// otherwise make git refuse to attach a fresh checkout at the same path.
	if _, err := runGit(ctx, m.repo, "worktree", "prune"); err != nil {
		return "", err
	}
	branchExists, err := gitOK(ctx, m.repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	if !branchExists {
		return m.Create(ctx, f)
	}
	if err := os.MkdirAll(m.worktreesDir(), 0o750); err != nil {
		return "", err
	}
	if _, err := runGit(ctx, m.repo, "worktree", "add", "--", p, branch); err != nil {
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
	if _, err := runGit(ctx, m.repo, args...); err != nil {
		return err
	}
	// The worktree is gone, so its recorded fork is meaningless. Clear it so
	// a recreate re-anchors to main's then-current head instead of keeping a
	// stale SHA that would trip the drift guard forever.
	return m.forkStore.ClearForkPoint(ctx, f.ID)
}

// DiskSize is how many bytes the feature's worktree occupies, walking
// the checkout on disk.
//
// It exists for the close-out sweep and for the board's archive line, and
// both are deliberately the only callers: a checkout is thousands of
// files and this walks all of them, so it belongs on a surface someone
// opened on purpose and never on a render path. A missing worktree is
// zero, not an error — "nothing there" is the answer, not a failure.
//
// Unreadable entries are skipped rather than failing the walk. A figure
// that is short by one directory is still the right order of magnitude,
// and the number's whole job is to tell someone whether tidying up is
// worth doing.
func (m *Manager) DiskSize(ctx context.Context, f *domain.Feature) (int64, error) {
	p, _, err := m.featurePaths(f)
	if err != nil {
		return 0, err
	}
	var total int64
	err = filepath.WalkDir(p, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return fs.SkipDir
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		total += info.Size()
		return nil
	})
	if os.IsNotExist(err) {
		return 0, nil
	}
	return total, err
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

// unregisterStaleWorktree clears git's worktree registration for f when
// the worktree directory is already gone (e.g. after a crash or manual
// removal). Without this, later branch deletion fails with "used by
// worktree" even though the directory no longer exists. An active
// worktree — one whose directory still exists — is left alone so callers
// don't silently destroy work-in-progress; Remove handles those.
func (m *Manager) unregisterStaleWorktree(ctx context.Context, f *domain.Feature) error {
	p, _, err := m.featurePaths(f)
	if err != nil {
		return err
	}
	if _, err := os.Stat(p); err == nil {
		// Directory still exists: worktree is active, leave it to Remove.
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	_, err = runGit(ctx, m.repo, "worktree", "remove", "--force", "--", p)
	if err != nil && !strings.Contains(err.Error(), "is not a working tree") {
		return err
	}
	return nil
}

// DeleteBranch removes the feature's branch. Without force it refuses
// branches that are not fully merged into HEAD (git -d semantics).
func (m *Manager) DeleteBranch(ctx context.Context, f *domain.Feature, force bool) error {
	_, branch, err := m.featurePaths(f)
	if err != nil {
		return err
	}
	// A stale worktree registration (directory gone, metadata left) pins
	// the branch and makes even -D fail. Clean it up first.
	if err := m.unregisterStaleWorktree(ctx, f); err != nil {
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
// via the recorded landed-sha lineage check and only then force-deletes.
// That check only ever passes for a branch gummi itself squash-merged, so
// nothing unlanded can slip through the -D.
func (m *Manager) DeleteLandedBranch(ctx context.Context, f *domain.Feature) error {
	_, branch, err := m.featurePaths(f)
	if err != nil {
		return err
	}
	// Clear any stale worktree registration so git's branch checks can
	// operate on the branch alone.
	if err := m.unregisterStaleWorktree(ctx, f); err != nil {
		return err
	}
	_, derr := runGit(ctx, m.repo, "branch", "-d", "--", branch)
	if derr == nil {
		return nil
	}
	landed, err := m.squashLanded(ctx, f)
	if err != nil || !landed {
		return derr
	}
	_, err = runGit(ctx, m.repo, "branch", "-D", "--", branch)
	return err
}

// Head returns the feature branch's current tip commit sha, resolved inside
// the feature's own worktree.
func (m *Manager) Head(ctx context.Context, f *domain.Feature) (string, error) {
	wtPath, err := m.requireWorktree(f)
	if err != nil {
		return "", err
	}
	return runGit(ctx, wtPath, "rev-parse", "HEAD")
}

// BranchAhead reports whether the feature branch carries commits of its
// own beyond where it forked from its base — i.e. the stage has
// committed work on the branch. Used to tell a budget park that stopped
// with work committed (nothing lost) from one that stopped mid-edit.
func (m *Manager) BranchAhead(ctx context.Context, f *domain.Feature) (bool, error) {
	_, branch, err := m.featurePaths(f)
	if err != nil {
		return false, err
	}
	base, err := runGit(ctx, m.repo, "merge-base", m.baseRev(ctx, f), branch)
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
	return trackedDirtyIn(ctx, p)
}

// trackedDirtyIn is TrackedDirty for a checkout named by path rather than
// by card — a goal's tree in a repository that is not its home has no card
// of its own to name it (goal.go).
func trackedDirtyIn(ctx context.Context, dir string) (bool, error) {
	out, err := runGit(ctx, dir, "status", "--porcelain", "--untracked-files=no")
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
//
// A branch still sitting exactly at its recorded fork point has produced
// no commits of its own — there is nothing of the feature's to land.
// Without this check, merge-base --is-ancestor is trivially true for such
// a branch against any HEAD, so the moment any *other* card lands and
// advances main, this one would misreport "landed" too, purely because
// its own tip predates main's new head.
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
	branchTip, err := runGit(ctx, m.repo, "rev-parse", branch)
	if err != nil {
		return false, err
	}
	// AssertNoForkDrift above lazily backfills a missing recorded fork
	// point before returning, so this read sees it even on a
	// pre-drift-detection worktree; an empty result here means the store
	// still could not persist one (no feature row) and we fall through to
	// the pre-existing ancestry/squash logic rather than block on it.
	recorded, err := m.forkStore.ForkPoint(ctx, f.ID)
	if err != nil {
		return false, err
	}
	if recorded != "" && branchTip == recorded {
		return false, nil
	}
	base := m.baseRev(ctx, f)
	anc, err := gitOK(ctx, m.repo, "merge-base", "--is-ancestor", branch, base)
	if err != nil {
		return false, err
	}
	head, err := runGit(ctx, m.repo, "rev-parse", base)
	if err != nil {
		return false, err
	}
	if anc {
		return branchTip != head, nil
	}
	return m.squashLanded(ctx, f)
}

// squashLanded reports whether f's branch landed on main via a
// gummi-performed squash merge: the commit SquashMerge recorded for it
// (LandedSHA) is reachable from main's current HEAD. A feature with no
// recorded landed sha reads as not-landed — without a recorded merge
// commit there is no lineage to test, and content equivalence alone
// cannot distinguish "this branch's squash landed" from "main
// independently carries the identical diff by some other route" (a
// sibling card, a cherry-pick, a hand-applied identical fix).
func (m *Manager) squashLanded(ctx context.Context, f *domain.Feature) (bool, error) {
	sha, err := m.forkStore.LandedSHA(ctx, f.ID)
	if err != nil {
		return false, err
	}
	if sha == "" {
		return false, nil
	}
	return gitOK(ctx, m.repo, "merge-base", "--is-ancestor", sha, m.baseRev(ctx, f))
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
	mainHead, err := runGit(ctx, m.repo, "rev-parse", m.baseRev(ctx, f))
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

// ResolveConflicts is handed an in-progress rebase's conflicted files to
// resolve in dir. It must resolve and `git add` each one and return; it
// must not commit, continue, or abort the rebase itself.
type ResolveConflicts func(ctx context.Context, dir string, files []string) error

// maxRebaseSteps bounds how many commits a resolving rebase will work
// through. A card's branch carries a handful of commits, so a rebase that
// keeps stopping past this is not making progress and is better aborted
// than left running an agent per step.
const maxRebaseSteps = 12

// RebaseOnMainResolving rebases the feature branch onto its base like
// RebaseOnMain, but hands a rebase that stops on conflicts to resolve
// rather than aborting on the spot — continuing through as many commits
// as conflict, and aborting (so the worktree is never left mid-rebase)
// only when resolving stops working.
//
// It exists because a goal has two places a merge conflict can happen and
// only one of them was cheap. Catching the goal branch up with main hands
// its conflicts to a bounded side session; a card's rebase onto the goal
// branch at landing time aborted and sent the card back to a full
// implement stage, which then re-ran its critique and its verify — the
// expensive path for the frequent case, since with more than one lane
// every card after the first lands onto a branch that moved, and cards a
// goal decomposed out of one area touch the same files by construction.
//
// The fallback is exactly the old behaviour: on any deviation the rebase
// is aborted and a *RebaseConflictError naming the conflicted files comes
// back, so a caller that cannot resolve loses nothing by asking.
func (m *Manager) RebaseOnMainResolving(ctx context.Context, f *domain.Feature, resolve ResolveConflicts) error {
	if resolve == nil {
		return m.RebaseOnMain(ctx, f)
	}
	p, err := m.requireWorktree(f)
	if err != nil {
		return err
	}
	mainHead, err := runGit(ctx, m.repo, "rev-parse", m.baseRev(ctx, f))
	if err != nil {
		return err
	}
	if _, rerr := runGit(ctx, p, "rebase", mainHead); rerr == nil {
		return nil
	} else if !m.rebaseInProgress(ctx, p) {
		return fmt.Errorf("rebase of %s did not start: %w", f.ID, rerr)
	}
	for step := 0; step < maxRebaseSteps && m.rebaseInProgress(ctx, p); step++ {
		files := m.conflictedFiles(ctx, p)
		if len(files) == 0 {
			break // stopped for something resolving conflicts cannot fix
		}
		if resolve(ctx, p, files) != nil {
			break
		}
		if left := m.conflictedFiles(ctx, p); len(left) > 0 {
			break // it did not finish the job; do not commit a half-merge
		}
		// core.editor=true: --continue would otherwise open an editor on
		// the replayed commit's message, which no unattended caller has.
		if _, cerr := runGit(ctx, p, "-c", "core.editor=true", "rebase", "--continue"); cerr != nil && !m.rebaseInProgress(ctx, p) {
			break
		}
	}
	if !m.rebaseInProgress(ctx, p) {
		return nil
	}
	conflicts := m.conflictedFiles(ctx, p)
	if _, abortErr := runGit(ctx, p, "rebase", "--abort"); abortErr != nil {
		return fmt.Errorf("rebase failed AND abort failed, worktree %s needs manual attention: %v", p, abortErr)
	}
	return &RebaseConflictError{Files: conflicts}
}

// ReanchorOnMain re-stamps the feature's recorded fork point to main's
// current HEAD — the recovery that clears fork drift. It is guarded by
// RebasedOnBase: the base tip must already be in the branch's history, so the
// merge base IS main's HEAD and the post-rebase diff can only be the
// feature's own commits. When the guard fails the feature is still drifted,
// and its current ForkDriftError is returned so the operator keeps the
// remedies. Idempotent: re-anchoring to the same HEAD twice is a no-op.
func (m *Manager) ReanchorOnMain(ctx context.Context, f *domain.Feature) error {
	mainHead, err := m.BaseHead(ctx, f)
	if err != nil {
		return err
	}
	rebased, err := m.RebasedOnBase(ctx, f)
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

// MainHead returns the managed checkout's current HEAD commit id.
//
// It is the card-less form, still used where the trunk itself is the
// subject: a goal branch catching up, a throwaway checkout of main. For
// anything scoped to a card, use BaseHead — a card's base is not
// necessarily what the checkout has out.
func (m *Manager) MainHead(ctx context.Context) (string, error) {
	return runGit(ctx, m.repo, "rev-parse", "HEAD")
}

// BaseHead returns the current tip of the revision f forks from — the
// commit a rebase of f targets, exposed so an agent-driven rebase can be
// pointed at the exact same target.
func (m *Manager) BaseHead(ctx context.Context, f *domain.Feature) (string, error) {
	return runGit(ctx, m.repo, "rev-parse", m.baseRev(ctx, f))
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

// RebasedOnBase reports whether the feature branch's history now
// includes the tip of the revision it forks from — the success test for a
// completed rebase, and the staleness test a stack asks of every member:
// a card whose base tip is NOT in its history is sitting on commits that
// have moved, and is what the restack walk replays.
//
// A conflicted, aborted, or never-started rebase leaves the base tip
// outside the branch (assuming the base has moved since the branch was
// cut; a branch already at its base tip trivially passes).
func (m *Manager) RebasedOnBase(ctx context.Context, f *domain.Feature) (bool, error) {
	p, err := m.requireWorktree(f)
	if err != nil {
		return false, err
	}
	mainHead, err := m.BaseHead(ctx, f)
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
	if changed, err := trackedChanges(ctx, m.repo); err != nil {
		return "", err
	} else if len(changed) > 0 {
		// Name the checkout. For an ordinary card it is the repository the
		// person is sitting in and they know where to look; for a goal
		// card it is the goal tree under .gummi/worktrees, which they have
		// never opened and would not think to check.
		return "", fmt.Errorf("main checkout has uncommitted changes — commit or stash them before merging (%s in %s)",
			strings.Join(changed, ", "), m.repo)
	}
	// The squash merge runs in the managed checkout as it stands, so the
	// branch it lands on is whatever that checkout has out. A card whose
	// base is some other branch would therefore land somewhere it never
	// forked from — silently, and with a diff nobody reviewed.
	//
	// gummi has never checked a branch out on anyone's behalf and this is
	// not the place to start: refuse, and say which branch needs to be out.
	// A card with no chosen base resolves to "HEAD" and skips this
	// entirely, which is every card that existed before bases did.
	base := m.baseRev(ctx, f)
	if base != "HEAD" {
		out := m.BaseBranch(ctx)
		if out != base {
			return "", fmt.Errorf("%s forked from %s but the checkout has %s out — check out %s before landing it",
				f.ID, base, out, base)
		}
	}
	forkBase, err := runGit(ctx, m.repo, "merge-base", base, branch)
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
	if err := m.assertNoForkDriftAgainstBase(ctx, f, forkBase); err != nil {
		return "", err
	}
	if n, err := runGit(ctx, m.repo, "rev-list", "--count", forkBase+".."+branch); err != nil {
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
	sha, err := runGit(ctx, m.repo, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	// Record the landed commit here, inside the operation that created it,
	// rather than leaving every caller to remember to persist the sha this
	// function returns — that's the gap BG-036 fell through: a discarded
	// return value left Landed with no lineage to check and only a
	// content-equality guess to fall back on.
	if err := m.forkStore.SetLandedSHA(ctx, f.ID, sha); err != nil {
		return "", fmt.Errorf("recording landed commit for %s: %w", f.ID, err)
	}
	return sha, nil
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
	// ForkedFrom is a branch that still carries the recorded fork, when
	// one was found: the card forked from THAT and the base moved out
	// from under it, rather than main being rewritten. Empty when no such
	// branch exists, which is the rewound-main case the remedy assumes.
	ForkedFrom string
}

func (e *ForkDriftError) Error() string {
	if e.ForkedFrom != "" {
		// A card adopted out of a goal is the common way to get here: it
		// forked from the goal branch, whose commits main has never seen.
		// Nothing happened to main, so the reflog advice below would send
		// a reader hunting for a rewind that never occurred.
		return fmt.Sprintf("%s (%s): fork drift — recorded fork %s is not in main's history because the branch forked from %s, which has not landed; main points at %s. Land %s first, or rebase this branch onto main and re-anchor it (r in the board)",
			e.FeatureID, e.Branch, e.Recorded, e.ForkedFrom, e.MainHead, e.ForkedFrom)
	}
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
		recorded, err = runGit(ctx, m.repo, "merge-base", m.baseRev(ctx, f), f.BranchName())
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
	base := m.baseRev(ctx, f)
	ok, err := gitOK(ctx, m.repo, "merge-base", "--is-ancestor", recorded, base)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	mainHead, err := runGit(ctx, m.repo, "rev-parse", base)
	if err != nil {
		return err
	}
	return &ForkDriftError{FeatureID: f.ID, Branch: f.BranchName(), Recorded: recorded, MainHead: mainHead,
		ForkedFrom: m.branchCarrying(ctx, recorded, f.BranchName())}
}

// branchCarrying names a local branch that still has sha in its history,
// other than the card's own — the branch the card actually forked from.
//
// Drift has two causes and the message owes a reader the right one. Main
// being rewritten is the one the remedy was written for; the other is the
// base moving out from under a card, which is what a card adopted out of
// a goal always looks like: its fork is a commit on the goal branch, and
// main has never seen it. Best effort — a name makes the sentence
// specific, and its absence only falls back to the original wording.
func (m *Manager) branchCarrying(ctx context.Context, sha, own string) string {
	out, err := runGit(ctx, m.repo, "branch", "--contains", sha, "--format=%(refname:short)")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(out, "\n") {
		name := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "* "))
		if name == "" || name == own {
			continue
		}
		return name
	}
	return ""
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
	p, base, err := m.diffBase(ctx, f)
	if err != nil {
		return "", err
	}
	return runGit(ctx, p, "diff", base)
}

// DiffStat is Diff's summary: the same base, `--stat` instead of the
// patch. It is what a caller shows when the full diff is too large to
// hand over inline, alongside DiffBase so the reader can fetch the rest.
func (m *Manager) DiffStat(ctx context.Context, f *domain.Feature) (string, error) {
	p, base, err := m.diffBase(ctx, f)
	if err != nil {
		return "", err
	}
	return runGit(ctx, p, "diff", "--stat", base)
}

// ChangedFile is one entry of a branch's changed-file list: the path, its
// size in the worktree, and whether git considers its content binary.
type ChangedFile struct {
	Path   string
	Size   int64
	Binary bool
	// Deleted marks a path the branch removed. Its size and binary flag
	// are meaningless, and a hygiene check must not read them as a file
	// the branch is shipping.
	Deleted bool
}

// ChangedFiles lists the files a feature's branch changes against the
// point it forked from main — the same range Diff reports, described per
// file instead of as a patch.
//
// It is the machine-readable half of "what is this branch actually
// shipping", for checks that have to hold whatever a model concludes: a
// committed build artifact or a stray fixture is a fact about the tree,
// not a judgement call, and asking a reviewer to notice it in a patch has
// already been shown not to work.
func (m *Manager) ChangedFiles(ctx context.Context, f *domain.Feature) ([]ChangedFile, error) {
	p, base, err := m.diffBase(ctx, f)
	if err != nil {
		return nil, err
	}
	// --numstat prints "-\t-\t<path>" for a binary file and line counts
	// otherwise, which is git's own answer to "is this binary" and costs
	// nothing extra alongside the name. -z keeps paths intact: without it
	// git quotes anything non-ASCII and the path has to be unescaped.
	out, err := runGit(ctx, p, "diff", "--numstat", "-z", base)
	if err != nil {
		return nil, err
	}
	var files []ChangedFile
	for _, cf := range parseNumstatZ(out) {
		if st, err := os.Stat(filepath.Join(p, cf.Path)); err == nil {
			cf.Size = st.Size()
		} else if os.IsNotExist(err) {
			cf.Deleted = true
		}
		files = append(files, cf)
	}
	return files, nil
}

// parseNumstatZ walks `git diff --numstat -z` output.
//
// The record shape is not one-per-NUL. An ordinary entry is
// "<added>\t<deleted>\t<path>\0", but a RENAME writes its counts with an
// empty path field and then two further NUL-terminated tokens, the old
// path and the new one: "<added>\t<deleted>\t\0<from>\0<to>\0". Splitting
// on NUL alone therefore drops every renamed file's destination — which is
// exactly the file a hygiene check needs to see.
func parseNumstatZ(out string) []ChangedFile {
	tok := strings.Split(out, "\x00")
	var files []ChangedFile
	for i := 0; i < len(tok); i++ {
		rec := tok[i]
		if strings.TrimSpace(rec) == "" {
			continue
		}
		fields := strings.SplitN(rec, "\t", 3)
		if len(fields) != 3 {
			continue
		}
		cf := ChangedFile{Path: fields[2], Binary: fields[0] == "-" && fields[1] == "-"}
		if cf.Path == "" {
			// a rename: the next two tokens are <from> and <to>, and the
			// destination is the file this branch now ships
			if i+2 >= len(tok) {
				continue
			}
			cf.Path = tok[i+2]
			i += 2
		}
		if cf.Path == "" {
			continue
		}
		files = append(files, cf)
	}
	return files
}

// DiffBase returns the SHA Diff and DiffStat compare against — the
// merge-base of main's HEAD and the feature branch. A caller that hands
// an agent a diff needs it too: without the base, "review the diff" makes
// the agent guess at a revision range, and a wrong guess reviews the
// wrong work.
func (m *Manager) DiffBase(ctx context.Context, f *domain.Feature) (string, error) {
	_, base, err := m.diffBase(ctx, f)
	return base, err
}

// diffBase resolves the worktree path and the base SHA the diff family
// shares, refusing on fork drift first: main rewound past the recorded
// fork makes every range below name work the feature never did.
func (m *Manager) diffBase(ctx context.Context, f *domain.Feature) (wtPath, base string, err error) {
	p, err := m.requireWorktree(f)
	if err != nil {
		return "", "", err
	}
	if err := m.AssertNoForkDrift(ctx, f); err != nil {
		return "", "", err
	}
	mainHead, err := m.BaseHead(ctx, f)
	if err != nil {
		return "", "", err
	}
	// The "HEAD" here is the card's own branch tip (the directory is its
	// worktree), not the trunk — the other meaning of the token that
	// baseRev's comment warns about. Both ends are deliberate.
	base, err = runGit(ctx, p, "merge-base", mainHead, "HEAD")
	if err != nil {
		return "", "", err
	}
	return p, base, nil
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
