package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
)

// A budget longer than the terminal's input takes is refused, not cut to
// fit: 123456789 saved as 12345678 would be a different answer from the
// one the person gave.
func TestAWebBudgetTooLongIsRefusedNotTruncated(t *testing.T) {
	got := -1
	d := newEnvelopeDialog(domain.Feature{ID: "FD-001"}, func(to int) tea.Cmd { got = to; return nil }, nil)
	n := 123456789
	res := d.webAnswer(nil, &webInput{number: &n})
	if got != -1 || res.refused == "" {
		t.Errorf("asked for %d: set %d, refused %q — want it refused and nothing set", n, got, res.refused)
	}
}

// The budget dialog's resume follow-up (a parked autopilot card whose
// budget is raised) is a question the TUI asks with y/n, and the web puts
// it to the person rather than taking its silence for a no.
func TestAWebBudgetRaiseAsksTheResumeQuestion(t *testing.T) {
	f := domain.Feature{ID: "FD-001", GateApproval: domain.GateAutopilot}
	f.Budget.Envelope = 10
	f.Spend.Add(12, 0, 0)
	resumed := false
	d := newEnvelopeDialog(f, func(int) tea.Cmd { return nil }, func() tea.Cmd { resumed = true; return nil })
	n := 50
	if !d.offersResume(n) {
		t.Fatal("precondition: the TUI would ask to resume")
	}
	res := d.webAnswer(nil, &webInput{number: &n})
	if res.cmd != nil {
		if msg := res.cmd(); msg != nil {
			_ = msg
		}
	}
	if res.needs == "" && !resumed {
		t.Errorf("the resume question was answered no without being asked: %+v", res)
	}
}

// A landing message longer than the dialog's textarea holds is refused,
// never landed cut short.
func TestAWebLandingMessageTooLongIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  string
	}{
		{"long", "feat: x\n\n" + repeatStr("word ", 1000)},
		{"many lines", "feat: x\n\n" + repeatStr("- item\n", 150)},
	} {
		landed := ""
		d := newCommitMsgDialog(domain.Feature{ID: "FD-001"}, func(m string, _ domain.LandMethod) tea.Cmd { landed = m; return nil }, nil)
		res := d.webAnswer(nil, &webInput{land: true, message: tc.msg})
		if want := trimSpace(tc.msg); len([]rune(want)) > d.input.CharLimit {
			if landed != "" || res.refused == "" {
				t.Errorf("%s: %d characters landed %d, refused %q — want it refused", tc.name, len(want), len(landed), res.refused)
			}
		} else if landed != "" && landed != want {
			t.Errorf("%s: sent %d bytes, landed %d bytes", tc.name, len(want), len(landed))
		}
	}
}

func repeatStr(s string, n int) string {
	out := ""
	for range n {
		out += s
	}
	return out
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\n') {
		s = s[:len(s)-1]
	}
	return s
}
