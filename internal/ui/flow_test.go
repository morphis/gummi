package ui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui/theme"
	"github.com/morphis/gummi/internal/worktree"
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

	ws, err := state.Init(root, root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	wt, err := worktree.NewManager(context.Background(), root, root, store)
	if err != nil {
		t.Fatal(err)
	}
	m := NewShell(theme.GummiDark(), "v0-test")
	m.Attach(store, worktree.WrapSingle(wt), ws)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return model.(*Shell), root
}

// pump executes a command and feeds resulting messages back into the
// model until the command chain settles. It mirrors the real Bubble Tea
// runtime without a wall-clock guess: a finite command is run to
// completion (no deadline — it may legitimately shell out to git and take
// as long as it takes), while a subscription-tagged command — one that in
// the real runtime never returns (the engine event listener, a re-arming
// timer) — is skipped via subscription's registry before being invoked.
func pump(t *testing.T, m *Shell, cmd tea.Cmd) *Shell {
	t.Helper()
	for cmd != nil {
		// subscription-wrapped commands never finish on their own; skip
		// them so the run doesn't block on a channel or timer that the
		// real tea loop handles asynchronously.
		if isSubscription(cmd) {
			return m
		}
		msg := cmd()
		if msg == nil {
			return m
		}
		// a batch fans out into concurrent commands (as the real runtime
		// does); drain each so tests see all their effects.
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				m = pump(t, m, c)
			}
			return m
		}
		var model tea.Model
		model, cmd = m.Update(msg)
		m = model.(*Shell)
	}
	return m
}

