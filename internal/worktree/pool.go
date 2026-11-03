package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/gummidir"
)

// NamedRepo names one selectable repository: its configured name and its
// resolved absolute root (joined against the workspace root).
type NamedRepo struct {
	Name string
	Root string
}

// ErrRepoNotConfigured reports a card whose repository cannot be reached
// by name: either it was dropped from `repos:` after the card was minted
// into it, or it names the workspace default and none is configured.
// Nothing on disk answers for it any more, so a caller that cannot reach the card's
// worktree or branch this way can still fall back to removing the record.
var ErrRepoNotConfigured = errors.New("repository is not configured")

// repoNotConfiguredError carries the detailed, user-facing message while
// still unwrapping to ErrRepoNotConfigured for callers that only need to
// recognize the case.
type repoNotConfiguredError struct{ msg string }

func (e *repoNotConfiguredError) Error() string { return e.msg }
func (e *repoNotConfiguredError) Unwrap() error { return ErrRepoNotConfigured }

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
	fs          ForkPointStore
	exclude     bool // run EnsureGummiExcluded on creation (mutating commands)
	mu          sync.Mutex
	// homeMu serializes making an agent's home; ceilOnce is the one time
	// this process is told not to climb out of the homes directory.
	homeMu   sync.Mutex
	ceilOnce sync.Once
	byName   map[string]string // configured name -> resolved repo root; under mu
	byRoot   map[string]*Manager
	// discover rescans the named set; nil when the set is fixed config.
	// ambiguous is its last report of the folder names nobody got.
	discover  Discover
	ambiguous map[string][]string
	// scanned is when a miss last rescanned: a card still holding a name
	// that is gone is asked about on every board tick, and must not walk
	// the disk each time.
	scanned time.Time
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

// Discover rescans the workspace's named repositories: the named set and
// the folder names it could not hand out (config.RepoSet's Named and
// Ambiguous). The default repository is fixed at launch and never rescanned.
type Discover func() (named []NamedRepo, ambiguous map[string][]string, err error)

// SetDiscover makes the named set discovered rather than fixed: d runs now,
// and again whenever a name is asked for that the set does not hold, so a
// checkout cloned after launch is usable without a restart.
func (p *Pool) SetDiscover(d Discover) error {
	p.mu.Lock()
	p.discover = d
	p.mu.Unlock()
	return p.Refresh()
}

// Discovering reports whether the named set is rescanned (SetDiscover).
func (p *Pool) Discovering() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.discover != nil
}

// Refresh rescans the named set when it is discovered; a no-op otherwise.
// A name that disappears is dropped like one removed from `repos:`, so a
// card still holding it gets ErrRepoNotConfigured; managers already built
// stay cached by root.
func (p *Pool) Refresh() error {
	p.mu.Lock()
	d := p.discover
	p.mu.Unlock()
	if d == nil {
		return nil
	}
	named, ambiguous, err := d()
	if err != nil {
		return err
	}
	byName := make(map[string]string, len(named))
	for _, n := range named {
		abs, aerr := filepath.Abs(n.Root)
		if aerr != nil {
			return fmt.Errorf("repo %q: %w", n.Name, aerr)
		}
		byName[n.Name] = abs
	}
	p.mu.Lock()
	p.byName = byName
	p.ambiguous = ambiguous
	p.mu.Unlock()
	return nil
}

// missRescanEvery bounds how often a miss rescans the disk.
const missRescanEvery = 2 * time.Second

// lookup resolves a non-empty name against the named set, rescanning on a
// miss when the set is discovered (at most once per missRescanEvery).
func (p *Pool) lookup(name string) (string, bool) {
	p.mu.Lock()
	root, ok := p.byName[name]
	rescan := !ok && p.discover != nil && time.Since(p.scanned) >= missRescanEvery
	if rescan {
		p.scanned = time.Now()
	}
	p.mu.Unlock()
	if !rescan {
		return root, ok
	}
	if err := p.Refresh(); err != nil {
		fmt.Fprintln(os.Stderr, "gummi: rescanning repositories:", err)
		return "", false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	root, ok = p.byName[name]
	return root, ok
}

// ClashError is the refusal for a name that several checkouts share, which
// discovery leaves unnamed, listing every path it could mean. It is nil for a
// name nobody clashes on. It reads the last scan and never rescans, so a
// caller that has already asked Known gets the clash, not "not configured".
func (p *Pool) ClashError(name string) error {
	p.mu.Lock()
	clash := p.ambiguous[name]
	p.mu.Unlock()
	if len(clash) == 0 {
		return nil
	}
	return &repoNotConfiguredError{msg: fmt.Sprintf("repository %q is ambiguous: %s all share that folder name; pin the one you mean under `repos:` in .gummi/config.yaml", name, strings.Join(clash, ", "))}
}

// Ambiguous reports the folder names discovery's last scan left unnamed,
// each with the checkouts sharing it. Empty for a fixed set.
func (p *Pool) Ambiguous() map[string][]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string][]string, len(p.ambiguous))
	for name, paths := range p.ambiguous {
		out[name] = append([]string(nil), paths...)
	}
	return out
}

