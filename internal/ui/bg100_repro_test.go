package ui

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui/theme"
)

// TestBG100GaveUpNeverReadsAsFinished is BG-100's regression test. The
// rule that closes an unattended run at the landing gate chooses between
// "autopilot finished" and "autopilot parked it" by reading the verdict
// the stage exited on — and a stage that could not reach a verdict at
// all exits with an empty one, which is not a failure and so read as
// success. So a card whose verification was blocked closed with the same
// congratulation as one whose verification passed.
//
// The park event says which of the two happened: gave-up is a stop at a
// decision only a person can take (state.ParkReasonGaveUp's own
// definition), needs-you is everything else. Both reasons are driven
// here from one fixture that differs in nothing else, because the defect
// was precisely that two cards differing only in that field read the
// same — and the assertion is against the rendered thread, since
// deciding how a period closed is not the same as drawing it.
func TestBG100GaveUpNeverReadsAsFinished(t *testing.T) {
	page := func(t *testing.T, reason, detail string) string {
		t.Helper()
		ctx := context.Background()
		ws, store, wt := uiRepo(t)
		at := time.Date(2026, 9, 4, 8, 9, 0, 0, time.UTC)

		f := mkFeature(t, store, 2, "profile applied across projects", domain.StageVerify)

		enter, _ := json.Marshal(map[string]string{"role": "reviewer", "model": "demo", "flavor": "stage"})
		says, _ := json.Marshal(map[string]string{"author": string(engine.AuthorAssistant), "content": "ran the checks"})
		// the verdict every stage in this workflow actually exits on: the
		// field is empty unless something wrote one, which is the whole
		// reason it cannot be the discriminator.
		exit, _ := json.Marshal(map[string]any{"credits": 18, "verdict": ""})
		took, _ := json.Marshal(state.AutopilotPayload{
			Event: state.AutopilotTookOver, Reason: "you handed it to autopilot", Mode: domain.GateFull,
		})
		crossed, _ := json.Marshal(state.GatePayload{
			From: string(domain.StageReview), To: string(domain.StageVerify), Actor: state.ActorAutopilot,
		})
		parked, _ := json.Marshal(state.ParkPayload{Reason: reason, Detail: detail})

		if err := store.AppendEvents(ctx, []state.CardEvent{
			{Feature: f.ID, Stage: domain.StageReview, Kind: state.EventStageEnter, At: at, Payload: string(enter), Dedupe: "rev:enter"},
			{Feature: f.ID, Stage: domain.StageReview, Kind: state.EventAutopilot, At: at.Add(time.Second), Payload: string(took), Dedupe: "ap:took"},
			{Feature: f.ID, Stage: domain.StageReview, Kind: state.EventStageExit, At: at.Add(2 * time.Second), Payload: string(exit), Dedupe: "rev:exit"},
			{Feature: f.ID, Stage: domain.StageReview, Kind: state.EventGate, At: at.Add(3 * time.Second), Payload: string(crossed), Dedupe: "ap:gate"},
			{Feature: f.ID, Stage: domain.StageVerify, Kind: state.EventStageEnter, At: at.Add(4 * time.Second), Payload: string(enter), Dedupe: "ver:enter"},
			{Feature: f.ID, Stage: domain.StageVerify, Kind: state.EventMessage, At: at.Add(5 * time.Second), Payload: string(says), Dedupe: "ver:said"},
			{Feature: f.ID, Stage: domain.StageVerify, Kind: state.EventStageExit, At: at.Add(6 * time.Second), Payload: string(exit), Dedupe: "ver:exit"},
			{Feature: f.ID, Stage: domain.StageVerify, Kind: state.EventPark, At: at.Add(7 * time.Second), Payload: string(parked), Dedupe: "ver:park"},
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveSession(ctx, state.SessionSnapshot{
			Feature: f.ID, Stage: domain.StageVerify, Role: "reviewer", State: "done",
		}); err != nil {
			t.Fatal(err)
		}
		eng := engine.New(engine.Config{
			Agents: singleAgent(agent.NewFake("ok")), Store: store, Pool: wt, Workspace: ws, Model: "demo", Persist: true,
		})
		t.Cleanup(func() { eng.Close() })
		if err := eng.Restore(ctx); err != nil {
			t.Fatal(err)
		}

		m := NewShell(theme.GummiDark(), "v0-test")
		sized, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
		m = sized.(*Shell)
		m.Attach(store, wt, ws)
		m.AttachEngine(eng)
		m.rows = []featureRow{{F: f}}
		m.sel = 0
		m.cardOpen = true
		m = pump(t, m, m.loadCardEvents(f.ID))
		w, h := m.threadSize()
		return ansi.Strip(m.threadView(w, h))
	}

	passed := page(t, state.ParkReasonNeedsYou, "verify passed — review & land on main")
	if !strings.Contains(passed, "autopilot finished") {
		t.Fatalf("a run that reached the landing gate cleanly no longer says it finished\n%s", passed)
	}

	blocked := page(t, state.ParkReasonGaveUp, "verify BLOCKED — the environment can't run the verification plan")
	if strings.Contains(blocked, "autopilot finished") {
		t.Errorf("a run that gave up is reported as finished\n%s", blocked)
	}
	if !strings.Contains(blocked, "autopilot parked it") {
		t.Errorf("a run that gave up never says it parked the card\n%s", blocked)
	}
}
