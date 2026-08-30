package ui

import (
	"context"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui/theme"
)

// TestSeedInboxFromOpenGateDecision is the whole point of the durable
// record (DESIGN §10.18): a card with an open gate decision seeds its
// inbox item straight from the store, with no engine session — restored
// or otherwise — behind it at all.
func TestSeedInboxFromOpenGateDecision(t *testing.T) {
	ws, store, wt := uiRepo(t)
	ctx := context.Background()
	f := mkFeature(t, store, 1, "needs review", domain.StageReview)
	at := time.Date(2026, 8, 30, 9, 0, 0, 0, time.UTC)
	if err := store.OpenDecision(ctx, f.ID, f.Stage, state.DecisionPayload{
		ID: "gate:1", Kind: state.DecisionKindGate, Question: "review is ready for your decision.",
	}, at); err != nil {
		t.Fatal(err)
	}
	decisions, err := store.OpenDecisions(ctx)
	if err != nil {
		t.Fatal(err)
	}

	m := NewShell(theme.GummiDark(), "v0-test")
	m.Attach(store, wt, ws)
	m.seedInboxFromDecisions(decisions)

	if m.inbox.len() != 1 {
		t.Fatalf("inbox len = %d, want 1: %+v", m.inbox.len(), m.inbox.list())
	}
	it, ok := m.inbox.get(f.ID)
	if !ok {
		t.Fatal("open gate decision did not seed an item")
	}
	if it.Kind != attnGate {
		t.Errorf("kind = %s, want gate", it.Kind)
	}
	if it.Escalated {
		t.Error("a plain gate decision should not read as escalated")
	}
	if it.Text != "review is ready for your decision." {
		t.Errorf("text = %q, want the decision's own question", it.Text)
	}
	if !it.At.Equal(at) {
		t.Errorf("At = %v, want the decision's own timestamp %v", it.At, at)
	}
}

// TestSeedInboxFromOpenAskDecision: an open ask decision seeds an
// attnQuestion item.
func TestSeedInboxFromOpenAskDecision(t *testing.T) {
	ws, store, wt := uiRepo(t)
	ctx := context.Background()
	f := mkFeature(t, store, 1, "dark mode", domain.StageBrainstorm)
	if err := store.OpenDecision(ctx, f.ID, f.Stage, state.DecisionPayload{
		ID: "ask:1", Kind: state.DecisionKindAsk, Question: "persist where?", FreeForm: true,
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	decisions, err := store.OpenDecisions(ctx)
	if err != nil {
		t.Fatal(err)
	}

	m := NewShell(theme.GummiDark(), "v0-test")
	m.Attach(store, wt, ws)
	m.seedInboxFromDecisions(decisions)

	it, ok := m.inbox.get(f.ID)
	if !ok || it.Kind != attnQuestion {
		t.Fatalf("ask decision seeded %+v, want an attnQuestion item", it)
	}
	if it.Text != "persist where?" {
		t.Errorf("text = %q, want the decision's own question", it.Text)
	}
}

// TestSeedInboxFromOpenBudgetDecision: an open budget decision seeds an
// attnBudget item — the only path back to the top-up key for a card
// parked by a restart with no session at all.
func TestSeedInboxFromOpenBudgetDecision(t *testing.T) {
	ws, store, wt := uiRepo(t)
	ctx := context.Background()
	f := mkFeature(t, store, 1, "big feature", domain.StageImplement)
	if err := store.OpenDecision(ctx, f.ID, f.Stage, state.DecisionPayload{
		ID: "budget:1", Kind: state.DecisionKindBudget, Question: "implement reached its envelope.",
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	decisions, err := store.OpenDecisions(ctx)
	if err != nil {
		t.Fatal(err)
	}

	m := NewShell(theme.GummiDark(), "v0-test")
	m.Attach(store, wt, ws)
	m.seedInboxFromDecisions(decisions)

	it, ok := m.inbox.get(f.ID)
	if !ok || it.Kind != attnBudget {
		t.Fatalf("budget decision seeded %+v, want an attnBudget item", it)
	}
}

// TestSeedInboxRanksBudgetOverGate: a card with BOTH a budget and a gate
// decision open yields exactly one row, and it is the budget one — the
// ranking rankOpenDecision applies (ask > budget > verify > gate > idle),
// matching the card thread's own pinned control.
func TestSeedInboxRanksBudgetOverGate(t *testing.T) {
	ws, store, wt := uiRepo(t)
	ctx := context.Background()
	f := mkFeature(t, store, 1, "big feature", domain.StageImplement)
	base := time.Date(2026, 8, 30, 9, 0, 0, 0, time.UTC)
	if err := store.OpenDecision(ctx, f.ID, f.Stage, state.DecisionPayload{
		ID: "gate:1", Kind: state.DecisionKindGate, Question: "implement is ready for your decision.",
	}, base); err != nil {
		t.Fatal(err)
	}
	if err := store.OpenDecision(ctx, f.ID, f.Stage, state.DecisionPayload{
		ID: "budget:1", Kind: state.DecisionKindBudget, Question: "implement reached its envelope.",
	}, base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	decisions, err := store.OpenDecisions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions[f.ID]) != 2 {
		t.Fatalf("OpenDecisions reported %d open decisions for %s, want 2 (both genuinely open)", len(decisions[f.ID]), f.ID)
	}

	m := NewShell(theme.GummiDark(), "v0-test")
	m.Attach(store, wt, ws)
	m.seedInboxFromDecisions(decisions)

	if m.inbox.len() != 1 {
		t.Fatalf("inbox len = %d, want exactly 1 row for the one card", m.inbox.len())
	}
	it, ok := m.inbox.get(f.ID)
	if !ok || it.Kind != attnBudget {
		t.Fatalf("ranked item = %+v, want the budget decision to win over the gate", it)
	}
}

// TestSeedInboxSkipsAbandonedDecision: a decision whose stage the card
// has since moved past is abandoned (Store.OpenDecisions' own contract) —
// it seeds nothing.
func TestSeedInboxSkipsAbandonedDecision(t *testing.T) {
	ws, store, wt := uiRepo(t)
	ctx := context.Background()
	f := mkFeature(t, store, 1, "moved on", domain.StageReview)
	if err := store.OpenDecision(ctx, f.ID, f.Stage, state.DecisionPayload{
		ID: "gate:1", Kind: state.DecisionKindGate, Question: "review is ready for your decision.",
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	// the card moves on to a later stage — the gate was crossed over the
	// decision without an answer event ever being recorded, which is
	// exactly what abandons it.
	if _, err := store.Transition(ctx, f.ID, domain.StageVerify, "user"); err != nil {
		t.Fatal(err)
	}
	decisions, err := store.OpenDecisions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions[f.ID]) != 0 {
		t.Fatalf("OpenDecisions reported %+v for a card that moved on, want none", decisions[f.ID])
	}

	m := NewShell(theme.GummiDark(), "v0-test")
	m.Attach(store, wt, ws)
	m.seedInboxFromDecisions(decisions)

	if m.inbox.len() != 0 {
		t.Fatalf("inbox len = %d, want 0 — the decision moved on with the stage", m.inbox.len())
	}
}
