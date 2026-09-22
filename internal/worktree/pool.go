package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/morphis/gummi/internal/domain"
)

// NamedRepo names one selectable repository: its configured name and its
// resolved absolute root (joined against the workspace root).
type NamedRepo struct {
	Name string
	Root string
}

// Pool caches one worktree.Manager per managed repository root, all bound to
// the same workspace root. It is the per-card gateway: a card names a
// configured repository (the empty name selects the default), and ManagerFor
// resolves that card to the cached manager for its repo, creating it on first
// use through the same validation and .gummi exclusion as a single-repo
// construction. The default manager is resolved eagerly at pool build, so a
// workspace with no `repos:` and no per-card choice behaves exactly as
// before; named repositories are created lazily on first access. Access is
// concurrency-safe: the board and the autonomous slots can touch several
// managers at once, and each manager already serializes main mutations.
type Pool struct {
	root        string // workspace root, shared by every cached manager
	defaultRoot string
	byName      map[string]string // configured name -> resolved repo root
	fs          ForkPointStore
	exclude     bool // run EnsureGummiExcluded on creation (mutating commands)
	mu          sync.Mutex
	byRoot      map[string]*Manager
	// goalLookup resolves a goal card by id, so a card whose goal worktree
	// is gone can tell an ended goal from a broken one (see goal.go).
	goalLookup GoalLookup
	// baseLookup resolves the revision a card forks from, installed on
	// every manager the pool builds. Nil leaves every card on its repo's
	// HEAD, which is what happened before bases were selectable.
	baseLookup BaseLookup
	// mergeHook reports a successful squash-merge landing, installed on
	// every manager the pool builds the same way baseLookup is. Nil
	// leaves landings unreported.
	mergeHook MergeHook
}

// SetBaseLookup installs the base resolver on the pool and on every
// manager it has already built, so a lookup registered after launch
// reaches the eagerly-created default manager too.
// A nil *Pool is a working no-op, the way a nil *state.CardLocks is: a
// caller with no pool at all (a test scaffold, a launch that got no
// further than the config) has nothing to teach about bases and must not
// have to know that.
func (p *Pool) SetBaseLookup(l BaseLookup) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.baseLookup = l
	for _, m := range p.byRoot {
		m.SetBaseLookup(l)
	}
}

// SetMergeHook installs the landing reporter on the pool and on every
// manager it has already built, so a hook registered after launch
// reaches the eagerly-created default manager too. Nil-safe like
// SetBaseLookup.
func (p *Pool) SetMergeHook(h MergeHook) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.mergeHook = h
	for _, m := range p.byRoot {
		m.SetMergeHook(h)
	}
}

