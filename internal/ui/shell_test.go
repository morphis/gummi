package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/ui/theme"
)

// testWaitTimeout bounds the poll loops that wait for engine state to
// reach a condition. It only ever runs out on a broken test, so it is
// sized for the slowest CI shape (race detector plus coverage on a loaded
// shared runner), not for the happy path — those loops return as soon as
// the condition holds.
const testWaitTimeout = 30 * time.Second

func shellAt(w, h int) *Shell {
	m := NewShell(theme.GummiDark(), "v0.1.0-test")
	model, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return model.(*Shell)
}

func TestShellView80(t *testing.T) {
	golden.RequireEqual(t, []byte(shellAt(80, 24).View().Content))
}

func TestShellView120(t *testing.T) {
	golden.RequireEqual(t, []byte(shellAt(120, 34).View().Content))
}

func TestShellViewNarrow(t *testing.T) {
	// a narrow terminal still renders without panicking
	out := shellAt(50, 12).View().Content
	if out == "" {
		t.Fatal("narrow view rendered empty")
	}
	golden.RequireEqual(t, []byte(out))
}

// TestViewPaintsEveryCell holds the frame to its own background. The
// shell asks for BgBase once, as tea.View's BackgroundColor — an OSC 11
// that tmux swallows — so any cell handed over without an explicit fill
// is painted in whatever background the terminal prefers, as is every
// span the renderer clears with EL/ECH after a style reset. That was the
// black bar trailing each transcript row: the bar started where the
// line's last glyph ended and ran to the right edge.
//
// Two properties keep it gone: every line is padded to the full width,
// and no line resets its style before its final cell (a mid-line reset
// hands the erase behind it back to the terminal).
func TestViewPaintsEveryCell(t *testing.T) {
	const w = 80
	for i, line := range strings.Split(populatedShell(w, 24).View().Content, "\n") {
		if got := ansi.StringWidth(line); got != w {
			t.Errorf("line %d is %d cells wide, want %d: %q", i, got, w, line)
		}
		if idx := strings.Index(line, ansi.ResetStyle); idx >= 0 && idx != len(line)-len(ansi.ResetStyle) {
			t.Errorf("line %d drops its background mid-line: %q", i, line)
		}
	}
}

func TestShellQuitKeys(t *testing.T) {
	m := shellAt(80, 24)
	for _, key := range []string{"q", "ctrl+c"} {
		var msg tea.KeyPressMsg
		if key == "q" {
			msg = tea.KeyPressMsg{Code: 'q', Text: "q"}
		} else {
			msg = tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
		}
		_, cmd := m.Update(msg)
		if cmd == nil {
			t.Errorf("%s did not quit", key)
		}
	}
}

func TestQuitWithoutLiveSessionQuits(t *testing.T) {
	// an idle shell has no live sessions, so q stays a single keypress.
	m := populatedShell(80, 24)
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if cmd == nil {
		t.Fatal("idle q returned no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("idle q returned %T, want QuitMsg", cmd())
	}
	if m.Overlay.Top() != nil {
		t.Fatal("idle q pushed a dialog when none should appear")
	}
}

func TestQuitWithLiveSessionPushesDialog(t *testing.T) {
	// hold a session in StateRunning (the plan writer) so the board has
	// live autonomous work; q must then offer a confirm dialog instead of
	// quitting.
	release := make(chan struct{})
	defer close(release)
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		// block only the plan writer (implementer) turn so its session
		// stays live in StateRunning; the auto-discovery scribe pass and
		// any other session must complete for the setup to finish.
		if opts.Role == agent.RoleImplementer {
			<-release
		}
		return []agent.Event{{Kind: agent.EventIdle}}
	}}
	m, eng := chatWorkspace(t, ag)
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"}) // brainstorm → spec
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"}) // spec → plan
	m = openAndAttach(t, m)                                // run plan (autonomous)
	waitLive(t, eng, "FD-001")

	m = toKeys(t, m)
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if cmd != nil {
		t.Fatalf("q with a live session returned a command, want the dialog")
	}
	d, ok := m.Overlay.Top().(*confirmDialog)
	if !ok || d.id != "confirm-quit" {
		t.Fatalf("top overlay = %v, want the confirm-quit dialog", m.Overlay.Top())
	}
}

