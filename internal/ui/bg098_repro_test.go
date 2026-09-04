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

// TestBG098TwoPeriodsReadInTheOrderTheyHappened is BG-098's regression
// test. A card handed to autopilot twice drew the second run opening
// above the first one closing, so the two read as one period nested
// inside another and their timestamps ran backwards down the page — and
// the reason and tally belonging to the first run sat under a rule the
// reader would attribute to the second.
//
// The cause is that the moment one run ends and the next begins is a
// single fold apart: you pick a parked card back up and hand it straight
// to autopilot again, so the closing event and the opening event land in
// the same folded stage. The render grouped a segment's rules by kind —
// every opening above the receipt, every closing below it — which put
// that pair on the page in the opposite order to the log.
//
// The fixture is the seed board's own shape, which is where the drive
// found it: a headless run that stopped at --until spec, then the switch
// pressed on the card where it parked.
func TestBG098TwoPeriodsReadInTheOrderTheyHappened(t *testing.T) {
	ctx := context.Background()
	m := populatedShell(140, 40)
	ws, store, wt := uiRepo(t)
	m.Attach(store, wt, ws)

	at := time.Date(2026, 9, 4, 8, 7, 0, 0, time.UTC)
	later := time.Date(2026, 9, 4, 8, 15, 0, 0, time.UTC)
	f := mkFeature(t, store, 2, "profile applied across projects", domain.StagePlan)
	m.rows = []featureRow{{F: f}}
	m.sel = 0
	m.cardOpen = true

	enter, _ := json.Marshal(map[string]string{"role": "architect", "model": "demo", "flavor": "stage"})
	says, _ := json.Marshal(map[string]string{"author": string(engine.AuthorAssistant), "content": "converged"})
	exit, _ := json.Marshal(map[string]any{"credits": 18, "verdict": ""})
	first, _ := json.Marshal(state.AutopilotPayload{
		Event: state.AutopilotTookOver, Reason: "the headless run is driving it unattended", Mode: domain.GateGates,
	})
	stopped, _ := json.Marshal(state.ParkPayload{
		Reason: state.ParkReasonNeedsYou, Detail: "stopped early at --until spec, as requested.",
	})
	second, _ := json.Marshal(state.AutopilotPayload{
		Event: state.AutopilotTookOver, Reason: "you handed it to autopilot", Mode: domain.GateFull,
	})
	crossed, _ := json.Marshal(state.GatePayload{
		From: string(domain.StageSpec), To: string(domain.StagePlan), Actor: state.ActorAutopilot,
	})

	// The takeover the headless run writes lands before the stage it goes
	// on to open, which is why the first rule belongs above the receipt
	// and the second one — pressed on a card whose spec had already run —
	// does not.
	if err := store.AppendEvents(ctx, []state.CardEvent{
		{Feature: f.ID, Stage: domain.StageSpec, Kind: state.EventAutopilot, At: at, Payload: string(first), Dedupe: "ap:1"},
		{Feature: f.ID, Stage: domain.StageSpec, Kind: state.EventStageEnter, At: at, Payload: string(enter), Dedupe: "spec:enter"},
		{Feature: f.ID, Stage: domain.StageSpec, Kind: state.EventMessage, At: at, Payload: string(says), Dedupe: "spec:said"},
		{Feature: f.ID, Stage: domain.StageSpec, Kind: state.EventStageExit, At: at, Payload: string(exit), Dedupe: "spec:exit"},
		{Feature: f.ID, Stage: domain.StageSpec, Kind: state.EventPark, At: at, Payload: string(stopped), Dedupe: "spec:park"},
		{Feature: f.ID, Stage: domain.StageSpec, Kind: state.EventAutopilot, At: later, Payload: string(second), Dedupe: "ap:2"},
		{Feature: f.ID, Stage: domain.StageSpec, Kind: state.EventGate, At: later, Payload: string(crossed), Dedupe: "ap:gate"},
		{Feature: f.ID, Stage: domain.StagePlan, Kind: state.EventStageEnter, At: later, Payload: string(enter), Dedupe: "plan:enter"},
		{Feature: f.ID, Stage: domain.StagePlan, Kind: state.EventMessage, At: later, Payload: string(says), Dedupe: "plan:said"},
	}); err != nil {
		t.Fatal(err)
	}
	m = pump(t, m, m.loadCardEvents(f.ID))

	w, h := m.threadSize()
	body := ansi.Strip(m.threadView(w, h))

	// The rules the page draws, in the order it draws them. Reading the
	// sequence rather than searching for one string is the assertion the
	// defect needs: every rule was on the page, in the wrong order.
	var rules []string
	for _, line := range strings.Split(body, "\n") {
		for _, label := range []string{"autopilot took over", "autopilot parked it", "autopilot finished", "you took back control"} {
			if strings.Contains(line, label) {
				rules = append(rules, label)
			}
		}
	}
	want := []string{"autopilot took over", "autopilot parked it", "autopilot took over"}
	if strings.Join(rules, " | ") != strings.Join(want, " | ") {
		t.Fatalf("the two runs do not read in the order they happened\ngot  %v\nwant %v\n%s", rules, want, body)
	}

	// and the crossing the second run made belongs under the rule saying
	// the card changed hands, not above it
	handover := strings.LastIndex(body, "autopilot took over")
	crossing := strings.Index(body, "crossed spec → plan")
	if crossing < 0 {
		t.Fatalf("the second run's crossing is missing from the page\n%s", body)
	}
	if crossing < handover {
		t.Errorf("the crossing autopilot made after taking over is drawn above the rule saying it took over\n%s", body)
	}
}
