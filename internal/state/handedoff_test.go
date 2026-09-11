package state

import (
	"context"
	"testing"
	"time"
)

// Every pre-existing row reads as "not handed off", which is what lets
// the column land with no backfill: a card with no ending stamp ended by
// landing, through its PR, or not at all.
func TestHandedOffDefaultsZero(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := feat(1, "Export the board as JSON")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.HandedOff() {
		t.Fatalf("a fresh card reads as handed off: %v", got.HandedOffAt)
	}
}

// The stamp survives a re-open (it is the card's durable record of how it
// ended, read long after the process that wrote it is gone), and clearing
// it — landing a handed-off card after all — takes it back off.
func TestHandedOffRoundtrip(t *testing.T) {
	dir := t.TempDir()
	dbPath := dir + "/state.db"

	s, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	f := feat(1, "Export the board as JSON")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}

	at := time.Date(2026, 9, 11, 10, 30, 0, 0, time.UTC)
	if err := s.SetHandedOffAt(ctx, f.ID, at); err != nil {
		t.Fatalf("SetHandedOffAt: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	got, err := s2.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.HandedOff() || !got.HandedOffAt.Equal(at) {
		t.Fatalf("HandedOffAt after reopen = %v, want %v", got.HandedOffAt, at)
	}

	if err := s2.ClearHandedOffAt(ctx, f.ID); err != nil {
		t.Fatalf("ClearHandedOffAt: %v", err)
	}
	if got, err := s2.GetFeature(ctx, f.ID); err != nil {
		t.Fatal(err)
	} else if got.HandedOff() {
		t.Fatalf("cleared stamp still reads as handed off: %v", got.HandedOffAt)
	}
}

// The stamp is a side channel like verified_at: it records how the card
// ended without moving the card, because the caller crosses the gate
// through Advance immediately afterwards and the stamp is what permits
// the crossing.
func TestHandedOffDoesNotMoveTheStage(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := feat(1, "Export the board as JSON")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	before, err := s.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetHandedOffAt(ctx, f.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	after, err := s.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Stage != before.Stage {
		t.Fatalf("stamp moved the stage from %s to %s", before.Stage, after.Stage)
	}
	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("stamp touched updated_at: %v → %v", before.UpdatedAt, after.UpdatedAt)
	}
}
