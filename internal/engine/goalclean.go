package engine

import (
	"context"
	"fmt"

	"github.com/morphis/gummi/internal/domain"
)

// GoalCardCleanup is what one sweep of a goal's cards took and what it
// left standing, by card id. Left is not a failure: a card holding work
// no merge has is never a cleanup's to discard, and a goal whose sweep
// stopped dead on one such card would leave every other checkout behind
// with nothing said about why.
type GoalCardCleanup struct {
	Took []domain.FeatureID
	Left []domain.FeatureID
}

// CleanGoalCards removes the worktrees and merged branches of the cards a
// goal landed, and reports what it took.
//
// It exists because a goal's cards have no other moment. Such a card
// resolves to a manager rooted at the GOAL's worktree, which is what puts
// its branch on the goal branch (worktree.Pool.ManagerFor) — so once the
// goal's trees come out, its cards' checkouts go with them and their
// branches read as unlanded forever after, measured against a trunk that
// never took their commits. And while the goal runs, the cards are the
// goal's to clean rather than a reader's (ui.featureRow.conducted): the
// person watching a landed card of a running goal is not offered the
// verb. Both halves point here, at the goal's own cleanup, just before it
// removes the trees.
//
// Only a card with a landing stamp is touched. A dropped or handed-off
// card wears the goal's id too and keeps work no merge has; its branch
// stays, and its checkout goes when the tree around it does.
func (e *Engine) CleanGoalCards(ctx context.Context, goalID domain.FeatureID) (GoalCardCleanup, error) {
	var out GoalCardCleanup
	if e.cfg.Store == nil || e.pool == nil {
		return out, nil
	}
	goal, err := e.cfg.Store.GetFeature(ctx, goalID)
	if err != nil {
		return out, err
	}
	if !goal.IsGoal() {
		return out, fmt.Errorf("%s is not a goal", goalID)
	}
	feats, err := e.cfg.Store.ListFeatures(ctx)
	if err != nil {
		return out, err
	}
	for _, c := range feats {
		if c.GoalID != goal.ID || c.LandedSHA == "" {
			continue
		}
		if e.cleanGoalCard(ctx, &c) {
			out.Took = append(out.Took, c.ID)
		} else {
			out.Left = append(out.Left, c.ID)
		}
	}
	return out, nil
}

// cleanGoalCard is one card's half of the sweep: its checkout and then
// its branch, each skipped rather than forced when it holds something the
// goal branch does not have. It answers whether the card came out whole.
func (e *Engine) cleanGoalCard(ctx context.Context, c *domain.Feature) bool {
	if ok, err := e.pool.Exists(ctx, c); err != nil {
		return false
	} else if ok {
		// the same rule the card's own cleanup follows: modified tracked
		// files are rework the landed commit does not carry, so the
		// checkout stays and its branch with it.
		if dirty, err := e.pool.TrackedDirty(ctx, c); err != nil || dirty {
			return false
		}
		if err := e.pool.Remove(ctx, c, true); err != nil {
			return false
		}
	}
	if ok, err := e.pool.BranchExists(ctx, c); err != nil {
		return false
	} else if ok {
		// DeleteLandedBranch, not a force: it re-verifies the landing with
		// the merge-tree content check before it forces anything, which is
		// what makes a squash-merged branch safe to delete.
		if err := e.pool.DeleteLandedBranch(ctx, c); err != nil {
			return false
		}
	}
	return true
}
