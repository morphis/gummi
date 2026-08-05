package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

func TestBudgetHintAndCap(t *testing.T) {
	ws, store, wt := newRepo(t)
	rec := recordingAgent()
	e := New(Config{
		Agents: singleAgent(rec), Store: store, Worktrees: wt, Workspace: ws,
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

// F7: read-mostly stages (plan critique, verify) get a leaner budget
// hint — the write-focused clauses ("batch edits", "avoid speculative
// refactors") are noise there, and critique specifically needs
// breadth to walk closure tables.
func TestBudgetHintReadMostlyForVerify(t *testing.T) {
	ws, store, wt := newRepo(t)
	rec := recordingAgent()
	e := New(Config{
		Agents: singleAgent(rec), Store: store, Worktrees: wt, Workspace: ws,
		Model: "m", MaxActive: 1, StageBudget: 100,
	})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "verify", domain.StageVerify)
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-001", StateDone)

	joined := strings.Join(rec.opts().SystemHints, "\n")
	if !strings.Contains(joined, "budget of about 100 credits") {
		t.Errorf("read-mostly hint missing credit figure:\n%s", joined)
	}
	if !strings.Contains(joined, "checkpoint") {
		t.Errorf("read-mostly hint missing checkpoint clause:\n%s", joined)
	}
	if strings.Contains(joined, "avoid speculative refactors") {
		t.Error("verify carried the write-focused refactor clause")
	}
	if strings.Contains(joined, "batch related edits") {
		t.Error("verify carried the write-focused batch-edits clause")
	}
}

func TestBudgetNoCapForInteractive(t *testing.T) {
	ws, store, wt := newRepo(t)
	rec := recordingAgent()
	e := New(Config{
		Agents: singleAgent(rec), Store: store, Worktrees: wt, Workspace: ws,
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
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1, StageBudget: 10})
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
	// no CLI cap fires (token-only backend or sub-floor budget): the agent keeps
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
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1, StageBudget: 10})
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

func TestEnvelopeDrivesStageBudget(t *testing.T) {
	// a feature with an envelope gets what's left of it as the stage
	// budget, not the flat config value.
	ws, store, wt := newRepo(t)
	rec := recordingAgent()
	e := New(Config{Agents: singleAgent(rec), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "impl", domain.StageImplement)
	f.Budget.Envelope = 300
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	// 40 credits already spent in earlier stages
	if err := store.AddSpend(context.Background(), f.ID, 40, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-001", StateDone)

	// 300 envelope minus 40 spent = 260 available; the enforced cap sits
	// 10% below that.
	if got := rec.opts().MaxCredits; got < 233.9 || got > 234.1 {
		t.Errorf("MaxCredits = %v, want ~234 (260 stage budget × 0.9)", got)
	}
}

func TestTopUpRaisesEnvelopeDurably(t *testing.T) {
	// a verify stage whose feature consumed the whole envelope gates
	// immediately; a top-up raises the envelope itself — persisted to the
	// store, so it survives stage advances and restarts — and resumes
	// with real headroom.
	ws, store, wt := newRepo(t)
	rec := recordingAgent()
	e := New(Config{Agents: singleAgent(rec), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "verify", domain.StageVerify)
	f.Budget.Envelope = 300
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	if err := store.AddSpend(context.Background(), f.ID, 300, 0, 0, 0); err != nil { // envelope drained
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

	// top up: envelope rederived from real spend (300 × 1.25 = 375 → 380),
	// persisted.
	if err := e.TopUp(context.Background(), f.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-001", StateDone)
	got, err := store.GetFeature(context.Background(), f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Budget.Envelope != 380 {
		t.Errorf("stored envelope = %d, want 380 (durable raise)", got.Budget.Envelope)
	}
	// stage budget 380 − 300 = 80 → MaxCredits 80 × 0.9 = 72.
	if mc := rec.opts().MaxCredits; mc < 71.9 || mc > 72.1 {
		t.Errorf("resumed MaxCredits = %v, want ~72 (raised envelope)", mc)
	}
}

func TestTopUpOverTightEnvelopeNoRegate(t *testing.T) {
	// a 3× underestimated envelope: the spend already dwarfs it, so a
	// sliver of a raise could never resume the stage — the raise must
	// leave real multi-turn headroom.
	ws, store, wt := newRepo(t)
	rec := recordingAgent()
	e := New(Config{Agents: singleAgent(rec), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "plan", domain.StagePlan)
	f.Budget.Envelope = 40
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	if err := store.AddSpend(context.Background(), f.ID, 120, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventExhausted)
	if rec.opts().MaxCredits != 0 {
		t.Errorf("a dry stage opened a session (MaxCredits=%v)", rec.opts().MaxCredits)
	}

	// top up: resume floor 120 + 60 = 180 beats rederive 150; the stage
	// resumes with two full turns of budget 180 − 120 = 60 → cap 54, not
	// the one-turn sliver that would re-gate after a single expensive turn.
	if err := e.TopUp(context.Background(), f.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-001", StateDone)
	got, err := store.GetFeature(context.Background(), f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Budget.Envelope != 180 {
		t.Errorf("stored envelope = %d, want 180 (resume floor from spend)", got.Budget.Envelope)
	}
	if mc := rec.opts().MaxCredits; mc < 53.9 || mc > 54.1 {
		t.Errorf("resumed MaxCredits = %v, want ~54 (two turns of headroom)", mc)
	}
}

func TestRaiseEnvelopeExplicitFigure(t *testing.T) {
	// the u envelope dialog: an explicit figure persists without resuming
	// anything, sub-floor figures are rejected (they would gate the next
	// turn immediately), and zero removes the cap.
	ws, store, wt := newRepo(t)
	rec := recordingAgent()
	e := New(Config{Agents: singleAgent(rec), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "plan", domain.StagePlan)
	f.Budget.Envelope = 300
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	if err := store.AddSpend(context.Background(), f.ID, 100, 0, 0, 0); err != nil {
		t.Fatal(err)
	}

	// floor = 100 spent + two turns of reserve (60) = 160
	if err := e.RaiseEnvelope(context.Background(), f.ID, 150); err == nil {
		t.Fatal("a sub-floor figure was accepted")
	}

	if err := e.RaiseEnvelope(context.Background(), f.ID, 500); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetFeature(context.Background(), f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Budget.Envelope != 500 {
		t.Errorf("stored envelope = %d, want 500", got.Budget.Envelope)
	}
	if rec.opts().MaxCredits != 0 {
		t.Errorf("a raise opened a session (MaxCredits=%v)", rec.opts().MaxCredits)
	}

	if err := e.RaiseEnvelope(context.Background(), f.ID, 0); err != nil {
		t.Fatal(err)
	}
	if got, err = store.GetFeature(context.Background(), f.ID); err != nil {
		t.Fatal(err)
	}
	if got.Budget.Envelope != 0 {
		t.Errorf("stored envelope = %d, want 0 (uncapped)", got.Budget.Envelope)
	}
}

func TestBudgetUsesAdapterCreditRate(t *testing.T) {
	// 6000 tokens is 3 credits at the default 0.5/1k (under an 8 budget),
	// but 12 at this adapter's 2.0/1k CreditRate — so the rate the adapter
	// reports (agent.CreditRate) must drive the exhaustion, proving per-
	// backend rates thread into the budget math.
	ag := &agent.Fake{
		Rate: 2.0,
		Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
			return []agent.Event{
				{Kind: agent.EventUsage, Usage: agent.Usage{OutputTokens: 6000}},
				{Kind: agent.EventIdle},
			}
		},
	}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1, StageBudget: 8})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "impl", domain.StageImplement)
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventExhausted)
}

func TestBudgetTokenOnlySessionEnforced(t *testing.T) {
	// a token-only session reports tokens, never credits: the budget must
	// still engage via the token→credit conversion (DESIGN §5.1 unified
	// spend). 20000 tokens at 0.5 credits/1K = 10 credit-equivalent > an 8 budget.
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
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1, StageBudget: 8})
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
		t.Error("token-only session was not interrupted on token overspend")
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
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1, StageBudget: 100})
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
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1, StageBudget: 10})
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
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1, StageBudget: 100})
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

