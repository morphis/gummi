package worktree

import (
	"strings"
	"testing"
)

func TestProvenanceWarnings(t *testing.T) {
	root := newRepo(t)
	m, f, p := committedFeature(t, root)

	// clean branch: the one "feature commit" carries no attribution
	warns, err := m.ProvenanceWarnings(ctx, f)
	if err != nil || len(warns) != 0 {
		t.Fatalf("clean branch flagged: %v %v", warns, err)
	}

	writeFile(t, p, "more.txt", "x\n")
	mustGit(t, p, "add", ".")
	mustGit(t, p, "commit", "-q", "-m",
		"add feature polish\n\nReview verdict below.\nVERDICT: pass\n\nCo-Authored-By: Claude Haiku 4.5 <noreply@example.invalid>")

	warns, err = m.ProvenanceWarnings(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "Co-Authored-By") && !strings.Contains(warns[0], "VERDICT") {
		t.Fatalf("attribution commit not flagged: %v", warns)
	}
}

func TestProvenanceIgnoresProductVocabulary(t *testing.T) {
	root := newRepo(t)
	m, f, p := committedFeature(t, root)
	// gummi's own domain legitimately talks about agents and reviews;
	// bare product words must not trip the scan
	writeFile(t, p, "docs.txt", "x\n")
	mustGit(t, p, "add", ".")
	mustGit(t, p, "commit", "-q", "-m",
		"feat(ui): show the reviewer role's copilot quota on the board")
	warns, err := m.ProvenanceWarnings(ctx, f)
	if err != nil || len(warns) != 0 {
		t.Fatalf("product vocabulary flagged: %v %v", warns, err)
	}
}
