package ui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/golden"

	"github.com/morphia/gummi/internal/spec"
)

// openSpecFor drives 's' on the selected feature and settles commands.
func openSpecFor(t *testing.T, m *Shell) *Shell {
	t.Helper()
	m = press(t, m, tea.KeyPressMsg{Code: 's', Text: "s"})
	if m.spec == nil {
		t.Fatal("s did not open the spec view")
	}
	return m
}

func specWorkspace(t *testing.T) *Shell {
	t.Helper()
	m, _ := newWorkspace(t)
	m.now = func() time.Time { return fixedTime }
	m = pump(t, m, m.Init())
	m = press(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	m = typeString(t, m, "Dark mode")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	return m
}

func TestSpecOpenCreatesDraftFromTemplate(t *testing.T) {
	m := specWorkspace(t)
	m = openSpecFor(t, m)
	if !strings.Contains(m.spec.content, "# FD-001: Dark mode") {
		t.Fatalf("draft not from template: %q", m.spec.content[:60])
	}
	if !strings.HasPrefix(m.spec.path, m.ws.DraftsDir()) {
		t.Errorf("draft not in drafts dir: %s", m.spec.path)
	}
	if _, err := os.Stat(m.spec.path); err != nil {
		t.Errorf("draft file missing: %v", err)
	}
	// reopening loads the same file, not a fresh template
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.spec != nil {
		t.Fatal("esc did not close spec view")
	}
	m = openSpecFor(t, m)
	if m.spec.path == "" {
		t.Fatal("reopen lost the spec path")
	}
}

func TestSpecViewReadGolden(t *testing.T) {
	m := specWorkspace(t)
	m = openSpecFor(t, m)
	golden.RequireEqual(t, []byte(m.View().Content))
}

func TestSpecViewAnnotateGolden(t *testing.T) {
	m := specWorkspace(t)
	m = openSpecFor(t, m)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	if !m.spec.annotate {
		t.Fatal("tab did not enter annotate mode")
	}
	golden.RequireEqual(t, []byte(m.View().Content))
}

func TestSpecAnnotateNavigation(t *testing.T) {
	m := specWorkspace(t)
	m = openSpecFor(t, m)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	if m.spec.cursor != 1 {
		t.Fatalf("cursor starts at %d", m.spec.cursor)
	}
	m = press(t, m, tea.KeyPressMsg{Code: 'j', Text: "j"})
	m = press(t, m, tea.KeyPressMsg{Code: 'j', Text: "j"})
	if m.spec.cursor != 3 {
		t.Fatalf("cursor after jj = %d", m.spec.cursor)
	}
	// n jumps to the first marker (template question under Problem)
	m = press(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	markers := m.spec.doc.MarkerLines()
	if m.spec.cursor != markers[0] {
		t.Fatalf("n jumped to %d, want %d", m.spec.cursor, markers[0])
	}
	m = press(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	if m.spec.cursor != markers[1] {
		t.Fatalf("second n jumped to %d, want %d", m.spec.cursor, markers[1])
	}
	m = press(t, m, tea.KeyPressMsg{Code: 'p', Text: "p"})
	if m.spec.cursor != markers[0] {
		t.Fatalf("p jumped to %d, want %d", m.spec.cursor, markers[0])
	}
	// k cannot go above line 1
	for range 99 {
		m = press(t, m, tea.KeyPressMsg{Code: 'k', Text: "k"})
	}
	if m.spec.cursor != 1 {
		t.Fatalf("cursor clamped wrong: %d", m.spec.cursor)
	}
}

func TestSpecCommentFlow(t *testing.T) {
	m := specWorkspace(t)
	m = openSpecFor(t, m)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	// cursor on line 1 (the title), c → dialog, type, enter
	m = press(t, m, tea.KeyPressMsg{Code: 'c', Text: "c"})
	if m.Overlay.Top() == nil {
		t.Fatal("c did not open the comment dialog")
	}
	m = typeString(t, m, "why this title?")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.Overlay.HasDialogs() {
		t.Fatal("comment dialog did not close")
	}
	want := "%% @user(2026-07-03): why this title?"
	if !strings.Contains(m.spec.content, want) {
		t.Fatalf("comment not written:\n%s", m.spec.content)
	}
	// persisted to disk, and threaded directly under line 1
	raw, err := os.ReadFile(m.spec.path)
	if err != nil || !strings.Contains(string(raw), want) {
		t.Fatalf("comment not persisted: %v", err)
	}
	d := spec.Parse(string(raw))
	if d.Markers[0].Anchor != 1 || d.Markers[0].Author != "user" {
		t.Fatalf("comment parsed wrong: %+v", d.Markers[0])
	}
	// esc cancels without writing
	m = press(t, m, tea.KeyPressMsg{Code: 'c', Text: "c"})
	m = typeString(t, m, "never saved")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if strings.Contains(m.spec.content, "never saved") {
		t.Fatal("cancelled comment was written")
	}
}

func TestSpecMigratesToWorktreeAtApproval(t *testing.T) {
	m := specWorkspace(t)
	// advance to spec, open the draft, annotate it
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = openSpecFor(t, m)
	draftPath := m.spec.path
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	m = press(t, m, tea.KeyPressMsg{Code: 'c', Text: "c"})
	m = typeString(t, m, "keep me")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})

	// approve the spec (leave Spec) → worktree + committed spec
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = openSpecFor(t, m)
	if m.spec.path == draftPath {
		t.Fatalf("spec still reads the draft after approval: %s", m.spec.path)
	}
	if !strings.Contains(m.spec.path, filepath.Join(".gummi", "worktrees", "FD-001")) {
		t.Fatalf("spec path not in worktree: %s", m.spec.path)
	}
	// annotations travel with the migrated spec
	if !strings.Contains(m.spec.content, "keep me") {
		t.Fatal("draft annotations lost in migration")
	}
	// the draft is retired
	if _, err := os.Stat(draftPath); !os.IsNotExist(err) {
		t.Fatal("draft still present after migration")
	}
	// and the spec is committed on the feature branch
	wtDir := filepath.Dir(filepath.Dir(filepath.Dir(m.spec.path))) // …/worktrees/FD-001
	out, err := exec.CommandContext(context.Background(), "git", "-C", wtDir, "log", "--oneline", "-1").Output()
	if err != nil || !strings.Contains(string(out), "docs(spec): FD-001") {
		t.Fatalf("spec commit missing: %v %q", err, out)
	}
}

func TestSpecEditorRequiresEDITOR(t *testing.T) {
	t.Setenv("EDITOR", "")
	m := specWorkspace(t)
	m = openSpecFor(t, m)
	m = press(t, m, tea.KeyPressMsg{Code: 'e', Text: "e"})
	if !m.notice.isErr || !strings.Contains(m.notice.text, "EDITOR") {
		t.Fatalf("notice = %+v, want $EDITOR error", m.notice)
	}
}
