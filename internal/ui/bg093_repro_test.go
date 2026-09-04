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

// TestBG093AStageThatNeverStartedKeepsItsOwnHeading is BG-093's
// regression test.
//
// The thread's stage blocks are opened by stage_enter events, and every
// other event used to be appended to whichever block was open — without
// looking at the stage the event itself carries. A stage whose session
// never started writes no stage_enter: an agent backend that refuses the
// stage outright leaves nothing behind but the park receipt saying why.
// That receipt was therefore drawn inside the previous stage's block,
// under its heading, its role and its model — so the reader was told the
// architect's shape session had failed with the reviewer's error, and
// went to change the wrong configuration.
//
// It only showed after a restart: while the refused session is still in
// memory the card page renders a live block for it instead, so the
// attribution looked right until the process ended and the page fell
// back to the event log. The DB was never wrong — card_events records
// the receipt at review — which is why this asserts against the rendered
// page and not the reconstruction alone.
func TestBG093AStageThatNeverStartedKeepsItsOwnHeading(t *testing.T) {
	at := time.Date(2026, 9, 4, 5, 52, 0, 0, time.UTC)
	const refusal = "backend cannot enforce a read-only research session"

	ctx := context.Background()
	m := populatedShell(140, 40)
	ws, store, wt := uiRepo(t)
	m.Attach(store, wt, ws)

	f := mkFeature(t, store, 5, "quota accounting", domain.StageReview)
	m.rows = []featureRow{{F: f}}
	m.sel = 0
	m.cardOpen = true

	enter, _ := json.Marshal(map[string]string{"role": "architect", "model": "demo-architect", "flavor": "stage"})
	says, _ := json.Marshal(map[string]string{"author": string(engine.AuthorAssistant), "content": "SHAPED-IT"})
	gate, _ := json.Marshal(state.GatePayload{
		From: string(domain.StageShape), To: string(domain.StageReview), Actor: state.ActorUser,
	})
	park, _ := json.Marshal(state.ParkPayload{Reason: "needs-you", Detail: refusal})
	// exactly what the engine leaves behind: the shape session, the
	// crossing (stamped with the stage it left), and the refused review's
	// park — with no stage_enter for review, because no session was ever
	// created for it.
	if err := store.AppendEvents(ctx, []state.CardEvent{
		{Feature: f.ID, Stage: domain.StageShape, Kind: state.EventStageEnter, At: at, Payload: string(enter), Dedupe: "shape:enter"},
		{Feature: f.ID, Stage: domain.StageShape, Kind: state.EventMessage, At: at.Add(time.Minute), Payload: string(says), Dedupe: "said"},
		{Feature: f.ID, Stage: domain.StageShape, Kind: state.EventGate, At: at.Add(2 * time.Minute), Payload: string(gate), Dedupe: "gate:1"},
		{Feature: f.ID, Stage: domain.StageReview, Kind: state.EventPark, At: at.Add(3 * time.Minute), Payload: string(park), Dedupe: "park:1"},
	}); err != nil {
		t.Fatal(err)
	}
	m = pump(t, m, m.loadCardEvents(f.ID))

	// the reconstruction: the receipt belongs to a review segment, not to
	// the shape session that happened to be the last one open.
	segs := stageSegments(m.cardEvents[f.ID])
	var found bool
	for _, seg := range segs {
		for _, ev := range seg.events {
			if ev.Kind != state.EventPark {
				continue
			}
			found = true
			if seg.stage != domain.StageReview {
				t.Errorf("the review park was filed under the %s segment", seg.stage)
			}
			if seg.role != "" || seg.model != "" {
				t.Errorf("a stage that never started was given a role/model: %q/%q", seg.role, seg.model)
			}
		}
	}
	if !found {
		t.Fatal("the park receipt reached no segment at all")
	}

	// and the page: the heading above the receipt names review. Asserting
	// on threadView rather than on the segments alone is the lesson
	// BG-085 left — deciding which block an event belongs to is not the
	// same as drawing it there.
	w, h := m.threadSize()
	lines := strings.Split(ansi.Strip(m.threadView(w, h)), "\n")
	parkAt := -1
	for i, l := range lines {
		if strings.Contains(l, "parked") && strings.Contains(l, refusal[:30]) {
			parkAt = i
			break
		}
	}
	if parkAt < 0 {
		t.Fatalf("the park receipt is not on the page:\n%s", strings.Join(lines, "\n"))
	}
	heading := ""
	for i := parkAt - 1; i >= 0; i-- {
		if strings.Contains(lines[i], "─") && (strings.Contains(lines[i], "review") || strings.Contains(lines[i], "shape")) {
			heading = lines[i]
			break
		}
	}
	if heading == "" {
		t.Fatalf("no stage heading above the receipt:\n%s", strings.Join(lines, "\n"))
	}
	if !strings.Contains(heading, "review") {
		t.Errorf("the receipt sits under %q, want a review heading", strings.TrimSpace(heading))
	}
	// and the heading must not describe a session that never happened:
	// "fresh context" is a fact about starting one.
	if strings.Contains(heading, "fresh context") {
		t.Errorf("the heading for a stage that never started claims a fresh context: %q", strings.TrimSpace(heading))
	}
}

// TestBG093SameStageEventsStayInOneBlock is the other half: the fix must
// not split a block every time an event arrives. Everything recorded at
// the stage whose session is open still belongs to that one segment,
// including the crossing that ends it — gate events are stamped with the
// stage they leave, so they close the block they belong to rather than
// opening one for the stage ahead.
func TestBG093SameStageEventsStayInOneBlock(t *testing.T) {
	at := time.Date(2026, 9, 4, 5, 52, 0, 0, time.UTC)
	enter, _ := json.Marshal(map[string]string{"role": "architect", "model": "demo", "flavor": "stage"})
	says, _ := json.Marshal(map[string]string{"author": string(engine.AuthorAssistant), "content": "hi"})
	gate, _ := json.Marshal(state.GatePayload{
		From: string(domain.StageSpec), To: string(domain.StagePlan), Actor: state.ActorUser,
	})
	segs := stageSegments([]state.CardEvent{
		{Stage: domain.StageSpec, Kind: state.EventStageEnter, At: at, Payload: string(enter)},
		{Stage: domain.StageSpec, Kind: state.EventMessage, At: at.Add(time.Minute), Payload: string(says)},
		{Stage: domain.StageSpec, Kind: state.EventGate, At: at.Add(2 * time.Minute), Payload: string(gate)},
	})
	if len(segs) != 1 {
		t.Fatalf("one stage session produced %d segments", len(segs))
	}
	if len(segs[0].events) != 2 {
		t.Errorf("the spec segment holds %d events, want the message and the crossing", len(segs[0].events))
	}
}

// TestBG093EventsBeforeAnyStageAreStillDropped pins the one case the fix
// deliberately leaves alone: the todo→first-stage crossing is recorded
// before any session exists, so there is no block for it to sit in and
// it opens none.
func TestBG093EventsBeforeAnyStageAreStillDropped(t *testing.T) {
	at := time.Date(2026, 9, 4, 5, 52, 0, 0, time.UTC)
	gate, _ := json.Marshal(state.GatePayload{
		From: string(domain.StageTodo), To: string(domain.StageInvestigate), Actor: "caller",
	})
	segs := stageSegments([]state.CardEvent{
		{Stage: domain.StageTodo, Kind: state.EventGate, At: at, Payload: string(gate)},
	})
	if len(segs) != 0 {
		t.Errorf("a crossing before the first stage opened %d segments", len(segs))
	}
}
