package state

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
)

func bug(num int, title, ref string) *domain.Feature {
	id, _ := domain.NewID(domain.KindBug, num)
	slug, _ := domain.Slugify(title)
	now := time.Now().UTC()
	return &domain.Feature{
		ID: id, Num: num, Kind: domain.KindBug, Title: title, Slug: slug,
		Stage: domain.StageTodo, ExternalRef: ref,
		CreatedAt: now, UpdatedAt: now,
	}
}

func TestStoreRoundTripsBugFields(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	b := bug(1, "Crash on empty input", "https://github.com/o/r/issues/9")
	if err := s.CreateFeature(ctx, b); err != nil {
		t.Fatalf("create bug: %v", err)
	}
	got, err := s.GetFeature(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != domain.KindBug {
		t.Errorf("kind = %q, want bug", got.Kind)
	}
	if got.ExternalRef != b.ExternalRef {
		t.Errorf("external_ref = %q, want %q", got.ExternalRef, b.ExternalRef)
	}
}

func TestFeatureByExternalRef(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	b := bug(1, "Login loops", "https://github.com/o/r/issues/42")
	if err := s.CreateFeature(ctx, b); err != nil {
		t.Fatal(err)
	}
	got, err := s.FeatureByExternalRef(ctx, "https://github.com/o/r/issues/42")
	if err != nil {
		t.Fatalf("lookup by ref: %v", err)
	}
	if got.ID != b.ID {
		t.Errorf("got %s, want %s", got.ID, b.ID)
	}
	// a miss and an empty ref both report ErrNotFound (never a false match).
	if _, err := s.FeatureByExternalRef(ctx, "https://github.com/o/r/issues/999"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown ref err = %v, want ErrNotFound", err)
	}
	if _, err := s.FeatureByExternalRef(ctx, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("empty ref err = %v, want ErrNotFound", err)
	}
}

// A bug used to have its own workflow graph; there is one graph now, so
// what used to distinguish a bug's transitions from a feature's is gone
// along with the test that pinned the difference. The kind still selects
// the card's CONTRACT (its artifact template, its stage hints) — that is
// covered where those live, not here.

// TestStoreRoundTripsRepo: the repo column round-trips; an empty value
// (the default) reads back empty, and a named repo persists.
func TestStoreRoundTripsRepo(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	b := bug(7, "Repo drift", "")
	b.Repo = "lxd"
	if err := s.CreateFeature(ctx, b); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetFeature(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Repo != "lxd" {
		t.Errorf("repo = %q, want lxd", got.Repo)
	}

	// a card created with no repo (a legacy row) reads back empty.
	d := bug(8, "No repo", "")
	if err := s.CreateFeature(ctx, d); err != nil {
		t.Fatal(err)
	}
	got2, err := s.GetFeature(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got2.Repo != "" {
		t.Errorf("repo = %q, want empty default", got2.Repo)
	}
}
