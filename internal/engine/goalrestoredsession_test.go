package engine

import (
	"context"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/goalpolicy"
	"github.com/morphis/gummi/internal/state"
)

// TestACardWhoseProcessDiedIsNotStillWorking: a process killed mid-turn
// leaves its session persisted, and the next gummi rehydrates it exactly
// as it was. A design conversation comes back INTERACTIVE, which the
// conductor read as a card that is working — so the goal never acted on
// it again: no lead turn, no drop, no stall, no exit, across every resume,
// because each resume restores the same snapshot. A conversation nobody is
// having is not a card that is working.
func TestACardWhoseProcessDiedIsNotStillWorking(t *testing.T) {
	e, _, store, _ := advanceEngine(t)
	ctx := context.Background()
	card := domain.Feature{ID: "FD-900", Num: 900, Slug: "a-card-of-a-goal", Title: "a card of a goal", Stage: domain.StagePlan, GoalID: "GL-900"}
	if err := store.CreateFeature(ctx, &card); err != nil {
		t.Fatal(err)
	}

	// the session the dead process left behind: nothing is driving it, and
	// no live file names a process that answers
	e.mu.Lock()
	sctx, scancel := context.WithCancel(context.Background())
	e.live[card.ID] = &Session{
		Feature: card, Role: "architect", state: StateInteractive, Interactive: true,
		done: make(chan struct{}), ctx: sctx, cancel: scancel,
	}
	e.mu.Unlock()

	now := time.Now()
	marks := state.CardMarks{
		Park:       state.CardMark{Seq: 90, At: now.Add(-3 * time.Hour)},
		StageEnter: state.CardMark{Seq: 10, At: now.Add(-4 * time.Hour)},
		Last:       state.CardMark{Seq: 90, At: now.Add(-3 * time.Hour)},
	}
	st, _ := e.goalCardState(ctx, card, marks, nil, 5, now.Add(-4*time.Hour), now)
	if st == goalpolicy.Running {
		t.Fatal("a card whose driving process is gone reads as still running; its goal will wait on it for ever")
	}
	if st != goalpolicy.Stuck {
		t.Fatalf("state = %v, want stuck — the park its dead drive left is what the conductor should act on", st)
	}
}
