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

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/ui/theme"
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

// depSpecShell builds a shell whose store has a feature at FD-001 with the
// given dependencies, and returns a specView over that feature.
func depSpecShell(t *testing.T, deps []domain.Feature) (*Shell, *specView) {
	t.Helper()
	m, _ := newWorkspace(t)
	m.now = func() time.Time { return fixedTime }
	ctx := context.Background()
	f := &domain.Feature{
		ID: "FD-001", Num: 1, Title: "dependent", Slug: "dependent",
		Stage: domain.StagePlan, CreatedAt: fixedTime, UpdatedAt: fixedTime,
	}
	if err := m.store.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	for _, d := range deps {
		if err := m.store.CreateFeature(ctx, &d); err != nil {
			t.Fatal(err)
		}
		if err := m.store.AddDependency(ctx, f.ID, d.ID); err != nil {
			t.Fatal(err)
		}
	}
	content := "## Problem\nNeeds a dep.\n>\n> Depends on: static prose\n\n## Chosen approach\nBuild it.\n"
	sv := &specView{f: *f, content: content, doc: spec.Parse(content), cursor: 1}
	m.spec = sv
	return m, sv
}

// TestSpecDependencyStatusGolden: each direct dependency renders with its
// live status (ID, stage, done/pending), the static Depends-on prose is
// gone from read mode, and an all-done line appears when all deps are done.
func TestSpecDependencyStatusGolden(t *testing.T) {
	pending := domain.Feature{
		ID: "FD-002", Num: 2, Title: "pending", Slug: "pending",
		Stage: domain.StageImplement, CreatedAt: fixedTime, UpdatedAt: fixedTime,
	}
	done := domain.Feature{
		ID: "FD-003", Num: 3, Title: "done", Slug: "done",
		Stage: domain.StageDone, CreatedAt: fixedTime, UpdatedAt: fixedTime,
	}
	m, sv := depSpecShell(t, []domain.Feature{pending, done})
	out := stripANSI(sv.renderRead(m, 90, 30))
	if strings.Contains(out, "Depends on:") {
		t.Fatal("static Depends on prose still rendered:\n" + out)
	}
	if strings.Contains(out, "all dependencies done") {
		t.Fatal("all-done line shown with a pending dep:\n" + out)
	}
	golden.RequireEqual(t, []byte(out))
}

