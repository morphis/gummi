package state

import (
	"context"
	"database/sql"
	"net/url"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

// A fresh DB builds the keyed rounds table directly and never adds the
// retired plan_rounds/review_rounds columns to features.
func TestRoundsMigrationFreshDBHasNoLegacyColumns(t *testing.T) {
	ctx := context.Background()
	w, err := Init(gitRoot(t), gitRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(w.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	f := feat(1, "Fresh")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Rounds(ctx, f.ID, domain.RoundKindPlan); err != nil || got != 0 {
		t.Fatalf("Rounds(plan) on fresh DB = %d, %v; want 0", got, err)
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT name FROM pragma_table_info('features') WHERE name IN ('plan_rounds', 'review_rounds')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("fresh DB's features table still carries a legacy rounds column")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

// An old-shape DB (features still carrying plan_rounds/review_rounds,
// predating the keyed rounds table) has its nonzero values copied into
// the rounds table and the two columns dropped from features on open.
func TestRoundsMigrationCopiesLegacyColumns(t *testing.T) {
	ctx := context.Background()
	w, err := Init(gitRoot(t), gitRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(w.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	f1 := feat(1, "Has rounds")
	if err := s.CreateFeature(ctx, f1); err != nil {
		t.Fatal(err)
	}
	f2 := feat(2, "No rounds")
	if err := s.CreateFeature(ctx, f2); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// regress features to the pre-keyed-rounds shape, with a nonzero and a
	// zero row.
	db, err := sql.Open("sqlite", "file:"+url.PathEscape(w.DBFile()))
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`ALTER TABLE features ADD COLUMN plan_rounds INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE features ADD COLUMN review_rounds INTEGER NOT NULL DEFAULT 0`,
		`UPDATE features SET plan_rounds = 1, review_rounds = 2 WHERE id = 'FD-001'`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("building old-schema db: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// reopen: the rebuild must carry the nonzero values into the keyed
	// rounds table and drop the two columns.
	s, err = OpenStore(w.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if got, err := s.Rounds(ctx, f1.ID, domain.RoundKindPlan); err != nil || got != 1 {
		t.Fatalf("Rounds(plan) for %s = %d, %v; want 1", f1.ID, got, err)
	}
	if got, err := s.Rounds(ctx, f1.ID, domain.RoundKindReview); err != nil || got != 2 {
		t.Fatalf("Rounds(review) for %s = %d, %v; want 2", f1.ID, got, err)
	}
	if got, err := s.Rounds(ctx, f2.ID, domain.RoundKindPlan); err != nil || got != 0 {
		t.Fatalf("Rounds(plan) for %s = %d, %v; want 0 (never had a nonzero legacy value)", f2.ID, got, err)
	}

	// GetFeature must still scan a valid row post-rebuild.
	if _, err := s.GetFeature(ctx, f1.ID); err != nil {
		t.Fatalf("GetFeature after rebuild: %v", err)
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT name FROM pragma_table_info('features') WHERE name IN ('plan_rounds', 'review_rounds')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("legacy rounds column survived the rebuild")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

// Reopening an already-migrated DB (no legacy columns left) is a no-op:
// the migrated values are unaffected.
func TestRoundsMigrationIdempotentOnReopen(t *testing.T) {
	ctx := context.Background()
	w, err := Init(gitRoot(t), gitRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(w.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	f := feat(1, "Migrate once")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPlanRounds(ctx, f.ID, 2); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// reopen twice: the second open must be a pure no-op over the first's
	// already-migrated (columnless) shape.
	for i := 0; i < 2; i++ {
		s, err = OpenStore(w.DBFile())
		if err != nil {
			t.Fatal(err)
		}
		if got, err := s.Rounds(ctx, f.ID, domain.RoundKindPlan); err != nil || got != 2 {
			t.Fatalf("open %d: PlanRounds = %d, %v; want 2", i, got, err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