// DefaultName is the empty string: the conventional name for the workspace
// default repository, used by creation surfaces to mean "no explicit choice".
func (p *Pool) DefaultName() string { return "" }

// Known reports whether name is a configured repository. The empty name
// (the workspace default) is known only when a default exists; any other
// name must be a key of the configured `repos:` set, or one discovery
// finds on a rescan. Creation surfaces use it to reject an unselectable
// repo at creation, before any drive-time resolution.
func (p *Pool) Known(name string) bool {
	if name == "" {
		return p.defaultRoot != ""
	}
	_, ok := p.lookup(name)
	return ok
}

// Names returns the sorted configured repo names (excluding the empty
// default), for the creation surfaces that offer an explicit selector.
func (p *Pool) Names() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
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
// a freeform session created resolves to the manager rooted at that
// freeform card's worktree instead, so its branch forks from and lands on
// the freeform branch. An agent names no
// repository at all and resolves to the manager rooted at its home.
func (p *Pool) ManagerFor(ctx context.Context, f *domain.Feature) (*Manager, error) {
	if f.Home {
		return p.managerForHome(ctx, f)
	}
	if f.ParentID != "" {
		return p.managerForDelegatedCard(ctx, f)
	}
	return p.ManagerForName(ctx, f.Repo)
}