// TestSpecDependencyStatusAllDone: every dependency done renders the
// all-done line.
func TestSpecDependencyStatusAllDone(t *testing.T) {
	done := domain.Feature{
		ID: "FD-002", Num: 2, Title: "done", Slug: "done",
		Stage: domain.StageDone, CreatedAt: fixedTime, UpdatedAt: fixedTime,
	}
	m, sv := depSpecShell(t, []domain.Feature{done})
	out := stripANSI(sv.renderRead(m, 90, 30))
	if !strings.Contains(out, "all dependencies done") {
		t.Fatalf("missing all-done line:\n%s", out)
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

func TestSpecPromotesToWorkspaceAtApproval(t *testing.T) {
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
	// resolve it (an open user annotation blocks approval); the comment
	// still travels with the migrated spec
	m = press(t, m, tea.KeyPressMsg{Code: 'c', Text: "c"})
	m = typeString(t, m, "resolved — keeping it")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})

	// approve the spec (leave Spec) → worktree + promoted spec
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = openSpecFor(t, m)
	if m.spec.path == draftPath {
		t.Fatalf("spec still reads the draft after approval: %s", m.spec.path)
	}
	if !strings.Contains(m.spec.path, filepath.Join(".gummi", "specs", "FD-001-dark-mode.md")) {
		t.Fatalf("spec path not at its workspace home: %s", m.spec.path)
	}
	if strings.Contains(m.spec.path, filepath.Join(".gummi", "worktrees")) {
		t.Fatalf("spec path leaked into the worktree: %s", m.spec.path)
	}
	// annotations travel with the promoted spec
	if !strings.Contains(m.spec.content, "keep me") {
		t.Fatal("draft annotations lost in promotion")
	}
	// the draft is retired
	if _, err := os.Stat(draftPath); !os.IsNotExist(err) {
		t.Fatal("draft still present after promotion")
	}
	// and nothing about the spec entered the feature branch: the worktree
	// carries no .gummi content and no artifact commit
	wtDir := filepath.Join(m.wt.Root(), ".gummi", "worktrees", "FD-001")
	if _, err := os.Stat(filepath.Join(wtDir, ".gummi")); !os.IsNotExist(err) {
		t.Fatalf(".gummi content present in the worktree: %v", err)
	}
	out, err := exec.CommandContext(context.Background(), "git", "-C", wtDir, "log", "--oneline").Output()
	if err != nil || strings.Contains(string(out), "docs(spec)") {
		t.Fatalf("artifact commit found on the branch: %v %q", err, out)
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

// TestSpecViewSeparatesBlockingThreads: a spec with both an open @user
// comment (blocks approval) and an agent thread renders them under
// distinct headers, so an agent question isn't misread as a blocker.
func TestSpecViewSeparatesBlockingThreads(t *testing.T) {
	content := "## Problem\n\nThe toggle persists via localStorage.\n" +
		"%% @user(2026-07-14): should this sync to the account?\n\n" +
		"It defaults to on for new installs.\n" +
		"%% @architect: is that the right default?\n"
	id, _ := domain.NewFeatureID(1)
	sv := &specView{
		f:       domain.Feature{ID: id, Num: 1, Title: "x", Slug: "x", Stage: domain.StageSpec},
		path:    "p.md",
		content: content,
		doc:     spec.Parse(content),
		cursor:  1,
	}
	m := NewShell(theme.GummiDark(), "t")
	model, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = model.(*Shell)
	m.spec = sv
	out := stripANSI(sv.renderRead(m, 90, 24))
	if !strings.Contains(out, "blocks approval (you)") {
		t.Errorf("missing blocking-thread header:\n%s", out)
	}
	if !strings.Contains(out, "should this sync to the account?") {
		t.Errorf("user thread not listed as blocking:\n%s", out)
	}
	// the architect-only thread is informational, not a blocker
	bi := strings.Index(out, "blocks approval")
	ii := strings.Index(out, "informational (agent)")
	if ii < 0 || bi < 0 || ii < bi {
		t.Errorf("informational group missing or misordered (blocks=%d info=%d):\n%s", bi, ii, out)
	}
	if !strings.Contains(out, "is that the right default?") {
		t.Errorf("agent thread not listed as informational:\n%s", out)
	}
}

func TestSpecResolveComment(t *testing.T) {
	m := specWorkspace(t)
	m = openSpecFor(t, m)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab}) // annotate
	// add an open @user marker threaded under line 1
	m = press(t, m, tea.KeyPressMsg{Code: 'c', Text: "c"})
	m = typeString(t, m, "open question")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(m.spec.doc.UserOpenThreads()) != 1 {
		t.Fatalf("open user threads = %d, want 1", len(m.spec.doc.UserOpenThreads()))
	}
	// move the cursor onto the marker (threaded under line 1) and resolve
	m = press(t, m, tea.KeyPressMsg{Code: 'j', Text: "j"})
	m = press(t, m, tea.KeyPressMsg{Code: 'x', Text: "x"})
	want := "%% @user(2026-07-03): resolved"
	if !strings.Contains(m.spec.content, want) {
		t.Fatalf("resolution not written:\n%s", m.spec.content)
	}
	raw, err := os.ReadFile(m.spec.path)
	if err != nil || !strings.Contains(string(raw), want) {
		t.Fatalf("resolution not persisted: %v", err)
	}
	if len(m.spec.doc.UserOpenThreads()) != 0 {
		t.Errorf("open user threads after resolve = %d, want 0", len(m.spec.doc.UserOpenThreads()))
	}
	// x on the now-resolved thread → already resolved
	m = press(t, m, tea.KeyPressMsg{Code: 'x', Text: "x"})
	if !strings.Contains(m.notice.text, "already resolved") {
		t.Errorf("x on a resolved thread: notice = %q", m.notice.text)
	}
	// x on a content line → no marker
	m.spec.cursor = 5
	m = press(t, m, tea.KeyPressMsg{Code: 'x', Text: "x"})
	if !strings.Contains(m.notice.text, "no marker") {
		t.Errorf("x on a content line: notice = %q", m.notice.text)
	}
}

