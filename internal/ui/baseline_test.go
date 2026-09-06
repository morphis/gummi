package ui

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/verify"
)

// Crossing the design gate chains discovery → baseline: a check that
// fails on the fresh branch is flagged to the user right away (loud notice, row
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
	m = advanceTo(t, m, domain.StageImplement) // the design gate: worktree + discovery + baseline

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

// TestBaselineNoticeNamesTheOptOutOn127: exit 127 is the shell's
// command-not-found code, which a missing script file returns too — so a
// check whose target the feature has yet to create looks identical to a
// broken command. The generic "fix the block" advice is wrong for that
// case (the command is already right), so 127 must name baseline: false
// instead. Every other failure keeps the fix-the-block advice.
func TestBaselineNoticeNamesTheOptOutOn127(t *testing.T) {
	id := domain.FeatureID("FD-005")
	notFound := verify.Result{
		Name: "check-devctl-syntax", Cmd: "bash -n bin/devctl",
		Status: verify.StatusFail, ExitCode: 127,
	}
	got := baselineNotice(id, []verify.Result{notFound})
	if !got.isErr {
		t.Error("a failing baseline check must render as an error notice")
	}
	for _, want := range []string{
		"check-devctl-syntax", "command or file not found", "baseline: false",
	} {
		if !strings.Contains(got.text, want) {
			t.Errorf("127 notice = %q, want %q", got.text, want)
		}
	}

	// A real failure on a command that ran is unchanged: the block is
	// still the thing to fix, and baseline: false is not the answer.
	ranAndFailed := notFound
	ranAndFailed.ExitCode = 2
	got = baselineNotice(id, []verify.Result{ranAndFailed})
	if !strings.Contains(got.text, "pre-existing failure or wrong command") {
		t.Errorf("exit-2 notice = %q, want the fix-the-block reason", got.text)
	}
	if strings.Contains(got.text, "baseline: false") {
		t.Errorf("exit-2 notice offers the opt-out for a command that ran: %q", got.text)
	}

	// All-green stays quiet.
	got = baselineNotice(id, []verify.Result{{Name: "build", OK: true, Status: verify.StatusPass}})
	if got.isErr || !strings.Contains(got.text, "all 1 repo check(s) pass") {
		t.Errorf("all-pass notice = %q (isErr=%v), want the quiet summary", got.text, got.isErr)
	}
}
