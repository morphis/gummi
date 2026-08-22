package state

import (
	"context"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

// A feature created without a linked PR reads back Empty() — the "unlinked"
// state every pre-existing row (created before this card) reads as.
func TestPullRequestDefaultsEmpty(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := feat(1, "Add outbound PR ref")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.PullRequest.Empty() {
		t.Fatalf("default PullRequest = %+v, want Empty()", got.PullRequest)
	}
}

// SetPullRequest links a card, the link survives a re-open of the store,
// and a later clear (an Empty() ref) re-reads as Empty() again.
func TestStoreSetPullRequestRoundtrip(t *testing.T) {
	dir := t.TempDir()
	dbPath := dir + "/state.db"

	s, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	f := feat(1, "Add outbound PR ref")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}

	ref := domain.PullRequestRef{
		Repo: "o/r", Number: 42, URL: "https://github.com/o/r/pull/42",
		HeadSHA: "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b",
	}
	if err := s.SetPullRequest(ctx, f.ID, ref); err != nil {
		t.Fatalf("SetPullRequest: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	got, err := s2.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PullRequest != ref {
		t.Fatalf("PullRequest after reopen = %+v, want %+v", got.PullRequest, ref)
	}

	// clearing (Empty() ref) re-reads as Empty() — the unlink path.
	if err := s2.SetPullRequest(ctx, f.ID, domain.PullRequestRef{}); err != nil {
		t.Fatalf("clearing SetPullRequest: %v", err)
	}
	got, err = s2.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.PullRequest.Empty() {
		t.Fatalf("PullRequest after clear = %+v, want Empty()", got.PullRequest)
	}
}

// SetPullRequest refuses a malformed non-empty ref rather than persisting a
// row Validate() would later reject on read.
func TestStoreSetPullRequestRejectsMalformed(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := feat(1, "Add outbound PR ref")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	bad := domain.PullRequestRef{Repo: "bare-owner", Number: 1, URL: "https://github.com/o/r/pull/1"}
	if err := s.SetPullRequest(ctx, f.ID, bad); err == nil {
		t.Fatal("SetPullRequest accepted a malformed ref")
	}
}
