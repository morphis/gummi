package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/domain"
)

func TestBudgetHintAndCap(t *testing.T) {
	ws, store, wt := newRepo(t)
	rec := recordingAgent()
	e := New(Config{
		Agent: rec, Store: store, Worktrees: wt, Workspace: ws,
		Model: "m", MaxActive: 1, StageBudget: 100,
	})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "impl", domain.StageImplement)
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-001", StateDone)

	opts := rec.opts()
	// cap is 10% below the budget (soft-stop headroom)
	if opts.MaxCredits != 90 {
		t.Errorf("MaxCredits = %v, want 90 (90%% of 100)", opts.MaxCredits)
	}
	// the model is told its budget
	joined := strings.Join(opts.SystemHints, "\n")
	if !strings.Contains(joined, "budget of about 100 credits") {
		t.Errorf("budget hint missing from system hints:\n%s", joined)
	}
}

func TestBudgetNoCapForInteractive(t *testing.T) {
	ws, store, wt := newRepo(t)
	rec := recordingAgent()
	e := New(Config{
		Agent: rec, Store: store, Worktrees: wt, Workspace: ws,
		Model: "m", MaxActive: 1, StageBudget: 100,
	})
	t.Cleanup(func() { e.Close() })

	s, err := e.Attach(context.Background(), feature(1, "x", domain.StageBrainstorm))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventStarted)
	if s.Budget() != 0 {
		t.Errorf("interactive session has a budget %v, want 0 (human-paced)", s.Budget())
	}
	if rec.opts().MaxCredits != 0 {
		t.Errorf("interactive session capped at %v, want uncapped", rec.opts().MaxCredits)
	}
}

func TestBudgetThresholdNudges(t *testing.T) {
	// stream usage that crosses 50%, then 80%, then 95% of a 10-credit
	// budget; each threshold should be reported once.
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{
			{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 5.5}}, // 55% → 50 nudge
			{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 3}},   // 85% → 80 nudge
			{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 1}},   // 95% → 95 nudge
			{Kind: agent.EventIdle},
		}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{Agent: ag, Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1, StageBudget: 10})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "impl", domain.StageImplement)
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}

	// collect the budget threshold events
	var thresholds []int
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-e.Events():
			if ev.Kind == EventBudget {
				thresholds = append(thresholds, ev.Threshold)
			}
			if ev.Kind == EventIdle {
				goto done
			}
		case <-deadline:
			t.Fatal("timed out")
		}
	}
done:
	if len(thresholds) != 3 || thresholds[0] != 50 || thresholds[1] != 80 || thresholds[2] != 95 {
		t.Errorf("thresholds = %v, want [50 80 95]", thresholds)
	}
	snap := e.Get("FD-001").Snapshot()
	if len(snap.Activity) != 3 {
		t.Errorf("nudge activity lines = %d, want 3", len(snap.Activity))
	}
}

func TestBudgetOverspendEnforcedGummiSide(t *testing.T) {
	// no CLI cap fires (BYOK / sub-floor budget): the agent keeps
	// spending past the budget and only reports usage. gummi-side
	// enforcement must interrupt and checkpoint on its own.
	interrupted := make(chan struct{}, 1)
	ag := &agent.Fake{
		Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
			return []agent.Event{
				{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 12}}, // 120% of a 10 budget
				{Kind: agent.EventIdle},
			}
		},
		OnInterrupt: func() { interrupted <- struct{}{} },
	}
	ws, store, wt := newRepo(t)
	e := New(Config{Agent: ag, Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1, StageBudget: 10})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "impl", domain.StageImplement)
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventExhausted)

	select {
	case <-interrupted:
	case <-time.After(time.Second):
		t.Error("agent was not interrupted on overspend")
	}
	snap := e.Get("FD-001").Snapshot()
	if snap.State != StateDone {
		t.Errorf("overspent session state = %s, want done", snap.State)
	}
}

func TestSpendPlanDrivesStageBudget(t *testing.T) {
	// a feature with an envelope gets its per-stage allocation with
	// rollover and the protected review/verify floor, not a flat budget.
	ws, store, wt := newRepo(t)
	rec := recordingAgent()
	e := New(Config{Agent: rec, Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "impl", domain.StageImplement)
	f.Budget.Envelope = 300
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	// 40 credits already spent in earlier stages
	if err := store.AddSpend(context.Background(), f.ID, 40, 0, 0); err != nil {
		t.Fatal(err)
	}
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-001", StateDone)

	// implement cap through 70% of 300 = 210, minus 40 spent = 170 available;
	// the enforced cap sits 10% below that.
	if got := rec.opts().MaxCredits; got < 152.9 || got > 153.1 {
		t.Errorf("MaxCredits = %v, want ~153 (170 stage budget × 0.9)", got)
	}
}

func TestTopUpReleasesReserve(t *testing.T) {
	// a verify stage at its 95% cap has nothing left and gates immediately;
	// a top-up releases the 5% reserve and resumes with that headroom.
	ws, store, wt := newRepo(t)
	rec := recordingAgent()
	e := New(Config{Agent: rec, Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "verify", domain.StageVerify)
	f.Budget.Envelope = 300
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	if err := store.AddSpend(context.Background(), f.ID, 285, 0, 0); err != nil { // at the 95% cap
		t.Fatal(err)
	}
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	// nothing left: the stage gates immediately without opening a session.
	waitFor(t, e, EventExhausted)
	if rec.opts().MaxCredits != 0 {
		t.Errorf("a dry stage opened a session (MaxCredits=%v)", rec.opts().MaxCredits)
	}

	// top up: reserve (15 credits) released, stage resumes with a cap.
	if err := e.TopUp(context.Background(), f.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-001", StateDone)
	// 15 credit budget × 0.9 headroom = 13.5.
	if got := rec.opts().MaxCredits; got < 13.4 || got > 13.6 {
		t.Errorf("resumed MaxCredits = %v, want ~13.5 (reserve released)", got)
	}
}

func TestBudgetUsesProviderRate(t *testing.T) {
	// 6000 tokens is 3 credits at the default 0.5/1k (under an 8 budget),
	// but 12 at this provider's 2.0/1k rate — so the rate must drive the
	// exhaustion, proving per-provider rates thread into the budget math.
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{
			{Kind: agent.EventUsage, Usage: agent.Usage{OutputTokens: 6000}},
			{Kind: agent.EventIdle},
		}
	}}
	profs := config.Profiles{Default: "r", Profiles: map[string]config.Profile{
		"r": {"implementer": {
			Model:    "m",
			Provider: &config.ProviderConfig{Type: "openai", BaseURL: "http://x/v1", CreditsPer1KTokens: 2.0},
		}},
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{Agent: ag, Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1, StageBudget: 8, Profiles: profs})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "impl", domain.StageImplement)
	f.Profile = "r"
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventExhausted)
}

