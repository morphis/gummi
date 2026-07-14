package worktree

import (
	"context"
	"fmt"
)

// MainState is a point-in-time snapshot of the main checkout: its HEAD
// commit, the checked-out branch ref (empty when detached), and its
// working-tree status. The engine records one before dispatching an
// agent turn and compares after, to detect writes that escaped the
// agent's worktree — CWD-pinning cannot stop absolute-path writes to
// the main checkout, which shares the worktrees' filesystem and .git.
//
// .gummi is excluded from the status: it is gummi's own workspace
// (worktrees, state, seq), so churn there is machinery, not escape
// evidence.
type MainState struct {
	Head   string // HEAD commit id
	Branch string // symbolic ref, e.g. refs/heads/main; "" when detached
	Status string // porcelain status of tracked+untracked files, .gummi excluded
}

// Equal reports whether two snapshots describe the same main-checkout
// state.
func (s MainState) Equal(o MainState) bool {
	return s.Head == o.Head && s.Branch == o.Branch && s.Status == o.Status
}

// Clean reports whether the snapshot's working tree had no uncommitted
// changes (outside .gummi).
func (s MainState) Clean() bool { return s.Status == "" }

// MainGen returns the main-checkout mutation generation: it increments
// every time gummi itself mutates the main checkout (a squash merge, an
// escape revert). The escape check compares generations around a turn so
// a sanctioned mutation that lands mid-turn is never misread as an
// agent escape (and never auto-reverted).
func (m *Manager) MainGen() uint64 {
	m.mainMu.Lock()
	defer m.mainMu.Unlock()
	return m.mainGen
}

// BumpMainGen records a sanctioned main-checkout mutation initiated
// outside the Manager's own methods (e.g. the launch-time .gummi
// untracking).
func (m *Manager) BumpMainGen() {
	m.mainMu.Lock()
	defer m.mainMu.Unlock()
	m.mainGen++
}

// MainSnapshot captures the main checkout's current state.
func (m *Manager) MainSnapshot(ctx context.Context) (MainState, error) {
	head, err := runGit(ctx, m.root, "rev-parse", "HEAD")
	if err != nil {
		return MainState{}, err
	}
	// symbolic-ref exits 1 on a detached HEAD; that is a state, not an error.
	branch, _ := runGit(ctx, m.root, "symbolic-ref", "--quiet", "HEAD")
	status, err := runGit(ctx, m.root, "status", "--porcelain", "--", ":(exclude).gummi")
	if err != nil {
		return MainState{}, err
	}
	return MainState{Head: head, Branch: branch, Status: status}, nil
}

// MainChainsFrom reports whether the main checkout's current HEAD is
// base itself or a descendant of it — i.e. whatever moved HEAD did so by
// adding commits on top of base, not by rewriting or switching history.
func (m *Manager) MainChainsFrom(ctx context.Context, base string) (bool, error) {
	return gitOK(ctx, m.root, "merge-base", "--is-ancestor", base, "HEAD")
}

// RestoreMain reverts the main checkout to a snapshot: reset --hard to
// its HEAD, then remove untracked files (sparing .gummi — the state DB
// and worktrees live there). It refuses when the mutation generation has
// moved past expectGen, meaning gummi itself sanctioned a main-checkout
// change (e.g. a squash merge) since the snapshot was taken — reverting
// would then destroy legitimate work. The caller must only invoke this
// when the snapshot was clean, so every untracked file is the escape's.
func (m *Manager) RestoreMain(ctx context.Context, to MainState, expectGen uint64) error {
	m.mainMu.Lock()
	defer m.mainMu.Unlock()
	if m.mainGen != expectGen {
		return fmt.Errorf("main checkout changed under gummi's control since the snapshot; refusing revert")
	}
	if !to.Clean() {
		return fmt.Errorf("refusing to revert main checkout: snapshot was not clean")
	}
	if _, err := runGit(ctx, m.root, "reset", "--hard", to.Head); err != nil {
		return err
	}
	if _, err := runGit(ctx, m.root, "clean", "-f", "-d", "-e", "/.gummi"); err != nil {
		return err
	}
	m.mainGen++
	return nil
}
