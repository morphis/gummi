package state

import (
	"context"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
)

func TestObjectiveRoundTripsAndClears(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := freeformFeat(1, "make the parser fast")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	if o, err := s.Objective(ctx, f.ID); err != nil || o != nil {
		t.Fatalf("a new card has an objective: %+v, %v", o, err)
	}
	at := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	want := domain.Objective{
		Text: "parse under 10ms", Check: "go test ./parser", State: domain.ObjectiveActive,
		Turns: 3, StuckStreak: 1, Note: "half way", BaseRev: "abc", SetAt: at, SetBy: "user",
	}
	if err := s.SetObjective(ctx, f.ID, &want); err != nil {
		t.Fatal(err)
	}
	want.State, want.Turns = domain.ObjectiveMet, 4
	if err := s.SetObjective(ctx, f.ID, &want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Objective(ctx, f.ID)
	if err != nil || got == nil || *got != want {
		t.Fatalf("Objective = %+v, %v; want %+v", got, err, want)
	}
	if err := s.SetObjective(ctx, f.ID, nil); err != nil {
		t.Fatal(err)
	}
	if o, _ := s.Objective(ctx, f.ID); o != nil {
		t.Fatalf("a cleared objective is still there: %+v", o)
	}
}

func TestAWorkflowCardRefusesAnObjective(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := feat(1, "a feature")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	o := domain.Objective{Text: "x", State: domain.ObjectiveActive, SetAt: time.Now()}
	if err := s.SetObjective(ctx, f.ID, &o); err == nil {
		t.Fatal("a feature card took an objective")
	}
}