func TestBudgetByokTokensEnforced(t *testing.T) {
	// a BYOK session reports tokens, never credits: the budget must still
	// engage via the token→credit conversion (DESIGN §5.1 unified spend).
	// 20000 tokens at 0.5 credits/1K = 10 credit-equivalent > an 8 budget.
	interrupted := make(chan struct{}, 1)
	ag := &agent.Fake{
		Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
			return []agent.Event{
				{Kind: agent.EventUsage, Usage: agent.Usage{OutputTokens: 20000}},
				{Kind: agent.EventIdle},
			}
		},
		OnInterrupt: func() { interrupted <- struct{}{} },
	}
	ws, store, wt := newRepo(t)
	e := New(Config{Agent: ag, Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1, StageBudget: 8})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "impl", domain.StageImplement)
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventExhausted)
	select {
	case <-interrupted:
	case <-time.After(time.Second):
		t.Error("BYOK session was not interrupted on token overspend")
	}
}

func TestBudgetExhaustionIsIdempotent(t *testing.T) {
	// the CLI may re-raise its limits-exhausted event; the checkpoint and
	// gate must fire exactly once, not once per event.
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{
			{Kind: agent.EventBudgetExhausted, Usage: agent.Usage{Credits: 90}},
			{Kind: agent.EventBudgetExhausted, Usage: agent.Usage{Credits: 90}},
		}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{Agent: ag, Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1, StageBudget: 100})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "impl", domain.StageImplement)
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventExhausted)

	// only one EventExhausted may be emitted for the feature
	extra := 0
	deadline := time.After(500 * time.Millisecond)
	for {
		select {
		case ev := <-e.Events():
			if ev.Kind == EventExhausted && ev.Feature == "FD-001" {
				extra++
			}
		case <-deadline:
			if extra > 0 {
				t.Fatalf("exhaustion fired %d extra times on a re-raised event", extra)
			}
			snap := e.Get("FD-001").Snapshot()
			n := 0
			for _, a := range snap.Activity {
				if strings.Contains(a, "budget exhausted") {
					n++
				}
			}
			if n != 1 {
				t.Fatalf("budget-exhausted checkpoint appears %d times, want 1", n)
			}
			return
		}
	}
}

func TestBudgetExhaustionSurvivesTrailingIdle(t *testing.T) {
	// the CLI sends SessionIdle after the turn even when the budget was
	// exhausted mid-turn; that trailing idle must not emit a plain idle
	// event (which the UI turns into a generic "finished" gate, hiding the
	// budget gate). The engine should stay silent after exhaustion.
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{
			{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 12}}, // > 10 budget
			{Kind: agent.EventIdle},                                   // trailing idle
		}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{Agent: ag, Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1, StageBudget: 10})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "impl", domain.StageImplement)
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventExhausted)

	// no EventIdle may follow the exhaustion for this feature
	deadline := time.After(500 * time.Millisecond)
	for {
		select {
		case ev := <-e.Events():
			if ev.Kind == EventIdle && ev.Feature == "FD-001" {
				t.Fatal("engine emitted a trailing idle after budget exhaustion")
			}
		case <-deadline:
			return
		}
	}
}

func TestBudgetExhaustedRaisesCheckpoint(t *testing.T) {
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{
			{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 90}},
			{Kind: agent.EventBudgetExhausted, Usage: agent.Usage{Credits: 90}},
		}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{Agent: ag, Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1, StageBudget: 100})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "impl", domain.StageImplement)
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventExhausted)

	snap := e.Get("FD-001").Snapshot()
	found := false
	for _, a := range snap.Activity {
		if strings.Contains(a, "budget exhausted") {
			found = true
		}
	}
	if !found {
		t.Errorf("no exhaustion checkpoint in activity: %+v", snap.Activity)
	}
	if snap.State != StateDone {
		t.Errorf("exhausted session state = %s, want done (slot freed)", snap.State)
	}
}
