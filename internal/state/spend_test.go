package state

import (
	"context"
	"testing"

	"github.com/morphia/gummi/internal/domain"
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

	if err := s.AddSpend(ctx, f.ID, 1.5, 100, 40); err != nil {
		t.Fatal(err)
	}
	if err := s.AddSpend(ctx, f.ID, 0.5, 20, 10); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetFeature(ctx, f.ID)
	if got.Spend.Credits != 2.0 || got.Spend.InputTokens != 120 || got.Spend.OutputTokens != 50 {
		t.Errorf("accumulated spend = %+v, want {2 120 50}", got.Spend)
	}
}

func TestUpdateFeaturePreservesSpend(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := feat(1, "x")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := s.AddSpend(ctx, f.ID, 3, 0, 0); err != nil {
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
