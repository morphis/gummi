package state

import (
	"context"
	"database/sql"
	"net/url"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

// The point of the session key, stated as a test: a stage that ran twice
// keeps its first attempt and its redo apart. Before the key existed both
// passes upserted onto one row, so a stage that cost 25 credits across a
// 10-credit try and a 15-credit redo reported 25 and no way to learn that
// most of it was the second go.
func TestSessionBreakdownSeparatesPasses(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := feat(1, "bounced through review")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}

	for _, x := range []SpendSample{
		{Stage: domain.StageImplement, Session: "first", Role: "implementer", Model: "m", Credits: 10, InputTokens: 100, OutputTokens: 20},
		{Stage: domain.StageImplement, Session: "first", Role: "implementer", Model: "m", Credits: 2, InputTokens: 10, OutputTokens: 2},
		{Stage: domain.StageImplement, Session: "redo", Role: "implementer", Model: "m", Credits: 13, InputTokens: 200, OutputTokens: 40},
	} {
		if err := s.RecordStageSpend(ctx, f.ID, x); err != nil {
			t.Fatal(err)
		}
	}

	sess, err := s.SessionBreakdown(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sess) != 2 {
		t.Fatalf("session rows = %d, want 2 (one per pass): %+v", len(sess), sess)
	}
	byPass := map[string]float64{}
	for _, r := range sess {
		byPass[r.Session] = r.Credits
	}
	// the two samples of the first pass accumulate onto its own row; the
	// redo never joins them.
	if byPass["first"] != 12 || byPass["redo"] != 13 {
		t.Errorf("per-pass credits = %v, want first 12 / redo 13", byPass)
	}

	// StageBreakdown's contract is unchanged: it sums the dimension it
	// never knew about, so a caller that only wants "what did implement
	// cost" reads exactly what it always did.
	bd, err := s.StageBreakdown(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(bd) != 1 {
		t.Fatalf("stage rows = %d, want 1 summed across sessions: %+v", len(bd), bd)
	}
	if bd[0].Credits != 25 || bd[0].InputTokens != 310 || bd[0].OutputTokens != 62 {
		t.Errorf("stage row = %+v, want 25 credits / 310 in / 62 out", bd[0])
	}
	if bd[0].Session != "" {
		t.Errorf("stage row session = %q, want empty: it is a sum across sessions", bd[0].Session)
	}
}

// A database written before the session key existed keeps every credit it
// recorded, admitted under the empty session — the same meaning a row
// written today by something that is not a stage session carries.
func TestStageSpendSessionMigrationKeepsOldRows(t *testing.T) {
	ctx := context.Background()
	w, err := Init(gitRoot(t), gitRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(w.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	f := feat(1, "old card")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// regress stage_spend to the four-column key, with a row in it.
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
			PRIMARY KEY (feature_id, stage, model, role)
		)`,
		`INSERT INTO stage_spend VALUES ('FD-001','implement','m','implementer',42.5,0,900,10,80,'2026-01-01T00:00:00Z')`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("building old-schema db: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = OpenStore(w.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	sess, err := s.SessionBreakdown(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sess) != 1 || sess[0].Credits != 42.5 || sess[0].InputTokens != 900 {
		t.Fatalf("migrated rows = %+v, want the one 42.5-credit row carried over", sess)
	}
	if sess[0].Session != "" {
		t.Errorf("migrated session = %q, want empty: nothing knew which pass spent it", sess[0].Session)
	}

	// and the migrated row is a normal row: a new sample under a real
	// session opens its own, rather than colliding with the legacy one.
	if err := s.RecordStageSpend(ctx, f.ID, SpendSample{
		Stage: domain.StageImplement, Session: "now", Role: "implementer", Model: "m", Credits: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if sess, err = s.SessionBreakdown(ctx, f.ID); err != nil || len(sess) != 2 {
		t.Fatalf("rows after a new sample = %+v (%v), want 2", sess, err)
	}
}