func TestQuitConfirmYesQuits(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		if opts.Role == agent.RoleImplementer {
			<-release
		}
		return []agent.Event{{Kind: agent.EventIdle}}
	}}
	m, eng := chatWorkspace(t, ag)
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = openAndAttach(t, m)
	waitLive(t, eng, "FD-001")

	// q pushes the dialog; confirming with y returns the quit command.
	m = toKeys(t, m)
	m = press(t, m, tea.KeyPressMsg{Code: 'q', Text: "q"})
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if cmd == nil {
		t.Fatal("confirming the quit dialog returned no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("confirmed quit returned %T, want QuitMsg", cmd())
	}
}

func TestQuitLiveDialogCancelStays(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		if opts.Role == agent.RoleImplementer {
			<-release
		}
		return []agent.Event{{Kind: agent.EventIdle}}
	}}
	m, eng := chatWorkspace(t, ag)
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = openAndAttach(t, m)
	waitLive(t, eng, "FD-001")

	m = toKeys(t, m)

	m = press(t, m, tea.KeyPressMsg{Code: 'q', Text: "q"})
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if cmd != nil {
		t.Fatalf("declining the quit dialog returned a command")
	}
	if m.Overlay.Top() != nil {
		t.Fatal("declining the dialog did not close it")
	}
}

// waitLive waits until the feature's engine session is running or queued
// (holding or waiting for a slot) — i.e. live autonomous work.
func waitLive(t *testing.T, eng *engine.Engine, id domain.FeatureID) {
	t.Helper()
	deadline := time.After(testWaitTimeout)
	for {
		if s := eng.Get(id); s != nil {
			switch s.State() {
			case engine.StateRunning, engine.StateQueued:
				return
			}
		}
		select {
		case <-deadline:
			t.Fatal("session never went live")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestShellZeroSizeSafe(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0")
	if v := m.View(); v.Content != "" {
		t.Error("zero-size view should be empty, not panic")
	}
}

// TestNoticeDoesNotReload proves the decouple: a routine status notice
// (no reload flag) yields no command from an attached shell — a status
// string no longer triggers a board reload.
func TestNoticeDoesNotReload(t *testing.T) {
	m, _ := newWorkspace(t)
	m = pump(t, m, m.Init())
	// a routine notice: "queued", "paused", a non-mutating error
	model, cmd := m.Update(noticeMsg{text: "FD-001 queued"})
	if cmd != nil {
		t.Fatalf("routine notice produced a command: %v", cmd)
	}
	if got := model.(*Shell).notice.text; got != "FD-001 queued" {
		t.Errorf("notice = %q, want the status text", got)
	}
}

// TestNoticeReloadOnFlag proves the opt-in: a notice carrying reload:true
// yields a reload command, so state-changing notices still refresh.
func TestNoticeReloadOnFlag(t *testing.T) {
	m, _ := newWorkspace(t)
	m = pump(t, m, m.Init())
	model, cmd := m.Update(noticeMsg{text: "FD-001 created", reload: true})
	if cmd == nil {
		t.Fatal("reload:true notice produced no command")
	}
	if got := model.(*Shell).notice.text; got != "FD-001 created" {
		t.Errorf("notice = %q, want the status text", got)
	}
}

func TestShellResizeRecomputesLayout(t *testing.T) {
	m := shellAt(120, 34)
	wide := m.View().Content
	model, _ := m.Update(tea.WindowSizeMsg{Width: 50, Height: 12})
	narrow := model.(*Shell).View().Content
	if wide == narrow {
		t.Error("resize did not change the rendered layout")
	}
	if strings.Contains(narrow, "BOARD") {
		t.Error("kanban pane should collapse at 50 cols")
	}
}
