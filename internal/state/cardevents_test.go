package state

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
)

// TestAppendEventsIdempotent is the highest-risk case for the event log:
// a mirrored save that replays the same batch (e.g. after a retried
// engine write) must never double up rows that carry a dedupe key.
// Appending the same batch five times must leave the event count
// unchanged after the first append.
func TestAppendEventsIdempotent(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := feat(1, "Idempotent events")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}

	batch := []CardEvent{
		{Feature: f.ID, Stage: domain.StageImplement, Kind: EventMessage, At: time.Now(), Payload: "hello", Dedupe: "msg-1"},
		{Feature: f.ID, Stage: domain.StageImplement, Kind: EventTool, Status: StatusOK, At: time.Now(), Payload: "tool call", Output: "output", Dedupe: "tool-1"},
		{Feature: f.ID, Stage: domain.StageImplement, Kind: EventStageEnter, At: time.Now(), Dedupe: "enter-implement"},
	}

	for round := 1; round <= 5; round++ {
		if err := s.AppendEvents(ctx, batch); err != nil {
			t.Fatalf("round %d: AppendEvents: %v", round, err)
		}
		evs, err := s.Events(ctx, f.ID)
		if err != nil {
			t.Fatalf("round %d: Events: %v", round, err)
		}
		if len(evs) != len(batch) {
			t.Fatalf("round %d: got %d events, want %d (stable across replays)", round, len(evs), len(batch))
		}
	}
}

