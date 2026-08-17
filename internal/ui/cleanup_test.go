package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// landFeature commits work on the feature branch and squash-merges it into
// main — the way gummi actually lands (a squash merge keeps the branch's
// own commits, so it stays non-ancestor and reads as landed).
func landFeature(t *testing.T, root, wt string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(wt, "feat.go"), []byte("package x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, wt, "add", ".")
	git(t, wt, "commit", "-qm", "feature work")
	git(t, root, "merge", "--squash", "gummi/FD-001-rebase-me")
	git(t, root, "commit", "-qm", "land FD-001")
}

func TestLandedDetectionAndCleanup(t *testing.T) {
	m, root, wt := rebaseFeatureFixture(t)
	landFeature(t, root, wt)
	// a landed worktree commonly has untracked build artifacts; cleanup
	// must still succeed (force removal), not abort on them.
	if err := os.WriteFile(filepath.Join(wt, "build.out"), []byte("artifact\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// reload rows → the feature is flagged landed
	m = pump(t, m, m.loadRows)
	var row featureRow
	for _, r := range m.rows {
		if r.F.ID == "FD-001" {
			row = r
		}
	}
	if !row.Landed {
		t.Fatal("merged branch not detected as landed")
	}

	// select it, press c → confirm dialog → y
	m.sel = 0
	m = press(t, m, tea.KeyPressMsg{Code: 'c', Text: "c"})
	if m.Overlay.Top() == nil {
		t.Fatal("c did not open the cleanup confirm dialog")
	}
	m = press(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})

	ctx := context.Background()
	// the feature record survives...
	if _, err := m.store.GetFeature(ctx, "FD-001"); err != nil {
		t.Errorf("feature record removed by cleanup: %v", err)
	}
	// ...but the worktree and branch are gone
	f, _ := m.store.GetFeature(ctx, "FD-001")
	if ok, _ := m.wt.Exists(ctx, &f); ok {
		t.Error("worktree survived cleanup")
	}
	if ok, _ := m.wt.BranchExists(ctx, &f); ok {
		t.Error("branch survived cleanup")
	}
	if !strings.Contains(m.notice.text, "cleaned up") {
		t.Errorf("notice = %q, want a cleanup confirmation", m.notice.text)
	}
}

// TestFreshWorktreeNotLanded asserts a worktree whose branch has no
// commits of its own reads as not-landed after loadRows, without walking
// the Landed squash/merge-tree path (it is gated behind BranchAhead).
func TestFreshWorktreeNotLanded(t *testing.T) {
	m, _, _ := rebaseFeatureFixture(t)
	m = pump(t, m, m.loadRows)
	var row featureRow
	for _, r := range m.rows {
		if r.F.ID == "FD-001" {
			row = r
		}
	}
	if !row.HasWorktree {
		t.Fatal("fixture worktree missing")
	}
	if row.Landed {
		t.Fatal("a branch with no commits of its own read as landed")
	}
}

func TestCleanupRefusedWhenNotLanded(t *testing.T) {
	m, _, _ := rebaseFeatureFixture(t)
	m = pump(t, m, m.loadRows)
	m.sel = 0
	// not landed → c reports so and opens no dialog
	m = press(t, m, tea.KeyPressMsg{Code: 'c', Text: "c"})
	if m.Overlay.Top() != nil {
		t.Fatal("cleanup confirm opened for an unlanded feature")
	}
	if !strings.Contains(m.notice.text, "hasn't landed") {
		t.Errorf("notice = %q, want a not-landed message", m.notice.text)
	}
}
