package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// landFeature commits work on the feature branch and merges it into main
// so the branch is an ancestor of main's HEAD (i.e. landed).
func landFeature(t *testing.T, root, wt string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(wt, "feat.go"), []byte("package x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, wt, "add", ".")
	git(t, wt, "commit", "-qm", "feature work")
	git(t, root, "merge", "--no-ff", "-m", "land FD-001", "gummi/FD-001-rebase-me")
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