// commitWork leaves a committed change on the item's feature branch —
// what the implement/fix stages produce in a real flow, and what the
// landing squash merge needs (an empty branch skips the merge).
func commitWork(t *testing.T, root, id string) {
	t.Helper()
	wt := filepath.Join(root, ".gummi", "worktrees", id)
	if err := os.WriteFile(filepath.Join(wt, "work.txt"), []byte("work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "work.txt"}, {"commit", "-q", "-m", id + ": work"}} {
		cmd := exec.CommandContext(context.Background(), "git", append([]string{"-C", wt}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
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

	// walk to verify: plan→implement→review→verify
	for _, want := range []domain.Stage{domain.StageImplement, domain.StageReview, domain.StageVerify} {
		m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
		if m.rows[0].F.Stage != want {
			t.Fatalf("stage = %s, want %s", m.rows[0].F.Stage, want)
		}
	}
	commitWork(t, root, "FD-001")

	// g at verify is the "done" decision: it routes through the squash
	// merge — commit-message dialog (drafts a message the user approves), then
	// land on main and move to done
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	if _, ok := m.Overlay.Top().(*commitMsgDialog); !ok {
		t.Fatalf("g at verify did not open the commit-message dialog (notice %q)", m.notice.text)
	}
	typeMessage(t, m, "FD-001: land the feature")
	m = press(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if m.rows[0].F.Stage != domain.StageDone {
		t.Fatalf("stage after merge = %s, want done (notice %q)", m.rows[0].F.Stage, m.notice.text)
	}
	f := m.rows[0].F
	if landed, err := m.wt.Landed(context.Background(), &f); !landed || err != nil {
		t.Errorf("Landed after done = %v, %v; want true", landed, err)
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
	if _, err := os.Stat(filepath.Join(root, ".gummi", "specs", "FD-001-dark-mode.md")); !os.IsNotExist(err) {
		t.Fatal("workspace spec survived delete")
	}
}

func TestBugLifecycleFlow(t *testing.T) {
	m, root := newWorkspace(t)
	m = pump(t, m, m.Init())

	// B → bug form, type a title, enter → bug created in todo
	m = press(t, m, tea.KeyPressMsg{Code: 'B', Text: "B"})
	if m.Overlay.Top() == nil || m.Overlay.Top().ID() != "new-bug" {
		t.Fatal("B did not open the new-bug form")
	}
	m = typeString(t, m, "Login loops")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(m.rows) != 1 || m.rows[0].F.Kind != domain.KindBug {
		t.Fatalf("bug not created: %+v", m.rows)
	}
	if m.rows[0].F.ID != "BG-001" || m.rows[0].F.Stage != domain.StageTodo {
		t.Fatalf("bad bug: %+v", m.rows[0].F)
	}

	// advance: todo → triage → diagnose (interactive; no worktree yet)
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	if m.rows[0].F.Stage != domain.StageDiagnose {
		t.Fatalf("stage = %s, want diagnose", m.rows[0].F.Stage)
	}
	if m.rows[0].HasWorktree {
		t.Fatal("worktree exists before diagnosis approval")
	}

	// advance out of diagnose → worktree + branch created, report promoted
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	if m.rows[0].F.Stage != domain.StageFix {
		t.Fatalf("stage = %s, want fix", m.rows[0].F.Stage)
	}
	if !m.rows[0].HasWorktree {
		t.Fatal("worktree missing after diagnosis approval")
	}
	if _, err := os.Stat(filepath.Join(root, ".gummi", "bugs", "BG-001-login-loops.md")); err != nil {
		t.Fatalf("bug report not at its workspace home: %v", err)
	}
	// and it stays out of the worktree
	if _, err := os.Stat(filepath.Join(root, ".gummi", "worktrees", "BG-001", ".gummi")); !os.IsNotExist(err) {
		t.Fatalf(".gummi content present in the bug worktree: %v", err)
	}

	// walk to verify: fix → review → verify
	for _, want := range []domain.Stage{domain.StageReview, domain.StageVerify} {
		m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
		if m.rows[0].F.Stage != want {
			t.Fatalf("stage = %s, want %s", m.rows[0].F.Stage, want)
		}
	}
	commitWork(t, root, "BG-001")

	// g at verify routes through the squash merge before done
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	if _, ok := m.Overlay.Top().(*commitMsgDialog); !ok {
		t.Fatalf("g at verify did not open the commit-message dialog (notice %q)", m.notice.text)
	}
	typeMessage(t, m, "BG-001: fix the login loop")
	m = press(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if m.rows[0].F.Stage != domain.StageDone {
		t.Fatalf("stage after merge = %s, want done (notice %q)", m.rows[0].F.Stage, m.notice.text)
	}
	// the squash commit carries only product work — no .gummi content
	if _, err := os.Stat(filepath.Join(root, "work.txt")); err != nil {
		t.Errorf("work missing from main after merge: %v", err)
	}
	out, err := exec.CommandContext(context.Background(), "git", "-C", root, "ls-files", "--", ".gummi").Output()
	if err != nil || len(out) != 0 {
		t.Errorf(".gummi content tracked on main after merge: %v %q", err, out)
	}
}

func TestBugBouncesReviewToFix(t *testing.T) {
	m, _ := newWorkspace(t)
	m = pump(t, m, m.Init())
	m = press(t, m, tea.KeyPressMsg{Code: 'B', Text: "B"})
	m = typeString(t, m, "Crash on nil")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	for range 4 { // todo→triage→diagnose→fix→review
		m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	}
	if m.rows[0].F.Stage != domain.StageReview {
		t.Fatalf("stage = %s, want review", m.rows[0].F.Stage)
	}
	// forward g from review goes to verify (not the rerun edge to fix)
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	if m.rows[0].F.Stage != domain.StageVerify {
		t.Fatalf("forward from review = %s, want verify", m.rows[0].F.Stage)
	}
	// b from verify bounces back to fix (the bug work stage), not implement
	m = press(t, m, tea.KeyPressMsg{Code: 'b', Text: "b"})
	if m.rows[0].F.Stage != domain.StageFix {
		t.Fatalf("bounce: stage = %s, want fix", m.rows[0].F.Stage)
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
	// toggle both skip flags on the options row: tab past the new
	// envelope field, then tab to options, then b and p
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab}) // envelope field
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab}) // options row
	m = press(t, m, tea.KeyPressMsg{Code: 'b', Text: "b"})
	m = press(t, m, tea.KeyPressMsg{Code: 'p', Text: "p"})
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
