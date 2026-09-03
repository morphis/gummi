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
// test.
//
// autopilotStretches closes a period on four things: a handback row, a
// park, a gate a person crossed, or a turn a person typed. Autopilot
// carrying a card into an interactive stage is none of them — it writes
// a machine gate crossing, which is autopilot working rather than
// stopping — and it writes no park either, because it did not give up
// on anything: it arrived at the stage it was always going to stop at.
// closeOrphaned could not cover the gap and must not, since the process
// that opened the period is normally still alive and holding the card,
// so the liveness test correctly answers "running".
//
// The period therefore stayed open for the rest of the card's life, and
// every later reading of the thread claimed a machine was driving across
// stages the reader went on to work by hand — the exact over-claim
// stretch.go's own header calls the one failure that matters.
func TestBG085PeriodClosesWhenAutopilotHandsOver(t *testing.T) {
	// a research card: autopilot runs investigate, crosses into shape,
	// and stops, because shape is interactive.
	f := aFeature()
	f.Kind = domain.KindResearch
	f.Stage = domain.StageShape

	events := []state.CardEvent{
		evTookOver(domain.GateGates, at(0)),
		evGate(domain.StageInvestigate, domain.StageShape, state.ActorAutopilot, at(6)),
	}

	// live: the TUI that ran the stage is still up and still holds the
	// card's live file, which is the case the defect lived in.
	st := onlyStretch(t, closeOrphaned(closeHandedOver(f, autopilotStretches(f, events), events), true))
	if st.running() {
		t.Fatal("the period is still open while the card waits at an interactive stage")
	}
	if st.closed != stretchHandedOver {
		t.Errorf("closed = %q, want %q", st.closed, stretchHandedOver)
	}
	if got := stretchLabel(st.closed); got != "autopilot handed it to you" {
		t.Errorf("the closing rule reads %q", got)
	}
	// dated from the crossing that brought the card here, not from now
	if !st.closedAt.Equal(at(6)) {
		t.Errorf("closedAt = %v, want the crossing's own stamp %v", st.closedAt, at(6))
	}

	// and it is NOT reported as a crash: orphaned is reserved for a
	// driver that died, and reusing it here would tell the reader
	// something went wrong when nothing did.
	if st.closed == stretchOrphaned {
		t.Error("a designed handover is reported as a driver that stopped without saying so")
	}
}

// TestBG085AutonomousStageKeepsThePeriodOpen: the judgement is scoped to
// stages autopilot may not drive. A card resting mid-route at an
// autonomous stage is still autopilot's, and its period must stay open —
// otherwise this fix would close every period the moment a stage ended.
func TestBG085AutonomousStageKeepsThePeriodOpen(t *testing.T) {
	f := aFeature()
	f.Stage = domain.StageImplement

	events := []state.CardEvent{
		evTookOver(domain.GateGates, at(0)),
		evGate(domain.StagePlan, domain.StageImplement, state.ActorAutopilot, at(6)),
	}
	st := onlyStretch(t, closeHandedOver(f, autopilotStretches(f, events), events))
	if !st.running() {
		t.Fatalf("closed = %q, want the period still open — implement is autopilot's to drive", st.closed)
	}

	// and with the driver gone, that same period still reads as orphaned,
	// so BG-059's judgement is not shadowed by this one
	st = onlyStretch(t, closeOrphaned(closeHandedOver(f, autopilotStretches(f, events), events), false))
	if st.closed != stretchOrphaned {
		t.Errorf("closed = %q, want %q for a dead driver mid-route", st.closed, stretchOrphaned)
	}
}

// TestBG085AlreadyClosedPeriodIsLeftAlone: a period the log itself
// closed keeps the ending the log gave it, even on a card sitting at an
// interactive stage. The park's own reason is more specific than
// anything derived here.
func TestBG085AlreadyClosedPeriodIsLeftAlone(t *testing.T) {
	f := aFeature()
	f.Kind = domain.KindResearch
	f.Stage = domain.StageShape

	events := []state.CardEvent{
		evTookOver(domain.GateGates, at(0)),
		evGate(domain.StageInvestigate, domain.StageShape, state.ActorAutopilot, at(6)),
		evPark(domain.StageShape, "stopped early at --until shape, as requested", at(8)),
	}
	st := onlyStretch(t, closeHandedOver(f, autopilotStretches(f, events), events))
	if st.closed != stretchParked {
		t.Errorf("closed = %q, want %q — the log's own ending wins", st.closed, stretchParked)
	}
	if st.reason == "" {
		t.Error("the park's reason was dropped")
	}
}

// TestBG085HandoverRuleIsActuallyDrawn is the same defect at the surface
// it was seen on. Closing the period in the derivation is only half of
// it: the thread places a closing rule at the index of the event that
// closed it, and a period closed by a query-time judgement closes at the
// end of the log, where there is no such event. So a card carrying a
// finished stage segment drew no closing rule at all — the state said
// handed over and the screen still said a machine had the card.
func TestBG085HandoverRuleIsActuallyDrawn(t *testing.T) {
	ctx := context.Background()
	m := populatedShell(120, 34)
	ws, store, wt := uiRepo(t)
	m.Attach(store, wt, ws)

	f := mkFeature(t, store, 7, "snapshot retention across backends", domain.StageShape)
	f.Kind = domain.KindResearch
	m.rows = []featureRow{{F: f}}
	m.sel = 0
	m.cardOpen = true

	stamp := time.Date(2026, 9, 3, 20, 53, 0, 0, time.UTC)
	took, _ := json.Marshal(state.AutopilotPayload{Event: state.AutopilotTookOver, Mode: domain.GateGates})
	enter, _ := json.Marshal(map[string]string{"role": "architect", "model": "demo", "flavor": "stage"})
	says, _ := json.Marshal(map[string]string{"author": string(engine.AuthorAssistant), "content": "Done."})
	exit, _ := json.Marshal(map[string]any{"verdict": "", "credits": 18})
	cross, _ := json.Marshal(state.GatePayload{
		From: string(domain.StageInvestigate), To: string(domain.StageShape), Actor: state.ActorAutopilot,
	})
	if err := store.AppendEvents(ctx, []state.CardEvent{
		{Feature: f.ID, Stage: domain.StageInvestigate, Kind: state.EventAutopilot, At: stamp, Payload: string(took), Dedupe: "took"},
		{Feature: f.ID, Stage: domain.StageInvestigate, Kind: state.EventStageEnter, At: stamp, Payload: string(enter), Dedupe: "enter"},
		{Feature: f.ID, Stage: domain.StageInvestigate, Kind: state.EventMessage, At: stamp.Add(time.Minute), Payload: string(says), Dedupe: "said"},
		{Feature: f.ID, Stage: domain.StageInvestigate, Kind: state.EventStageExit, At: stamp.Add(time.Minute), Payload: string(exit), Dedupe: "exit"},
		{Feature: f.ID, Stage: domain.StageInvestigate, Kind: state.EventGate, At: stamp.Add(2 * time.Minute), Payload: string(cross), Dedupe: "cross"},
	}); err != nil {
		t.Fatal(err)
	}
	m = pump(t, m, m.loadCardEvents(f.ID))

	w, h := m.threadSize()
	body := ansi.Strip(m.threadView(w, h))

	if !strings.Contains(body, "autopilot took over") {
		t.Fatalf("precondition: the period never opened\n%s", body)
	}
	if !strings.Contains(body, "autopilot handed it to you") {
		t.Errorf("the period was closed in the derivation but no closing rule reached the screen:\n%s", body)
	}
}
