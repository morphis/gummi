package state

import (
	"context"
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
