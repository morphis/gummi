package engine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// TestStopForQuitParksAutopilotSession: a live autopilot session
// (GateApproval GateGates, the default) is stopped and the park event
// StopForQuit writes carries reason "quit". A second call is a no-op —
// the dedupe key holds the marker to one write per session generation.
func TestStopForQuitParksAutopilotSession(t *testing.T) {
	release := make(chan struct{})
	ag := &agent.Fake{Responder: func(agent.SessionOpts, string) []agent.Event {
		<-release
		return []agent.Event{{Kind: agent.EventIdle}}
	}}
	ws, store, wt := newRepo(t)
	e := persistEngine(t, ag, ws, store, wt)
	t.Cleanup(func() { close(release) })

	f := feature(1, "one", domain.StageImplement)
	f.GateApproval = domain.GateGates
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-001", StateRunning)

	ctx := context.Background()
	e.StopForQuit(ctx)

	waitState(t, e, "FD-001", StatePaused)

	evs, err := store.Events(ctx, "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	var parks []state.CardEvent
	for _, ev := range evs {
		if ev.Kind == state.EventPark {
			parks = append(parks, ev)
		}
	}
	if len(parks) != 1 {
		t.Fatalf("got %d park events, want 1: %+v", len(parks), parks)
	}
	var p state.ParkPayload
	if err := json.Unmarshal([]byte(parks[0].Payload), &p); err != nil {
		t.Fatal(err)
	}
	if p.Reason != state.ParkReasonQuit {
		t.Errorf("park reason = %q, want %q", p.Reason, state.ParkReasonQuit)
	}

	// a second call must not double the marker.
	e.StopForQuit(ctx)
	evs, err = store.Events(ctx, "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	parks = nil
	for _, ev := range evs {
		if ev.Kind == state.EventPark {
			parks = append(parks, ev)
		}
	}
	if len(parks) != 1 {
		t.Fatalf("after a second StopForQuit: got %d park events, want 1 (dedupe)", len(parks))
	}
}

// TestStopForQuitLeavesGateOffSessionRunning: a card driven by hand
// (GateOff) is untouched by StopForQuit — it is not what "on autopilot"
// means, and quitting the process stops it the way it always did,
// without a marker claiming a reopen should offer it back.
func TestStopForQuitLeavesGateOffSessionRunning(t *testing.T) {
	release := make(chan struct{})
	ag := &agent.Fake{Responder: func(agent.SessionOpts, string) []agent.Event {
		<-release
		return []agent.Event{{Kind: agent.EventIdle}}
	}}
	ws, store, wt := newRepo(t)
	e := persistEngine(t, ag, ws, store, wt)
	t.Cleanup(func() { close(release) })

	f := feature(1, "one", domain.StageImplement)
	f.GateApproval = domain.GateOff
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-001", StateRunning)

	e.StopForQuit(context.Background())

	if st := e.Get("FD-001").State(); st != StateRunning {
		t.Fatalf("GateOff session state after StopForQuit = %s, want still running", st)
	}
	evs, err := store.Events(context.Background(), "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs {
		if ev.Kind == state.EventPark {
			t.Fatalf("GateOff session got a park event, want none: %+v", ev)
		}
	}
}

// TestQuitStoppedCardsAfterRestore is the round trip StopForQuit exists
// for: one engine stops a card on the way out, a second engine sharing
// the same store restores it on the way back in, and QuitStoppedCards
// offers it with the corrective rounds already spent.
func TestQuitStoppedCardsAfterRestore(t *testing.T) {
	release := make(chan struct{})
	ag := &agent.Fake{Responder: func(agent.SessionOpts, string) []agent.Event {
		<-release
		return []agent.Event{{Kind: agent.EventIdle}}
	}}
	ws, store, wt := newRepo(t)
	e1 := persistEngine(t, ag, ws, store, wt)

	f := feature(1, "csv export", domain.StageImplement)
	f.GateApproval = domain.GateFull
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	withWorktree(t, wt, f)
	if err := e1.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e1, "FD-001", StateRunning)

	ctx := context.Background()
	for range 2 {
		if err := store.IncrementRounds(ctx, "FD-001", domain.RoundKindCorrective); err != nil {
			t.Fatal(err)
		}
	}

	e1.StopForQuit(ctx)
	close(release)
	if err := e1.Close(); err != nil {
		t.Fatal(err)
	}

	e2 := persistEngine(t, ag, ws, store, wt)
	if err := e2.Restore(ctx); err != nil {
		t.Fatal(err)
	}

	cards, err := e2.QuitStoppedCards(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 {
		t.Fatalf("got %d quit-stopped cards, want 1: %+v", len(cards), cards)
	}
	c := cards[0]
	if c.Feature.ID != "FD-001" || c.Feature.Stage != domain.StageImplement {
		t.Errorf("card feature = %+v, want FD-001 at implement", c.Feature)
	}
	if c.Corrective != 2 {
		t.Errorf("Corrective = %d, want 2", c.Corrective)
	}
	if c.ParkedAt.IsZero() {
		t.Error("ParkedAt is zero, want the park event's timestamp")
	}
}

// TestQuitStoppedCardsEmptyWithNothingStopped: a fresh Restore with no
// quit park in the log offers nothing.
func TestQuitStoppedCardsEmptyWithNothingStopped(t *testing.T) {
	ws, store, wt := newRepo(t)
	e := persistEngine(t, agent.NewFake("hi"), ws, store, wt)

	cards, err := e.QuitStoppedCards(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 0 {
		t.Fatalf("got %d quit-stopped cards, want 0: %+v", len(cards), cards)
	}
}
