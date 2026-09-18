package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/morphis/gummi/internal/domain"
)

// Goals (domain.KindGoal) share one branch between their cards. The trick
// that makes this cheap is that nothing in this package names a trunk:
// every "main" is HEAD of Manager.repo. So a goal card resolves to a
// Manager whose repo is the GOAL'S WORKTREE, and with no other change its
// branch forks from the goal branch, SquashMerge lands it there as one
// commit, Landed/BranchAhead/Diff read against the goal branch, and the
// dependency gate's "met at done" means "landed on the goal branch".
//
// An outcome is not a checkout, so a goal is not in one repository: its
// cards name their own, and the goal has a GoalTree — a worktree on a
// branch of the goal's name — in each one. The tree in the goal's HOME
// repo (the repository the goal card itself lives in) is the goal card's
// own worktree, .gummi/worktrees/GL-NNN; every other repo's is
// .gummi/worktrees/GL-NNN@<repo>, a sibling of it. A card resolves to the
// goal tree OF ITS OWN REPOSITORY, so the paragraph above holds once per
// repository rather than once per goal — a single-repo goal is the case
// where that is the same sentence.
//
// The goal itself is an ordinary card of its home repo. It lands with one
// merge per repository (GoalTree.Merge — the home tree's also carries the
// goal's fork-point and landed-commit bookkeeping, which belongs to the
// card's row) and catches up with main per repository (GoalTree.CatchUp,
// a merge of that repo's main into that repo's goal branch, keeping every
// in-flight card's recorded fork point an ancestor of it). Git has no
// cross-repository merge, so landing a goal that spans repositories is N
// landings and can stop half-way; the conductor reports that rather than
// pretending otherwise.

// GoalLookup resolves a goal card by id — the store's GetFeature. The pool
// uses it to tell a goal whose worktree is legitimately gone (the goal
// ended) from one whose worktree went missing while its cards still need
// it.
type GoalLookup func(ctx context.Context, id domain.FeatureID) (domain.Feature, error)

// ErrGoalWorktreeMissing reports a card of a running goal whose goal
// worktree is not on disk. Its cards cannot fork from or land on a branch
// with no checkout, and silently using main instead would put goal work on
// the wrong branch.
var ErrGoalWorktreeMissing = errors.New("goal worktree is missing")

// SetGoalLookup wires the goal resolver. Without one, a card whose goal
// worktree is absent falls back to its repository's manager.
func (p *Pool) SetGoalLookup(fn GoalLookup) {
	p.mu.Lock()
	p.goalLookup = fn
	p.mu.Unlock()
}

// goalTreeName is the directory a goal's tree in repo takes under
// .gummi/worktrees: the goal's id in its home repo — where the tree is
// the goal card's own worktree, and where every goal that has ever run
// already has one — and id@repo elsewhere.
func goalTreeName(goal domain.Feature, repo string) string {
	if repo == goal.Repo {
		return string(goal.ID)
	}
	if repo == "" {
		// A workspace has either one default repo or a named set, never
		// both (config.ResolveRepos), so this is unreachable in practice;
		// it is spelled out rather than left to produce a bare "GL-1@".
		return string(goal.ID) + "@default"
	}
	return string(goal.ID) + "@" + repo
}

// goalTreeDir is where a goal's tree in repo lives.
func (p *Pool) goalTreeDir(goal domain.Feature, repo string) string {
	return filepath.Join(p.root, ".gummi", "worktrees", goalTreeName(goal, repo))
}

// goalOf resolves the goal card a card belongs to.
func (p *Pool) goalOf(ctx context.Context, f *domain.Feature) (domain.Feature, bool, error) {
	p.mu.Lock()
	lookup := p.goalLookup
	p.mu.Unlock()
	if lookup == nil {
		return domain.Feature{}, false, nil
	}
	goal, err := lookup(ctx, f.GoalID)
	if err != nil {
		return domain.Feature{}, false, fmt.Errorf("resolving goal %s of %s: %w", f.GoalID, f.ID, err)
	}
	return goal, true, nil
}

