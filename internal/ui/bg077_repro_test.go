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

// TestBG077GateCrossingRefreshesOpenThreadHistory is BG-077's regression
// test. The thread's history is composed from featureRow.Events, and a
// gate crossing records its transition in that log — but the crossing's
// own outcome only ever asked for a row reload, and rows are where the
// masthead and the stage rail come from, not the history. So a crossing
// made with nothing running moved the rail and left the thread behind:
// the newest receipt still named the previous transition, and the page
// disagreed with itself until the reader left the card and came back.
//
// BG-069's refresh covers the other half of this — a crossing that
// follows a live stage stops that session, and the resulting Stopped
// event reloads the log — which is exactly why the same card could show
// a receipt for one crossing and not the next.
//
// Both outcomes a successful crossing can return are driven here: the
// plain notice, and the one an approval that enters a worktree returns
// instead. The drive found it on a bug card at the diagnose gate; the
// path is shared by every kind.
func TestBG077GateCrossingRefreshesOpenThreadHistory(t *testing.T) {
	at := time.Date(2026, 9, 3, 16, 18, 0, 0, time.UTC)

	// open renders a card page whose log holds one finished-looking stage
	// and the crossing that ended it, then appends a second crossing —
	// the one made with nothing running — without telling the page.
	open := func(t *testing.T) (*Shell, domain.Feature, func() string) {
		t.Helper()
		ctx := context.Background()
		m := populatedShell(120, 34)
		ws, store, wt := uiRepo(t)
		m.Attach(store, wt, ws)

		f := mkFeature(t, store, 4, "file pull truncation", domain.StagePlan)
		m.rows = []featureRow{{F: f}}
		m.sel = 0
		m.cardOpen = true

		enter, _ := json.Marshal(map[string]string{"role": "architect", "model": "demo", "flavor": "stage"})
		says, _ := json.Marshal(map[string]string{"author": string(engine.AuthorAssistant), "content": "SHAPED-THE-WORK"})
		first, _ := json.Marshal(state.GatePayload{From: string(domain.StageBrainstorm), To: string(domain.StageSpec), Actor: state.ActorUser})
		if err := store.AppendEvents(ctx, []state.CardEvent{
			{Feature: f.ID, Stage: domain.StageSpec, Kind: state.EventStageEnter, At: at, Payload: string(enter), Dedupe: "spec:enter"},
			{Feature: f.ID, Stage: domain.StageSpec, Kind: state.EventMessage, At: at.Add(time.Minute), Payload: string(says), Dedupe: "said"},
			{Feature: f.ID, Stage: domain.StageSpec, Kind: state.EventGate, At: at.Add(2 * time.Minute), Payload: string(first), Dedupe: "gate:1"},
		}); err != nil {
			t.Fatal(err)
		}
		m = pump(t, m, m.loadCardEvents(f.ID))

		body := func() string {
			w, h := m.threadSize()
			return ansi.Strip(m.threadView(w, h))
		}
		if !strings.Contains(body(), "brainstorm → spec") {
			t.Fatalf("precondition: the first crossing is not in the thread\n%s", body())
		}

		// the crossing under test: the engine has written it to the log and
		// moved the card, exactly as engine.Advance does, and the page has
		// not been told.
		second, _ := json.Marshal(state.GatePayload{From: string(domain.StageSpec), To: string(domain.StagePlan), Actor: state.ActorUser})
		if err := store.AppendEvents(ctx, []state.CardEvent{
			{Feature: f.ID, Stage: domain.StagePlan, Kind: state.EventGate, At: at.Add(3 * time.Minute), Payload: string(second), Dedupe: "gate:2"},
		}); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(body(), "spec → plan") {
			t.Fatal("precondition: the page already shows the crossing before its outcome was delivered")
		}
		return m, f, body
	}

	t.Run("plain crossing", func(t *testing.T) {
		m, f, body := open(t)
		model, cmd := m.Update(noticeMsg{
			text: string(f.ID) + " → plan", reload: true, clearInbox: f.ID,
		})
		m = pump(t, model.(*Shell), cmd)
		if got := body(); !strings.Contains(got, "spec → plan") {
			t.Errorf("the crossing left no receipt in the open thread:\n%s", got)
		}
	})

	t.Run("crossing into a worktree", func(t *testing.T) {
		m, f, body := open(t)
		model, cmd := m.Update(worktreeEnteredMsg{id: f.ID, note: string(f.ID) + " → plan"})
		m = pump(t, model.(*Shell), cmd)
		if got := body(); !strings.Contains(got, "spec → plan") {
			t.Errorf("the approval that entered a worktree left no receipt in the open thread:\n%s", got)
		}
	})
}
