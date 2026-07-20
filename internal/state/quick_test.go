package state

import (
	"context"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

// TestQuickRouteRoundtrip: the quick marker survives create → read, and
// UpdateFeature can loosen the route (clearing Plan and Quick — the P
// escalation) without touching the other flags.
func TestQuickRouteRoundtrip(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	f := feat(1, "Add a healthz endpoint")
	f.Skip = domain.QuickRoute()
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Skip.Quick || !got.Skip.Brainstorm || !got.Skip.Plan {
		t.Fatalf("quick route lost in roundtrip: %+v", got.Skip)
	}

	// the quick route honors the same skip edges: todo → spec is legal
	got, err = s.Transition(ctx, f.ID, domain.StageSpec, "user")
	if err != nil {
		t.Fatalf("quick todo → spec rejected: %v", err)
	}

	// escalation: clear Plan (and the marker with it), keep Brainstorm
	got.Skip.Plan, got.Skip.Quick = false, false
	if err := s.UpdateFeature(ctx, &got); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Skip.Quick || got.Skip.Plan || !got.Skip.Brainstorm {
		t.Fatalf("escalated flags = %+v, want plan+quick cleared, brainstorm kept", got.Skip)
	}
	// spec → implement needs the plan skip, which is gone now
	if _, err := s.Transition(ctx, f.ID, domain.StageImplement, "user"); err == nil {
		t.Fatal("spec → implement accepted after the plan skip was cleared")
	}
	if _, err := s.Transition(ctx, f.ID, domain.StagePlan, "user"); err != nil {
		t.Fatalf("spec → plan rejected after escalation: %v", err)
	}
}
