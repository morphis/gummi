package engine

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// Adopt takes a dropped card back onto the open board with its work kept.
//
// A drop is the one ending nobody chose. The goal decided it — stuck and
// not recoverable, wrapping up, waiting on something else that was
// dropped — and until now that decision was final in one direction only:
// an ATTACHED card went back to the board automatically when its goal let
// it go, while a card the goal had created was closed inside the goal
// with thirteen good commits on a branch and no way back to it. "The goal
// gave up" should never mean "and so must you".
//
// It is the inverse of the drop, step for step:
//
//  1. Reopen the stage the close took the card out of, read from the
//     closing transition rather than guessed (state.ReopenGoalDropped).
//     `done` stays terminal in the workflow — the drop did not walk the
//     graph on the way in, and this does not add an edge to it.
//  2. Take it out of the goal and move its branch onto main, which is
//     exactly what an attached card's release already does (goalDetach).
//     The branch keeps everything it had written; a failed rebase is
//     reported in the detach line rather than failing the adoption, since
//     a card on the board with a branch to rebase is strictly better than
//     a card still inside a goal that is finished with it.
//
// The goal's log records it, so the goal's own report still accounts for
// the card it dropped and what became of it afterwards.
func (e *Engine) Adopt(ctx context.Context, id domain.FeatureID, actor string) (domain.Feature, error) {
	f, err := e.cfg.Store.GetFeature(ctx, id)
	if err != nil {
		return domain.Feature{}, err
	}
	if !f.GoalDropped() {
		// Not a refusal of a legal act — there is no act. Say which of the
		// two cases it is, because they want different things: a card in a
		// goal that is still running is the goal's to finish, and an
		// open-board card is already adopted.
		if f.InGoal() {
			return domain.Feature{}, fmt.Errorf("%s belongs to %s and was not dropped — its goal is still working it", id, f.GoalID)
		}
		return domain.Feature{}, fmt.Errorf("%s is already on the open board", id)
	}
	goal := domain.Feature{ID: f.GoalID}
	if f.GoalID != "" {
		if g, gerr := e.cfg.Store.GetFeature(ctx, f.GoalID); gerr == nil {
			goal = g
		}
	}
	back, err := e.cfg.Store.ReopenGoalDropped(ctx, id, actor, e.now())
	if err != nil {
		return domain.Feature{}, err
	}
	f.Stage = back
	if err := e.goalDetach(ctx, goal, f); err != nil {
		return domain.Feature{}, err
	}
	// The card's OWN log, not just the goal's. A card that reappears on
	// the board mid-stage with no session and no explanation is the
	// silent half of the same problem: the goal's log is where the goal's
	// reader looks, and this is where the card's reader does.
	e.cardNote(ctx, id, back, "adopted from "+goalName(goal.ID)+" — back on the board at "+string(back)+", its work kept")
	return e.cfg.Store.GetFeature(ctx, id)
}

// There is deliberately no "acknowledge the drop" verb beside Adopt.
//
// It was designed and cut: a stored "a person saw this" flag would be a
// second fact about an ending the card already records, kept only to
// decide when a row stops being shown. The board answers that with time
// instead — a dropped card sits in its own group for a day with its
// reason and its one real answer, and then folds into the archive with
// everything else that has settled. Agreeing with a drop is not an act;
// it is what happens when you do nothing.

// goalName is the goal's id for a sentence, falling back to the generic
// noun so a line about a goal that is no longer loaded still reads.
func goalName(id domain.FeatureID) string {
	if id == "" {
		return "its goal"
	}
	return string(id)
}

// cardNote appends one system line to a card's own thread — the same
// event kind a stage's messages use, so it renders where a reader is
// already looking rather than needing a surface of its own.
//
// Best effort: a note that cannot be written must never fail the act it
// describes.
func (e *Engine) cardNote(ctx context.Context, id domain.FeatureID, stage domain.Stage, text string) {
	payload, err := json.Marshal(map[string]string{
		"author": string(AuthorSystem), "content": text,
	})
	if err != nil {
		return
	}
	_ = e.cfg.Store.AppendEvent(ctx, state.CardEvent{
		Feature: id,
		Stage:   stage,
		Kind:    state.EventMessage,
		At:      e.now(),
		Payload: string(payload),
	})
}
