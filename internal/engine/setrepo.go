package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/workflow"
)

// ErrRepoLocked is returned by SetRepo when the card has already entered
// a worktree-cutting stage: its repo is fixed once a branch/worktree
// exists.
var ErrRepoLocked = errors.New("repo is fixed once a worktree exists")

// SetRepo changes the managed repository for a card that has not yet cut a
// worktree. It validates the target against the configured repo set and
// persists the change. An unselectable repo or a card past the pre-worktree
// stages returns an error and leaves the stored card unchanged.
func (e *Engine) SetRepo(ctx context.Context, id domain.FeatureID, repo string) (domain.Feature, error) {
	f, err := e.cfg.Store.GetFeature(ctx, id)
	if err != nil {
		return domain.Feature{}, err
	}
	if workflow.NeedsWorktree(f.Kind, f.Stage) {
		return domain.Feature{}, fmt.Errorf("%s: %w", id, ErrRepoLocked)
	}
	if err := e.requireRepo(repo); err != nil {
		return domain.Feature{}, err
	}
	if repo == f.Repo {
		return f, nil
	}
	f.Repo = repo
	f.UpdatedAt = e.now()
	if err := e.cfg.Store.UpdateFeature(ctx, &f); err != nil {
		return domain.Feature{}, err
	}
	return f, nil
}