// ManagerForName resolves a repo name ("" = default) to its cached manager.
// An empty name with no default configured fails here — at the point a card
// actually needs the default — never at pool construction, unwrapping to
// ErrRepoNotConfigured exactly like the named-but-unconfigured case, so the
// two kinds of unreachable repo are one error to every consumer.
func (p *Pool) ManagerForName(ctx context.Context, name string) (*Manager, error) {
	var root string
	if name == "" {
		if p.defaultRoot == "" {
			return nil, &repoNotConfiguredError{msg: "no default repository configured; name one with --repo (a configured `repos:` entry) or set `repo:` in .gummi/config.yaml"}
		}
		root = p.defaultRoot
	} else {
		r, ok := p.lookup(name)
		if !ok {
			if err := p.ClashError(name); err != nil {
				return nil, err
			}
			return nil, &repoNotConfiguredError{msg: fmt.Sprintf("repository %q is not configured; add it to `repos:` in .gummi/config.yaml, or recreate the card against a configured repository", name)}
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

func (p *Pool) Create(ctx context.Context, f *domain.Feature) (string, error) {
	wt, err := p.ManagerFor(ctx, f)
	if err != nil {
		return "", err
	}
	return wt.Create(ctx, f)
}

// CreateFrom is Manager.CreateFrom in the card's repository.
func (p *Pool) CreateFrom(ctx context.Context, f *domain.Feature, start string) (string, error) {
	wt, err := p.ManagerFor(ctx, f)
	if err != nil {
		return "", err
	}
	return wt.CreateFrom(ctx, f, start)
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

func (p *Pool) DiffBase(ctx context.Context, f *domain.Feature) (string, error) {
	wt, err := p.ManagerFor(ctx, f)
	if err != nil {
		return "", err
	}
	return wt.DiffBase(ctx, f)
}

func (p *Pool) DiffSince(ctx context.Context, f *domain.Feature, rev string) (string, error) {
	wt, err := p.ManagerFor(ctx, f)
	if err != nil {
		return "", err
	}
	return wt.DiffSince(ctx, f, rev)
}

func (p *Pool) Upstream(ctx context.Context, f *domain.Feature) (remote, branch string, ok bool) {
	wt, err := p.ManagerFor(ctx, f)
	if err != nil {
		return "", "", false
	}
	return wt.Upstream(ctx, f)
}

func (p *Pool) UpstreamRewritten(ctx context.Context, f *domain.Feature) bool {
	wt, err := p.ManagerFor(ctx, f)
	if err != nil {
		return false
	}
	return wt.UpstreamRewritten(ctx, f)
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

func (p *Pool) Spent(ctx context.Context, f *domain.Feature) (bool, error) {
	wt, err := p.ManagerFor(ctx, f)
	if err != nil {
		return false, err
	}
	return wt.Spent(ctx, f)
}

// Cleanable reports whether a closed card still holds something a clean
// would remove: a worktree, or a branch whose work exists elsewhere. It
// leaves out a main-checkout card, which has neither, and anything
// holding uncommitted work. A branch that is the only copy of its work
// (Manager.Spent) does not make a card cleanable on its own: a clean
// would keep it, and a card with nothing else left is already tidy. A
// read that fails answers no — this picks which cards are worth handing
// to a clean, and the clean repeats every check on the card it is given.
func (p *Pool) Cleanable(ctx context.Context, f *domain.Feature) bool {
	// a session's hidden child has no branch of its own to clean: a step
	// works in its session's tree, and an old crew's branches are left in
	// the repository as they were (DESIGN §25.2)
	if f.Stage != domain.StageDone || f.MainCheckout || f.IsChild() {
		return false
	}
	spent, err := p.Spent(ctx, f)
	if err != nil {
		return false
	}
	if ok, err := p.Exists(ctx, f); err != nil {
		return false
	} else if !ok {
		branch, err := p.BranchExists(ctx, f)
		return err == nil && branch && spent && !f.Adopted()
	}
	loose := p.TrackedDirty
	if !spent {
		loose = p.Dirty
	}
	dirty, err := loose(ctx, f)
	return err == nil && !dirty
}

// GoesWithClose reports whether closing a card takes its worktree and
// branch with it, where the person did not ask to keep them. A branch is
// a piece of work at a point in time and a closed card has no further use
// for one — unless it is the only copy of that work, in which case the
// card is left exactly as the closing found it, worktree included: it is
// what the person pushes, or lands after all. A stacked card stays too:
// other branches fork from its own.
func (p *Pool) GoesWithClose(ctx context.Context, f *domain.Feature) bool {
	if f.Stacked() || !p.Cleanable(ctx, f) {
		return false
	}
	spent, err := p.Spent(ctx, f)
	return err == nil && spent
}

func (p *Pool) DeleteClosedBranch(ctx context.Context, f *domain.Feature) error {
	wt, err := p.ManagerFor(ctx, f)
	if err != nil {
		return err
	}
	return wt.DeleteClosedBranch(ctx, f)
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

// Land mirrors Manager.Land, resolving f's repository manager first.
func (p *Pool) Land(ctx context.Context, f *domain.Feature, message string, method domain.LandMethod) (string, error) {
	wt, err := p.ManagerFor(ctx, f)
	if err != nil {
		return "", err
	}
	return wt.Land(ctx, f, message, method)
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

// Unmerged mirrors Manager.Unmerged, resolving f's repository manager first.
func (p *Pool) Unmerged(ctx context.Context, f *domain.Feature) ([]string, error) {
	wt, err := p.ManagerFor(ctx, f)
	if err != nil {
		return nil, err
	}
	return wt.Unmerged(ctx, f)
}

// Drift mirrors Manager.Drift, resolving f's repository manager first.
func (p *Pool) Drift(ctx context.Context, f *domain.Feature) (*ForkDriftError, error) {
	wt, err := p.ManagerFor(ctx, f)
	if err != nil {
		return nil, err
	}
	return wt.Drift(ctx, f)
}

func (p *Pool) ReanchorOnMain(ctx context.Context, f *domain.Feature) error {
	wt, err := p.ManagerFor(ctx, f)
	if err != nil {
		return err
	}
	return wt.ReanchorOnMain(ctx, f)
}

// Remove removes the card's worktree.
func (p *Pool) Remove(ctx context.Context, f *domain.Feature, force bool) error {
	// an agent has no worktree, and its home is never a cleanup's to take
	if f.Home {
		return nil
	}
	wt, err := p.ManagerFor(ctx, f)
	if err != nil {
		return err
	}
	return wt.Remove(ctx, f, force)
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

// managerForDelegatedCard resolves a card a freeform session created: the
// manager rooted at the freeform card's own worktree while it exists, so
// the card forks from and lands on the freeform branch; its repository's
// manager once the freeform card has landed and its worktree is gone.
func (p *Pool) managerForDelegatedCard(ctx context.Context, f *domain.Feature) (*Manager, error) {
	dir := gummidir.Path(p.root, "worktrees", string(f.ParentID))
	if _, err := os.Stat(dir); err == nil {
		return p.managerAt(ctx, dir)
	}
	return p.ManagerForName(ctx, f.Repo)
}

// managerAt returns the cached manager rooted at a linked worktree of a
// managed repository — a freeform card's, whose delegated and child cards
// fork from it. It never runs the .gummi exclusion pass: the worktree is
// a linked worktree of an already-excluded repository.
func (p *Pool) managerAt(ctx context.Context, dir string) (*Manager, error) {
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	if m, ok := p.byRoot[real]; ok {
		p.mu.Unlock()
		return m, nil
	}
	p.mu.Unlock()
	m, err := NewManager(ctx, p.root, real, p.fs)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	if cur, ok := p.byRoot[real]; ok {
		m = cur
	} else {
		p.byRoot[real] = m
	}
	p.mu.Unlock()
	return m, nil
}
