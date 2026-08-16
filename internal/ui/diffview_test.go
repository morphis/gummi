package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

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
		Agents: singleAgent(agent.NewFake("ok")), Store: m.store, Worktrees: m.wt,
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
	if !strings.Contains(m.notice.text, "implement, review, or verify") {
		t.Errorf("missing guard notice, got %q", m.notice.text)
	}
}

func TestDiffRequestChangesRerunsWorkStage(t *testing.T) {
	// R at the work stage itself (the "implement finished" gate) has no
	// bounce edge to take: it re-runs implement in place, and the fresh
	// run carries the open annotations via the engine's diff hints.
	m, _ := diffWorkspace(t)
	ctx := context.Background()
	if _, err := m.store.Transition(ctx, "FD-001", domain.StageImplement, "review"); err != nil {
		t.Fatal(err)
	}
	eng := engine.New(engine.Config{
		Agents: singleAgent(agent.NewFake("addressed")), Store: m.store, Worktrees: m.wt,
		Workspace: m.ws, MaxActive: 1,
	})
	t.Cleanup(func() { eng.Close() })
	m.AttachEngine(eng)
	m = pump(t, m, m.Init()) // reload rows at the new stage

	m = openDiffFor(t, m)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	for i, l := range m.diff.lines {
		if l == "+second line" {
			m.diff.cursor = i + 1
		}
	}
	m = press(t, m, tea.KeyPressMsg{Code: 'c', Text: "c"})
	m = typeString(t, m, "extract this into a helper")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	m = press(t, m, tea.KeyPressMsg{Code: 'R', Text: "R"})
	if m.diff != nil {
		t.Fatal("request-changes did not close the diff surface")
	}
	if !strings.Contains(m.notice.text, "re-running implement") {
		t.Errorf("notice = %q, want a re-run confirmation", m.notice.text)
	}
	f, _ := m.store.GetFeature(ctx, "FD-001")
	if f.Stage != domain.StageImplement {
		t.Errorf("stage changed to %s, want implement (no transition)", f.Stage)
	}
	settleChat(t, eng)
	s := eng.Get("FD-001")
	if s == nil || s.Feature.Stage != domain.StageImplement {
		t.Fatal("request-changes did not re-run the implement stage")
	}
}

func TestOpenDiffCommentBlocksGate(t *testing.T) {
	// unresolved diff annotations block g (DESIGN §6.1), same as spec
	// annotations; resolving unblocks.
	m, _ := diffWorkspace(t) // FD-001 at review with a worktree
	ctx := context.Background()
	ann := domain.DiffAnnotation{
		Feature: "FD-001", File: "README.md",
		Anchor: "second line", Excerpt: "+second line", Comment: "nit",
	}
	id, err := m.store.AddDiffAnnotation(ctx, ann, fixedTime)
	if err != nil {
		t.Fatal(err)
	}

	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	f, _ := m.store.GetFeature(ctx, "FD-001")
	if f.Stage != domain.StageReview {
		t.Fatalf("open diff comment did not block the gate (stage=%s)", f.Stage)
	}
	if !strings.Contains(m.notice.text, "diff comment") {
		t.Errorf("notice = %q, want a blocking message", m.notice.text)
	}

	if err := m.store.SetDiffAnnotationResolved(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	f, _ = m.store.GetFeature(ctx, "FD-001")
	if f.Stage != domain.StageVerify {
		t.Fatalf("resolving the diff comment did not unblock the gate (stage=%s)", f.Stage)
	}
}

// commentOnAddedLine moves the cursor onto "+second line" and attaches
// a comment through the dialog.
func commentOnAddedLine(t *testing.T, m *Shell, text string) *Shell {
	t.Helper()
	for i, l := range m.diff.lines {
		if l == "+second line" {
			m.diff.cursor = i + 1
		}
	}
	m = press(t, m, tea.KeyPressMsg{Code: 'c', Text: "c"})
	m = typeString(t, m, text)
	return press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
}

func TestDiffDeleteAnnotation(t *testing.T) {
	m, _ := diffWorkspace(t)
	m = openDiffFor(t, m)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})

	// D with nothing under the cursor refuses with a notice
	m.diff.cursor = 1
	m = press(t, m, tea.KeyPressMsg{Code: 'D', Text: "D"})
	if !strings.Contains(m.notice.text, "no annotation") {
		t.Errorf("D on a bare line: notice = %q", m.notice.text)
	}

	m = commentOnAddedLine(t, m, "typo'd commnet")
	if m.diff.openCount() != 1 {
		t.Fatalf("openCount = %d after comment, want 1", m.diff.openCount())
	}
	// D on the annotated line removes it from the store entirely
	m = press(t, m, tea.KeyPressMsg{Code: 'D', Text: "D"})
	if m.diff.openCount() != 0 {
		t.Errorf("openCount = %d after delete, want 0", m.diff.openCount())
	}
	anns, err := m.store.ListDiffAnnotations(context.Background(), "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	if len(anns) != 0 {
		t.Errorf("annotation survived delete: %+v", anns)
	}
}

func TestDiffReadModeShowsComments(t *testing.T) {
	m, _ := diffWorkspace(t)
	m = openDiffFor(t, m)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	m = commentOnAddedLine(t, m, "guard the empty case")

	// back in read mode the comment block renders under its line
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	out := m.diff.renderRead(m, 80, 40)
	if !strings.Contains(out, "guard the empty case") {
		t.Errorf("read mode does not show the comment:\n%s", out)
	}
}

