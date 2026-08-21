package state

import (
	"context"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

// A feature's plan-round count set via the SetPlanRounds side-channel
// survives a round-trip through Rounds — the record the engine uses to
// size the remaining critique budget on resume.
func TestPlanRoundsRoundtrip(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	f := feat(1, "Add a healthz endpoint")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPlanRounds(ctx, f.ID, 2); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Rounds(ctx, f.ID, domain.RoundKindPlan); err != nil || got != 2 {
		t.Fatalf("Rounds(plan) = %d, %v; want 2", got, err)
	}
	if got, err := s.Rounds(ctx, f.ID, domain.RoundKindPlan); err != nil || got != 2 {
		t.Fatalf("PlanRounds = %d, %v; want 2", got, err)
	}
}

// A feature created without a plan-round value reads back 0 — the default
// for a fresh row that has not started a plan cycle.
func TestPlanRoundsDefaultsZero(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := feat(2, "Another feature")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Rounds(ctx, f.ID, domain.RoundKindPlan); err != nil || got != 0 {
		t.Fatalf("Rounds(plan) = %d, %v; want 0", got, err)
	}
	if got, err := s.Rounds(ctx, f.ID, domain.RoundKindPlan); err != nil || got != 0 {
		t.Fatalf("PlanRounds = %d, %v; want 0", got, err)
	}
}

// IncrementRounds(plan) is the live-cycle atomic bump: each call steps the
// counter by one via a single upsert, from a fresh 0.
func TestIncrementPlanRounds(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := feat(3, "Incremental")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}

	if err := s.IncrementRounds(ctx, f.ID, domain.RoundKindPlan); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Rounds(ctx, f.ID, domain.RoundKindPlan); err != nil || got != 1 {
		t.Fatalf("PlanRounds after first increment = %d, %v; want 1", got, err)
	}
	if err := s.IncrementRounds(ctx, f.ID, domain.RoundKindPlan); err != nil {
		t.Fatal(err)
	}
	if err := s.IncrementRounds(ctx, f.ID, domain.RoundKindPlan); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Rounds(ctx, f.ID, domain.RoundKindPlan); err != nil || got != 3 {
		t.Fatalf("PlanRounds after three increments = %d, %v; want 3", got, err)
	}
}

// SetPlanRounds is the set-by-value side-channel write; the value it
// stores round-trips through both Rounds(plan) and PlanRounds.
func TestSetPlanRounds(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := feat(4, "Set by value")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}

	if err := s.SetPlanRounds(ctx, f.ID, 2); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Rounds(ctx, f.ID, domain.RoundKindPlan); err != nil || got != 2 {
		t.Fatalf("PlanRounds after set = %d, %v; want 2", got, err)
	}
	if got, err := s.Rounds(ctx, f.ID, domain.RoundKindPlan); err != nil || got != 2 {
		t.Fatalf("Rounds(plan) after set = %d, %v; want 2", got, err)
	}
}

// ClearRounds(plan) resets the live-cycle counter to 0 and is harmless on
// a row that is already 0 — the reset a completed plan cycle performs.
func TestClearPlanRounds(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := feat(5, "Clear")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}

	if err := s.IncrementRounds(ctx, f.ID, domain.RoundKindPlan); err != nil {
		t.Fatal(err)
	}
	if err := s.IncrementRounds(ctx, f.ID, domain.RoundKindPlan); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearRounds(ctx, f.ID, domain.RoundKindPlan); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Rounds(ctx, f.ID, domain.RoundKindPlan); err != nil || got != 0 {
		t.Fatalf("PlanRounds after clear = %d, %v; want 0", got, err)
	}

	// clearing an already-0 row succeeds and stays 0.
	if err := s.ClearRounds(ctx, f.ID, domain.RoundKindPlan); err != nil {
		t.Fatalf("clear on zero row: %v", err)
	}
	if got, err := s.Rounds(ctx, f.ID, domain.RoundKindPlan); err != nil || got != 0 {
		t.Fatalf("PlanRounds after second clear = %d, %v; want 0", got, err)
	}
}

// Reopening an existing DB applies the idempotent rounds migration: a
// second open succeeds and the plan-round count survives.
func TestPlanRoundsMigrationIdempotent(t *testing.T) {
	w, err := Init(gitRoot(t), gitRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(w.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	f := feat(6, "Migrate")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPlanRounds(ctx, f.ID, 2); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// reopen: the idempotent migration must be a no-op on an existing DB.
	s, err = OpenStore(w.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got, err := s.Rounds(ctx, f.ID, domain.RoundKindPlan); err != nil || got != 2 {
		t.Fatalf("PlanRounds after reopen = %d, %v; want 2", got, err)
	}
}
