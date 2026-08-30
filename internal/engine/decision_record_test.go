package engine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// TestAskOpensItsDecisionRow: the moment an agent puts a question to the
// user, a decision_open row goes down (§10.18 — nothing may block a card
// without leaving a row). It is OpenDecisions' open record until the
// answer event correlates to it.
func TestAskOpensItsDecisionRow(t *testing.T) {
	args := askArgs(t, Ask{
		Question: "Persist where?",
		Options:  []AskOption{{Label: "per-device"}, {Label: "synced"}},
	})
	ag := clientToolFake(args)
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "fake-model", MaxActive: 1})

	f := feature(1, "Dark mode", domain.StageBrainstorm)
	putFeature(t, store, f)
	if _, err := e.Attach(context.Background(), f); err != nil {
		e.Close()
		t.Fatal(err)
	}
	waitFor(t, e, EventQuestion)

	opens, err := store.OpenDecisions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	list := opens["FD-001"]
	if len(list) != 1 || list[0].Kind != state.DecisionKindAsk {
		t.Fatalf("open decisions after ask = %+v, want one ask-kind row", opens["FD-001"])
	}
	if list[0].ID == "" {
		t.Fatal("decision_open row carries no id")
	}
	if list[0].Stage != domain.StageBrainstorm {
		t.Errorf("decision stage = %q, want the stage it was asked in", list[0].Stage)
	}
	e.Close()
}

// TestAskDecisionClosesOnAnswer: the ask-shaped round trip whose payload
// carries the decision id is the answer half of the record — once it
// lands, OpenDecisions reports nothing waiting, and the answer event
// says who answered (by) and which option was chosen (choice).
func TestAskDecisionClosesOnAnswer(t *testing.T) {
	args := askArgs(t, Ask{
		Question: "Persist where?",
		Options:  []AskOption{{Label: "per-device"}, {Label: "synced"}},
	})
	ag := clientToolFake(args)
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "fake-model", MaxActive: 1})

	f := feature(1, "Dark mode", domain.StageBrainstorm)
	putFeature(t, store, f)
	if _, err := e.Attach(context.Background(), f); err != nil {
		e.Close()
		t.Fatal(err)
	}
	waitFor(t, e, EventQuestion)

	opens, err := store.OpenDecisions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(opens["FD-001"]) != 1 {
		e.Close()
		t.Fatalf("the ask's decision is not open: %+v (err=%v)", opens, err)
	}
	decisionID := opens["FD-001"][0].ID

	if err := e.AnswerAs(context.Background(), f.ID, "per-device", state.ActorUser); err != nil {
		e.Close()
		t.Fatalf("Answer: %v", err)
	}
	e.Close()

	evs, err := store.Events(context.Background(), "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	var answer *state.AskPayload
	for _, ev := range evs {
		if ev.Kind != state.EventAsk {
			continue
		}
		var p state.AskPayload
		if err := json.Unmarshal([]byte(ev.Payload), &p); err != nil {
			t.Fatal(err)
		}
		answer = &p
	}
	if answer == nil {
		t.Fatal("no ask event recorded")
	}
	if answer.By != state.ActorUser {
		t.Errorf("by = %q, want %q — the answerer declares itself", answer.By, state.ActorUser)
	}
	if answer.ID != decisionID {
		t.Errorf("answer id = %q, want the decision id %q", answer.ID, decisionID)
	}
	if answer.Choice != "per-device" {
		t.Errorf("choice = %q, want the chosen option", answer.Choice)
	}

	opens, err = store.OpenDecisions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(opens["FD-001"]) != 0 {
		t.Fatalf("the answered ask still reads open: %+v", opens["FD-001"])
	}
}

// TestGateCrossingCorrelatesToItsOpenDecision: a design gate decision
// raised at a checkpoint is answered by the crossing that crosses it —
// the gate event carries the decision id in this same transaction, and
// the record closes. A crossing with no open decision stays
// uncorrelated, as every crossing written before decisions were durable.
func TestGateCrossingCorrelatesToItsOpenDecision(t *testing.T) {
	_, store, _ := newRepo(t)
	ctx := context.Background()

	f := feature(1, "Dark mode", domain.StageSpec)
	f.Skip = domain.QuickRoute() // spec → implement: a legal crossing
	putFeature(t, store, f)

	decisionID := "gate:spec->implement:1788093491271089319"
	if err := store.OpenDecision(ctx, f.ID, domain.StageSpec, state.DecisionPayload{
		ID: decisionID, Kind: state.DecisionKindGate,
		Question: "spec is ready for your decision.",
	}, time.Now()); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Transition(ctx, "FD-001", domain.StagePlan, "caller"); err != nil {
		t.Fatal(err)
	}

	evs, err := store.Events(ctx, "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	var gate *state.GatePayload
	for _, ev := range evs {
		if ev.Kind != state.EventGate || ev.Stage != domain.StageSpec {
			continue
		}
		var p state.GatePayload
		if err := json.Unmarshal([]byte(ev.Payload), &p); err != nil {
			t.Fatal(err)
		}
		gate = &p
	}
	if gate == nil {
		t.Fatal("no gate event recorded")
	}
	if gate.ID != decisionID {
		t.Fatalf("gate event id = %q, want the decision id %q", gate.ID, decisionID)
	}
	if gate.Actor != "caller" {
		t.Errorf("actor = %q, want the crossing's own actor", gate.Actor)
	}

	opens, err := store.OpenDecisions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(opens["FD-001"]) != 0 {
		t.Fatalf("the crossed gate's decision still reads open: %+v", opens)
	}
}

// TestCrossingWithoutOpenDecisionIsUncorrelated: a gate crossed with no
// open decision to answer (an auto crossing under GateFull) carries no
// correlating id — the record stays zero rather than inventing one.
func TestCrossingWithoutOpenDecisionIsUncorrelated(t *testing.T) {
	_, store, _ := newRepo(t)
	ctx := context.Background()

	f := feature(1, "Dark mode", domain.StageSpec)
	f.Skip = domain.QuickRoute()
	putFeature(t, store, f)

	if _, err := store.Transition(ctx, "FD-001", domain.StagePlan, "auto"); err != nil {
		t.Fatal(err)
	}
	evs, err := store.Events(ctx, "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs {
		if ev.Kind != state.EventGate || ev.Stage != domain.StageSpec {
			continue
		}
		var p state.GatePayload
		if err := json.Unmarshal([]byte(ev.Payload), &p); err != nil {
			t.Fatal(err)
		}
		if p.ID != "" {
			t.Errorf("uncorrelated crossing carries id %q, want empty", p.ID)
		}
	}
}
