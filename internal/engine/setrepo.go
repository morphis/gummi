package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/morphis/gummi/internal/domain"
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
	// The repo is locked once the card has a worktree: the tree is cut
	// from that repo, so moving the card would orphan it. Under one
	// worktree per card that happens at the card's first stage run, so
	// the lock is keyed on the tree itself rather than on a stage list
	// that used to stand in for it.
	// The repo is fixed once the card's tree is cut from it — moving the
	// card then would orphan the tree. Under one worktree per card that
	// happens at the first stage run, so the tree on disk is the primary
	// test; an unresolvable repo cannot have one, so a lookup error reads
	// as "no tree" rather than failing the move.
	//
	// A card past its design gate is locked whether or not a tree is
	// currently on disk: it has committed work to find, and a tree that
	// went missing is a thing to recover, not a licence to re-home the
	// card into a different repository.
	locked := f.Kind != domain.KindResearch &&
		(f.Stage == domain.StageImplement || f.Stage == domain.StageVerify || f.Stage == domain.StageDone)
	if !locked {
		if ok, err := e.pool.Exists(ctx, &f); err == nil && ok {
			locked = true
		}
	}
	if locked {
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
