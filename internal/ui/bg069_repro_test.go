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

// TestBG069StageBoundaryRefreshesOpenThreadHistory is BG-069's
// regression test. Everything above the live stage in the thread — the
// folded receipt for each finished stage session, the period rules
// around them — is composed from featureRow.Events, and that snapshot
// used to be taken only when the card page opened or the selection moved
// on it. A stage that started and finished while the page stayed open
// therefore left no receipt behind: the live block moved on and the
// stage before it disappeared from the history until the reader left the
// card and came back.
//
// The engine says when the log has grown, and says it after the write:
// Stopped follows the setState(StateDone)/persist pair that records
// stage_exit. So the event that closes a stage is what this drives.
func TestBG069StageBoundaryRefreshesOpenThreadHistory(t *testing.T) {
	ctx := context.Background()
	m := populatedShell(120, 34)
	ws, store, wt := uiRepo(t)
	m.Attach(store, wt, ws)

	f := mkFeature(t, store, 1, "warn across projects", domain.StagePlan)
	m.rows = []featureRow{{F: f}}
	m.sel = 0
	m.cardOpen = true

	at := time.Date(2026, 9, 3, 15, 15, 0, 0, time.UTC)
	enter := func(stage domain.Stage, role string, at time.Time) state.CardEvent {
		p, _ := json.Marshal(map[string]string{"role": role, "model": "demo", "flavor": "stage"})
		return state.CardEvent{
			Feature: f.ID, Stage: stage, Kind: state.EventStageEnter,
			At: at, Payload: string(p), Dedupe: role + ":" + string(stage) + ":enter",
		}
	}
	says := func(stage domain.Stage, text string, at time.Time) state.CardEvent {
		p, _ := json.Marshal(map[string]string{"author": string(engine.AuthorAssistant), "content": text})
		return state.CardEvent{
			Feature: f.ID, Stage: stage, Kind: state.EventMessage,
			At: at, Payload: string(p), Dedupe: text,
		}
	}
	exit := func(stage domain.Stage, at time.Time) state.CardEvent {
		p, _ := json.Marshal(map[string]any{"verdict": "", "credits": 18})
		return state.CardEvent{
			Feature: f.ID, Stage: stage, Kind: state.EventStageExit,
			At: at, Payload: string(p), Dedupe: string(stage) + ":exit",
		}
	}

	// the log as it stood when the page opened: the plan stage ran and is
	// the newest thing there is.
	if err := store.AppendEvents(ctx, []state.CardEvent{
		enter(domain.StagePlan, "architect", at),
		says(domain.StagePlan, "PLANNED-THE-WORK", at),
	}); err != nil {
		t.Fatal(err)
	}
	m = pump(t, m, m.loadCardEvents(f.ID))

	body := func() string {
		w, h := m.threadSize()
		return ansi.Strip(m.threadView(w, h))
	}
	if !strings.Contains(body(), "PLANNED-THE-WORK") {
		t.Fatal("precondition: the plan stage is not in the thread at all")
	}

	// the plan stage closes and implement runs a turn, all while the card
	// page stays open — exactly what the engine writes as it crosses a
	// gate on its own.
	if err := store.AppendEvents(ctx, []state.CardEvent{
		exit(domain.StagePlan, at.Add(time.Minute)),
		enter(domain.StageImplement, "implementer", at.Add(2*time.Minute)),
		says(domain.StageImplement, "IMPLEMENTED-THE-WORK", at.Add(3*time.Minute)),
	}); err != nil {
		t.Fatal(err)
	}

	model, cmd := m.Update(engineEventMsg{ev: engine.Event{
		Feature: f.ID, Stage: domain.StagePlan, Kind: engine.EventStopped,
	}})
	m = pump(t, model.(*Shell), cmd)

	got := body()
	if !strings.Contains(got, "IMPLEMENTED-THE-WORK") {
		t.Error("the stage that started while the page was open never reached the thread")
	}
	// the stage that closed is the point: it must still be in the history,
	// as its own folded receipt, not erased by the one that replaced it.
	if !strings.Contains(got, "plan · architect") {
		t.Error("the stage that finished while the page was open left no folded receipt")
	}
	if !strings.Contains(got, "PLANNED-THE-WORK") && !strings.Contains(got, "plan · architect") {
		t.Error("the plan stage vanished from the thread entirely")
	}
	if t.Failed() {
		t.Logf("thread body:\n%s", got)
	}
}