func TestSpecResolveFirstOfTwoMarkers(t *testing.T) {
	// x targets the marker at the cursor, not the whole thread: resolving
	// the first of two markers on one anchor must leave the second one
	// open, so the gate stays blocked.
	m := specWorkspace(t)
	m = openSpecFor(t, m)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab}) // annotate
	content := "## Problem\nTwo questions.\n" +
		"%% @user(2026-08-16): per-device or synced?\n" +
		"%% @user(2026-08-16): what about SSR?\n"
	if err := os.WriteFile(m.spec.path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	m.spec.content = content
	m.spec.doc = spec.Parse(content)
	if len(m.spec.doc.UserOpenThreads()) != 1 {
		t.Fatalf("setup: user open threads = %d, want 1", len(m.spec.doc.UserOpenThreads()))
	}
	// put the cursor on the FIRST marker (line 3) and resolve it
	m.spec.cursor = 3
	m = press(t, m, tea.KeyPressMsg{Code: 'x', Text: "x"})
	if !strings.Contains(m.spec.content, "%% @user(2026-07-03): resolved") {
		t.Fatalf("resolution not written:\n%s", m.spec.content)
	}
	// the resolution lands immediately under the first marker; the second
	// marker stays open, so the gate must remain blocked
	if len(m.spec.doc.UserOpenThreads()) != 1 {
		t.Fatalf("second marker was closed: user open threads = %d, want 1\n%s", len(m.spec.doc.UserOpenThreads()), m.spec.content)
	}
	open := m.spec.doc.UserOpenThreads()
	u := spec.UnresolvedUserMarker(open[0])
	if u == nil || u.Text != "what about SSR?" {
		t.Fatalf("open user marker = %+v, want the SSR question", u)
	}
}

func TestSpecApproveFromSurface(t *testing.T) {
	// A from the spec surface advances the gate exactly as board g does.
	m := specWorkspace(t)
	ctx := context.Background()
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	f, _ := m.store.GetFeature(ctx, "FD-001")
	if f.Stage != domain.StageSpec {
		t.Fatalf("setup: feature at %s, want spec", f.Stage)
	}
	m = openSpecFor(t, m)
	m = press(t, m, tea.KeyPressMsg{Code: 'A', Text: "A"})
	if m.spec != nil {
		t.Fatal("A did not close the spec surface")
	}
	f, _ = m.store.GetFeature(ctx, "FD-001")
	if f.Stage != domain.StagePlan {
		t.Errorf("A did not advance the gate: stage = %s, want plan", f.Stage)
	}

	// with an open @user marker the surface still closes but the gate
	// stays shut and the blocking notice surfaces.
	m2 := specWorkspace(t)
	m2 = press(t, m2, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m2 = press(t, m2, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m2 = openSpecFor(t, m2)
	m2 = press(t, m2, tea.KeyPressMsg{Code: tea.KeyTab})
	m2 = press(t, m2, tea.KeyPressMsg{Code: 'c', Text: "c"})
	m2 = typeString(t, m2, "still open")
	m2 = press(t, m2, tea.KeyPressMsg{Code: tea.KeyEnter})
	m2 = press(t, m2, tea.KeyPressMsg{Code: 'A', Text: "A"})
	if m2.spec != nil {
		t.Fatal("A should close the surface even when blocked")
	}
	f2, _ := m2.store.GetFeature(ctx, "FD-001")
	if f2.Stage != domain.StageSpec {
		t.Errorf("open marker did not hold the gate: stage = %s, want spec", f2.Stage)
	}
	if !strings.Contains(m2.notice.text, "block approval") {
		t.Errorf("blocked notice = %q, want a blocking message", m2.notice.text)
	}
}

func TestSpecViewAnnotateResolvedGolden(t *testing.T) {
	m := specWorkspace(t)
	m = openSpecFor(t, m)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	// one resolved thread, nothing open → no "N open" in the header and
	// the resolution renders success-tinted
	content := "## Problem\nResolved design question.\n" +
		"%% @user(2026-07-03): was this the right call?\n" +
		"%% @user(2026-07-03): resolved\n## Chosen approach\n"
	m.spec.content = content
	m.spec.doc = spec.Parse(content)
	golden.RequireEqual(t, []byte(m.View().Content))
}

func TestSpecBindingsIncludeXAndA(t *testing.T) {
	has := func(bindings []binding, key string) bool {
		for _, b := range bindings {
			if b.key == key {
				return true
			}
		}
		return false
	}
	m := specWorkspace(t)
	m = openSpecFor(t, m)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	if !has(m.spec.bindings(), "x") || !has(m.spec.bindings(), "A") {
		t.Errorf("annotate bindings missing x/A: %+v", m.spec.bindings())
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab}) // read mode
	if !has(m.spec.bindings(), "A") {
		t.Errorf("read bindings missing A: %+v", m.spec.bindings())
	}
}
