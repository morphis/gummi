package ui

import (
	"context"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

func TestEstimateEnvelopeFromHistory(t *testing.T) {
	m, _ := newWorkspace(t)
	ctx := context.Background()

	// a completed feature that cost 100 credits — the history to learn from
	done := &domain.Feature{
		ID: "FD-001", Num: 1, Title: "Prior", Slug: "prior",
		Stage: domain.StageDone, CreatedAt: fixedTime, UpdatedAt: fixedTime,
	}
	if err := m.store.CreateFeature(ctx, done); err != nil {
		t.Fatal(err)
	}
	if err := m.store.AddSpend(ctx, done.ID, 100, 0, 0); err != nil {
		t.Fatal(err)
	}

	// a new feature at spec approval gets an envelope sized from that history
	f := &domain.Feature{
		ID: "FD-002", Num: 2, Title: "New", Slug: "new",
		Stage: domain.StageSpec, CreatedAt: fixedTime, UpdatedAt: fixedTime,
	}
	if err := m.store.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	suffix := m.estimateEnvelope(ctx, f)
	// 100 median × 1.25 = 125 → round up to 130
	if f.Budget.Envelope != 130 {
		t.Errorf("estimated envelope = %d, want 130", f.Budget.Envelope)
	}
	if !strings.Contains(suffix, "estimated at 130") || !strings.Contains(suffix, "1 metered") {
		t.Errorf("suffix = %q", suffix)
	}
	// persisted to the store
	if got, _ := m.store.GetFeature(ctx, "FD-002"); got.Budget.Envelope != 130 {
		t.Errorf("envelope not persisted: %d", got.Budget.Envelope)
	}
}

func TestEstimateEnvelopeRespectsExplicitEnvelope(t *testing.T) {
	m, _ := newWorkspace(t)
	ctx := context.Background()
	// history exists (would estimate 150)...
	done := &domain.Feature{
		ID: "FD-001", Num: 1, Title: "Prior", Slug: "prior",
		Stage: domain.StageDone, CreatedAt: fixedTime, UpdatedAt: fixedTime,
	}
	if err := m.store.CreateFeature(ctx, done); err != nil {
		t.Fatal(err)
	}
	_ = m.store.AddSpend(ctx, done.ID, 120, 0, 0)
	// ...but this feature was given an explicit envelope, which must win
	f := &domain.Feature{
		ID: "FD-002", Num: 2, Title: "New", Slug: "new",
		Stage: domain.StageSpec, Budget: domain.Budget{Envelope: 200},
		CreatedAt: fixedTime, UpdatedAt: fixedTime,
	}
	if err := m.store.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	if suffix := m.estimateEnvelope(ctx, f); suffix != "" {
		t.Errorf("suffix = %q, want empty (explicit envelope wins)", suffix)
	}
	if f.Budget.Envelope != 200 {
		t.Errorf("explicit envelope overridden to %d, want 200", f.Budget.Envelope)
	}
}

func TestEstimateEnvelopeNoHistoryLeavesDefault(t *testing.T) {
	m, _ := newWorkspace(t)
	ctx := context.Background()
	// only an in-progress feature exists — nothing completed to learn from
	other := &domain.Feature{
		ID: "FD-001", Num: 1, Title: "WIP", Slug: "wip",
		Stage: domain.StageImplement, CreatedAt: fixedTime, UpdatedAt: fixedTime,
	}
	if err := m.store.CreateFeature(ctx, other); err != nil {
		t.Fatal(err)
	}
	_ = m.store.AddSpend(ctx, other.ID, 50, 0, 0)

	f := &domain.Feature{
		ID: "FD-002", Num: 2, Title: "New", Slug: "new",
		Stage:     domain.StageSpec, // unset envelope (0)
		CreatedAt: fixedTime, UpdatedAt: fixedTime,
	}
	if err := m.store.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	if suffix := m.estimateEnvelope(ctx, f); suffix != "" {
		t.Errorf("suffix = %q, want empty (no completed history)", suffix)
	}
	if f.Budget.Envelope != 0 {
		t.Errorf("envelope set to %d with no history, want 0 (unbudgeted)", f.Budget.Envelope)
	}
}