// TestAppendEventEmptyDedupeAlwaysAppends: an event with no dedupe key
// has no natural identity, so every append is a genuinely new row, even
// when the events are otherwise identical.
func TestAppendEventEmptyDedupeAlwaysAppends(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := feat(1, "Always append")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}

	for range 5 {
		ev := CardEvent{
			Feature: f.ID, Stage: domain.StageImplement, Kind: EventMessage,
			At: time.Now(), Payload: "identical",
		}
		if err := s.AppendEvent(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	evs, err := s.Events(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 5 {
		t.Fatalf("got %d events, want 5 (empty dedupe always appends)", len(evs))
	}
}

// TestEventsRoundTrip appends several distinct events and checks they
// come back in ascending seq order with every field intact.
func TestEventsRoundTrip(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := feat(1, "Round trip")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}

	at1 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	at2 := at1.Add(time.Minute)
	at3 := at2.Add(time.Minute)
	want := []CardEvent{
		{Feature: f.ID, Stage: domain.StageImplement, Kind: EventStageEnter, At: at1, Dedupe: "enter"},
		{Feature: f.ID, Stage: domain.StageImplement, Kind: EventMessage, Status: "", At: at2, Payload: `{"text":"hi"}`, Dedupe: ""},
		{Feature: f.ID, Stage: domain.StageImplement, Kind: EventTool, Status: StatusFail, At: at3, Payload: `{"cmd":"go test"}`, Output: "FAIL", Dedupe: "tool-9"},
	}
	for _, ev := range want {
		if err := s.AppendEvent(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.Events(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d", len(got), len(want))
	}
	for i, w := range want {
		g := got[i]
		if g.Seq <= 0 {
			t.Errorf("event %d: seq = %d, want > 0", i, g.Seq)
		}
		if i > 0 && got[i-1].Seq >= g.Seq {
			t.Errorf("event %d: seq %d not ascending after %d", i, g.Seq, got[i-1].Seq)
		}
		if g.Feature != w.Feature || g.Stage != w.Stage || g.Kind != w.Kind ||
			g.Status != w.Status || g.Payload != w.Payload || g.Output != w.Output || g.Dedupe != w.Dedupe {
			t.Errorf("event %d = %+v, want fields matching %+v", i, g, w)
		}
		if !g.At.Equal(w.At) {
			t.Errorf("event %d: At = %v, want %v", i, g.At, w.At)
		}
	}
}

// TestPruneStageOutput blanks output on a successful tool event,
// preserves it on a failed one, and leaves other stages untouched.
func TestPruneStageOutput(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := feat(1, "Prune")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}

	events := []CardEvent{
		{Feature: f.ID, Stage: domain.StageImplement, Kind: EventTool, Status: StatusOK, At: time.Now(), Output: "ok output", Dedupe: "impl-ok"},
		{Feature: f.ID, Stage: domain.StageImplement, Kind: EventTool, Status: StatusFail, At: time.Now(), Output: "fail output", Dedupe: "impl-fail"},
		{Feature: f.ID, Stage: domain.StageImplement, Kind: EventMessage, At: time.Now(), Output: "", Dedupe: "impl-msg"},
		{Feature: f.ID, Stage: domain.StageReview, Kind: EventTool, Status: StatusOK, At: time.Now(), Output: "review output", Dedupe: "review-ok"},
	}
	if err := s.AppendEvents(ctx, events); err != nil {
		t.Fatal(err)
	}

	if err := s.PruneStageOutput(ctx, f.ID, domain.StageImplement); err != nil {
		t.Fatal(err)
	}

	got, err := s.Events(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	byDedupe := map[string]CardEvent{}
	for _, ev := range got {
		byDedupe[ev.Dedupe] = ev
	}
	if out := byDedupe["impl-ok"].Output; out != "" {
		t.Errorf("successful implement tool output = %q, want pruned to empty", out)
	}
	if out := byDedupe["impl-fail"].Output; out != "fail output" {
		t.Errorf("failed implement tool output = %q, want preserved", out)
	}
	if out := byDedupe["review-ok"].Output; out != "review output" {
		t.Errorf("review-stage output = %q, want untouched by an implement-stage prune", out)
	}
}

// TestCardEventsCascadeOnDelete: deleting a feature deletes its events,
// same as every other feature-scoped table.
func TestCardEventsCascadeOnDelete(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := feat(1, "Cascade")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendEvent(ctx, CardEvent{Feature: f.ID, Stage: domain.StageImplement, Kind: EventMessage, At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if evs, err := s.Events(ctx, f.ID); err != nil || len(evs) != 1 {
		t.Fatalf("precondition: Events = %v, %v; want 1 event", evs, err)
	}

	if err := s.DeleteFeature(ctx, f.ID); err != nil {
		t.Fatal(err)
	}

	evs, err := s.Events(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 0 {
		t.Errorf("Events after DeleteFeature = %d, want 0 (cascade)", len(evs))
	}
}

// TestQuitStopped covers QuitStopped's five load-bearing shapes: a bare
// quit park; a quit park a card was resumed past; a human park (any
// other reason); a card that never parked; and a quit park superseded by
// a later human one. Only the first shape is true.
func TestQuitStopped(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	quitPayload, err := json.Marshal(ParkPayload{Reason: ParkReasonQuit})
	if err != nil {
		t.Fatal(err)
	}
	humanPayload, err := json.Marshal(ParkPayload{Reason: "manual"})
	if err != nil {
		t.Fatal(err)
	}

	mk := func(num int, title string) *domain.Feature {
		f := feat(num, title)
		if err := s.CreateFeature(ctx, f); err != nil {
			t.Fatal(err)
		}
		return f
	}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	next := func() time.Time {
		at = at.Add(time.Minute)
		return at
	}
	add := func(f *domain.Feature, kind, payload string) {
		t.Helper()
		if err := s.AppendEvent(ctx, CardEvent{
			Feature: f.ID, Stage: domain.StageImplement, Kind: kind,
			At: next(), Payload: payload,
		}); err != nil {
			t.Fatal(err)
		}
	}

	// 1. a quit park with nothing after it -> true.
	fQuit := mk(1, "bare quit park")
	add(fQuit, EventStageEnter, "")
	add(fQuit, EventPark, string(quitPayload))

	// 2. a quit park followed by a stage_enter (resumed) -> false.
	fResumed := mk(2, "resumed past its quit park")
	add(fResumed, EventPark, string(quitPayload))
	add(fResumed, EventStageEnter, "")

	// 3. a human park (any other reason) -> false.
	fHuman := mk(3, "human park")
	add(fHuman, EventPark, string(humanPayload))

	// 4. never parked -> false.
	fNever := mk(4, "never parked")
	add(fNever, EventStageEnter, "")
	add(fNever, EventMessage, "")

	// 5. an old quit park superseded by a newer human park -> false.
	fSuperseded := mk(5, "superseded quit park")
	add(fSuperseded, EventPark, string(quitPayload))
	add(fSuperseded, EventPark, string(humanPayload))

	got, err := s.QuitStopped(ctx)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		id   domain.FeatureID
		want bool
	}{
		{fQuit.ID, true},
		{fResumed.ID, false},
		{fHuman.ID, false},
		{fNever.ID, false},
		{fSuperseded.ID, false},
	}
	for _, c := range cases {
		if g := got[c.id]; g != c.want {
			t.Errorf("QuitStopped()[%s] = %v, want %v", c.id, g, c.want)
		}
	}
}

// TestCardEventsSurviveRestart writes events through a real OpenStore,
// closes it, reopens the same path, and checks the events are still
// there — the durability guarantee the event log exists for.
func TestCardEventsSurviveRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	ctx := context.Background()

	s1, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	f := feat(1, "Restart")
	if err := s1.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := s1.AppendEvent(ctx, CardEvent{
		Feature: f.ID, Stage: domain.StageImplement, Kind: EventMessage,
		At: time.Now(), Payload: "before restart",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	evs, err := s2.Events(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Payload != "before restart" {
		t.Fatalf("Events after reopen = %+v, want one event with Payload %q", evs, "before restart")
	}
}

// TestSetGateApprovalRecordsAutopilotEvent: every caller funnels a
// card's gate-approval mode change through SetGateApproval, so it's the
// single place that write can be logged — the decision receipt (and any
// future audit) needs the card's own history to say when its mode
// changed and to what, not just the current row.
func TestSetGateApprovalRecordsAutopilotEvent(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := feat(1, "gate approval history")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}

	if err := s.SetGateApproval(ctx, f.ID, domain.GateFull); err != nil {
		t.Fatal(err)
	}
	evs, err := s.Events(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Kind != EventAutopilot {
		t.Fatalf("Events = %+v, want exactly one autopilot event", evs)
	}
	var p AutopilotPayload
	if err := json.Unmarshal([]byte(evs[0].Payload), &p); err != nil {
		t.Fatal(err)
	}
	if p.Mode != domain.GateFull {
		t.Errorf("payload mode = %q, want %q", p.Mode, domain.GateFull)
	}

	// an invalid mode is refused before the store write, so no event
	// should be recorded for it.
	if err := s.SetGateApproval(ctx, f.ID, "bogus"); err == nil {
		t.Fatal("expected an error for an invalid mode")
	}
	evs, err = s.Events(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Fatalf("Events after a rejected mode = %+v, want still exactly one", evs)
	}
}
