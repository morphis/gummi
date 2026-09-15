package engine

import (
	"strings"
	"testing"
)

// TestReentryTurnRestatesTheExchange: a restored ask is delivered into a
// session that never asked the question, so the bare answer string leaves
// it with nothing to attach the answer to. The turn must carry the
// question, the alternatives and the answer.
func TestReentryTurnRestatesTheExchange(t *testing.T) {
	ask := &Ask{
		Question: "Where should the --format flag be parsed?",
		Options: []AskOption{
			{Label: "A. Subcommand-scoped flag"},
			{Label: "B. Global top-level flag"},
		},
	}
	got := reentryTurn(ask, "A. Subcommand-scoped flag")

	for _, want := range []string{
		"Where should the --format flag be parsed?",
		"A. Subcommand-scoped flag",
		"B. Global top-level flag",
		"fresh session",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("re-entry turn omits %q:\n%s", want, got)
		}
	}
	// the answer must be unambiguous, not just present among the options
	if !strings.Contains(got, "The answer is: A. Subcommand-scoped flag") {
		t.Errorf("the chosen answer is not stated as the answer:\n%s", got)
	}
}

// An ask with no question text cannot be quoted; the turn degrades to the
// bare answer rather than framing an exchange it cannot describe.
func TestReentryTurnDegradesWithoutAQuestion(t *testing.T) {
	if got := reentryTurn(&Ask{}, "yes"); got != "yes" {
		t.Errorf("questionless ask = %q, want the bare answer", got)
	}
	if got := reentryTurn(nil, "yes"); got != "yes" {
		t.Errorf("nil ask = %q, want the bare answer", got)
	}
}

// A blank option label must not reach the turn as a stray separator.
func TestReentryTurnSkipsEmptyOptionLabels(t *testing.T) {
	got := reentryTurn(&Ask{
		Question: "Pick one",
		Options:  []AskOption{{Label: "A"}, {Label: "  "}},
	}, "A")
	if strings.Contains(got, "· ·") || strings.Contains(got, "A ·\n") {
		t.Errorf("empty label rendered as a separator:\n%s", got)
	}
}
