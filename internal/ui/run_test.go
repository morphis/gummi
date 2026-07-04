package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/golden"

	"github.com/morphia/gummi/internal/agent"
	"github.com/morphia/gummi/internal/domain"
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
	// p pauses: the active session is stopped
	m = press(t, m, tea.KeyPressMsg{Code: 'p', Text: "p"})
	if m.engine.Active() != nil {
		t.Error("pause did not stop the active session")
	}
	if !strings.Contains(m.notice.text, "paused") {
		t.Errorf("notice = %q, want paused", m.notice.text)
	}
}

func TestPauseBeforeKickoffNoError(t *testing.T) {
	m, _ := chatWorkspace(t, agent.NewFake("x"))
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})

	// press enter (runStage starts the session, returns a kickoff cmd)
	model, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = model.(*Shell)
	// pause BEFORE running the kickoff command
	m = press(t, m, tea.KeyPressMsg{Code: 'p', Text: "p"})
	if m.engine.Active() != nil {
		t.Fatal("pause did not stop the session")
	}
	// now run the deferred kickoff — it must not produce an error notice
	m = pump(t, m, cmd)
	if m.notice.isErr {
		t.Errorf("kickoff after pause produced an error: %q", m.notice.text)
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

func TestDashboardActivityGolden(t *testing.T) {
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{
			{Kind: agent.EventToolCall, Tool: "edit palette.go"},
			{Kind: agent.EventToolCall, Tool: "go test ./..."},
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
