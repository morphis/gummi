package state

import (
	"context"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

func TestAddSpendAccumulates(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := feat(1, "Dark mode")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	// a fresh feature has zero spend
	got, _ := s.GetFeature(ctx, f.ID)
	if !got.Spend.Zero() {
		t.Fatalf("new feature spend = %+v, want zero", got.Spend)
	}

	if err := s.AddSpend(ctx, f.ID, 1.5, 0, 100, 40); err != nil {
		t.Fatal(err)
	}
	if err := s.AddSpend(ctx, f.ID, 0.5, 0, 20, 10); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetFeature(ctx, f.ID)
	if got.Spend.Credits != 2.0 || got.Spend.InputTokens != 120 || got.Spend.OutputTokens != 50 {
		t.Errorf("accumulated spend = %+v, want {2 120 50}", got.Spend)
	}
	// both samples were provider-metered, so nothing reads as estimated
	if got.Spend.Estimated() {
		t.Errorf("metered spend flagged estimated: %+v", got.Spend)
	}
}

func TestAddSpendTracksEstimatedPortion(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := feat(1, "Dark mode")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	// a token-derived sample (no provider cost) plus a metered one: the
	// estimated accumulator carries only the former
	if err := s.AddSpend(ctx, f.ID, 4, 4, 0, 8000); err != nil {
		t.Fatal(err)
	}
	if err := s.AddSpend(ctx, f.ID, 1.5, 0, 100, 40); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetFeature(ctx, f.ID)
	if got.Spend.Credits != 5.5 || got.Spend.EstimatedCredits != 4 {
		t.Errorf("spend = %+v, want credits 5.5 with 4 estimated", got.Spend)
	}
	if !got.Spend.Estimated() {
		t.Errorf("spend with token-derived portion not flagged estimated: %+v", got.Spend)
	}
}

func TestUpdateFeaturePreservesSpend(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := feat(1, "x")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := s.AddSpend(ctx, f.ID, 3, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	// an unrelated update (e.g. title) must not reset the metered spend
	got, _ := s.GetFeature(ctx, f.ID)
	got.Title = "renamed"
	if err := s.UpdateFeature(ctx, &got); err != nil {
		t.Fatal(err)
	}
	after, _ := s.GetFeature(ctx, f.ID)
	if after.Spend.Credits != 3 {
		t.Errorf("spend after update = %v, want 3 preserved", after.Spend.Credits)
	}
	_ = domain.Spend{}
}