// managerForGoalCard resolves a card that belongs to a goal: the manager
// rooted at the goal's tree IN THE CARD'S OWN REPOSITORY while it exists;
// the card repository's manager once the goal has ended and its trees are
// gone.
//
// Which directory that is depends on the goal's home repo, so this reads
// the goal card. Without a resolver installed (a pool built around a
// single manager) it keeps the pre-multi-repo behavior: the flat
// .gummi/worktrees/GL-NNN, which is the home tree of the only repo there
// is.
func (p *Pool) managerForGoalCard(ctx context.Context, f *domain.Feature) (*Manager, error) {
	goal, known, err := p.goalOf(ctx, f)
	if err != nil {
		return nil, err
	}
	if !known {
		dir := filepath.Join(p.root, ".gummi", "worktrees", string(f.GoalID))
		if _, statErr := os.Stat(dir); statErr == nil {
			return p.goalManagerAt(ctx, dir)
		}
		return p.ManagerForName(ctx, f.Repo)
	}
	dir := p.goalTreeDir(goal, f.Repo)
	if _, statErr := os.Stat(dir); statErr == nil {
		return p.goalManagerAt(ctx, dir)
	}
	if goal.Stage == domain.StageDone {
		// the goal is over and its trees are cleaned up: the card belongs
		// to its own repository again, which is where its branch is
		return p.ManagerForName(ctx, f.Repo)
	}
	return nil, fmt.Errorf("%s belongs to %s: %w (%s)", f.ID, f.GoalID, ErrGoalWorktreeMissing, dir)
}

// GoalManager returns the manager a goal's cards in its home repo resolve
// to: rooted at the goal's home tree, which must exist.
func (p *Pool) GoalManager(ctx context.Context, goal *domain.Feature) (*Manager, error) {
	return p.GoalManagerIn(ctx, goal, goal.Repo)
}

// GoalManagerIn returns the manager a goal's cards in repo resolve to:
// rooted at the goal's tree there, which must exist.
func (p *Pool) GoalManagerIn(ctx context.Context, goal *domain.Feature, repo string) (*Manager, error) {
	if !goal.IsGoal() {
		return nil, fmt.Errorf("%s is not a goal", goal.ID)
	}
	dir := p.goalTreeDir(*goal, repo)
	if _, err := os.Stat(dir); err != nil {
		return nil, fmt.Errorf("%s: %w (%s)", goal.ID, ErrGoalWorktreeMissing, dir)
	}
	return p.goalManagerAt(ctx, dir)
}

// GoalTree is one goal's branch in one repository: the repository's
// manager, the goal worktree there, and whether it is the goal's home.
// Every goal verb that used to hang off the repository's Manager is a
// method here instead, because "which tree" is the question a goal that
// spans repositories has to answer and a Manager alone cannot.
type GoalTree struct {
	Repo string // the configured repo name ("" = the workspace default)
	Home bool   // the goal card's own repository
	Dir  string // the goal worktree in this repository
	mgr  *Manager
	goal domain.Feature
}

// Manager is the repository's manager — the one whose main checkout the
// goal branch lands on, not the one rooted at the tree.
func (t GoalTree) Manager() *Manager { return t.mgr }

// Branch is the goal's branch name in this repository. Every repo spells
// it the same: a branch name collides only within one repository.
func (t GoalTree) Branch() string { return t.goal.BranchName() }

// Exists reports whether the tree is on disk.
func (t GoalTree) Exists() bool {
	_, err := os.Stat(t.Dir)
	return err == nil
}

// Label names the tree in a sentence a person reads: the repo name, or
// "the goal branch" when there is only the default repository to mean.
func (t GoalTree) Label() string {
	if t.Repo == "" {
		return "the goal branch"
	}
	return "the goal branch in " + t.Repo
}

func (t GoalTree) requireDir() (string, error) {
	if _, err := os.Stat(t.Dir); err != nil {
		return "", fmt.Errorf("%s: %w (%s)", t.goal.ID, ErrGoalWorktreeMissing, t.Dir)
	}
	return t.Dir, nil
}

