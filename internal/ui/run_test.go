package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/golden"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
)

func TestRunAutonomousStage(t *testing.T) {
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{
			{Kind: agent.EventToolCall, Tool: "edit internal/theme/palette.go"},
			{Kind: agent.EventToolCall, Tool: "run go test ./..."},
			{Kind: agent.EventMessage, Text: "Implemented the toggle and added a test."},
			{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 3, OutputTokens: 120}},
			{Kind: agent.EventIdle},
		}
	}}
	m, eng := chatWorkspace(t, ag)
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	if m.rows[0].F.Stage != domain.StageImplement {
		t.Fatalf("stage = %s, want implement", m.rows[0].F.Stage)
	}

	// enter runs the autonomous stage (no chat pane for implement)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.chat != nil {
		t.Fatal("implement should not open the chat pane")
	}
	settleChat(t, eng)

	sess := m.sessionFor("FD-001")
	if sess == nil {
		t.Fatal("no active session for the running feature")
	}
	snap := sess.Snapshot()
	if len(snap.Activity) != 2 || snap.Activity[0] != "edit internal/theme/palette.go" {
		t.Errorf("activity feed wrong: %+v", snap.Activity)
	}
	if snap.Spend.Credits != 3 {
		t.Errorf("spend not metered: %+v", snap.Spend)
	}

	// the dashboard shows the activity feed
	view := m.View().Content
	if !strings.Contains(view, "activity") || !strings.Contains(view, "run go test") {
		t.Error("dashboard missing live activity feed")
	}
}

func TestPauseStopsRun(t *testing.T) {
	m, eng := chatWorkspace(t, agent.NewFake("working…"))
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // run
	settleChat(t, eng)
	if m.sessionFor("FD-001") == nil {
		t.Fatal("run did not start a session")
	}
	// p pauses: the session is stopped and marked paused (kept visible)
	m = press(t, m, tea.KeyPressMsg{Code: 'p', Text: "p"})
	s := m.engine.Get("FD-001")
	if s == nil || s.State() != engine.StatePaused {
		t.Errorf("after pause: session state = %v, want paused", s)
	}
	if !strings.Contains(m.notice.text, "paused") {
		t.Errorf("notice = %q, want paused", m.notice.text)
	}
}

func TestRunRejectsInteractiveViaRunPath(t *testing.T) {
	// enter on a brainstorm feature opens chat, not a run
	m, _ := chatWorkspace(t, agent.NewFake("hi"))
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.chat == nil {
		t.Fatal("brainstorm enter should open chat, not run")
	}
}

// selectRow points the board selection at a feature by ID.
func selectRow(t *testing.T, m *Shell, id domain.FeatureID) {
	t.Helper()
	for i, r := range m.rows {
		if r.F.ID == id {
			m.sel = i
			return
		}
	}
	t.Fatalf("row %s not found in %d rows", id, len(m.rows))
}

func TestBugInteractiveStagesOpenChat(t *testing.T) {
	// enter on a bug at its interactive stages (triage/diagnose) opens
	// the gummi chat pane, exactly like brainstorm/spec for features.
	m, _ := chatWorkspace(t, agent.NewFake("Can you reproduce it?"))
	m = press(t, m, tea.KeyPressMsg{Code: 'B', Text: "B"})
	m = typeString(t, m, "Login loops")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	for _, stage := range []domain.Stage{domain.StageTriage, domain.StageDiagnose} {
		selectRow(t, m, "BG-002")
		m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
		if m.rows[m.sel].F.Stage != stage {
			t.Fatalf("setup: stage = %s, want %s", m.rows[m.sel].F.Stage, stage)
		}
		selectRow(t, m, "BG-002")
		m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
		if m.chat == nil {
			t.Fatalf("enter at %s did not open the chat pane (notice: %q)", stage, m.notice.text)
		}
		if m.chat.feature != "BG-002" {
			t.Fatalf("chat bound to %s, want BG-002", m.chat.feature)
		}
		s := m.engine.Get("BG-002")
		if s == nil || !s.Interactive {
			t.Fatalf("%s did not start an interactive session", stage)
		}
		m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape}) // detach for the next round
	}
}

func TestWatchAttachesRunningSession(t *testing.T) {
	// no trailing idle: the session stays busy, so the run keeps going
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{
			{Kind: agent.EventMessage, Text: "Wiring the toggle."},
			{Kind: agent.EventToolCall, Tool: "edit theme.go"},
		}
	}}
	m, eng := chatWorkspace(t, ag)
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})

	// first enter starts the run — no pane, activity goes to the dashboard
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.chat != nil {
		t.Fatal("starting a run must not open the chat pane")
	}
	waitForActivity(t, eng)

	// second enter attaches the observer pane over the running session
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.chat == nil {
		t.Fatalf("enter on a running session did not attach (notice: %q)", m.notice.text)
	}
	view := m.View().Content
	if !strings.Contains(view, "Wiring the toggle.") || !strings.Contains(view, "edit theme.go") {
		t.Errorf("watch pane missing transcript content:\n%s", view)
	}

	// the tool call is a transcript entry, ordered after the message
	snap := m.chat.session.Snapshot()
	var msgAt, toolAt int
	for i, msg := range snap.Transcript {
		switch {
		case msg.Content == "Wiring the toggle.":
			msgAt = i
		case msg.Author == engine.AuthorTool:
			toolAt = i
		}
	}
	if toolAt <= msgAt {
		t.Errorf("tool call not interleaved after its message: %+v", snap.Transcript)
	}

	// esc detaches; the run keeps going
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.chat != nil {
		t.Fatal("esc did not detach the watch pane")
	}
	if s := eng.Get("FD-001"); s == nil || s.State() != engine.StateRunning {
		t.Error("detaching stopped the run")
	}
}

// waitForActivity polls until FD-001's session has a tool-call line.
func waitForActivity(t *testing.T, eng *engine.Engine) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		if s := eng.Get("FD-001"); s != nil && len(s.Snapshot().Activity) > 0 {
			return
		}
		select {
		case <-deadline:
			t.Fatal("no activity arrived")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestDashboardActivityGolden(t *testing.T) {
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{
			{Kind: agent.EventToolCall, Tool: "edit", Detail: "palette.go"},
			{Kind: agent.EventToolCall, Tool: "bash", Detail: "go test ./..."},
			{Kind: agent.EventMessage, Text: "Done: toggle wired, tests green."},
			{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 2, OutputTokens: 88}},
			{Kind: agent.EventIdle},
		}
	}}
	m, eng := chatWorkspace(t, ag)
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	settleChat(t, eng)
	golden.RequireEqual(t, []byte(m.View().Content))
}
