package state

import (
	"context"
	"database/sql"
	"net/url"
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
		if err := s.RecordStageSpend(ctx, f.ID, x.stage, x.role, x.model, x.credits, 0, x.in, x.cd, x.out); err != nil {
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

// TestRecordStageSpendRoleRows checks that role is part of the row key:
// two roles spending on the same (stage, model) — a plan writer and the
// critique pass, say — keep separate attributed rows instead of the
// later role overwriting the earlier one, and the credits sum invariant
// still holds across them.
func TestRecordStageSpendRoleRows(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := feat(1, "Dark mode")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordStageSpend(ctx, f.ID, domain.StagePlan, "architect", "claude-haiku", 20, 0, 1000, 0, 500); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordStageSpend(ctx, f.ID, domain.StagePlan, "reviewer", "claude-haiku", 5, 0, 200, 0, 100); err != nil {
		t.Fatal(err)
	}
	bd, err := s.StageBreakdown(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(bd) != 2 {
		t.Fatalf("breakdown rows = %d, want 2 (one per role): %+v", len(bd), bd)
	}
	byRole := map[string]float64{}
	var total float64
	for _, r := range bd {
		byRole[r.Role] = r.Credits
		total += r.Credits
	}
	if byRole["architect"] != 20 || byRole["reviewer"] != 5 {
		t.Errorf("per-role credits = %v, want architect 20 / reviewer 5", byRole)
	}
	if total != 25 {
		t.Errorf("breakdown sum = %v, want 25 (feature total invariant)", total)
	}
	// accumulation still merges within one role's row
	if err := s.RecordStageSpend(ctx, f.ID, domain.StagePlan, "reviewer", "claude-haiku", 3, 0, 0, 0, 50); err != nil {
		t.Fatal(err)
	}
	bd, err = s.StageBreakdown(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(bd) != 2 {
		t.Fatalf("breakdown rows after accumulate = %d, want 2", len(bd))
	}
}

// TestRecordStageSpendEstimated checks the estimated accumulator: a
// token-derived sample and a metered one on the same (stage, model) keep
// the estimated portion separate, so displays can label the row.
func TestRecordStageSpendEstimated(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := feat(1, "Dark mode")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	// token-derived (estimated == credits), then provider-metered (0)
	if err := s.RecordStageSpend(ctx, f.ID, domain.StageReview, "reviewer", "gpt-5-codex", 6, 6, 0, 0, 12000); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordStageSpend(ctx, f.ID, domain.StageReview, "reviewer", "gpt-5-codex", 30, 0, 1200, 300, 400); err != nil {
		t.Fatal(err)
	}
	bd, err := s.StageBreakdown(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(bd) != 1 || bd[0].Credits != 36 || bd[0].EstimatedCredits != 6 {
		t.Fatalf("breakdown = %+v, want one row of 36 credits with 6 estimated", bd)
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
	if err := s.RecordStageSpend(ctx, f.ID, domain.StageFix, "implementer", "", 5, 0, 10, 0, 20); err != nil {
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

// TestStageSpendPKRebuild opens a store whose stage_spend table still
// carries the old three-column primary key (no role) and existing data:
// OpenStore must rebuild it to the role-keyed shape without losing a
// row, admit a second role on the same (stage, model) afterwards, and
// leave an already-rebuilt table alone on the next open.
func TestStageSpendPKRebuild(t *testing.T) {
	ctx := context.Background()
	w, err := Init(gitRoot(t), gitRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(w.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	f := feat(1, "Dark mode")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// regress the table to the pre-role-key shape, keeping a data row
	db, err := sql.Open("sqlite", "file:"+url.PathEscape(w.DBFile()))
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`DROP TABLE stage_spend`,
		`CREATE TABLE stage_spend (
			feature_id TEXT    NOT NULL REFERENCES features(id) ON DELETE CASCADE,
			stage      TEXT    NOT NULL,
			model      TEXT    NOT NULL,
			role       TEXT    NOT NULL,
			credits     REAL    NOT NULL DEFAULT 0,
			est_credits REAL    NOT NULL DEFAULT 0,
			input_tok   INTEGER NOT NULL DEFAULT 0,
			cached_tok  INTEGER NOT NULL DEFAULT 0,
			output_tok  INTEGER NOT NULL DEFAULT 0,
			updated_at  TEXT    NOT NULL,
			PRIMARY KEY (feature_id, stage, model)
		)`,
		`INSERT INTO stage_spend VALUES
			('FD-001','plan','claude-haiku','architect',20,0,1000,0,500,'2026-07-01T00:00:00Z')`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("building old-schema db: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// reopen: the rebuild must carry the row into the role-keyed table
	s, err = OpenStore(w.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	bd, err := s.StageBreakdown(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(bd) != 1 || bd[0].Credits != 20 || bd[0].Role != "architect" {
		t.Fatalf("row lost in rebuild: %+v", bd)
	}
	// a second role on the same (stage, model) now coexists
	if err := s.RecordStageSpend(ctx, f.ID, domain.StagePlan, "reviewer", "claude-haiku", 5, 0, 0, 0, 100); err != nil {
		t.Fatal(err)
	}
	if bd, _ = s.StageBreakdown(ctx, f.ID); len(bd) != 2 {
		t.Fatalf("breakdown rows = %d, want 2 (role in the key): %+v", len(bd), bd)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// idempotent: a second open leaves the rebuilt table (and rows) alone
	s, err = OpenStore(w.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if bd, _ = s.StageBreakdown(ctx, f.ID); len(bd) != 2 {
		t.Fatalf("breakdown rows after reopen = %d, want 2: %+v", len(bd), bd)
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
	if err := s.RecordStageSpend(ctx, f.ID, domain.StageReview, "reviewer", "gpt-5", 1, 0, 1, 0, 1); err != nil {
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
