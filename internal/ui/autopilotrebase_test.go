package ui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// TestAnAutopilotCardStartsItsLandingRebase: a landing that hits conflicts
// on a card on autopilot starts the rebase that resolves them by itself —
// no r, and no yes to the agent hand-off, because autopilot answers every
// stop but budget. An attended card keeps waiting for the person.
func TestAnAutopilotCardStartsItsLandingRebase(t *testing.T) {
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{
			{Kind: agent.EventMessage, Text: "All resolved."},
			{Kind: agent.EventIdle},
		}
	}}
	m, _ := chatWorkspace(t, ag)
	m, _ = conflictOnMain(t, m)
	if err := m.store.SetGateApproval(context.Background(), "FD-001", domain.GateAutopilot); err != nil {
		t.Fatal(err)
	}

	m = pump(t, m, func() tea.Msg {
		return landConflictMsg{
			id: "FD-001", files: []string{"README.md"},
			notice: noticeMsg{text: "FD-001: squash merge conflicts", isErr: true},
		}
	})
	if top := m.Overlay.Top(); top != nil && top.ID() == "agent-rebase" {
		t.Fatal("an autopilot card waited on the agent-rebase confirm")
	}
	if !strings.Contains(m.notice.text, "dispatched") {
		t.Fatalf("autopilot did not start the rebase: notice = %q (err=%v)", m.notice.text, m.notice.isErr)
	}
}