// GoalTree resolves the goal's tree in repo, whether or not it is on disk.
func (p *Pool) GoalTree(goal *domain.Feature, repo string) (GoalTree, error) {
	if !goal.IsGoal() {
		return GoalTree{}, fmt.Errorf("%s is not a goal", goal.ID)
	}
	return GoalTree{
		Repo: repo, Home: repo == goal.Repo, Dir: p.goalTreeDir(*goal, repo),
		goal: *goal,
	}, nil
}

// GoalTrees resolves a tree for every repository the goal touches: its
// home repo first, then the ones named (its cards'), then any repo whose
// goal tree is already on disk. That last source is not belt and braces:
// a repository whose cards have all landed and left still carries their
// commits on its goal branch, and a landing that forgot it would land a
// partial goal as if it were the whole one.
//
// A repo whose manager cannot be built (an unconfigured name stored on a
// card) fails the call for the same reason: a goal that cannot reach one
// of its repositories cannot honestly be caught up or landed.
func (p *Pool) GoalTrees(ctx context.Context, goal *domain.Feature, repos []string) ([]GoalTree, error) {
	ordered := []string{goal.Repo}
	seen := map[string]bool{goal.Repo: true}
	for _, r := range append(append([]string(nil), repos...), p.goalTreeRepos(*goal)...) {
		if !seen[r] {
			seen[r] = true
			ordered = append(ordered, r)
		}
	}
	sort.Strings(ordered[1:])
	out := make([]GoalTree, 0, len(ordered))
	for _, repo := range ordered {
		t, err := p.GoalTree(goal, repo)
		if err != nil {
			return nil, err
		}
		if t.mgr, err = p.ManagerForName(ctx, repo); err != nil {
			return nil, fmt.Errorf("%s in %s: %w", goal.ID, repoLabel(repo), err)
		}
		out = append(out, t)
	}
	return out, nil
}

// goalTreeRepos reads the repositories a goal has a tree for off disk, by
// the id@repo spelling of the directories beside its home tree. The home
// tree is not among them: its directory is the bare id, which says nothing
// about which repo it is in.
func (p *Pool) goalTreeRepos(goal domain.Feature) []string {
	entries, err := os.ReadDir(filepath.Join(p.root, ".gummi", "worktrees"))
	if err != nil {
		return nil
	}
	prefix := string(goal.ID) + "@"
	var out []string
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		repo := strings.TrimPrefix(e.Name(), prefix)
		if repo == "default" {
			repo = ""
		}
		out = append(out, repo)
	}
	return out
}

// EnsureGoalTree returns the goal's tree in repo, cutting it if it is not
// there yet: the goal card's own worktree in its home repo, a worktree on
// a branch of the same name anywhere else. It is idempotent, so the
// conductor can call it whenever a card of a repository the goal has not
// touched before appears.
func (p *Pool) EnsureGoalTree(ctx context.Context, goal *domain.Feature, repo string) (GoalTree, error) {
	t, err := p.GoalTree(goal, repo)
	if err != nil {
		return GoalTree{}, err
	}
	if t.mgr, err = p.ManagerForName(ctx, repo); err != nil {
		return GoalTree{}, fmt.Errorf("%s in %s: %w", goal.ID, repoLabel(repo), err)
	}
	if t.Exists() {
		return t, nil
	}
	if t.Home {
		if _, err := t.mgr.Ensure(ctx, goal); err != nil {
			return GoalTree{}, err
		}
		return t, nil
	}
	if _, err := t.mgr.CreateGoalTree(ctx, goal, goalTreeName(*goal, repo)); err != nil {
		return GoalTree{}, fmt.Errorf("%s in %s: %w", goal.ID, repoLabel(repo), err)
	}
	return t, nil
}

// repoLabel names a repo in an error: the configured name, or the
// workspace default when there is no name to give.
func repoLabel(repo string) string {
	if repo == "" {
		return "the default repository"
	}
	return "repository " + repo
}

