package engine

import (
	"context"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
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

// TestSettledUsageClearsEstimates replays the claude adapter's metering
// shape: token-only events before a rate is known (the engine prices
// them), rate-derived estimates mid-turn, then a settle event carrying
// the provider's actual cost. After the settle, the stage rollup must
// hold exactly the metered figure with no estimated remainder — the
// dashboard then says "provider-metered" instead of "~ estimated".
func TestSettledUsageClearsEstimates(t *testing.T) {
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{
			// turn one, rate unknown: tokens only → engine token-prices it
			{Kind: agent.EventUsage, Usage: agent.Usage{InputTokens: 1000, OutputTokens: 1000, Model: "m"}},
			// mid-turn adapter estimate at a realized rate
			{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 4, Model: "m", Estimate: true}},
			// the result settles: real cost 10; the correction is
			// 10 − 4 (the adapter subtracts its own estimates)
			{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 6, Model: "m", Settled: true}},
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

	got, err := store.GetFeature(ctx, "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	// 2000 tokens at the 0.5/1K default = 1 estimated credit, retired by
	// the settle along with the adapter's 4: total = 1+4+(6−1) = 10 real.
	if got.Spend.Credits != 10 {
		t.Errorf("settled spend = %v, want 10 (provider-metered)", got.Spend.Credits)
	}
	if got.Spend.Estimated() {
		t.Errorf("settled spend still flagged estimated: %+v", got.Spend)
	}
	rows, err := store.StageBreakdown(ctx, "FD-001")
	if err != nil || len(rows) != 1 {
		t.Fatalf("breakdown rows = %+v (%v)", rows, err)
	}
	if rows[0].Credits != 10 || rows[0].EstimatedCredits != 0 {
		t.Errorf("stage row = %+v, want credits 10, estimated 0", rows[0])
	}
}

// TestUnsettledEstimatesStayLabeled: a backend that never settles (token
// fallback all the way) keeps its estimated label — only a settle event
// may clear it.
func TestUnsettledEstimatesStayLabeled(t *testing.T) {
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{
			{Kind: agent.EventUsage, Usage: agent.Usage{InputTokens: 1000, OutputTokens: 3000, Model: "m"}},
			{Kind: agent.EventIdle},
		}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{Agent: ag, Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1, Persist: true})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "impl", domain.StageImplement)
	createFeature(t, store, f)
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-001", StateDone)

	got, err := store.GetFeature(context.Background(), "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Spend.Estimated() || got.Spend.Credits != 2 {
		t.Errorf("token-derived spend = %+v, want 2 estimated credits", got.Spend)
	}
}

// TestHelperSpendAttributedToHelperRole: a backend's internal side-model
// call (a title/summary on a different model than the session's) is
// booked to the helper role in the breakdown, not the stage's working
// role — so it neither inflates nor mis-attributes that role's row. Its
// credits still count toward the feature total.
func TestHelperSpendAttributedToHelperRole(t *testing.T) {
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{
			// the session's own working model does the stage work
			{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 10, InputTokens: 500, OutputTokens: 500, Model: "sonnet"}},
			// a token-less internal helper call on a side model
			{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 0.2, Model: "haiku", Settled: true, Helper: true}},
			{Kind: agent.EventIdle},
		}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{Agent: ag, Store: store, Worktrees: wt, Workspace: ws, Model: "sonnet", MaxActive: 1, Persist: true})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "plan", domain.StagePlan)
	createFeature(t, store, f)
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-001", StateDone)

	rows, err := store.StageBreakdown(context.Background(), "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	var stageRole, helperRole *state.StageSpend
	for i := range rows {
		switch rows[i].Role {
		case "helper":
			helperRole = &rows[i]
		case string(agent.RoleArchitect):
			stageRole = &rows[i]
		}
	}
	if stageRole == nil || stageRole.Model != "sonnet" {
		t.Fatalf("stage work not attributed to the architect/sonnet row: %+v", rows)
	}
	if helperRole == nil || helperRole.Model != "haiku" {
		t.Fatalf("helper call not attributed to the helper role: %+v", rows)
	}
	// the helper's cost still reaches the feature total
	got, _ := store.GetFeature(context.Background(), "FD-001")
	if got.Spend.Credits != 10.2 {
		t.Errorf("feature total = %v, want 10.2 (stage + helper)", got.Spend.Credits)
	}
}
