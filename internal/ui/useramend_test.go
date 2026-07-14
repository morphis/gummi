package ui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
)

// TestAnnotationCommitsUserAmendment: adding a spec annotation commits
// the artifact to the feature branch with the user-provenance trailer,
// and the commit carries only the artifact — not other dirt an agent
// may have left in the worktree.
func TestAnnotationCommitsUserAmendment(t *testing.T) {
	m, root := diffWorkspace(t) // FD-001 at review with a worktree (dirty README)
	m = press(t, m, tea.KeyPressMsg{Code: 's', Text: "s"})
	if m.spec == nil {
		t.Fatalf("s did not open the spec surface (notice: %q)", m.notice.text)
	}

	msg := m.addSpecComment(1, "tighten the problem statement")()
	if n, ok := msg.(noticeMsg); ok && n.isErr {
		t.Fatalf("addSpecComment failed: %s", n.text)
	}

	wt := filepath.Join(root, m.spec.f.WorktreePath())
	body := gitOut(t, wt, "log", "-1", "--format=%B")
	if !strings.HasPrefix(body, "docs(spec): FD-001 user amendment") {
		t.Errorf("commit subject = %q, want docs(spec): FD-001 user amendment", body)
	}
	if !strings.Contains(body, "Gummi-Author: user") {
		t.Errorf("commit body missing Gummi-Author: user trailer:\n%s", body)
	}
	// per-path staging: the dirty README stays uncommitted
	files := gitOut(t, wt, "show", "--name-only", "--format=", "HEAD")
	if strings.Contains(files, "README.md") {
		t.Errorf("amendment commit swept unrelated dirt:\n%s", files)
	}
	if status := gitOut(t, wt, "status", "--porcelain", "--", "README.md"); status == "" {
		t.Error("dirty README was absorbed by the amendment commit")
	}
}

// TestAmendmentCommitIdempotent: re-committing identical artifact
// content is a no-op — no second commit, no error notice.
func TestAmendmentCommitIdempotent(t *testing.T) {
	m, root := diffWorkspace(t)
	m = press(t, m, tea.KeyPressMsg{Code: 's', Text: "s"})
	if m.spec == nil {
		t.Fatal("s did not open the spec surface")
	}
	f := m.spec.f
	ctx := context.Background()

	if notice := m.commitUserAmendment(ctx, f, m.spec.content+"\namended\n"); notice != "" {
		t.Fatalf("first commit returned notice %q", notice)
	}
	wt := filepath.Join(root, f.WorktreePath())
	head := gitOut(t, wt, "rev-parse", "HEAD")

	if notice := m.commitUserAmendment(ctx, f, m.spec.content+"\namended\n"); notice != "" {
		t.Fatalf("identical re-commit returned notice %q", notice)
	}
	if again := gitOut(t, wt, "rev-parse", "HEAD"); again != head {
		t.Errorf("identical content produced a second commit: %s → %s", head, again)
	}
}

// TestDraftStageEditNoCommit: before a worktree exists the artifact is
// a draft — the amendment helper is a silent no-op and creates nothing.
func TestDraftStageEditNoCommit(t *testing.T) {
	m, _ := newWorkspace(t)
	ctx := context.Background()
	f := &domain.Feature{
		ID: "FD-002", Num: 2, Title: "Light mode", Slug: "light-mode",
		Stage: domain.StageSpec, CreatedAt: fixedTime, UpdatedAt: fixedTime,
	}
	if err := m.store.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	if notice := m.commitUserAmendment(ctx, *f, "draft content"); notice != "" {
		t.Errorf("draft-stage amendment returned notice %q, want silent no-op", notice)
	}
	if ok, err := m.wt.Exists(ctx, f); err != nil || ok {
		t.Errorf("draft-stage amendment materialized a worktree (ok=%v err=%v)", ok, err)
	}
}
