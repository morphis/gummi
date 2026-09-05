package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/engine"
)

// TestUnlabelledAskOptionIsNotSilentlyUnanswerable: parseAsk validates that an
// ask_user call had a question and at least one option, but never that an
// option carried a label. An option whose label decoded empty rendered as
// a blank picker row enter could never answer: decisionAnswerText
// returned "", answerDecision returned nil, and nothing at all happened —
// no answer, no notice, no error. The card parked on a question that
// could not be answered from the TUI. parseAsk now rejects it at the
// boundary; this asserts the UI no longer swallows the keystroke either.
func TestUnlabelledAskOptionIsNotSilentlyUnanswerable(t *testing.T) {
	m := populatedShell(120, 34)
	r := m.rows[m.sel]
	d := &threadDecision{
		key: "ask:FD-001",
		ask: &engine.Ask{
			Question: "which approach?",
			Options:  []engine.AskOption{{Label: "", Detail: "the label never arrived"}},
		},
	}
	m.syncDecision(d)
	m.notice.text = ""

	cmd := m.answerDecision(r, d)
	if cmd == nil && m.notice.text == "" {
		t.Fatal("enter on an unlabelled option did nothing and said nothing")
	}
}

// TestUndrawnDecisionSaysWhyEnterDidNothing: the second silent path. An open
// decision that did not fit on screen is indistinguishable, to every
// caller, from no decision at all — visibleDecision returns nil for both
// — so enter is a no-op with no explanation.
func TestUndrawnDecisionSaysWhyEnterDidNothing(t *testing.T) {
	m, eng := chatWorkspace(t, askingFake())
	m = openAndAttach(t, m)
	waitAsk(t, eng)
	m = pump(t, m, nil)

	var undrawnAt int
	for h := 40; h >= 1; h-- {
		model, _ := m.update(tea.WindowSizeMsg{Width: 100, Height: h})
		m = model.(*Shell)
		if m.openDecision(m.rows[m.sel]) != nil && m.visibleDecision(m.rows[m.sel]) == nil {
			undrawnAt = h
			break
		}
	}
	if undrawnAt == 0 {
		t.Skip("the picker fit at every height this fixture could reach")
	}
	t.Logf("decision open but undrawn at height %d", undrawnAt)

	m.notice.text = ""
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if eng.Get("FD-001").Snapshot().PendingAsk != nil && m.notice.text == "" {
		t.Fatal("enter dropped the answer and said nothing")
	}
}
