package engine

import (
	"context"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

func TestFeatureSpendMeteredAcrossStages(t *testing.T) {
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{
			{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 2, OutputTokens: 40}},
			{Kind: agent.EventIdle},
		}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{Agent: ag, Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1, Persist: true})
	t.Cleanup(func() { e.Close() })
	ctx := context.Background()

	f := feature(1, "impl", domain.StageImplement)
	createFeature(t, store, f)
	withWorktree(t, wt, f)

	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-001", StateDone)

	// the feature's persisted spend reflects the metered usage
	got, err := store.GetFeature(ctx, "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	if got.Spend.Credits != 2 || got.Spend.OutputTokens != 40 {
		t.Errorf("metered feature spend = %+v, want {2 _ 40}", got.Spend)
	}

	// a second stage adds to the same running total
	f2 := got
	f2.Stage = domain.StageReview
	e.Drop("FD-001")
	if _, err := store.Transition(ctx, "FD-001", domain.StageReview, "user"); err != nil {
		t.Fatal(err)
	}
	rf, _ := store.GetFeature(ctx, "FD-001")
	if err := e.Run(rf); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-001", StateDone)

	got, _ = store.GetFeature(ctx, "FD-001")
	if got.Spend.Credits != 4 {
		t.Errorf("cumulative spend after two stages = %v, want 4", got.Spend.Credits)
	}
	// every event carried provider credits, so nothing reads as estimated
	if got.Spend.Estimated() {
		t.Errorf("provider-metered spend flagged estimated: %+v", got.Spend)
	}
}

// TestFeatureSpendTokenDerivedIsEstimated prices a usage event that
// carries only tokens (no provider credits — e.g. the copilot CLI's
// message fallback) and checks the whole sample lands in the estimated
// accumulator, feature-level and in the stage breakdown.
func TestFeatureSpendTokenDerivedIsEstimated(t *testing.T) {
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{
			{Kind: agent.EventUsage, Usage: agent.Usage{OutputTokens: 4000, Model: "m"}},
			{Kind: agent.EventIdle},
		}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{Agent: ag, Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1, Persist: true})
	t.Cleanup(func() { e.Close() })
	ctx := context.Background()

	f := feature(1, "impl", domain.StageImplement)
	createFeature(t, store, f)
	withWorktree(t, wt, f)

	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-001", StateDone)

	// 4000 tokens at the default 0.5 credits/1K → 2 credits, all estimated
	got, err := store.GetFeature(ctx, "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	if got.Spend.Credits != 2 || got.Spend.EstimatedCredits != 2 {
		t.Errorf("token-derived spend = %+v, want 2 credits all estimated", got.Spend)
	}
	bd, err := store.StageBreakdown(ctx, "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	if len(bd) != 1 || bd[0].EstimatedCredits != bd[0].Credits {
		t.Errorf("stage breakdown = %+v, want one fully-estimated row", bd)
	}
}
