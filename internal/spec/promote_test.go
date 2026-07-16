package spec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

func promoteFixture(t *testing.T) (f *domain.Feature, artifact, draft, legacy string) {
	t.Helper()
	root := t.TempDir()
	id, _ := domain.NewFeatureID(1)
	f = &domain.Feature{ID: id, Num: 1, Title: "Dark mode", Slug: "dark-mode", Stage: domain.StageSpec}
	artifact = filepath.Join(root, f.ArtifactPath())
	draft = filepath.Join(root, ".gummi", "state", "drafts", DraftFilename(f))
	legacy = filepath.Join(root, f.WorktreePath(), f.ArtifactPath())
	return f, artifact, draft, legacy
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPromoteConsumesDraft(t *testing.T) {
	f, artifact, draft, legacy := promoteFixture(t)
	write(t, draft, "# the draft\n")
	if err := Promote(artifact, draft, legacy, f); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(artifact); err != nil || string(raw) != "# the draft\n" {
		t.Fatalf("artifact = %q, %v", raw, err)
	}
	if _, err := os.Stat(draft); !os.IsNotExist(err) {
		t.Fatal("draft not retired")
	}
}

func TestPromoteIdempotentKeepsArtifact(t *testing.T) {
	f, artifact, draft, legacy := promoteFixture(t)
	write(t, artifact, "# already home\n")
	write(t, draft, "# stale draft remnant\n")
	if err := Promote(artifact, draft, legacy, f); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(artifact); string(raw) != "# already home\n" {
		t.Fatalf("promoted-over artifact: %q", raw)
	}
	if _, err := os.Stat(draft); !os.IsNotExist(err) {
		t.Fatal("draft remnant not retired")
	}
}

// TestPromoteMigratesLegacyWorktreeCopy: an item mid-flight from the era
// when the artifact was committed to the feature branch has no draft and
// no workspace copy — the worktree copy seeds the promotion so its
// content isn't lost to a blank template.
func TestPromoteMigratesLegacyWorktreeCopy(t *testing.T) {
	f, artifact, draft, legacy := promoteFixture(t)
	write(t, legacy, "# committed-era spec\n")
	if err := Promote(artifact, draft, legacy, f); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(artifact); err != nil || string(raw) != "# committed-era spec\n" {
		t.Fatalf("artifact = %q, %v", raw, err)
	}
}

func TestPromoteFallsBackToTemplate(t *testing.T) {
	f, artifact, draft, legacy := promoteFixture(t)
	if err := Promote(artifact, draft, legacy, f); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(artifact)
	if err != nil || !strings.Contains(string(raw), "# FD-001: Dark mode") {
		t.Fatalf("artifact = %q, %v", raw, err)
	}
}
