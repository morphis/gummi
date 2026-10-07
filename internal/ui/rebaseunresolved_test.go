package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
)

// TestAnUnresolvedAgentRebaseKeepsThePassedVerify: a card whose verify
// passed, whose landing hit a conflict, and whose agent rebase then ended
// without resolving anything, is still a card with a passed verify. The
// live board used to read the rebase session's missing verdict as the
// stage's, and offered "land anyway" over a verify failure that never
// happened; only a restart put it right.
func TestAnUnresolvedAgentRebaseKeepsThePassedVerify(t *testing.T) {
	ag := &agent.Fake{Caps: agent.Capabilities{ClientTools: true}, Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		if strings.Contains(msg, "Proceed with the rebase") {
			// does nothing at all
			return []agent.Event{{Kind: agent.EventMessage, Text: "Looked at it."}, {Kind: agent.EventIdle}}
		}
		// the verdict as a real reviewer files it — the tool, which is
		// what the card's log keeps once the session is gone
		call := &agent.ToolCall{ID: "v-1", Name: "submit_verdict", Args: []byte(`{"verdict":"pass","summary":"green"}`)}
		return []agent.Event{
			{Kind: agent.EventClientToolCall, ToolCall: call},
			{Kind: agent.EventMessage, Text: "Checks green.\nVERDICT: pass"},
			{Kind: agent.EventIdle},
		}
	}}
	// the log is what a card's page reads once its session is gone
	m, eng := agentWorkspaceProfiles(t, ag, config.Profiles{}, func(c *engine.Config) { c.Persist = true })
	m, f := conflictOnMain(t, m)

	// verify runs and passes: the landing gate is up
	m = pump(t, m, m.runStage(m.rows[0].F))
	settleChat(t, eng)
	m = drainEngineLoop(t, m)
	if it, ok := m.inbox.get("FD-001"); !ok || !strings.Contains(it.Text, "verify passed") {
		t.Fatalf("fixture: no passed verify gate: %+v", it)
	}

	m = pump(t, m, m.rebaseFeature(f))
	if top := m.Overlay.Top(); top == nil || top.ID() != "agent-rebase" {
		t.Fatalf("conflict did not offer the agent hand-off (notice %q)", m.notice.text)
	}
	m = press(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	settleChat(t, eng)
	m = drainEngineLoop(t, m)
	m = pump(t, m, m.loadRows)

	if !m.notice.isErr || !strings.Contains(m.notice.text, "still on its old base") {
		t.Errorf("notice = %q, want it to say the rebase did not happen", m.notice.text)
	}
	if strings.Contains(m.notice.text, "(t)") {
		t.Errorf("notice names a terminal key: %q", m.notice.text)
	}
	it, ok := m.inbox.get("FD-001")
	if !ok || it.Escalated || !strings.Contains(it.Text, "verify passed") {
		t.Errorf("the passed verify's decision did not stand: %+v", it)
	}
	in := m.nextInputFor(m.rows[0])
	if in.verdict != verdictPass {
		t.Errorf("verdict after the failed rebase = %v, want the verify's pass", in.verdict)
	}
	d := m.openDecision(m.rows[0])
	if d == nil {
		t.Fatal("no decision after the failed rebase")
	}
	for _, a := range d.actions {
		if a.label == "land anyway" {
			t.Errorf("a passed verify offers %q after an unresolved rebase", a.label)
		}
	}
	if got := m.rows[0].F.Stage; got != domain.StageVerify {
		t.Errorf("stage = %s", got)
	}
}
