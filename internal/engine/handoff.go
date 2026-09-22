package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/worktree"
)

// HandOff ends a card without landing it: the branch stays exactly where
// it is, and the card moves to done.
//
// It is the third ending a verified card has, beside gummi's own squash
// merge and a landing on GitHub. The work is finished; who merges it is a
// separate question, and hand-off is the person answering "not gummi" —
// they will push the branch, open the PR by hand, cherry-pick two commits
// out of it, or leave it sitting there. Before this existed the only way
// to say that was `delete`, which destroys the branch, so the honest
// answer cost the work.
//
// Three steps, in this order:
//
//  1. Commit whatever is loose in the worktree. The branch IS the
//     deliverable here — nothing downstream is going to squash it onto
//     main and sweep the remainder in — so uncommitted work left behind
//     is work lost to the next `clean` or `delete`. This is the same
//     final checkpoint the merge path takes, for the same reason.
//  2. Stamp HandedOffAt. It is written BEFORE the transition, because it
//     is what gives Advance permission to cross the verify→done gate
//     with a branch still ahead of main (advance.go's fourth skip),
//     rather than a record written afterwards about what happened.
//  3. Advance. Every other floor at that gate still applies — open %%
//     threads, open diff annotations, the bug omission gate, a research
//     card's document floor. A hand-off waives the landing and nothing
//     else, so a card that could not be finalized cannot be handed off
//     out of trouble either; the returned AdvanceResult carries that
//     refusal verbatim for the caller to render.
//
// On a refusal at step 3 the stamp stays. That is deliberate and it is
// the cheapest of the three possible behaviours to reason about: the
// stamp says "this card is not gummi's to land", which remains true while
// the person resolves the blocker, and the next Advance — from the board,
// the CLI, or here — crosses without asking the question again. Landing
// the card after all clears it (ClearHandedOffAt).
func (e *Engine) HandOff(ctx context.Context, id domain.FeatureID, actor string) (AdvanceResult, error) {
	f, err := e.cfg.Store.GetFeature(ctx, id)
	if err != nil {
		return AdvanceResult{}, err
	}
	if f.Stage == domain.StageDone {
		return AdvanceResult{Feature: f, From: f.Stage, Status: StatusNoop}, nil
	}
	if f.Kind == domain.KindResearch {
		return AdvanceResult{}, fmt.Errorf("%s carries no branch — advance it instead", id)
	}

	// A landed branch has already left by the other door. Saying so beats
	// stamping a hand-off onto a card that is on main, which would then
	// badge as handed off forever (nothing re-reads git for a done card).
	//
	// Fork drift is not a refusal here. Landed declines to answer when
	// the base was rewritten past the recorded fork, because the landing
	// path must not trust a bool that may be wrong. A hand-off asks the
	// same question for a weaker reason — only to avoid stamping a card
	// that is already on the base — and a drifted branch is by definition
	// not sitting on it. The person is taking the branch as it is; what
	// the base did since the fork is theirs to reconcile before they push,
	// and the confirm says so. Refusing would offer exactly two exits, a
	// rebase and a delete, neither of which is the verb they chose.
	wt, err := e.mgr(ctx, &f)
	if err != nil {
		return AdvanceResult{}, err
	}
	if landed, err := landedOrDrifted(ctx, wt, &f); err != nil {
		return AdvanceResult{}, err
	} else if landed {
		// the branch it landed on, not the literal "main": a card of a goal
		// lands on the goal branch, and a `master` repo has never been
		// called main.
		return AdvanceResult{}, fmt.Errorf("%s already landed on %s — there is nothing to hand off", id, wt.BaseBranch(ctx))
	}

	if exists, err := wt.Exists(ctx, &f); err != nil {
		return AdvanceResult{}, err
	} else if exists {
		// AsIs: the ordinary checkpoint refuses on fork drift, to keep a
		// landing coherent. Nothing lands here, and the loose work would
		// otherwise be lost with the branch the person is about to take.
		if _, err := wt.CommitAllAsIs(ctx, &f, string(id)+": final checkpoint"); err != nil {
			return AdvanceResult{}, err
		}
	}

	now := time.Now().UTC()
	if err := e.cfg.Store.SetHandedOffAt(ctx, id, now); err != nil {
		return AdvanceResult{}, err
	}

	res, err := e.Advance(ctx, id, actor)
	if err != nil {
		return res, err
	}
	res.Feature.HandedOffAt = now
	return res, nil
}

// landedOrDrifted is Landed with fork drift read as "not landed": the
// answer a hand-off needs, since a branch whose fork the base no longer
// carries cannot be sitting on that base. Every other error is the
// caller's to report.
func landedOrDrifted(ctx context.Context, wt *worktree.Manager, f *domain.Feature) (bool, error) {
	landed, err := wt.Landed(ctx, f)
	var drift *worktree.ForkDriftError
	if errors.As(err, &drift) {
		return false, nil
	}
	return landed, err
}
