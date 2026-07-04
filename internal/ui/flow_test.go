package ui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphia/gummi/internal/domain"
	"github.com/morphia/gummi/internal/state"
	"github.com/morphia/gummi/internal/ui/theme"
	"github.com/morphia/gummi/internal/worktree"
)

// newWorkspace creates a real repo + initialized gummi workspace.
func newWorkspace(t *testing.T) (*Shell, string) {
	t.Helper()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(context.Background(), "git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.name", "test")
	git("config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-q", "-m", "initial")

	ws, err := state.Init(root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	wt, err := worktree.NewManager(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	m := NewShell(theme.GummiDark(), "v0-test")
	m.Attach(store, wt, ws)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return model.(*Shell), root
}

// pump executes a command and feeds resulting messages back into the
// model until the command chain settles. Commands that don't return
// promptly (timers such as the textarea cursor blink, or the engine
// event listener that blocks on its channel) run asynchronously in the
// real Bubble Tea runtime; here we treat a slow command as async and
// stop following it rather than blocking on the timer.
func pump(t *testing.T, m *Shell, cmd tea.Cmd) *Shell {
	t.Helper()
	for cmd != nil {
		done := make(chan tea.Msg, 1)
		go func(c tea.Cmd) { done <- c() }(cmd)
		var msg tea.Msg
		select {
		case msg = <-done:
		case <-time.After(100 * time.Millisecond):
			return m // slow/timer command: async in the real runtime
		}
		if msg == nil {
			return m
		}
		var model tea.Model
		model, cmd = m.Update(msg)
		m = model.(*Shell)
	}
	return m
}

func press(t *testing.T, m *Shell, key tea.KeyPressMsg) *Shell {
	t.Helper()
	model, cmd := m.Update(key)
	return pump(t, model.(*Shell), cmd)
}

func typeString(t *testing.T, m *Shell, str string) *Shell {
	t.Helper()
	for _, r := range str {
		m = press(t, m, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return m
}

func TestFullCRUDAndLifecycleFlow(t *testing.T) {
	m, root := newWorkspace(t)
	m = pump(t, m, m.Init())
	if len(m.rows) != 0 {
		t.Fatalf("fresh workspace has %d rows", len(m.rows))
	}

	// n → form, type a title, enter → feature created in todo
	m = press(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	if m.Overlay.Top() == nil {
		t.Fatal("n did not open the form")
	}
	m = typeString(t, m, "Dark mode")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.Overlay.HasDialogs() {
		t.Fatal("form did not close on submit")
	}
	if len(m.rows) != 1 || m.rows[0].F.Stage != domain.StageTodo {
		t.Fatalf("rows after create: %+v", m.rows)
	}
	if m.rows[0].F.ID != "FD-001" || m.rows[0].F.Slug != "dark-mode" {
		t.Fatalf("bad feature: %+v", m.rows[0].F)
	}

	// advance: todo → brainstorm → spec (no worktree yet)
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	if m.rows[0].F.Stage != domain.StageSpec {
		t.Fatalf("stage = %s, want spec", m.rows[0].F.Stage)
	}
	if m.rows[0].HasWorktree {
		t.Fatal("worktree exists before spec approval")
	}

	// advance out of spec → worktree + branch created (DESIGN §10.11)
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	if m.rows[0].F.Stage != domain.StagePlan {
		t.Fatalf("stage = %s, want plan", m.rows[0].F.Stage)
	}
	if !m.rows[0].HasWorktree {
		t.Fatal("worktree missing after spec approval")
	}
	if _, err := os.Stat(filepath.Join(root, ".gummi", "worktrees", "FD-001", "README.md")); err != nil {
		t.Fatalf("worktree not checked out: %v", err)
	}

	// walk to done: plan→implement→review→verify→done
	for _, want := range []domain.Stage{domain.StageImplement, domain.StageReview, domain.StageVerify, domain.StageDone} {
		m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
		if m.rows[0].F.Stage != want {
			t.Fatalf("stage = %s, want %s", m.rows[0].F.Stage, want)
		}
	}
	// g on done is a no-op with a notice
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	if m.rows[0].F.Stage != domain.StageDone {
		t.Fatal("done advanced somewhere")
	}

	// history is the full audit trail
	if len(m.rows[0].History) != 7 {
		t.Fatalf("history has %d records, want 7", len(m.rows[0].History))
	}

	// x → confirm → y deletes record, worktree, branch
	m = press(t, m, tea.KeyPressMsg{Code: 'x', Text: "x"})
	if m.Overlay.Top() == nil {
		t.Fatal("x did not open confirm")
	}
	m = press(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	if len(m.rows) != 0 {
		t.Fatalf("rows after delete: %+v", m.rows)
	}
	if _, err := os.Stat(filepath.Join(root, ".gummi", "worktrees", "FD-001")); !os.IsNotExist(err) {
		t.Fatal("worktree survived delete")
	}
}

func TestBounceFromReview(t *testing.T) {
	m, _ := newWorkspace(t)
	m = pump(t, m, m.Init())
	m = press(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	m = typeString(t, m, "Bouncy")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	for range 5 { // todo→brainstorm→spec→plan→implement→review
		m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	}
	if m.rows[0].F.Stage != domain.StageReview {
		t.Fatalf("stage = %s, want review", m.rows[0].F.Stage)
	}
	m = press(t, m, tea.KeyPressMsg{Code: 'b', Text: "b"})
	if m.rows[0].F.Stage != domain.StageImplement {
		t.Fatalf("bounce: stage = %s, want implement", m.rows[0].F.Stage)
	}
	// b from todo is illegal → error notice, stage unchanged
	m2, _ := newWorkspace(t)
	m2 = pump(t, m2, m2.Init())
	m2 = press(t, m2, tea.KeyPressMsg{Code: 'n', Text: "n"})
	m2 = typeString(t, m2, "Stuck")
	m2 = press(t, m2, tea.KeyPressMsg{Code: tea.KeyEnter})
	m2 = press(t, m2, tea.KeyPressMsg{Code: 'b', Text: "b"})
	if m2.rows[0].F.Stage != domain.StageTodo {
		t.Fatal("illegal bounce moved the feature")
	}
	if !m2.notice.isErr {
		t.Error("illegal bounce produced no error notice")
	}
}

func TestBounceFromPlanRefused(t *testing.T) {
	// plan → implement is a legal *forward* edge; b must refuse it —
	// and critically, a skip-plan feature in spec must not reach
	// implement via b without its worktree (DESIGN §10.11).
	m, _ := newWorkspace(t)
	m = pump(t, m, m.Init())
	m = press(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	m = typeString(t, m, "No shortcut")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	for range 3 { // → brainstorm → spec → plan
		m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	}
	m = press(t, m, tea.KeyPressMsg{Code: 'b', Text: "b"})
	if m.rows[0].F.Stage != domain.StagePlan {
		t.Fatalf("b from plan moved feature to %s", m.rows[0].F.Stage)
	}
	if !m.notice.isErr {
		t.Error("b from plan produced no error notice")
	}
}

func TestSkipFlagsChangeRoute(t *testing.T) {
	m, _ := newWorkspace(t)
	m = pump(t, m, m.Init())
	m = press(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	m = typeString(t, m, "Tiny fix")
	// toggle both skip flags: tab to skip-brainstorm, space, tab, space
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})              // one-liner
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})              // profile
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})              // skip brainstorm
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}) // toggle
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})              // skip plan
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}) // toggle
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.rows[0].F.Skip.Brainstorm || !m.rows[0].F.Skip.Plan {
		t.Fatalf("skip flags not set: %+v", m.rows[0].F.Skip)
	}
	// todo → spec directly, then spec → implement directly
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	if m.rows[0].F.Stage != domain.StageSpec {
		t.Fatalf("stage = %s, want spec (brainstorm skipped)", m.rows[0].F.Stage)
	}
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	if m.rows[0].F.Stage != domain.StageImplement {
		t.Fatalf("stage = %s, want implement (plan skipped)", m.rows[0].F.Stage)
	}
	if !m.rows[0].HasWorktree {
		t.Fatal("worktree missing after leaving spec via skip edge")
	}
}

func TestFormValidationBlocksEmptyTitle(t *testing.T) {
	m, _ := newWorkspace(t)
	m = pump(t, m, m.Init())
	m = press(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.Overlay.HasDialogs() {
		t.Fatal("form closed despite empty title")
	}
	if len(m.rows) != 0 {
		t.Fatal("feature created from empty title")
	}
	// esc cancels
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.Overlay.HasDialogs() {
		t.Fatal("esc did not close the form")
	}
}
