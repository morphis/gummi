package state

import (
	"context"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

// TestRecordStageSpendAccumulates checks that repeated samples for the same
// (stage, model) accumulate in place, that a second model on a stage opens
// its own row, and that the per-(stage,model) credits sum back to what the
// feature total would carry — the invariant the breakdown relies on.
func TestRecordStageSpendAccumulates(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := feat(1, "Dark mode")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}

	// review, twice on gpt-5-codex + once on a cheaper model; implement once.
	samples := []struct {
		stage       domain.Stage
		role, model string
		credits     float64
		in, cd, out int64
	}{
		{domain.StageReview, "reviewer", "gpt-5-codex", 30, 1200, 300, 400},
		{domain.StageReview, "reviewer", "gpt-5-codex", 8, 300, 100, 90},
		{domain.StageReview, "reviewer", "gpt-4o-mini", 4, 800, 0, 100},
		{domain.StageImplement, "implementer", "gpt-5-codex", 50, 5000, 1000, 2000},
	}
	var wantTotal float64
	for _, x := range samples {
		if err := s.RecordStageSpend(ctx, f.ID, x.stage, x.role, x.model, x.credits, x.in, x.cd, x.out); err != nil {
			t.Fatal(err)
		}
		wantTotal += x.credits
	}

	bd, err := s.StageBreakdown(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(bd) != 3 {
		t.Fatalf("breakdown rows = %d, want 3 (2 review models + 1 implement)", len(bd))
	}

	// workflow order: implement precedes review; within review the dominant
	// (highest-credit) model leads.
	if bd[0].Stage != domain.StageImplement {
		t.Errorf("row[0] stage = %s, want implement (workflow order)", bd[0].Stage)
	}
	if bd[1].Stage != domain.StageReview || bd[1].Model != "gpt-5-codex" {
		t.Errorf("row[1] = %s/%s, want review/gpt-5-codex (dominant first)", bd[1].Stage, bd[1].Model)
	}
	if bd[2].Stage != domain.StageReview || bd[2].Model != "gpt-4o-mini" {
		t.Errorf("row[2] = %s/%s, want review/gpt-4o-mini", bd[2].Stage, bd[2].Model)
	}

	// the two gpt-5-codex review samples merged in place.
	rev := bd[1]
	if rev.Credits != 38 || rev.InputTokens != 1500 || rev.CachedTokens != 400 || rev.OutputTokens != 490 {
		t.Errorf("review/gpt-5-codex = {c%.0f in%d cd%d out%d}, want {c38 in1500 cd400 out490}",
			rev.Credits, rev.InputTokens, rev.CachedTokens, rev.OutputTokens)
	}
	if rev.Role != "reviewer" {
		t.Errorf("role = %q, want reviewer", rev.Role)
	}

	// the breakdown sums to the same credits the feature total accumulates.
	var got float64
	for _, r := range bd {
		got += r.Credits
	}
	if got != wantTotal {
		t.Errorf("breakdown sum = %v, want %v (feature total)", got, wantTotal)
	}
}

// TestRecordStageSpendEmptyModel stores an unnamed model under a stable
// "unknown" key rather than dropping the spend or keying on ”.
func TestRecordStageSpendEmptyModel(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := feat(1, "x")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordStageSpend(ctx, f.ID, domain.StageFix, "implementer", "", 5, 10, 0, 20); err != nil {
		t.Fatal(err)
	}
	bd, err := s.StageBreakdown(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(bd) != 1 || bd[0].Model != "unknown" || bd[0].Credits != 5 {
		t.Fatalf("breakdown = %+v, want one unknown-model row of 5 credits", bd)
	}
}

// TestStageBreakdownEmpty returns no rows (not an error) for a feature that
// never recorded stage spend — the forward-only, pre-deploy case.
func TestStageBreakdownEmpty(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := feat(1, "x")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	bd, err := s.StageBreakdown(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(bd) != 0 {
		t.Fatalf("breakdown = %+v, want empty", bd)
	}
}

// TestRecordStageSpendCascades confirms the FK cascade: deleting a feature
// clears its stage_spend rows too.
func TestRecordStageSpendCascades(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := feat(1, "x")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordStageSpend(ctx, f.ID, domain.StageReview, "reviewer", "gpt-5", 1, 1, 0, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteFeature(ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	bd, err := s.StageBreakdown(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(bd) != 0 {
		t.Fatalf("breakdown after delete = %+v, want empty (cascade)", bd)
	}
}