// CreateGoalTree cuts a goal's worktree in this repository at the named
// directory, on the goal's branch. It is Create for a tree that is not a
// card's own: no fork point is stamped (the goal's belongs to its home
// repo, where its card's row lives) and an existing branch is checked out
// rather than refused, so a tree removed by hand comes back with its
// landed cards still on it.
func (m *Manager) CreateGoalTree(ctx context.Context, goal *domain.Feature, name string) (string, error) {
	if !goal.IsGoal() {
		return "", fmt.Errorf("%s is not a goal", goal.ID)
	}
	p, err := m.cardTreePathNamed(m.worktreesDir(), goal, name)
	if err != nil {
		return "", err
	}
	branch := goal.BranchName()
	if _, err := os.Stat(p); err == nil {
		return p, nil
	}
	if _, err := runGit(ctx, m.repo, "rev-parse", "--verify", "HEAD"); err != nil {
		return "", fmt.Errorf("repository has no commits yet; commit something before creating a goal worktree: %w", err)
	}
	if err := os.MkdirAll(m.worktreesDir(), 0o750); err != nil {
		return "", err
	}
	have, err := gitOK(ctx, m.repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	args := []string{"worktree", "add"}
	if have {
		args = append(args, "--", p, branch)
	} else {
		head, herr := m.MainHead(ctx)
		if herr != nil {
			return "", herr
		}
		args = append(args, "-b", branch, "--", p, head)
	}
	if _, err := runGit(ctx, m.repo, args...); err != nil {
		return "", err
	}
	if err := untrackGummiInWorktree(ctx, m.wsRoot, m.repo, p); err != nil {
		if _, rmErr := runGit(ctx, m.repo, "worktree", "remove", "--force", "--", p); rmErr == nil && !have {
			_, _ = runGit(ctx, m.repo, "branch", "-D", "--", branch)
		}
		return "", fmt.Errorf("untracking .gummi in new goal worktree: %w", err)
	}
	return p, nil
}

// RemoveGoalTree removes a goal's tree in this repository, and the branch
// with it when the branch carries nothing main does not already have. It
// is how a goal leaves a repository it turned out not to need — the
// provisional home a goal is minted with, before its plan says where its
// work actually is.
func (m *Manager) RemoveGoalTree(ctx context.Context, goal *domain.Feature, name string) error {
	p, err := m.cardTreePathNamed(m.worktreesDir(), goal, name)
	if err != nil {
		return err
	}
	branch := goal.BranchName()
	if _, statErr := os.Stat(p); statErr == nil {
		if _, err := runGit(ctx, m.repo, "worktree", "remove", "--force", "--", p); err != nil {
			return err
		}
	}
	if ok, err := gitOK(ctx, m.repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err != nil || !ok {
		return err
	}
	// -d, never -D: a branch with commits of its own is work, and work is
	// never what this is for. Git refusing it is the right outcome, and it
	// is reported as its own kind of error so a caller that is cleaning up
	// (Pool.removeGoalTrees) can leave the branch and carry on, while one
	// that is re-homing a goal (engine.settleGoalHome) fails loudly.
	if _, err := runGit(ctx, m.repo, "branch", "-d", "--", branch); err != nil {
		return &unmergedBranchError{Branch: branch, ID: goal.ID, err: err}
	}
	return m.forkStore.ClearForkPoint(ctx, goal.ID)
}

// unmergedBranchError reports a branch git would not delete because it
// carries commits nothing else has.
type unmergedBranchError struct {
	Branch string
	ID     domain.FeatureID
	err    error
}

func (e *unmergedBranchError) Error() string {
	return fmt.Sprintf("%s still has the branch %s, which carries work nothing else does: %v", e.ID, e.Branch, e.err)
}

func (e *unmergedBranchError) Unwrap() error { return e.err }

// goalManagerAt returns the cached manager rooted at a goal worktree. It
// never runs the .gummi exclusion pass: the goal worktree is a linked
// worktree of an already-excluded repository, and its creation already
// untracked .gummi on the goal branch.
func (p *Pool) goalManagerAt(ctx context.Context, dir string) (*Manager, error) {
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

// Landed reports whether this repository's goal branch is already an
// ancestor of its main — whether the goal has landed here. It is what
// makes a second landing attempt safe after one repository's merge
// succeeded and the next one failed: the repos that are done are skipped
// rather than merged again.
func (t GoalTree) Landed(ctx context.Context) (bool, error) {
	branch := t.Branch()
	if ok, err := gitOK(ctx, t.mgr.repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err != nil || !ok {
		return false, err
	}
	return gitOK(ctx, t.mgr.repo, "merge-base", "--is-ancestor", branch, "HEAD")
}

// Merge lands this repository's goal branch on its main checkout as one
// merge commit (--no-ff) carrying message, keeping every card commit on
// the goal branch beneath it: `git log --first-parent` then reads one line
// per goal, and a single card can still be reverted by its own commit. It
// refuses the same things SquashMerge refuses and undoes a conflicted
// merge.
//
// The home tree also carries the goal card's own bookkeeping — the
// fork-drift assertion and the landed commit — because that is the
// repository the goal card's row is in. The other repositories have no row
// of their own to stamp, and their goal branch was never anchored to a
// recorded fork point, so asserting against one would refuse a perfectly
// good landing.
func (t GoalTree) Merge(ctx context.Context, message string) (string, error) {
	goal := &t.goal
	m := t.mgr
	if !goal.IsGoal() {
		return "", fmt.Errorf("refusing goal merge of %s: not a goal", goal.ID)
	}
	if strings.TrimSpace(message) == "" {
		return "", fmt.Errorf("refusing goal merge of %s: empty commit message", goal.ID)
	}
	m.mainMu.Lock()
	defer m.mainMu.Unlock()
	branch := t.Branch()
	if ok, err := gitOK(ctx, m.repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err != nil {
		return "", err
	} else if !ok {
		return "", fmt.Errorf("goal %s has no branch %s in %s", goal.ID, branch, repoLabel(t.Repo))
	}
	if dirty, err := m.MainTrackedDirty(ctx); err != nil {
		return "", err
	} else if dirty {
		return "", fmt.Errorf("the main checkout of %s has uncommitted changes — commit or stash them before landing the goal", repoLabel(t.Repo))
	}
	if t.Home {
		if err := m.AssertNoForkDrift(ctx, goal); err != nil {
			return "", err
		}
	}
	base, err := runGit(ctx, m.repo, "merge-base", "HEAD", branch)
	if err != nil {
		return "", err
	}
	if n, err := runGit(ctx, m.repo, "rev-list", "--count", base+".."+branch); err != nil {
		return "", err
	} else if n == "0" {
		return "", fmt.Errorf("goal branch %s has nothing to land in %s", branch, repoLabel(t.Repo))
	}
	if _, err := runGit(ctx, m.repo, "merge", "--no-ff", "-m", message, branch); err != nil {
		conflicts := m.conflictedFiles(ctx, m.repo)
		if _, abortErr := runGit(ctx, m.repo, "merge", "--abort"); abortErr != nil {
			return "", fmt.Errorf("goal merge failed AND abort failed, the main checkout of %s needs manual attention: %w (abort: %v)", repoLabel(t.Repo), err, abortErr)
		}
		if len(conflicts) > 0 {
			return "", &MergeConflictError{Files: conflicts}
		}
		return "", err
	}
	sha, err := runGit(ctx, m.repo, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	if t.Home {
		if err := m.forkStore.SetLandedSHA(ctx, goal.ID, sha); err != nil {
			return "", fmt.Errorf("recording landed commit for %s: %w", goal.ID, err)
		}
	}
	return sha, nil
}

// CatchUpConflictError reports that merging main into a goal branch stopped
// on conflicts. Open says whether the merge was left in progress in the
// goal worktree for someone to resolve (CatchUpGoal with keepConflict), or
// aborted with the goal worktree clean.
type CatchUpConflictError struct {
	Files []string
	Open  bool
}

func (e *CatchUpConflictError) Error() string {
	state := "aborted, goal worktree clean"
	if e.Open {
		state = "left open for resolution"
	}
	if len(e.Files) == 0 {
		return "catching the goal branch up with main hit conflicts — " + state
	}
	return "catching the goal branch up with main conflicts in " + strings.Join(e.Files, ", ") + " — " + state
}

// Behind reports whether this repository's main HEAD is missing from its
// goal branch — whether a catch-up has anything to bring in.
func (t GoalTree) Behind(ctx context.Context) (bool, error) {
	p, err := t.requireDir()
	if err != nil {
		return false, err
	}
	mainHead, err := t.mgr.MainHead(ctx)
	if err != nil {
		return false, err
	}
	in, err := gitOK(ctx, p, "merge-base", "--is-ancestor", mainHead, "HEAD")
	if err != nil {
		return false, err
	}
	return !in, nil
}

// CatchUpGoal merges main's current HEAD into the goal branch, inside the
// goal worktree, and reports whether anything was merged. A merge rather
// than a rebase on purpose: every card still running on the goal forked
// from some goal-branch commit, and a merge keeps each of those an
// ancestor, so no card's recorded fork point drifts. On conflicts the
// merge is aborted (goal worktree clean) unless keepConflict, which leaves
// it open for a resolver and reports Open.
func (t GoalTree) CatchUp(ctx context.Context, keepConflict bool) (bool, error) {
	p, err := t.requireDir()
	if err != nil {
		return false, err
	}
	m := t.mgr
	behind, err := t.Behind(ctx)
	if err != nil || !behind {
		return false, err
	}
	if dirty, err := trackedDirtyIn(ctx, p); err != nil {
		return false, err
	} else if dirty {
		return false, fmt.Errorf("goal worktree %s has uncommitted changes; refusing to catch up", p)
	}
	mainHead, err := m.MainHead(ctx)
	if err != nil {
		return false, err
	}
	msg := fmt.Sprintf("Merge main into %s", t.goal.ID)
	if _, err := runGit(ctx, p, "merge", "--no-ff", "-m", msg, mainHead); err != nil {
		conflicts := m.conflictedFiles(ctx, p)
		if !m.mergeInProgress(ctx, p) {
			return false, fmt.Errorf("catching %s up with main did not start: %w", t.goal.ID, err)
		}
		if keepConflict {
			return false, &CatchUpConflictError{Files: conflicts, Open: true}
		}
		if _, abortErr := runGit(ctx, p, "merge", "--abort"); abortErr != nil {
			return false, fmt.Errorf("catch-up failed AND abort failed, goal worktree %s needs manual attention: %w (abort: %v)", p, err, abortErr)
		}
		return false, &CatchUpConflictError{Files: conflicts}
	}
	return true, nil
}

// MergeInProgress reports whether a catch-up merge is open in this
// repository's goal worktree.
func (t GoalTree) MergeInProgress(ctx context.Context) (bool, error) {
	p, err := t.requireDir()
	if err != nil {
		return false, err
	}
	return t.mgr.mergeInProgress(ctx, p), nil
}

// AbortMerge aborts an open catch-up merge; a no-op reporting false when
// none is open.
func (t GoalTree) AbortMerge(ctx context.Context) (bool, error) {
	p, err := t.requireDir()
	if err != nil {
		return false, err
	}
	if !t.mgr.mergeInProgress(ctx, p) {
		return false, nil
	}
	if _, err := runGit(ctx, p, "merge", "--abort"); err != nil {
		return false, err
	}
	return true, nil
}

// ConcludeMerge commits an open catch-up merge once every conflict is
// resolved. It refuses while any path is still unmerged, so a resolver
// that stopped halfway can never have its half-resolution committed.
func (t GoalTree) ConcludeMerge(ctx context.Context) error {
	p, err := t.requireDir()
	if err != nil {
		return err
	}
	if !t.mgr.mergeInProgress(ctx, p) {
		return nil
	}
	if left := t.mgr.conflictedFiles(ctx, p); len(left) > 0 {
		return &CatchUpConflictError{Files: left, Open: true}
	}
	if _, err := runGit(ctx, p, "commit", "--no-edit"); err != nil {
		return fmt.Errorf("concluding the catch-up merge on %s: %w", t.goal.ID, err)
	}
	return nil
}

func (m *Manager) mergeInProgress(ctx context.Context, wt string) bool {
	_, err := runGit(ctx, wt, "rev-parse", "--verify", "--quiet", "MERGE_HEAD")
	return err == nil
}

// RebaseOnto moves a card's own commits from the base it forked from
// (oldBase) onto this manager's HEAD, then re-anchors its fork point
// there. It is how a card changes branches: attaching an existing card to
// a goal moves it from main onto the goal branch (called on the goal's
// manager), and detaching one moves it back (called on the repository's
// manager). `--onto` replays only the commits after oldBase, so a card
// leaving a goal never carries other cards' landings with it. A card with
// no worktree has no commits to move and only its fork point is cleared.
func (m *Manager) RebaseOnto(ctx context.Context, f *domain.Feature, oldBase string) error {
	p, _, err := m.featurePaths(f)
	if err != nil {
		return err
	}
	if _, statErr := os.Stat(p); statErr != nil {
		return m.forkStore.ClearForkPoint(ctx, f.ID)
	}
	// The card's OWN base, not the checkout's HEAD. For a goal card the
	// two are the same thing — its manager is rooted at the goal
	// worktree, so that checkout's HEAD *is* the goal branch — which is
	// why this read as MainHead for as long as goals were the only
	// caller. A stacked card's base is the branch of the card below it,
	// resolved in a manager rooted at the ordinary repository, so
	// targeting the checkout's HEAD would replay it onto main and
	// quietly flatten the chain.
	head, err := m.BaseHead(ctx, f)
	if err != nil {
		return err
	}
	if strings.TrimSpace(oldBase) == "" {
		oldBase, err = runGit(ctx, p, "merge-base", head, "HEAD")
		if err != nil {
			return fmt.Errorf("finding where %s forked: %w", f.ID, err)
		}
	}
	if _, err := runGit(ctx, p, "rebase", "--onto", head, oldBase); err != nil {
		if !m.rebaseInProgress(ctx, p) {
			return fmt.Errorf("moving %s onto its new base did not start: %w", f.ID, err)
		}
		conflicts := m.conflictedFiles(ctx, p)
		if _, abortErr := runGit(ctx, p, "rebase", "--abort"); abortErr != nil {
			return fmt.Errorf("move failed AND abort failed, worktree %s needs manual attention: %w (abort: %v)", p, err, abortErr)
		}
		return &RebaseConflictError{Files: conflicts}
	}
	return m.forkStore.ReanchorForkPoint(ctx, f.ID, head)
}

// CommitStat is `git show --stat` for one commit, without the message
// body: the per-card section header of a goal's diff by card.
func (m *Manager) CommitStat(ctx context.Context, sha string) (string, error) {
	return runGit(ctx, m.repo, "show", "--stat", "--format=%h %s", sha)
}

// CommitPatch is the full patch one commit introduced.
func (m *Manager) CommitPatch(ctx context.Context, sha string) (string, error) {
	return runGit(ctx, m.repo, "show", "--format=%h %s", sha)
}

// WithMainCheckout runs fn in a throwaway detached checkout of the main
// checkout's HEAD, removed afterwards: somewhere a command can be run
// against main without touching the working tree a person uses.
func (m *Manager) WithMainCheckout(ctx context.Context, fn func(dir string) error) error {
	head, err := m.MainHead(ctx)
	if err != nil {
		return err
	}
	parent, err := os.MkdirTemp("", "gummi-main-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(parent) }()
	dir := filepath.Join(parent, "main")
	if _, err := runGit(ctx, m.repo, "worktree", "add", "--detach", dir, head); err != nil {
		return fmt.Errorf("checking out main: %w", err)
	}
	defer func() {
		_, _ = runGit(context.WithoutCancel(ctx), m.repo, "worktree", "remove", "--force", dir)
		_, _ = runGit(context.WithoutCancel(ctx), m.repo, "worktree", "prune")
	}()
	return fn(dir)
}

// AddDetached checks sha out, detached, at dir — a snapshot of one commit
// of the repository at repoRoot that nothing else will move. An experiment
// deploys from one rather than from a goal tree, because a goal tree is
// what cards land on: a landing under a running deploy would make the run
// evidence about a commit it only half deployed.
func AddDetached(ctx context.Context, repoRoot, dir, sha string) error {
	if _, err := runGit(ctx, repoRoot, "worktree", "add", "--detach", dir, sha); err != nil {
		return fmt.Errorf("checking out %s: %w", sha, err)
	}
	return nil
}

// RemoveDetached removes a snapshot AddDetached made.
func RemoveDetached(ctx context.Context, repoRoot, dir string) {
	_, _ = runGit(context.WithoutCancel(ctx), repoRoot, "worktree", "remove", "--force", dir)
	_, _ = runGit(context.WithoutCancel(ctx), repoRoot, "worktree", "prune")
	_ = os.RemoveAll(dir)
}

// RestoreTracked puts a gummi-owned checkout's tracked files back the way
// its HEAD has them, and reports what it restored.
//
// It exists for the goal tree, which is the one checkout gummi both runs
// commands in and merges into. A goal's checks run there — the baseline
// at plan approval, the goal's own verify later — and a repository whose
// test suite regenerates a tracked file (golden files, generated docs, a
// snapshot keyed on the machine it ran on) leaves that file modified
// afterwards. Nothing in a goal tree is anyone's work: the cards' work is
// on the cards' branches, the goal doc lives outside the tree entirely,
// and the engine's checkpoint commit skips a goal's own worktree on
// purpose — so a modified tracked file there is a side effect of running
// the repository's own commands, and the honest answer is to put it back
// rather than to refuse the landing it would otherwise block.
//
// A tree with a merge, rebase or cherry-pick in progress is left alone
// and reported: those are half-finished operations with an owner, not
// side effects.
func RestoreTracked(ctx context.Context, dir string) ([]string, error) {
	gitDir, err := runGit(ctx, dir, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil, err
	}
	gitDir = strings.TrimSpace(gitDir)
	for _, marker := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply"} {
		if _, err := os.Stat(filepath.Join(gitDir, marker)); err == nil {
			return nil, fmt.Errorf("%s has a %s in progress", dir, strings.ToLower(strings.TrimSuffix(marker, "_HEAD")))
		}
	}
	changed, err := trackedChanges(ctx, dir)
	if err != nil || len(changed) == 0 {
		return nil, err
	}
	if _, err := runGit(ctx, dir, "reset", "-q", "HEAD", "--", "."); err != nil {
		return nil, err
	}
	if _, err := runGit(ctx, dir, "checkout", "-q", "--", "."); err != nil {
		return nil, err
	}
	left, err := trackedChanges(ctx, dir)
	if err != nil {
		return nil, err
	}
	if len(left) > 0 {
		return changed, fmt.Errorf("%s still has %s modified after restoring", dir, strings.Join(left, ", "))
	}
	return changed, nil
}

// trackedChanges names the tracked files a checkout has modified, staged
// or not, ignoring .gummi for the same reason MainTrackedDirty does. It
// reads the diff against HEAD rather than porcelain status because that
// gives bare paths: status prefixes each line with its two-letter code,
// and the leading space of an unstaged change does not survive the
// trimming every command in this package does.
func trackedChanges(ctx context.Context, dir string) ([]string, error) {
	out, err := runGit(ctx, dir, "diff", "--name-only", "HEAD", "--", ":(exclude).gummi")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			paths = append(paths, line)
		}
	}
	return paths, nil
}
