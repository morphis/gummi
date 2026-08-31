package state

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// A card that has never been marked seen reads as 0 — both through the
// single-card read and through the bulk read, where it is simply absent
// from the map rather than present with a zero value.
func TestLastSeenDefaultsUnseen(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	f := feat(1, "Never opened")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}

	got, err := s.LastSeen(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Errorf("LastSeen on an unmarked card = %d, want 0", got)
	}

	all, err := s.LastSeenSeqs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := all[f.ID]; ok {
		t.Errorf("LastSeenSeqs carries an entry for a card never marked seen: %+v", all)
	}
}

// SetLastSeen then LastSeen round-trips the exact seq, a later call
// overwrites rather than accumulates, and LastSeenSeqs reflects the same
// value for the same card alongside an unmarked sibling.
func TestLastSeenRoundtrip(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	f1 := feat(1, "Caught up")
	f2 := feat(2, "Still unread")
	if err := s.CreateFeature(ctx, f1); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateFeature(ctx, f2); err != nil {
		t.Fatal(err)
	}

	if err := s.SetLastSeen(ctx, f1.ID, 5); err != nil {
		t.Fatal(err)
	}
	if got, err := s.LastSeen(ctx, f1.ID); err != nil || got != 5 {
		t.Fatalf("LastSeen after SetLastSeen(5) = %d, %v; want 5", got, err)
	}

	// a later mark overwrites, it does not add to, the stored seq —
	// re-opening the card and scrolling further must move the mark
	// forward to the new position, not stack on top of the old one.
	if err := s.SetLastSeen(ctx, f1.ID, 9); err != nil {
		t.Fatal(err)
	}
	if got, err := s.LastSeen(ctx, f1.ID); err != nil || got != 9 {
		t.Fatalf("LastSeen after second SetLastSeen(9) = %d, %v; want 9 (overwrite, not accumulate)", got, err)
	}

	all, err := s.LastSeenSeqs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if all[f1.ID] != 9 {
		t.Errorf("LastSeenSeqs[%s] = %d, want 9", f1.ID, all[f1.ID])
	}
	if _, ok := all[f2.ID]; ok {
		t.Errorf("LastSeenSeqs carries an entry for %s, which was never marked seen: %+v", f2.ID, all)
	}

	// a negative seq makes no sense against an AUTOINCREMENT PK and is
	// refused rather than silently stored.
	if err := s.SetLastSeen(ctx, f1.ID, -1); err == nil {
		t.Fatal("SetLastSeen accepted a negative seq")
	}
	if got, err := s.LastSeen(ctx, f1.ID); err != nil || got != 9 {
		t.Fatalf("LastSeen after a refused negative mark = %d, %v; want unchanged 9", got, err)
	}
}

// A database created before card_last_seen existed opens cleanly (the
// table is created on open the same way card_events itself was, per
// TestCardEventsSchemaFreshAndMigratedMatch) and every card in it reads
// as never-seen — no backfill is needed or attempted.
func TestLastSeenOldSchemaStillOpens(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")

	// Hand-build a pre-existing database using the same minimal
	// pre-reconciliation features table TestOldSchemaStillOpens and
	// TestCardEventsSchemaFreshAndMigratedMatch use — old enough that it
	// predates card_last_seen entirely, but complete enough to satisfy the
	// schema const's own CREATE INDEX statements so OpenStore's migration
	// path runs cleanly over it.
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `CREATE TABLE features (
		id              TEXT PRIMARY KEY,
		num             INTEGER NOT NULL UNIQUE,
		title           TEXT NOT NULL,
		one_liner       TEXT NOT NULL DEFAULT '',
		slug            TEXT NOT NULL,
		stage           TEXT NOT NULL,
		skip_brainstorm INTEGER NOT NULL DEFAULT 0,
		skip_plan       INTEGER NOT NULL DEFAULT 0,
		profile         TEXT NOT NULL DEFAULT '',
		budget_envelope INTEGER NOT NULL DEFAULT 0,
		budget_spent    INTEGER NOT NULL DEFAULT 0,
		spend_credits   REAL NOT NULL DEFAULT 0,
		spend_est       REAL NOT NULL DEFAULT 0,
		spend_in        INTEGER NOT NULL DEFAULT 0,
		spend_out       INTEGER NOT NULL DEFAULT 0,
		created_at      TEXT NOT NULL,
		updated_at      TEXT NOT NULL,
		kind            TEXT NOT NULL DEFAULT 'feature',
		external_ref    TEXT NOT NULL DEFAULT '',
		skip_triage     INTEGER NOT NULL DEFAULT 0,
		skip_diagnose   INTEGER NOT NULL DEFAULT 0,
		quick           INTEGER NOT NULL DEFAULT 0,
		verified_at     TEXT NOT NULL DEFAULT '',
		gate_approval   TEXT NOT NULL DEFAULT '',
		fork_point      TEXT NOT NULL DEFAULT ''
	);`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	// Seed a row directly (bypassing CreateFeature, which requires columns
	// this old shape doesn't have yet) so there is a pre-existing card to
	// read last-seen for once OpenStore has migrated the schema.
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO features (id, num, title, slug, stage, created_at, updated_at)
		VALUES ('FD-001', 1, 'Old DB feature', 'old-db-feature', 'todo', '2020-01-01T00:00:00Z', '2020-01-01T00:00:00Z')`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()

	s, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("open over pre-card_last_seen schema: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	got, err := s.LastSeen(ctx, "FD-001")
	if err != nil {
		t.Fatalf("LastSeen over migrated old schema: %v", err)
	}
	if got != 0 {
		t.Errorf("LastSeen for a pre-existing row on a migrated old DB = %d, want 0 (never seen)", got)
	}

	all, err := s.LastSeenSeqs(ctx)
	if err != nil {
		t.Fatalf("LastSeenSeqs over migrated old schema: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("LastSeenSeqs over a freshly migrated old DB = %+v, want empty", all)
	}

	// the migrated table is fully usable going forward, not just readable.
	if err := s.SetLastSeen(ctx, "FD-001", 3); err != nil {
		t.Fatalf("SetLastSeen over migrated old schema: %v", err)
	}
	if got, err := s.LastSeen(ctx, "FD-001"); err != nil || got != 3 {
		t.Fatalf("LastSeen after marking on migrated old schema = %d, %v; want 3", got, err)
	}
}
