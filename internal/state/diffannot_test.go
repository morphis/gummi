package state

import (
	"context"
	"database/sql"
	"net/url"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
)

func TestDiffAnnotationCRUD(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := feat(1, "Dark mode")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()

	id, err := s.AddDiffAnnotation(ctx, domain.DiffAnnotation{
		Feature: f.ID, File: "ui/toggle.go", Anchor: "abc123",
		Excerpt: "+func Toggle() {}", Comment: "handle the nil case",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if id == 0 {
		t.Fatal("expected a nonzero id")
	}

	got, err := s.ListDiffAnnotations(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("listed %d annotations, want 1", len(got))
	}
	a := got[0]
	if a.File != "ui/toggle.go" || a.Anchor != "abc123" || a.Comment != "handle the nil case" || a.Resolved {
		t.Errorf("round-trip mismatch: %+v", a)
	}

	if err := s.SetDiffAnnotationResolved(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	got, _ = s.ListDiffAnnotations(ctx, f.ID)
	if !got[0].Resolved {
		t.Error("resolve flag did not persist")
	}

	if err := s.DeleteDiffAnnotation(ctx, id); err != nil {
		t.Fatal(err)
	}
	got, _ = s.ListDiffAnnotations(ctx, f.ID)
	if len(got) != 0 {
		t.Errorf("annotation survived delete: %d rows", len(got))
	}
}

func TestDiffAnnotationCascadeDelete(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := feat(1, "Dark mode")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddDiffAnnotation(ctx, domain.DiffAnnotation{
		Feature: f.ID, File: "x.go", Anchor: "h", Comment: "c",
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteFeature(ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListDiffAnnotations(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("annotations survived feature delete: %d", len(got))
	}
}

func TestDiffAnnotationSourceRefRoundtrip(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := feat(1, "Dark mode")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()

	if _, err := s.AddDiffAnnotation(ctx, domain.DiffAnnotation{
		Feature: f.ID, File: "x.go", Anchor: "h1", Comment: "c1", SourceRef: "gh:pr/42/rt/A1",
	}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddDiffAnnotation(ctx, domain.DiffAnnotation{
		Feature: f.ID, File: "y.go", Anchor: "h2", Comment: "c2",
	}, now); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListDiffAnnotations(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("listed %d annotations, want 2", len(got))
	}
	if got[0].SourceRef != "gh:pr/42/rt/A1" {
		t.Errorf("SourceRef = %q, want %q", got[0].SourceRef, "gh:pr/42/rt/A1")
	}
	if got[1].SourceRef != "" {
		t.Errorf("SourceRef = %q, want empty", got[1].SourceRef)
	}
}

func TestDiffAnnotationSourceRefIdempotent(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := feat(1, "Dark mode")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()

	id1, err := s.AddDiffAnnotation(ctx, domain.DiffAnnotation{
		Feature: f.ID, File: "x.go", Anchor: "h1", Comment: "c1", SourceRef: "gh:pr/42/rt/A1",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := s.AddDiffAnnotation(ctx, domain.DiffAnnotation{
		Feature: f.ID, File: "x.go", Anchor: "h1", Comment: "c1", SourceRef: "gh:pr/42/rt/A1",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Fatalf("ids differ across idempotent calls: %d != %d", id1, id2)
	}

	got, err := s.ListDiffAnnotations(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("listed %d annotations, want 1", len(got))
	}
}

func TestDiffAnnotationSourceRefIntroducerNotReconciler(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := feat(1, "Dark mode")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	first := time.Now().UTC().Add(-time.Hour)

	id1, err := s.AddDiffAnnotation(ctx, domain.DiffAnnotation{
		Feature: f.ID, File: "x.go", Anchor: "h1", Comment: "original", SourceRef: "gh:pr/42/rt/A1",
	}, first)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := s.AddDiffAnnotation(ctx, domain.DiffAnnotation{
		Feature: f.ID, File: "x.go", Anchor: "h1", Comment: "changed", SourceRef: "gh:pr/42/rt/A1",
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Fatalf("ids differ: %d != %d", id1, id2)
	}

	got, err := s.ListDiffAnnotations(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("listed %d annotations, want 1", len(got))
	}
	if got[0].Comment != "original" {
		t.Errorf("Comment = %q, want %q (conflict must not reconcile)", got[0].Comment, "original")
	}
	if !got[0].CreatedAt.Equal(first) {
		t.Errorf("CreatedAt = %v, want %v (conflict must not update created_at)", got[0].CreatedAt, first)
	}
}

func TestDiffAnnotationSourceRefEmptyNotDeduped(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := feat(1, "Dark mode")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()

	id1, err := s.AddDiffAnnotation(ctx, domain.DiffAnnotation{
		Feature: f.ID, File: "x.go", Anchor: "h1", Comment: "c1",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := s.AddDiffAnnotation(ctx, domain.DiffAnnotation{
		Feature: f.ID, File: "x.go", Anchor: "h1", Comment: "c1",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if id1 == id2 {
		t.Fatalf("empty SourceRef inserts collapsed: both got id %d", id1)
	}

	got, err := s.ListDiffAnnotations(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("listed %d annotations, want 2", len(got))
	}
}

func TestDiffAnnotationSourceRefScopedByFeature(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f1 := feat(1, "Dark mode")
	f2 := feat(2, "Light mode")
	if err := s.CreateFeature(ctx, f1); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateFeature(ctx, f2); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()

	id1, err := s.AddDiffAnnotation(ctx, domain.DiffAnnotation{
		Feature: f1.ID, File: "x.go", Anchor: "h1", Comment: "c1", SourceRef: "gh:pr/42/rt/A1",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := s.AddDiffAnnotation(ctx, domain.DiffAnnotation{
		Feature: f2.ID, File: "x.go", Anchor: "h1", Comment: "c1", SourceRef: "gh:pr/42/rt/A1",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if id1 == id2 {
		t.Fatalf("same SourceRef under different features collapsed: both got id %d", id1)
	}
}

func TestDiffAnnotationSourceRefDistinctRefs(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := feat(1, "Dark mode")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()

	id1, err := s.AddDiffAnnotation(ctx, domain.DiffAnnotation{
		Feature: f.ID, File: "x.go", Anchor: "h1", Comment: "c1", SourceRef: "gh:pr/42/rt/A1",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := s.AddDiffAnnotation(ctx, domain.DiffAnnotation{
		Feature: f.ID, File: "y.go", Anchor: "h2", Comment: "c2", SourceRef: "gh:pr/42/rt/A2",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if id1 == id2 {
		t.Fatalf("distinct SourceRefs collapsed: both got id %d", id1)
	}

	got, err := s.ListDiffAnnotations(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("listed %d annotations, want 2", len(got))
	}
}

func TestDiffAnnotationSourceRefSchema(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	func() {
		rows, err := s.db.QueryContext(ctx,
			`SELECT name FROM pragma_table_info('diff_annotations') WHERE name = 'source_ref'`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		if !rows.Next() {
			t.Fatal("diff_annotations missing source_ref column")
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
	}()

	var found bool
	func() {
		idxRows, err := s.db.QueryContext(ctx, `PRAGMA index_list('diff_annotations')`)
		if err != nil {
			t.Fatal(err)
		}
		defer idxRows.Close()
		for idxRows.Next() {
			var seq int
			var name string
			var unique, partial int
			var origin string
			if err := idxRows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
				t.Fatal(err)
			}
			if name == "diff_annotations_source_ref" {
				found = true
				if unique != 1 {
					t.Errorf("index %s: unique = %d, want 1", name, unique)
				}
				if partial != 1 {
					t.Errorf("index %s: partial = %d, want 1", name, partial)
				}
			}
		}
		if err := idxRows.Err(); err != nil {
			t.Fatal(err)
		}
	}()
	if !found {
		t.Fatal("diff_annotations_source_ref index not found")
	}

	colRows, err := s.db.QueryContext(ctx, `PRAGMA index_info('diff_annotations_source_ref')`)
	if err != nil {
		t.Fatal(err)
	}
	defer colRows.Close()
	var cols []string
	for colRows.Next() {
		var seqno, cid int
		var name string
		if err := colRows.Scan(&seqno, &cid, &name); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, name)
	}
	if err := colRows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(cols) != 2 || cols[0] != "feature_id" || cols[1] != "source_ref" {
		t.Fatalf("diff_annotations_source_ref covers %v, want [feature_id source_ref]", cols)
	}
}

func TestDiffAnnotationSourceRefMigration(t *testing.T) {
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
	if _, err := s.AddDiffAnnotation(ctx, domain.DiffAnnotation{
		Feature: f.ID, File: "x.go", Anchor: "h1", Comment: "pre-migration",
	}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// regress diff_annotations to the pre-migration shape: the partial
	// index must be dropped before the column it references, or SQLite
	// refuses the column drop ("no such column: source_ref").
	db, err := sql.Open("sqlite", "file:"+url.PathEscape(w.DBFile()))
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`DROP INDEX IF EXISTS diff_annotations_source_ref`,
		`ALTER TABLE diff_annotations DROP COLUMN source_ref`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("building old-schema db: %v", err)
		}
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO diff_annotations (feature_id, file, anchor, excerpt, comment, resolved, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		string(f.ID), "y.go", "h2", "", "second pre-migration row", 0, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// reopen: the migration must land, and the partial unique index with it.
	s, err = OpenStore(w.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	got, err := s.ListDiffAnnotations(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("listed %d annotations, want 2", len(got))
	}
	for _, a := range got {
		if a.SourceRef != "" {
			t.Errorf("pre-migration row SourceRef = %q, want empty", a.SourceRef)
		}
	}

	id1, err := s.AddDiffAnnotation(ctx, domain.DiffAnnotation{
		Feature: f.ID, File: "z.go", Anchor: "h3", Comment: "post-migration", SourceRef: "gh:pr/42/rt/A1",
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	id2, err := s.AddDiffAnnotation(ctx, domain.DiffAnnotation{
		Feature: f.ID, File: "z.go", Anchor: "h3", Comment: "post-migration again", SourceRef: "gh:pr/42/rt/A1",
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Fatalf("partial unique index not created by migration path: ids differ %d != %d", id1, id2)
	}
}
