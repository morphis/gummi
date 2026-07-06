package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
)

// diffWorkspace creates a feature at the review stage with a worktree that
// has an uncommitted change, so the diff surface has something to show.
func diffWorkspace(t *testing.T) (*Shell, string) {
	t.Helper()
	m, root := newWorkspace(t)
	m.now = func() time.Time { return fixedTime }
	ctx := context.Background()
	f := &domain.Feature{
		ID: "FD-001", Num: 1, Title: "Dark mode", Slug: "dark-mode",
		Stage: domain.StageReview, CreatedAt: fixedTime, UpdatedAt: fixedTime,
	}
	if err := m.store.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	if _, err := m.wt.Create(ctx, f); err != nil {
		t.Fatal(err)
	}
	// change a file in the worktree so `git diff` is non-empty
	wtFile := filepath.Join(root, f.WorktreePath(), "README.md")
	if err := os.WriteFile(wtFile, []byte("x\nsecond line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m = pump(t, m, m.Init()) // load rows
	return m, root
}

func openDiffFor(t *testing.T, m *Shell) *Shell {
	t.Helper()
	m = press(t, m, tea.KeyPressMsg{Code: 'd', Text: "d"})
	if m.diff == nil {
		t.Fatalf("d did not open the diff surface (notice: %q)", m.notice.text)
	}
	return m
}

func TestDiffViewOpensWithChange(t *testing.T) {
	m, _ := diffWorkspace(t)
	m = openDiffFor(t, m)
	found := false
	for _, l := range m.diff.lines {
		if l == "+second line" {
			found = true
		}
	}
	if !found {
		t.Fatalf("diff does not show the added line:\n%v", m.diff.lines)
	}
}

func TestDiffCommentPersistsAndAnchors(t *testing.T) {
	m, root := diffWorkspace(t)
	m = openDiffFor(t, m)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab}) // annotate mode

	// move the cursor onto the "+second line" line
	target := 0
	for i, l := range m.diff.lines {
		if l == "+second line" {
			target = i + 1
		}
	}
	m.diff.cursor = target
	m = press(t, m, tea.KeyPressMsg{Code: 'c', Text: "c"})
	if m.Overlay.Top() == nil {
		t.Fatal("c did not open the comment dialog")
	}
	m = typeString(t, m, "guard the empty case")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	// persisted to the store
	anns, err := m.store.ListDiffAnnotations(context.Background(), "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	if len(anns) != 1 || anns[0].Comment != "guard the empty case" || anns[0].File != "README.md" {
		t.Fatalf("annotation not persisted correctly: %+v", anns)
	}
	// and re-anchored on reload: located, not orphaned
	if len(m.diff.orphans) != 0 {
		t.Errorf("fresh annotation orphaned: %v", m.diff.orphans)
	}
	if m.diff.openCount() != 1 {
		t.Errorf("openCount = %d, want 1", m.diff.openCount())
	}
	anchoredLine := -1
	for idx := range m.diff.located {
		anchoredLine = idx
	}
	if anchoredLine < 0 || m.diff.lines[anchoredLine] != "+second line" {
		t.Errorf("annotation anchored to wrong line %d", anchoredLine)
	}

	// change the annotated line's content → the anchor orphans on reload
	changed := filepath.Join(root, (&domain.Feature{ID: "FD-001", Slug: "dark-mode"}).WorktreePath(), "README.md")
	if err := os.WriteFile(changed, []byte("x\ntotally different content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m = pump(t, m, m.reloadDiff())
	if len(m.diff.orphans) != 1 {
		t.Errorf("annotation did not orphan after its line changed: located=%v orphans=%v",
			m.diff.located, m.diff.orphans)
	}
}

func TestDiffRequestChangesGuardsStage(t *testing.T) {
	m, _ := diffWorkspace(t)
	eng := engine.New(engine.Config{
		Agent: agent.NewFake("ok"), Store: m.store, Worktrees: m.wt,
		Workspace: m.ws, MaxActive: 1,
	})
	t.Cleanup(func() { eng.Close() })
	m.AttachEngine(eng)
	m = openDiffFor(t, m)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	for i, l := range m.diff.lines {
		if l == "+second line" {
			m.diff.cursor = i + 1
		}
	}
	m = press(t, m, tea.KeyPressMsg{Code: 'c', Text: "c"})
	m = typeString(t, m, "nit")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	// force a stage from which implement is unreachable; "request changes"
	// must refuse without transitioning or tearing anything down.
	m.diff.f.Stage = domain.StageDone
	m = press(t, m, tea.KeyPressMsg{Code: 'R', Text: "R"})
	if m.diff == nil {
		t.Fatal("surface closed on a rejected request-changes")
	}
	f, _ := m.store.GetFeature(context.Background(), "FD-001")
	if f.Stage != domain.StageReview {
		t.Errorf("guard failed: stage changed to %s", f.Stage)
	}
	if !strings.Contains(m.notice.text, "review or verify") {
		t.Errorf("missing guard notice, got %q", m.notice.text)
	}
}

func TestDiffResolveTogglesOpenCount(t *testing.T) {
	m, _ := diffWorkspace(t)
	m = openDiffFor(t, m)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	for i, l := range m.diff.lines {
		if l == "+second line" {
			m.diff.cursor = i + 1
		}
	}
	m = press(t, m, tea.KeyPressMsg{Code: 'c', Text: "c"})
	m = typeString(t, m, "nit")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.diff.openCount() != 1 {
		t.Fatalf("openCount = %d before resolve, want 1", m.diff.openCount())
	}
	// x on the annotated line toggles resolved → open count drops to 0
	m = press(t, m, tea.KeyPressMsg{Code: 'x', Text: "x"})
	if m.diff.openCount() != 0 {
		t.Errorf("openCount = %d after resolve, want 0", m.diff.openCount())
	}
}
