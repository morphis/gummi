package driver

import (
	"context"
	"errors"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// TestACardWhoseDriveFailedLeavesNothingRunning: the conductor reads a
// card with a live session as RUNNING (`Engine.goalCardState`), so a
// session left behind by a drive that failed is a card the goal waits on
// for ever — no lead turn, no drop, no stall, no exit. A drive that failed
// leaves nothing working, and the session goes with it.
func TestACardWhoseDriveFailedLeavesNothingRunning(t *testing.T) {
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StagePlan: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return msgIdle(o.Model, "Plan written.")
		},
	})
	ctx := context.Background()
	d := h.driver(Options{Autonomous: true})

	f, err := d.createFeature(ctx, domain.CardType{Kind: domain.KindFeature}, "a card of a goal")
	if err != nil {
		t.Fatal(err)
	}
	if f, err = h.store.Transition(ctx, f.ID, domain.StagePlan, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.eng.Attach(ctx, f); err != nil {
		t.Fatalf("starting the card's session: %v", err)
	}
	if h.eng.Get(f.ID) == nil {
		t.Fatal("no session to leave behind")
	}

	d.settleGoalCard(ctx, childResult{
		id: f.ID, out: Outcome{Status: StatusError, ID: string(f.ID)},
		err: errors.New("claude run failed: You've hit your session limit"),
	})

	if h.eng.Get(f.ID) != nil {
		t.Fatal("the card's session outlived the drive that failed; the goal will read it as still running and wait on it for ever")
	}
}
