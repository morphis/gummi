package ui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/golden"

	"github.com/morphia/gummi/internal/agent"
	"github.com/morphia/gummi/internal/domain"
	"github.com/morphia/gummi/internal/engine"
	"github.com/morphia/gummi/internal/state"
	"github.com/morphia/gummi/internal/ui/theme"
	"github.com/morphia/gummi/internal/worktree"
)

// chatWorkspace builds a shell wired to a real workspace and a
// Fake-backed engine, with one brainstorm feature created.
func chatWorkspace(t *testing.T, ag agent.Agent) (*Shell, *engine.Engine) {
	t.Helper()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	git := func(a ...string) {
		t.Helper()
		if out, err := exec.CommandContext(context.Background(), "git",
			append([]string{"-C", root}, a...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", a, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.name", "t")
	git("config", "user.email", "t@e.invalid")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-q", "-m", "init")

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
	eng := engine.New(engine.Config{Agent: ag, Store: store, Worktrees: wt, Workspace: ws, Model: "fake-model"})
	t.Cleanup(func() { eng.Close() })

	m := NewShell(theme.GummiDark(), "v0-test")
	m.now = func() time.Time { return fixedTime }
	m.Attach(store, wt, ws)
	m.AttachEngine(eng)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = model.(*Shell)

	// create a brainstorm feature
	m = press(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	m = typeString(t, m, "Dark mode")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	// advance todo → brainstorm
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	return m, eng
}

// settleChat waits for the engine's async stream to finish a turn.
func settleChat(t *testing.T, eng *engine.Engine) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		if a := eng.Active(); a != nil {
			snap := a.Snapshot()
			if !snap.Busy && len(snap.Transcript) > 0 {
				return
			}
		}
		select {
		case <-deadline:
			t.Fatal("chat did not settle")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestChatAttachAndSend(t *testing.T) {
	m, eng := chatWorkspace(t, agent.NewFake("Two options: localStorage or synced account."))

	// enter attaches the chat pane
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.chat == nil {
		t.Fatal("enter did not attach the chat pane")
	}
	if m.chat.feature != "FD-001" {
		t.Fatalf("attached to wrong feature: %s", m.chat.feature)
	}

	// type and send
	m = typeString(t, m, "how should it persist?")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	settleChat(t, eng)

	snap := m.chat.session.Snapshot()
	if len(snap.Transcript) != 2 {
		t.Fatalf("transcript = %+v", snap.Transcript)
	}
	if snap.Transcript[0].Author != engine.AuthorUser || snap.Transcript[0].Content != "how should it persist?" {
		t.Errorf("user turn wrong: %+v", snap.Transcript[0])
	}
	if snap.Transcript[1].Content != "Two options: localStorage or synced account." {
		t.Errorf("assistant turn wrong: %+v", snap.Transcript[1])
	}

	// esc detaches; the session stays alive
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.chat != nil {
		t.Fatal("esc did not detach")
	}
	if eng.Active() == nil {
		t.Fatal("detach killed the engine session")
	}

	// re-attach reuses the same session (transcript preserved)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.chat == nil || len(m.chat.session.Snapshot().Transcript) != 2 {
		t.Fatal("re-attach lost the transcript")
	}
}

func TestChatReuseRespectsStage(t *testing.T) {
	m, eng := chatWorkspace(t, agent.NewFake("hi"))
	// attach at brainstorm, note the session, detach
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	brainstormSess := m.chat.session
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})

	// advance brainstorm → spec while detached
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	if m.rows[0].F.Stage != domain.StageSpec {
		t.Fatalf("stage = %s, want spec", m.rows[0].F.Stage)
	}

	// re-attach: must NOT reuse the brainstorm session for a spec stage
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.chat == nil {
		t.Fatal("re-attach failed")
	}
	if m.chat.session == brainstormSess {
		t.Error("reused the stale brainstorm session for the spec stage")
	}
	if a := eng.Active(); a == nil || a.Feature.Stage != domain.StageSpec {
		t.Errorf("active session stage = %v, want spec", a)
	}
}

func TestChatViewGolden(t *testing.T) {
	m, eng := chatWorkspace(t, agent.NewFake("Persist per-device via localStorage; account sync is a follow-up."))
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = typeString(t, m, "per-device or synced?")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	settleChat(t, eng)
	golden.RequireEqual(t, []byte(m.View().Content))
}

func TestChatRejectsNonInteractiveStage(t *testing.T) {
	m, _ := chatWorkspace(t, agent.NewFake("x"))
	// advance brainstorm → spec → plan (needs worktree at spec approval)
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"}) // brainstorm→spec
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"}) // spec→plan (worktree created)
	if m.rows[0].F.Stage != domain.StagePlan {
		t.Fatalf("stage = %s, want plan", m.rows[0].F.Stage)
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.chat != nil {
		t.Fatal("chat attached for a non-interactive stage")
	}
	if !m.notice.isErr {
		t.Error("no notice for non-interactive attach")
	}
}

func TestChatNoEngine(t *testing.T) {
	// a workspace-attached shell without an engine: enter yields a notice
	m, _ := chatWorkspace(t, agent.NewFake("x"))
	m.engine = nil
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.chat != nil {
		t.Fatal("chat attached without an engine")
	}
	if !m.notice.isErr {
		t.Error("no notice when attaching without an engine")
	}
}
