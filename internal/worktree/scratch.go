package worktree

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/morphis/gummi/internal/domain"
)

// Research stages run in a scratch tree. Every OTHER stage of every other
// kind now runs in the card's own branch worktree, from its first stage
// run to its last (Ensure, below) — but a research branch never receives
// a commit (DESIGN decision 5), so a branch worktree would break the
// merge/clean/rebase/done assumptions built on one. Research keeps the
// detached tree instead.
//
// A card's pre-worktree stages used to run in the main checkout, fenced
// only by a prompt asking the model not to write there. That is a request, not a
// boundary: an agent running ordinary build commands while exploring is
// enough to dirty main (Go rewriting go.sum was observed parking a card
// at spec having produced nothing), and a weaker model asked politely
// simply writes the feature's files into the operator's tree.
//
// A scratch tree is that boundary. It is a detached checkout of main's
// HEAD at .gummi/scratch/<ID>: a real filesystem cage every backend
// already knows how to enforce (each adapter cages its file tools to
// opts.WorkDir), sitting outside the main checkout the tripwire watches.
// It is deliberately NOT a branch worktree — nothing committed in it can
// become the card's work, and it never collides with the card's own
// gummi/<ID>-slug branch. Its edits are discarded when the card's real
// worktree is cut; the design artifact under .gummi/ is the only thing
// that crosses the hand-off, and the agent reaches that through gummi's
// spec tools rather than the filesystem.

// scratchDir is the directory every card's scratch tree lives in. It is a
// sibling of worktreesDir, never inside it: List and every
// .gummi/worktrees-prefixed check must keep seeing branch worktrees only.
func (m *Manager) scratchDir() string {
	return filepath.Join(m.wsRoot, ".gummi", "scratch")
}

// ScratchPath returns the absolute scratch-tree path for a (valid)
// feature, whether or not it exists on disk.
func (m *Manager) ScratchPath(f *domain.Feature) (string, error) {
	return m.cardTreePath(m.scratchDir(), f)
}

// ScratchExists reports whether the feature's scratch tree is present.
func (m *Manager) ScratchExists(f *domain.Feature) (bool, error) {
	p, err := m.ScratchPath(f)
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

// EnsureScratch returns the feature's scratch tree, creating it at main's
// current HEAD on first use. It is idempotent: one tree per card, reused
// across every pre-worktree stage, so a card's design chats share a
// working directory the way its work stages share a worktree.
//
// The checkout is detached (`worktree add --detach`), so nothing done in
// it can land on a branch, and no branch name is consumed.
func (m *Manager) EnsureScratch(ctx context.Context, f *domain.Feature) (string, error) {
	p, err := m.ScratchPath(f)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(p); err == nil {
		return p, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if _, err := runGit(ctx, m.repo, "rev-parse", "--verify", "HEAD"); err != nil {
		return "", fmt.Errorf("repository has no commits yet; commit something before starting a design stage: %w", err)
	}
	if err := os.MkdirAll(m.scratchDir(), 0o750); err != nil {
		return "", err
	}
	add := func() error {
		_, err := runGit(ctx, m.repo, "worktree", "add", "--detach", "--", p, "HEAD")
		return err
	}
	if err := add(); err != nil {
		// A directory removed out of band leaves admin metadata that makes
		// git refuse a fresh checkout at the same path, and would keep
		// refusing forever. Prune and retry once — on the failure path
		// only, since this runs at the start of every design stage and a
		// prune on the common path is work for a case that almost never
		// holds.
		if _, perr := runGit(ctx, m.repo, "worktree", "prune"); perr != nil {
			return "", err
		}
		if err := add(); err != nil {
			return "", err
		}
	}
	// Same reason as Create: the checkout tracks whatever HEAD carries,
	// including .gummi content the launch untracking only removed from
	// main's index. On a detached HEAD the untrack commit is a throwaway
	// the tree takes with it when it is discarded.
	if err := untrackGummiInWorktree(ctx, m.wsRoot, m.repo, p); err != nil {
		_ = m.RemoveScratch(ctx, f)
		return "", fmt.Errorf("untracking .gummi in scratch tree: %w", err)
	}
	return p, nil
}

// RemoveScratch discards the feature's scratch tree and everything in it.
// Discarding is the contract, not a side effect: a design stage's edits
// must never silently become the card's work, so the hand-off to the real
// worktree (and any card teardown) drops the tree outright. Absent is
// success, so callers can invoke it unconditionally.
func (m *Manager) RemoveScratch(ctx context.Context, f *domain.Feature) error {
	p, err := m.ScratchPath(f)
	if err != nil {
		return err
	}
	if _, err := os.Stat(p); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if _, err := runGit(ctx, m.repo, "worktree", "remove", "--force", "--", p); err == nil {
		return nil
	}
	// git refused (unregistered path, corrupt admin metadata). The tree is
	// disposable by construction, so take the directory directly and let
	// prune reconcile git's view — leaving it behind would block the next
	// EnsureScratch for this card forever.
	if err := os.RemoveAll(p); err != nil {
		return err
	}
	_, err = runGit(ctx, m.repo, "worktree", "prune")
	return err
}

// Ensure returns the card's branch worktree, creating it on first use.
//
// This is the whole of the one-worktree-per-card rule: a card gets one
// directory and keeps it, from its first stage run to the day it lands.
// There is no scratch tree for a feature or a bug any more, and so no
// hand-off between two trees to get wrong — a design stage that writes a
// spike writes it on the branch implement will continue, and an
// implement → plan bounce is a stage change and nothing else.
//
// Allocation is lazy, at the first stage RUN rather than at card
// creation, so a backlog of todo cards is not a backlog of checkouts.
// It is idempotent: an existing tree is returned as-is.
func (m *Manager) Ensure(ctx context.Context, f *domain.Feature) (string, error) {
	p, _, err := m.featurePaths(f)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(p); err == nil {
		return p, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	return m.Create(ctx, f)
}