// NewPool builds the pool from the workspace root, the default repo root, and
// the configured named roots, then eagerly resolves the default manager when
// one exists (an empty defaultRoot marks a repos:-only workspace with no
// default). Named roots are validated (git toplevel) lazily on first use.
// When exclude is set, each manager gets the .gummi exclusion treatment at
// creation.
func NewPool(ctx context.Context, wsRoot, defaultRoot string, named []NamedRepo, fs ForkPointStore, exclude bool) (*Pool, error) {
	absWs, err := filepath.Abs(wsRoot)
	if err != nil {
		return nil, err
	}
	p := &Pool{
		root: absWs, defaultRoot: "",
		byName: map[string]string{}, fs: fs, exclude: exclude,
		byRoot: map[string]*Manager{},
	}
	if defaultRoot != "" {
		absDef, err := filepath.Abs(defaultRoot)
		if err != nil {
			return nil, err
		}
		p.defaultRoot = absDef
	}
	for _, n := range named {
		abs, aerr := filepath.Abs(n.Root)
		if aerr != nil {
			return nil, fmt.Errorf("repo %q: %w", n.Name, aerr)
		}
		p.byName[n.Name] = abs
	}
	if p.defaultRoot != "" {
		if _, err := p.manager(ctx, p.defaultRoot); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// Root returns the absolute workspace root shared by every cached manager —
// the base .gummi-relative paths join onto.
func (p *Pool) Root() string { return p.root }

// WrapSingle adopts an already-constructed manager as the pool's only
// (default) repository, with no exclusion pass and no named roots. It backs
// the compatibility path where a caller binds one manager directly (the
// engine's test fixtures); the pool then resolves every card to it.
func WrapSingle(m *Manager) *Pool {
	return &Pool{
		root: m.Root(), defaultRoot: m.RepoRoot(),
		byName: map[string]string{}, fs: m.forkStore, exclude: false,
		byRoot: map[string]*Manager{m.RepoRoot(): m},
	}
}

// DefaultName is the empty string: the conventional name for the workspace
// default repository, used by creation surfaces to mean "no explicit choice".
func (p *Pool) DefaultName() string { return "" }

// Known reports whether name is a configured repository. The empty name
// (the workspace default) is known only when a default exists; any other
// name must be a key of the configured `repos:` set. Creation surfaces use
// it to reject an unselectable repo at creation, before any drive-time
// resolution.
func (p *Pool) Known(name string) bool {
	if name == "" {
		return p.defaultRoot != ""
	}
	_, ok := p.byName[name]
	return ok
}

// ProvisionalRepo is the repository a card gets when nobody named one: the
// workspace default when there is one, else the first configured name.
//
// Only a goal takes it. A goal is not in a repository — its cards name
// their own — but its own branch has to be cut somewhere while its plan
// is still being agreed, and in a repos:-only workspace there is no
// default to cut it in. The plan gate settles the goal's home for real,
// from the repos its cards turned out to be in.
func (p *Pool) ProvisionalRepo() string {
	if p.Known("") {
		return ""
	}
	if names := p.Names(); len(names) > 0 {
		return names[0]
	}
	return ""
}

// Names returns the sorted configured repo names (excluding the empty
// default), for the creation surfaces that offer an explicit selector.
func (p *Pool) Names() []string {
	names := make([]string, 0, len(p.byName))
	for n := range p.byName {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// ManagerFor resolves f's repository (the empty name selects the default) and
// returns the cached manager for that repo, creating it on first use. A
// stored-but-unconfigured repo name is a resolution-time error. A card that
// belongs to a goal resolves to the manager rooted at the goal's worktree
// instead, so its branch forks from and lands on the goal branch.
func (p *Pool) ManagerFor(ctx context.Context, f *domain.Feature) (*Manager, error) {
	if f.GoalID != "" {
		return p.managerForGoalCard(ctx, f)
	}
	return p.ManagerForName(ctx, f.Repo)
}

// ManagerForName resolves a repo name ("" = default) to its cached manager.
// An empty name with no default configured fails here — at the point a card
// actually needs the default — never at pool construction.
func (p *Pool) ManagerForName(ctx context.Context, name string) (*Manager, error) {
	var root string
	if name == "" {
		if p.defaultRoot == "" {
			return nil, fmt.Errorf("no default repository configured; name one with --repo (a configured `repos:` entry) or set `repo:` in .gummi/config.yaml")
		}
		root = p.defaultRoot
	} else {
		r, ok := p.byName[name]
		if !ok {
			return nil, fmt.Errorf("repository %q is not configured; add it to `repos:` in .gummi/config.yaml, or recreate the card against a configured repository", name)
		}
		root = r
	}
	return p.manager(ctx, root)
}

// manager returns the cached manager for root, creating and caching one on
// first use. Double-checked under p.mu so concurrent callers never build two
// live managers for the same root.
func (p *Pool) manager(ctx context.Context, root string) (*Manager, error) {
	p.mu.Lock()
	if m, ok := p.byRoot[root]; ok {
		p.mu.Unlock()
		return m, nil
	}
	p.mu.Unlock()
	m, err := NewManager(ctx, p.root, root, p.fs)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	lookup := p.baseLookup
	hook := p.mergeHook
	p.mu.Unlock()
	if lookup != nil {
		m.SetBaseLookup(lookup)
	}
	if hook != nil {
		m.SetMergeHook(hook)
	}
	if p.exclude {
		if untracked, xerr := m.EnsureGummiExcluded(ctx); xerr != nil {
			// the same best-effort warning as the single-repo launch path:
			// a repo it cannot clean must not silently block the launch.
			fmt.Fprintln(os.Stderr, "gummi: excluding .gummi from tracking:", xerr)
		} else if untracked {
			fmt.Fprintln(os.Stderr, "gummi: .gummi was tracked in this repo — untracked it (index only; the removal rides into your next commit)")
		}
	}
	p.mu.Lock()
	if cur, ok := p.byRoot[root]; ok {
		m = cur
	} else {
		p.byRoot[root] = m
	}
	p.mu.Unlock()
	return m, nil
}

// --- per-card facade -----------------------------------------------------
// The methods below resolve the card's repository manager and delegate. They
// keep call sites that operate on a specific feature unchanged (the same
// `wt.Method(ctx, &f)` shape), routing through the pool so a multi-repo
// board's operations always hit the card's own repo.

// BaseBranch names the branch repo `name` (the empty name selects the
// workspace default) currently has out, for the UI's copy. It never
// fails: an unconfigured name or an unreadable HEAD reports
// DefaultBaseBranchName, because a sentence that cannot name the branch
// still has to say something.
func (p *Pool) BaseBranch(ctx context.Context, name string) string {
	wt, err := p.ManagerForName(ctx, name)
	if err != nil || wt == nil {
		return DefaultBaseBranchName
	}
	return wt.BaseBranch(ctx)
}

func (p *Pool) Path(f *domain.Feature) (string, error) {
	wt, err := p.ManagerFor(context.Background(), f)
	if err != nil {
		return "", err
	}
	return wt.Path(f)
}

func (p *Pool) Exists(ctx context.Context, f *domain.Feature) (bool, error) {
	wt, err := p.ManagerFor(ctx, f)
	if err != nil {
		return false, err
	}
	return wt.Exists(ctx, f)
}

// Ensure resolves the card's repo and returns its branch worktree,
// creating it on first use (Manager.Ensure).
func (p *Pool) Ensure(ctx context.Context, f *domain.Feature) (string, error) {
	wt, err := p.ManagerFor(ctx, f)
	if err != nil {
		return "", err
	}
	return wt.Ensure(ctx, f)
}

func (p *Pool) EnsureScratch(ctx context.Context, f *domain.Feature) (string, error) {
	wt, err := p.ManagerFor(ctx, f)
	if err != nil {
		return "", err
	}
	return wt.EnsureScratch(ctx, f)
}

func (p *Pool) RemoveScratch(ctx context.Context, f *domain.Feature) error {
	wt, err := p.ManagerFor(ctx, f)
	if err != nil {
		return err
	}
	return wt.RemoveScratch(ctx, f)
}

func (p *Pool) Create(ctx context.Context, f *domain.Feature) (string, error) {
	wt, err := p.ManagerFor(ctx, f)
	if err != nil {
		return "", err
	}
	return wt.Create(ctx, f)
}

// Attach is Create's counterpart for an adopted card (Manager.Attach).
func (p *Pool) Attach(ctx context.Context, f *domain.Feature) (string, error) {
	wt, err := p.ManagerFor(ctx, f)
	if err != nil {
		return "", err
	}
	return wt.Attach(ctx, f)
}

// BehindBase reports how far the card's branch trails its base, as
// Manager.BehindBase does; 0 when the card's repo cannot be resolved,
// since this is reporting, never a gate.
func (p *Pool) BehindBase(ctx context.Context, f *domain.Feature) int {
	wt, err := p.ManagerFor(ctx, f)
	if err != nil {
		return 0
	}
	return wt.BehindBase(ctx, f)
}

func (p *Pool) Diff(ctx context.Context, f *domain.Feature) (string, error) {
	wt, err := p.ManagerFor(ctx, f)
	if err != nil {
		return "", err
	}
	return wt.Diff(ctx, f)
}

func (p *Pool) CommitAll(ctx context.Context, f *domain.Feature, message string) (bool, error) {
	wt, err := p.ManagerFor(ctx, f)
	if err != nil {
		return false, err
	}
	return wt.CommitAll(ctx, f, message)
}

func (p *Pool) BranchExists(ctx context.Context, f *domain.Feature) (bool, error) {
	wt, err := p.ManagerFor(ctx, f)
	if err != nil {
		return false, err
	}
	return wt.BranchExists(ctx, f)
}

func (p *Pool) DeleteBranch(ctx context.Context, f *domain.Feature, force bool) error {
	wt, err := p.ManagerFor(ctx, f)
	if err != nil {
		return err
	}
	return wt.DeleteBranch(ctx, f, force)
}

func (p *Pool) DeleteLandedBranch(ctx context.Context, f *domain.Feature) error {
	wt, err := p.ManagerFor(ctx, f)
	if err != nil {
		return err
	}
	return wt.DeleteLandedBranch(ctx, f)
}

func (p *Pool) Head(ctx context.Context, f *domain.Feature) (string, error) {
	wt, err := p.ManagerFor(ctx, f)
	if err != nil {
		return "", err
	}
	return wt.Head(ctx, f)
}

func (p *Pool) BranchAhead(ctx context.Context, f *domain.Feature) (bool, error) {
	wt, err := p.ManagerFor(ctx, f)
	if err != nil {
		return false, err
	}
	return wt.BranchAhead(ctx, f)
}

func (p *Pool) Dirty(ctx context.Context, f *domain.Feature) (bool, error) {
	wt, err := p.ManagerFor(ctx, f)
	if err != nil {
		return false, err
	}
	return wt.Dirty(ctx, f)
}

func (p *Pool) TrackedDirty(ctx context.Context, f *domain.Feature) (bool, error) {
	wt, err := p.ManagerFor(ctx, f)
	if err != nil {
		return false, err
	}
	return wt.TrackedDirty(ctx, f)
}

func (p *Pool) MainTrackedDirty(ctx context.Context, f *domain.Feature) (bool, error) {
	wt, err := p.ManagerFor(ctx, f)
	if err != nil {
		return false, err
	}
	return wt.MainTrackedDirty(ctx)
}

func (p *Pool) Landed(ctx context.Context, f *domain.Feature) (bool, error) {
	wt, err := p.ManagerFor(ctx, f)
	if err != nil {
		return false, err
	}
	return wt.Landed(ctx, f)
}

func (p *Pool) SquashMerge(ctx context.Context, f *domain.Feature, message string) (string, error) {
	wt, err := p.ManagerFor(ctx, f)
	if err != nil {
		return "", err
	}
	return wt.SquashMerge(ctx, f, message)
}

// Collapse mirrors Manager.Collapse, resolving f's repository manager first.
// It exists for TUI-facing symmetry with SquashMerge (Shell.wt is a *Pool);
// the CLI `gummi squash` path calls Manager.Collapse directly via the
// per-card manager it already resolves through Driver.
func (p *Pool) Collapse(ctx context.Context, f *domain.Feature, message, baseSHA string) (string, error) {
	wt, err := p.ManagerFor(ctx, f)
	if err != nil {
		return "", err
	}
	return wt.Collapse(ctx, f, message, baseSHA)
}

func (p *Pool) RebaseOnMain(ctx context.Context, f *domain.Feature) error {
	wt, err := p.ManagerFor(ctx, f)
	if err != nil {
		return err
	}
	return wt.RebaseOnMain(ctx, f)
}

func (p *Pool) RebaseOnMainAutostash(ctx context.Context, f *domain.Feature) error {
	wt, err := p.ManagerFor(ctx, f)
	if err != nil {
		return err
	}
	return wt.RebaseOnMainAutostash(ctx, f)
}

func (p *Pool) RebaseInProgress(ctx context.Context, f *domain.Feature) (bool, error) {
	wt, err := p.ManagerFor(ctx, f)
	if err != nil {
		return false, err
	}
	return wt.RebaseInProgress(ctx, f)
}

func (p *Pool) AbortRebase(ctx context.Context, f *domain.Feature) (bool, error) {
	wt, err := p.ManagerFor(ctx, f)
	if err != nil {
		return false, err
	}
	return wt.AbortRebase(ctx, f)
}

func (p *Pool) RebasedOnBase(ctx context.Context, f *domain.Feature) (bool, error) {
	wt, err := p.ManagerFor(ctx, f)
	if err != nil {
		return false, err
	}
	return wt.RebasedOnBase(ctx, f)
}

func (p *Pool) AssertNoForkDrift(ctx context.Context, f *domain.Feature) error {
	wt, err := p.ManagerFor(ctx, f)
	if err != nil {
		return err
	}
	return wt.AssertNoForkDrift(ctx, f)
}

func (p *Pool) ReanchorOnMain(ctx context.Context, f *domain.Feature) error {
	wt, err := p.ManagerFor(ctx, f)
	if err != nil {
		return err
	}
	return wt.ReanchorOnMain(ctx, f)
}

// Remove removes the card's worktree. A goal has one per repository it
// touched (goal.go), and a cleanup that took only the one named after the
// card would leave the others checked out forever — so the siblings go
// first, each with the branch it was holding.
func (p *Pool) Remove(ctx context.Context, f *domain.Feature, force bool) error {
	if f.IsGoal() {
		if err := p.removeGoalTrees(ctx, f); err != nil {
			return err
		}
	}
	wt, err := p.ManagerFor(ctx, f)
	if err != nil {
		return err
	}
	return wt.Remove(ctx, f, force)
}

// removeGoalTrees removes a goal's trees in every repository but its home,
// and the branch each was holding when that branch is fully merged. A
// branch git refuses to delete (commits nothing has) is left alone with
// its tree gone: the cleanup's job is the checkouts, and unmerged work is
// never a cleanup's to discard.
func (p *Pool) removeGoalTrees(ctx context.Context, goal *domain.Feature) error {
	return p.sweepGoalTrees(ctx, goal, false)
}

// DeleteGoalTrees removes a goal's trees in every repository but its
// home, branch and all. It is removeGoalTrees for the caller that is
// destroying the goal rather than tidying up after it (the board's D):
// there, a branch left standing belongs to a card, a tree and a record
// that are all gone, so nothing will ever offer to remove it again.
//
// The home repo's tree is the goal card's own worktree and goes the way
// every card's does, through Remove — which, finding no siblings left on
// disk, has nothing more to sweep.
func (p *Pool) DeleteGoalTrees(ctx context.Context, goal *domain.Feature) error {
	return p.sweepGoalTrees(ctx, goal, true)
}

// sweepGoalTrees is both of the above, over every repository the goal
// has a tree in but its home.
func (p *Pool) sweepGoalTrees(ctx context.Context, goal *domain.Feature, force bool) error {
	for _, repo := range p.goalTreeRepos(*goal) {
		if repo == goal.Repo {
			continue
		}
		m, err := p.ManagerForName(ctx, repo)
		if err != nil {
			return err
		}
		remove := m.RemoveGoalTree
		if force {
			remove = m.DeleteGoalTree
		}
		if err := remove(ctx, goal, goalTreeName(*goal, repo)); err != nil {
			var unmerged *unmergedBranchError
			if errors.As(err, &unmerged) {
				continue
			}
			return err
		}
	}
	return nil
}

// DiskSize reports the bytes f's worktree holds. See Manager.DiskSize —
// it walks the checkout, so it belongs on a surface someone opened on
// purpose, never on a render path.
func (p *Pool) DiskSize(ctx context.Context, f *domain.Feature) (int64, error) {
	wt, err := p.ManagerFor(ctx, f)
	if err != nil {
		return 0, err
	}
	return wt.DiskSize(ctx, f)
}

func (p *Pool) ProvenanceWarnings(ctx context.Context, f *domain.Feature) ([]string, error) {
	wt, err := p.ManagerFor(ctx, f)
	if err != nil {
		return nil, err
	}
	return wt.ProvenanceWarnings(ctx, f)
}
