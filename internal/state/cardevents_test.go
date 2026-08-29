package state

import (
	"context"
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
