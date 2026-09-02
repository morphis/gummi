package ui

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// Spec approval chains discovery → baseline: a check that fails on the
// fresh branch is flagged to the user right away (loud notice, row
// counter), instead of surfacing as the feature's fault at verify.
func TestApprovalRunsBaselineAndFlagsFailure(t *testing.T) {
	ag := &agent.Fake{Responder: func(_ agent.SessionOpts, msg string) []agent.Event {
		reply := "ok"
		if strings.Contains(msg, "gummi-checks") { // the discovery prompt
			reply = "```gummi-checks\n- name: lint\n  cmd: \"exit 7\"\n```"
		}
		return []agent.Event{
			{Kind: agent.EventMessage, Text: reply},
			{Kind: agent.EventIdle},
		}
	}}
	m, _ := chatWorkspace(t, ag)
	m = advanceTo(t, m, domain.StagePlan) // spec approval: worktree + discovery + baseline

	if len(m.baselining) != 0 {
		t.Errorf("baseline still marked in flight: %+v", m.baselining)
	}
	if len(m.scribing) != 0 {
		t.Errorf("scribe passes still marked in flight: %+v", m.scribing)
	}
	if !m.notice.isErr {
		t.Errorf("failing baseline did not raise an error notice: %+v", m.notice)
	}
	for _, want := range []string{"baseline", "'lint'", "exit 7"} {
		if !strings.Contains(m.notice.text, want) {
			t.Errorf("baseline notice missing %q: %q", want, m.notice.text)
		}
	}
	if m.rows[0].BaselineFails != 1 {
		t.Errorf("BaselineFails = %d, want 1", m.rows[0].BaselineFails)
	}
}
