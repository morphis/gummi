package state

import (
	"context"
	"testing"
)

// A pre-drafted landing message survives create → read and the
// SetCommitDraft side-channel, always with the branch tip it was composed
// against: the message and the SHA are one fact, and a message read back
// without its SHA would read as fresh against a branch it never saw.
func TestCommitDraftRoundtrip(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	f := feat(1, "Add a healthz endpoint")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.CommitDraft != "" || got.CommitDraftSHA != "" {
		t.Fatalf("a fresh card carries a draft: %q @ %q", got.CommitDraft, got.CommitDraftSHA)
	}

	const (
		msg = "feat(api): answer readiness probes without a database round trip\n\n- keep the probe honest when the pool is exhausted"
		sha = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b"
	)
	before, err := s.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetCommitDraft(ctx, f.ID, msg, sha); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.CommitDraft != msg || got.CommitDraftSHA != sha {
		t.Fatalf("draft = %q @ %q, want %q @ %q", got.CommitDraft, got.CommitDraftSHA, msg, sha)
	}
	// side-channel: a draft is something gummi noticed about the card, not
	// a step the card took, so it must not surface in the audit trail as
	// one.
	if !got.UpdatedAt.Equal(before.UpdatedAt) {
		t.Errorf("SetCommitDraft moved updated_at: %v → %v", before.UpdatedAt, got.UpdatedAt)
	}
	if got.Stage != before.Stage {
		t.Errorf("SetCommitDraft moved the stage: %s → %s", before.Stage, got.Stage)
	}
}

// Clearing the draft clears its SHA with it. A stored SHA with no message
// is the one state that could later be mistaken for a fresh draft of "",
// so the write refuses to produce it.
func TestCommitDraftClearsItsSHA(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	f := feat(2, "Another feature")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCommitDraft(ctx, f.ID, "feat(x): y", "deadbeef"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCommitDraft(ctx, f.ID, "", "deadbeef"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.CommitDraft != "" || got.CommitDraftSHA != "" {
		t.Fatalf("cleared draft left %q @ %q", got.CommitDraft, got.CommitDraftSHA)
	}
}

// A card created carrying a draft keeps it: the INSERT and the SELECT
// have to agree about the two columns, which is the drift this catches.
func TestCommitDraftSurvivesCreate(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	f := feat(3, "A third feature")
	f.CommitDraft = "fix(ui): stop clobbering the operator's keystrokes"
	f.CommitDraftSHA = "1e0cbd7ac2f2a6c2b6f5a6a54d2a2b9b7e6b4c3d"
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.CommitDraft != f.CommitDraft || got.CommitDraftSHA != f.CommitDraftSHA {
		t.Fatalf("draft lost in create: %q @ %q, want %q @ %q",
			got.CommitDraft, got.CommitDraftSHA, f.CommitDraft, f.CommitDraftSHA)
	}
}
