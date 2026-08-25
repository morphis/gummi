package worktree

import (
	"context"
	"fmt"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// ResolveCollapseBase resolves the base commit a collapse should reset onto:
// the fork point of f's branch with its parent card's branch when a
// dependency edge names one (FD-058..062), else the fork point with the
// repository's local main. A card with more than one declared parent has no
// single unambiguous base, so it is a hard error rather than an arbitrary
// pick; a named parent whose branch cannot be resolved is likewise a hard
// error rather than a silent fallback to main.
func ResolveCollapseBase(ctx context.Context, store *state.Store, mgr *Manager, f *domain.Feature) (string, error) {
	deps, err := store.ListDependencies(ctx, f.ID)
	if err != nil {
		return "", fmt.Errorf("resolving collapse base for %s: %w", f.ID, err)
	}
	switch len(deps) {
	case 0:
		sha, err := runGit(ctx, mgr.RepoRoot(), "merge-base", "HEAD", f.BranchName())
		if err != nil {
			return "", fmt.Errorf("resolving collapse base for %s: fork point with main: %w", f.ID, err)
		}
		return sha, nil
	case 1:
		parent, err := store.GetFeature(ctx, deps[0])
		if err != nil {
			return "", fmt.Errorf("resolving collapse base for %s: parent %s: %w", f.ID, deps[0], err)
		}
		if _, err := runGit(ctx, mgr.RepoRoot(), "rev-parse", "--verify", "--quiet", "refs/heads/"+parent.BranchName()); err != nil {
			return "", fmt.Errorf("resolving collapse base for %s: parent %s branch %s: %w", f.ID, parent.ID, parent.BranchName(), err)
		}
		sha, err := runGit(ctx, mgr.RepoRoot(), "merge-base", parent.BranchName(), f.BranchName())
		if err != nil {
			return "", fmt.Errorf("resolving collapse base for %s: fork point with parent %s: %w", f.ID, parent.ID, err)
		}
		return sha, nil
	default:
		return "", fmt.Errorf("cannot resolve collapse base for %s: card has %d dependencies", f.ID, len(deps))
	}
}
