package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
// The goal itself is an ordinary card of the default (or named) repo. It
// lands on main with MergeGoal — a merge commit joining its cards' commits
// — and catches up with main with CatchUpGoal, a merge of main into the
// goal branch that keeps every in-flight card's recorded fork point an
// ancestor of the goal branch.

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

// goalWorktreeDir is where goal id's worktree lives.
func (p *Pool) goalWorktreeDir(id domain.FeatureID) string {
	return filepath.Join(p.root, ".gummi", "worktrees", string(id))
}

// managerForGoalCard resolves a card that belongs to a goal: the manager
// rooted at the goal's worktree while it exists; the goal repository's
// manager once the goal has ended and its worktree is gone.
func (p *Pool) managerForGoalCard(ctx context.Context, f *domain.Feature) (*Manager, error) {
	dir := p.goalWorktreeDir(f.GoalID)
	if _, err := os.Stat(dir); err == nil {
		return p.goalManagerAt(ctx, dir)
	}
	p.mu.Lock()
	lookup := p.goalLookup
	p.mu.Unlock()
	if lookup == nil {
		return p.ManagerForName(ctx, f.Repo)
	}
	goal, err := lookup(ctx, f.GoalID)
	if err != nil {
		return nil, fmt.Errorf("resolving goal %s of %s: %w", f.GoalID, f.ID, err)
	}
	if goal.Stage == domain.StageDone {
		return p.ManagerForName(ctx, goal.Repo)
	}
	return nil, fmt.Errorf("%s belongs to %s: %w (%s)", f.ID, f.GoalID, ErrGoalWorktreeMissing, dir)
}

// GoalManager returns the manager a goal's cards resolve to: rooted at the
// goal's worktree, which must exist.
func (p *Pool) GoalManager(ctx context.Context, goal *domain.Feature) (*Manager, error) {
	if !goal.IsGoal() {
		return nil, fmt.Errorf("%s is not a goal", goal.ID)
	}
	dir := p.goalWorktreeDir(goal.ID)
	if _, err := os.Stat(dir); err != nil {
		return nil, fmt.Errorf("%s: %w (%s)", goal.ID, ErrGoalWorktreeMissing, dir)
	}
	return p.goalManagerAt(ctx, dir)
}

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

// MergeGoal lands a goal branch on the main checkout as one merge commit
// (--no-ff) carrying message, keeping every card commit on the goal branch
// beneath it: `git log --first-parent` then reads one line per goal, and a
// single card can still be reverted by its own commit. It refuses the same
// things SquashMerge refuses, undoes a conflicted merge, and records the
// merge commit as the goal's landed commit.
func (m *Manager) MergeGoal(ctx context.Context, goal *domain.Feature, message string) (string, error) {
	if !goal.IsGoal() {
		return "", fmt.Errorf("refusing goal merge of %s: not a goal", goal.ID)
	}
	if strings.TrimSpace(message) == "" {
		return "", fmt.Errorf("refusing goal merge of %s: empty commit message", goal.ID)
	}
	m.mainMu.Lock()
	defer m.mainMu.Unlock()
	_, branch, err := m.featurePaths(goal)
	if err != nil {
		return "", err
	}
	if ok, err := gitOK(ctx, m.repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err != nil {
		return "", err
	} else if !ok {
		return "", fmt.Errorf("goal %s has no branch %s", goal.ID, branch)
	}
	if dirty, err := m.MainTrackedDirty(ctx); err != nil {
		return "", err
	} else if dirty {
		return "", fmt.Errorf("main checkout has uncommitted changes — commit or stash them before landing the goal")
	}
	if err := m.AssertNoForkDrift(ctx, goal); err != nil {
		return "", err
	}
	base, err := runGit(ctx, m.repo, "merge-base", "HEAD", branch)
	if err != nil {
		return "", err
	}
	if n, err := runGit(ctx, m.repo, "rev-list", "--count", base+".."+branch); err != nil {
		return "", err
	} else if n == "0" {
		return "", fmt.Errorf("goal branch %s has nothing to land", branch)
	}
	if _, err := runGit(ctx, m.repo, "merge", "--no-ff", "-m", message, branch); err != nil {
		conflicts := m.conflictedFiles(ctx, m.repo)
		if _, abortErr := runGit(ctx, m.repo, "merge", "--abort"); abortErr != nil {
			return "", fmt.Errorf("goal merge failed AND abort failed, main checkout needs manual attention: %w (abort: %v)", err, abortErr)
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
	if err := m.forkStore.SetLandedSHA(ctx, goal.ID, sha); err != nil {
		return "", fmt.Errorf("recording landed commit for %s: %w", goal.ID, err)
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

// GoalBehindMain reports whether main's HEAD is missing from the goal
// branch — whether a catch-up has anything to bring in.
func (m *Manager) GoalBehindMain(ctx context.Context, goal *domain.Feature) (bool, error) {
	p, err := m.requireWorktree(goal)
	if err != nil {
		return false, err
	}
	mainHead, err := m.MainHead(ctx)
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
func (m *Manager) CatchUpGoal(ctx context.Context, goal *domain.Feature, keepConflict bool) (bool, error) {
	p, err := m.requireWorktree(goal)
	if err != nil {
		return false, err
	}
	behind, err := m.GoalBehindMain(ctx, goal)
	if err != nil || !behind {
		return false, err
	}
	if dirty, err := m.TrackedDirty(ctx, goal); err != nil {
		return false, err
	} else if dirty {
		return false, fmt.Errorf("goal worktree %s has uncommitted changes; refusing to catch up", p)
	}
	mainHead, err := m.MainHead(ctx)
	if err != nil {
		return false, err
	}
	msg := fmt.Sprintf("Merge main into %s", goal.ID)
	if _, err := runGit(ctx, p, "merge", "--no-ff", "-m", msg, mainHead); err != nil {
		conflicts := m.conflictedFiles(ctx, p)
		if !m.mergeInProgress(ctx, p) {
			return false, fmt.Errorf("catching %s up with main did not start: %w", goal.ID, err)
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

// GoalMergeInProgress reports whether a catch-up merge is open in the
// goal worktree.
func (m *Manager) GoalMergeInProgress(ctx context.Context, goal *domain.Feature) (bool, error) {
	p, err := m.requireWorktree(goal)
	if err != nil {
		return false, err
	}
	return m.mergeInProgress(ctx, p), nil
}

// AbortGoalMerge aborts an open catch-up merge; a no-op reporting false
// when none is open.
func (m *Manager) AbortGoalMerge(ctx context.Context, goal *domain.Feature) (bool, error) {
	p, err := m.requireWorktree(goal)
	if err != nil {
		return false, err
	}
	if !m.mergeInProgress(ctx, p) {
		return false, nil
	}
	if _, err := runGit(ctx, p, "merge", "--abort"); err != nil {
		return false, err
	}
	return true, nil
}

// ConcludeGoalMerge commits an open catch-up merge once every conflict is
// resolved. It refuses while any path is still unmerged, so a resolver
// that stopped halfway can never have its half-resolution committed.
func (m *Manager) ConcludeGoalMerge(ctx context.Context, goal *domain.Feature) error {
	p, err := m.requireWorktree(goal)
	if err != nil {
		return err
	}
	if !m.mergeInProgress(ctx, p) {
		return nil
	}
	if left := m.conflictedFiles(ctx, p); len(left) > 0 {
		return &CatchUpConflictError{Files: left, Open: true}
	}
	if _, err := runGit(ctx, p, "commit", "--no-edit"); err != nil {
		return fmt.Errorf("concluding the catch-up merge on %s: %w", goal.ID, err)
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