func TestStageCapFlooredAtTurnReserve(t *testing.T) {
	// enforcement runs between turns, so a sliver of remaining budget
	// (here 5 credits) cannot be held as a cap — the first turn would
	// blow through it anyway. The cap is floored at one turn's reserve
	// instead.
	ws, store, wt := newRepo(t)
	rec := recordingAgent()
	e := New(Config{Agents: singleAgent(rec), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "impl", domain.StageImplement)
	f.Budget.Envelope = 300
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	// leave 5 of the 300 envelope remaining
	if err := store.AddSpend(context.Background(), f.ID, 295, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-001", StateDone)

	if got := e.Get("FD-001").Budget(); got != domain.TurnReserveCredits {
		t.Errorf("stage budget = %v, want the %v-credit turn reserve", got, float64(domain.TurnReserveCredits))
	}
	if mc := rec.opts().MaxCredits; mc != domain.TurnReserveCredits*capHeadroom {
		t.Errorf("MaxCredits = %v, want %v (turn reserve × headroom)", mc, domain.TurnReserveCredits*capHeadroom)
	}
}

func TestExhaustedPlanStillGatesDespiteFloor(t *testing.T) {
	// the turn-reserve floor applies to positive budgets only: an
	// envelope with nothing left still returns 0 and gates before a
	// session opens.
	ws, store, wt := newRepo(t)
	rec := recordingAgent()
	e := New(Config{Agents: singleAgent(rec), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "impl", domain.StageImplement)
	f.Budget.Envelope = 300
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	if err := store.AddSpend(context.Background(), f.ID, 300, 0, 0, 0); err != nil { // exactly at the envelope
		t.Fatal(err)
	}
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventExhausted)
	if rec.opts().MaxCredits != 0 {
		t.Errorf("a dry stage opened a session (MaxCredits=%v)", rec.opts().MaxCredits)
	}
}
