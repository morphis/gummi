package ui

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/livelog"
	"github.com/morphis/gummi/internal/state"
)

// TestBG105HandoverOnARunningCardKeepsThePeriodOpen is BG-105's
// regression test.
//
// Pressing the autopilot switch on a card whose plan stage is running
// drew both rules at once — "autopilot took over" immediately followed
// by "autopilot handed it to you" — while the masthead went on saying
// autopilot was on and the stage went on writing the plan. Nothing had
// been handed anywhere: the period was closed by a render-time
// judgement that asked whether the card was at plan, back when plan was
// the interactive stage a machine was not allowed to drive. It is an
// autonomous stage now, so that question answers the wrong thing about
// every card in the design phase, running or not.
//
// A period ends when the log says it did. While a stage is running and
// nothing has stopped it, the period is open.
func TestBG105HandoverOnARunningCardKeepsThePeriodOpen(t *testing.T) {
	f := aFeature()
	f.Stage = domain.StagePlan

	// the switch pressed on a card already working: the stage was
	// entered before anyone handed it over, and it is still running.
	events := []state.CardEvent{
		{Kind: state.EventStageEnter, Stage: domain.StagePlan, At: at(0)},
		evTookOver(domain.GateAutopilot, at(4)),
	}

	st := onlyStretch(t, closeOrphaned(autopilotStretches(events), events, true))
	if !st.running() {
		t.Fatalf("closed = %q, want the period still open — the plan stage is running and nothing stopped it", st.closed)
	}
}

// TestBG105TheThreadDrawsNoClosingRuleForARunningHandover is the same
// defect at the surface it was seen on: two rules a minute apart at the
// foot of a card that was still writing its plan.
func TestBG105TheThreadDrawsNoClosingRuleForARunningHandover(t *testing.T) {
	ctx := context.Background()
	m := populatedShell(120, 34)
	ws, store, wt := uiRepo(t)
	m.Attach(store, wt, ws)

	f := mkFeature(t, store, 11, "use the rhea machine to test drive charmed-openshell", domain.StagePlan)
	m.rows = []featureRow{{F: f}}
	m.sel = 0
	m.cardOpen = true

	// the stage is running here and now, in this very process: the live
	// file names a pid that answers, so the card is not a candidate for
	// the one closing made outside the log (closeOrphaned, BG-059).
	w, err := livelog.Create(ws.LiveFile(f.ID), livelog.Record{
		Feature: string(f.ID), Stage: string(f.Stage), PID: os.Getpid(),
	})
	if err != nil {
		t.Fatalf("create live file: %v", err)
	}
	w.Close()

	stamp := time.Date(2026, 9, 16, 7, 48, 0, 0, time.UTC)
	enter, _ := json.Marshal(map[string]string{"role": "architect", "model": "demo", "flavor": "stage"})
	took, _ := json.Marshal(state.AutopilotPayload{
		Event: state.AutopilotTookOver, Reason: "you handed it to autopilot", Mode: domain.GateAutopilot,
	})
	// the stage is still running: it entered, it is talking, and it has
	// written no exit, no park and no handback.
	says, _ := json.Marshal(map[string]string{"author": string(engine.AuthorAssistant), "content": "Reading the machine's devctl config."})
	if err := store.AppendEvents(ctx, []state.CardEvent{
		{Feature: f.ID, Stage: domain.StagePlan, Kind: state.EventStageEnter, At: stamp, Payload: string(enter), Dedupe: "enter"},
		{Feature: f.ID, Stage: domain.StagePlan, Kind: state.EventAutopilot, At: stamp.Add(10 * time.Minute), Payload: string(took), Dedupe: "took"},
		{Feature: f.ID, Stage: domain.StagePlan, Kind: state.EventMessage, At: stamp.Add(11 * time.Minute), Payload: string(says), Dedupe: "said"},
	}); err != nil {
		t.Fatal(err)
	}
	m = pump(t, m, m.loadCardEvents(f.ID))

	tw, th := m.threadSize()
	body := ansi.Strip(m.threadView(tw, th))

	if !strings.Contains(body, "autopilot took over") {
		t.Fatalf("precondition: the period never opened\n%s", body)
	}
	for _, closing := range []string{
		"autopilot handed it to you", "autopilot parked it",
		"autopilot finished", "you took back control",
		"autopilot stopped without saying so",
	} {
		if strings.Contains(body, closing) {
			t.Errorf("the thread says %q about a card whose stage is still running:\n%s", closing, body)
		}
	}
}

// TestBG105ACardComeToRestClosesFromItsPark is the other half, and the
// reason the render-time judgement is not needed: when autopilot really
// does run out of things it may do, the stop is written down. A stage
// that settles at a gate a person must cross raises the card's
// needs-you item, and that raise records a park (shell.go's
// parkAttentionItem) — which closes the period, with the gate's own
// sentence as the reason.
func TestBG105ACardComeToRestClosesFromItsPark(t *testing.T) {
	f := aFeature()
	f.Stage = domain.StagePlan

	events := []state.CardEvent{
		evTookOver(domain.GateAttended, at(0)),
		{Kind: state.EventStageEnter, Stage: domain.StagePlan, At: at(1)},
		evExit(domain.StagePlan, "", at(20)),
		evPark(domain.StagePlan, "plan critiqued: clean — review & approve", at(20)),
	}

	st := onlyStretch(t, closeOrphaned(autopilotStretches(events), events, true))
	if st.closed != stretchParked {
		t.Fatalf("closed = %q, want %q — the gate's own park ended it", st.closed, stretchParked)
	}
	if !st.closedAt.Equal(at(20)) {
		t.Errorf("closedAt = %v, want the park's own stamp %v", st.closedAt, at(20))
	}
	if st.reason == "" {
		t.Error("the park's reason was dropped — it is what the rule tells the reader to go and do")
	}
}