func TestDiffAnnotateWrapsLongLines(t *testing.T) {
	m, root := diffWorkspace(t)
	long := "x\n" + strings.Repeat("wide ", 40) + "ENDMARK\n"
	wtFile := filepath.Join(root, (&domain.Feature{ID: "FD-001", Slug: "dark-mode"}).WorktreePath(), "README.md")
	if err := os.WriteFile(wtFile, []byte(long), 0o600); err != nil {
		t.Fatal(err)
	}
	m = openDiffFor(t, m)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})

	const w = 40
	out := m.diff.renderAnnotate(m, w, 200)
	if !strings.Contains(out, "ENDMARK") {
		t.Errorf("long line truncated instead of wrapped:\n%s", out)
	}
	for _, row := range strings.Split(out, "\n") {
		if got := ansi.StringWidth(row); got > w {
			t.Errorf("row wider than the pane (%d > %d): %q", got, w, row)
		}
	}
}

func TestDiffAnnotateWrapsLongComments(t *testing.T) {
	m, _ := diffWorkspace(t)
	m = openDiffFor(t, m)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	m = commentOnAddedLine(t, m, strings.Repeat("nit ", 30)+"ENDMARK")

	const w = 40
	out := m.diff.renderAnnotate(m, w, 200)
	if !strings.Contains(out, "ENDMARK") {
		t.Errorf("long comment truncated instead of wrapped:\n%s", out)
	}
	for _, row := range strings.Split(out, "\n") {
		if got := ansi.StringWidth(row); got > w {
			t.Errorf("row wider than the pane (%d > %d): %q", got, w, row)
		}
	}
}

func TestDiffOrphanReachableWithCursor(t *testing.T) {
	// A comment whose line changed degrades to the orphan footer; it must
	// stay reachable (n), resolvable (x), and deletable (D) — otherwise
	// it blocks the gate with no way to clear it.
	m, root := diffWorkspace(t)
	m = openDiffFor(t, m)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	m = commentOnAddedLine(t, m, "stale nit")

	wtFile := filepath.Join(root, (&domain.Feature{ID: "FD-001", Slug: "dark-mode"}).WorktreePath(), "README.md")
	if err := os.WriteFile(wtFile, []byte("x\ntotally different content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m = pump(t, m, m.reloadDiff())
	if len(m.diff.orphans) != 1 {
		t.Fatalf("annotation did not orphan: %v", m.diff.orphans)
	}

	// n jumps to the orphan's footer slot past the last diff line
	m.diff.cursor = 1
	m = press(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	if want := len(m.diff.lines) + 1; m.diff.cursor != want {
		t.Fatalf("n jumped to %d, want the orphan slot %d", m.diff.cursor, want)
	}
	// the footer renders the cursor on the orphan block
	out := m.diff.renderAnnotate(m, 80, 200)
	if !strings.Contains(out, "orphaned (line changed since comment):") {
		t.Errorf("orphan footer missing:\n%s", out)
	}

	m = press(t, m, tea.KeyPressMsg{Code: 'x', Text: "x"})
	if m.diff.openCount() != 0 {
		t.Errorf("openCount = %d after resolving the orphan, want 0", m.diff.openCount())
	}
	m = press(t, m, tea.KeyPressMsg{Code: 'D', Text: "D"})
	anns, err := m.store.ListDiffAnnotations(context.Background(), "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	if len(anns) != 0 {
		t.Errorf("orphan survived delete: %+v", anns)
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

func TestDiffApproveFromSurface(t *testing.T) {
	// A from the diff surface advances the gate (Review → Verify).
	m, _ := diffWorkspace(t)
	ctx := context.Background()
	m = openDiffFor(t, m)
	m = press(t, m, tea.KeyPressMsg{Code: 'A', Text: "A"})
	if m.diff != nil {
		t.Fatal("A did not close the diff surface")
	}
	f, _ := m.store.GetFeature(ctx, "FD-001")
	if f.Stage != domain.StageVerify {
		t.Errorf("A did not advance the gate: stage = %s, want verify", f.Stage)
	}

	// with an open diff comment the surface closes but the gate stays shut.
	m2, _ := diffWorkspace(t)
	ann := domain.DiffAnnotation{
		Feature: "FD-001", File: "README.md",
		Anchor: "second line", Excerpt: "+second line", Comment: "open",
	}
	if _, err := m2.store.AddDiffAnnotation(ctx, ann, fixedTime); err != nil {
		t.Fatal(err)
	}
	m2 = openDiffFor(t, m2)
	m2 = press(t, m2, tea.KeyPressMsg{Code: 'A', Text: "A"})
	if m2.diff != nil {
		t.Fatal("A should close the surface even when blocked")
	}
	f2, _ := m2.store.GetFeature(ctx, "FD-001")
	if f2.Stage != domain.StageReview {
		t.Errorf("open diff comment did not hold the gate: stage = %s, want review", f2.Stage)
	}
	if !strings.Contains(m2.notice.text, "diff comment") {
		t.Errorf("blocked notice = %q, want a diff blocking message", m2.notice.text)
	}
}
