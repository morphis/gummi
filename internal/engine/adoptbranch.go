package engine

import (
	"context"
	"fmt"

	"github.com/morphis/gummi/internal/domain"
)

// AdoptInspector returns the callback cardmint asks about a branch a card
// is being minted onto (DESIGN §10 D22): does it exist, and what is on it.
//
// It is shaped exactly like RequireRepo and exists for the same reason —
// cardmint holds no worktree pool and cannot answer a question about git,
// while the engine can. Both are wired at the same place by each caller,
// so a mint either validates both of its git-shaped inputs or neither.
//
// repo and base are the card's own, as the mint is about to record them,
// so the staleness this reports is measured against the branch the card
// will actually land on rather than whatever happens to be checked out.
func (e *Engine) AdoptInspector(ctx context.Context, repo, base string) func(string) (domain.AdoptedWork, error) {
	return func(branch string) (domain.AdoptedWork, error) {
		if e.pool == nil {
			return domain.AdoptedWork{}, fmt.Errorf("no repository is available to adopt %s from", branch)
		}
		return e.pool.InspectBranch(ctx, repo, branch, base)
	}
}
