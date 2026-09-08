package engine

import (
	"context"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// The card's running total is carried on the session so a render can
// read the store's figure without a read of its own: seeded from the
// feature row at spawn (so spend from earlier stages counts) and then
// moved by exactly what recordUsage books against that row. A board
// snapshot reloads on a handful of events and never on a usage one, so
// this is the only live account of the envelope a display has.
func TestCardSpentTracksTheStoreRow(t *testing.T) {
	ag := &agent.Fake{Responder: func(agent.SessionOpts, string) []agent.Event {
		return []agent.Event{
			{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 4, Model: "m"}},
			{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 2.5, Model: "m"}},
			{Kind: agent.EventIdle},
		}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{
		Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws,
		Model: "m", MaxActive: 1, Persist: true,
	})
	t.Cleanup(func() { e.Close() })

	ctx := context.Background()
	f := feature(1, "impl", domain.StageImplement)
	createFeature(t, store, f)
	// earlier stages already spent against this card's envelope
	if err := store.AddSpend(ctx, f.ID, 300, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-001", StateDone)

	cur, err := store.GetFeature(ctx, "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	want := cur.Spend.CreditEquivalent()
	if want != 306.5 {
		t.Fatalf("store row = %v, want 300 carried + 6.5 spent", want)
	}
	if got := e.Get("FD-001").CardSpent(); got != want {
		t.Errorf("CardSpent() = %v, want %v (the store's running total)", got, want)
	}
}

// Interactive chat is uncapped but not free: it moves the card's total
// the same way, and the masthead above the conversation is the surface
// showing it, so the seed has to happen on that path too.
func TestCardSpentSeededForInteractiveChat(t *testing.T) {
	ws, store, wt := newRepo(t)
	e := New(Config{
		Agents: singleAgent(agent.NewFake("hello")), Store: store, Worktrees: wt,
		Workspace: ws, Model: "m", MaxActive: 1, Persist: true,
	})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "chat", domain.StagePlan)
	createFeature(t, store, f)
	if err := store.AddSpend(context.Background(), f.ID, 120, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	s, err := e.Attach(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.CardSpent(); got != 120 {
		t.Errorf("CardSpent() on attach = %v, want 120 (the card's spend so far)", got)
	}
}
