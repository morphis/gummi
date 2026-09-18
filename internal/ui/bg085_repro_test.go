package ui

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
)

// TestBG085PeriodClosesWhenAutopilotHandsOver is BG-085's regression
// test: a period must not outlive the handover. It stayed open for the
// rest of the card's life, and every later reading of the thread claimed
// a machine was driving across stages the reader went on to work by hand
// — the exact over-claim stretch.go's own header calls the one failure
// that matters.
//
// What closes it has changed, and the test follows the mechanism rather
// than the wording. When this was written the design stage was
// interactive, autopilot's arrival there was a stop nothing wrote down,
// and the fix was a render-time judgement that read the card's resting
// stage. Every stage is autonomous now, and the stop writes itself
// down: a stage that settles at a gate a person must cross raises the
// card's needs-you item, and that raise records a park (shell.go's
// parkAttentionItem). The period closes on the park, carrying the gate's
// own sentence as the reason — which is more than the judgement could
// say. Reading the stage instead closed periods on cards that were
// still running (BG-105).
func TestBG085PeriodClosesWhenAutopilotHandsOver(t *testing.T) {
	f := aFeature()
	f.Stage = domain.StagePlan

	events := []state.CardEvent{
		evTookOver(domain.GateAttended, at(0)),
		{Kind: state.EventStageEnter, Stage: domain.StagePlan, At: at(1)},
		evExit(domain.StagePlan, "", at(6)),
		evPark(domain.StagePlan, "plan critiqued: clean — review & approve", at(6)),
	}

	// live: the TUI that ran the stage is still up and still holds the
	// card's live file, which is the case the defect lived in.
	st := onlyStretch(t, closeOrphaned(autopilotStretches(events), events, true))
	if st.running() {
		t.Fatal("the period is still open while the card waits at a gate for you")
	}
	if st.closed != stretchParked {
		t.Errorf("closed = %q, want %q", st.closed, stretchParked)
	}
	// dated from the stop itself, not from now
	if !st.closedAt.Equal(at(6)) {
		t.Errorf("closedAt = %v, want the park's own stamp %v", st.closedAt, at(6))
	}
	if st.reason == "" {
		t.Error("the park's reason was dropped — it is what the rule tells the reader to go and do")
	}

	// and it is NOT reported as a crash: orphaned is reserved for a
	// driver that died, and reusing it here would tell the reader
	// something went wrong when nothing did.
	if st.closed == stretchOrphaned {
		t.Error("a designed handover is reported as a driver that stopped without saying so")
	}
}

// TestBG085AutonomousStageKeepsThePeriodOpen: a card resting mid-route
// at a stage autopilot is driving is still autopilot's, and its period
// must stay open — otherwise closing would swallow every period the
// moment a stage ended.
func TestBG085AutonomousStageKeepsThePeriodOpen(t *testing.T) {
	f := aFeature()
	f.Stage = domain.StageImplement

	events := []state.CardEvent{
		evTookOver(domain.GateAttended, at(0)),
		evGate(domain.StagePlan, domain.StageImplement, state.ActorAutopilot, at(6)),
	}
	st := onlyStretch(t, autopilotStretches(events))
	if !st.running() {
		t.Fatalf("closed = %q, want the period still open — implement is autopilot's to drive", st.closed)
	}

	// and with the driver gone, that same period reads as orphaned, so
	// BG-059's judgement still lands on the case it was made for
	st = onlyStretch(t, closeOrphaned(autopilotStretches(events), events, false))
	if st.closed != stretchOrphaned {
		t.Errorf("closed = %q, want %q for a dead driver mid-route", st.closed, stretchOrphaned)
	}
}

// TestBG085HandoverRuleIsActuallyDrawn is the same defect at the surface
// it was seen on. Closing the period in the derivation is only half of
// it: the thread has to place the closing rule, and a card carrying a
// finished stage segment drew none at all — the state said the card was
// back with the reader and the screen still said a machine had it.
func TestBG085HandoverRuleIsActuallyDrawn(t *testing.T) {
	ctx := context.Background()
	m := populatedShell(120, 34)
	ws, store, wt := uiRepo(t)
	m.Attach(store, wt, ws)

	f := mkFeature(t, store, 7, "snapshot retention across backends", domain.StagePlan)
	m.rows = []featureRow{{F: f}}
	m.sel = 0
	m.cardOpen = true

	stamp := time.Date(2026, 9, 3, 20, 53, 0, 0, time.UTC)
	took, _ := json.Marshal(state.AutopilotPayload{Event: state.AutopilotTookOver, Mode: domain.GateAttended})
	enter, _ := json.Marshal(map[string]string{"role": "architect", "model": "demo", "flavor": "stage"})
	says, _ := json.Marshal(map[string]string{"author": string(engine.AuthorAssistant), "content": "Done."})
	exit, _ := json.Marshal(map[string]any{"verdict": "", "credits": 18})
	park, _ := json.Marshal(state.ParkPayload{
		Reason: state.ParkReasonNeedsYou, Detail: "plan critiqued: clean — review & approve",
	})
	if err := store.AppendEvents(ctx, []state.CardEvent{
		{Feature: f.ID, Stage: domain.StagePlan, Kind: state.EventAutopilot, At: stamp, Payload: string(took), Dedupe: "took"},
		{Feature: f.ID, Stage: domain.StagePlan, Kind: state.EventStageEnter, At: stamp, Payload: string(enter), Dedupe: "enter"},
		{Feature: f.ID, Stage: domain.StagePlan, Kind: state.EventMessage, At: stamp.Add(time.Minute), Payload: string(says), Dedupe: "said"},
		{Feature: f.ID, Stage: domain.StagePlan, Kind: state.EventStageExit, At: stamp.Add(time.Minute), Payload: string(exit), Dedupe: "exit"},
		{Feature: f.ID, Stage: domain.StagePlan, Kind: state.EventPark, At: stamp.Add(2 * time.Minute), Payload: string(park), Dedupe: "park"},
	}); err != nil {
		t.Fatal(err)
	}
	m = pump(t, m, m.loadCardEvents(f.ID))

	w, h := m.threadSize()
	body := ansi.Strip(m.threadView(w, h))

	if !strings.Contains(body, "autopilot took over") {
		t.Fatalf("precondition: the period never opened\n%s", body)
	}
	if !strings.Contains(body, "autopilot parked it") {
		t.Errorf("the period was closed in the derivation but no closing rule reached the screen:\n%s", body)
	}
}
