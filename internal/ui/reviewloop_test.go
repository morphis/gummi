package ui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

func TestParseVerdict(t *testing.T) {
	cases := map[string]reviewVerdict{
		"looks good\nVERDICT: pass":                        verdictPass,
		"issues found\nVERDICT: changes\n":                 verdictChanges,
		"VERDICT: PASS":                                    verdictPass,
		"  verdict:   changes  ":                           verdictChanges,
		"no verdict here":                                  verdictUnclear,
		"VERDICT: pass\nthen later\nVERDICT: changes":      verdictChanges, // last wins
		"the word verdict: pass appears mid-sentence only": verdictUnclear, // not on its own line
	}
	for in, want := range cases {
		if got := parseVerdict(in); got != want {
			t.Errorf("parseVerdict(%q) = %d, want %d", in, got, want)
		}
	}
}

// reviewAgent scripts an autonomous agent whose reply depends on the
// feature's stage (passed via SystemHints) — it emits the given verdict
// only for review sessions.
func verdictAgent(reply func(opts agent.SessionOpts) string) *agent.Fake {
	return &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{
			{Kind: agent.EventMessage, Text: reply(opts)},
			{Kind: agent.EventIdle},
		}
	}}
}

func isReview(opts agent.SessionOpts) bool { return opts.Role == agent.RoleReviewer }

// advanceTo drives g until the feature reaches the target stage.
func advanceTo(t *testing.T, m *Shell, target domain.Stage) *Shell {
	t.Helper()
	for i := 0; i < 8 && m.rows[0].F.Stage != target; i++ {
		m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	}
	if m.rows[0].F.Stage != target {
		t.Fatalf("could not reach %s (at %s)", target, m.rows[0].F.Stage)
	}
	return m
}

func TestReviewPassAdvancesToVerify(t *testing.T) {
	ag := verdictAgent(func(opts agent.SessionOpts) string {
		if isReview(opts) {
			return "No issues.\nVERDICT: pass"
		}
		return "done"
	})
	m, eng := chatWorkspace(t, ag)
	m = advanceTo(t, m, domain.StageReview)

	// run review; a passing verdict should auto-advance to verify
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	settleChat(t, eng)
	m = drainEngineLoop(t, m)

	if m.rows[0].F.Stage != domain.StageVerify {
		t.Fatalf("review pass did not advance to verify (at %s)", m.rows[0].F.Stage)
	}
}

func TestReviewChangesBouncesAndLoops(t *testing.T) {
	// review always requests changes; the loop should bounce to implement,
	// re-review, and after the cap escalate to the inbox.
	ag := verdictAgent(func(opts agent.SessionOpts) string {
		if isReview(opts) {
			return "Found a bug.\nVERDICT: changes"
		}
		return "fixed"
	})
	m, eng := chatWorkspace(t, ag)
	m = advanceTo(t, m, domain.StageReview)

	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // first review
	settleChat(t, eng)
	m = drainEngineLoop(t, m)

	// after the cap, the loop stops and escalates
	if m.reviewRounds["FD-001"] != 0 {
		t.Errorf("rounds not reset after escalation: %d", m.reviewRounds["FD-001"])
	}
	found := false
	for _, it := range m.inbox.list() {
		if it.Feature == "FD-001" && it.Kind == attnGate {
			found = true
		}
	}
	if !found {
		t.Error("changes-loop did not escalate to the inbox after the cap")
	}
}

func TestReviewUnclearVerdictEscalates(t *testing.T) {
	ag := verdictAgent(func(opts agent.SessionOpts) string { return "I reviewed it." })
	m, eng := chatWorkspace(t, ag)
	m = advanceTo(t, m, domain.StageReview)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	settleChat(t, eng)
	m = drainEngineLoop(t, m)

	if m.rows[0].F.Stage != domain.StageReview {
		t.Errorf("unclear verdict advanced the stage to %s", m.rows[0].F.Stage)
	}
	if m.inbox.len() == 0 {
		t.Error("unclear verdict did not escalate to the inbox")
	}
}

// drainEngineLoop pumps engine events AND the review-loop commands they
// produce until the loop settles (no active session, no pending events).
func drainEngineLoop(t *testing.T, m *Shell) *Shell {
	t.Helper()
	for i := 0; i < 60; i++ {
		select {
		case ev := <-m.engine.Events():
			cmd := m.handleEngineEvent(ev)
			m = pump(t, m, cmd) // run any auto-step (transition + run)
		case <-time.After(80 * time.Millisecond):
			return m
		}
	}
	return m
}
