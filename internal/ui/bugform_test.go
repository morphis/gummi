package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestBugFormEnvelopeDefault: the bug form pre-fills its envelope from
// the global default, mirroring the feature form.
func TestBugFormEnvelopeDefault(t *testing.T) {
	form := newBugForm(nil, 3000, func(bugFormResult) tea.Cmd { return nil })
	if got := form.env.Value(); got != "3000" {
		t.Fatalf("envelope pre-fill = %q, want \"3000\"", got)
	}
}

// TestBugFormEnvelopeCustom: a valid custom value parses into an
// explicit Envelope on submit.
func TestBugFormEnvelopeCustom(t *testing.T) {
	var got *int
	form := newBugForm(nil, 1000, func(res bugFormResult) tea.Cmd {
		got = res.Envelope
		return nil
	})
	form.desc.SetValue("crash on empty diff")
	form.env.SetValue("7500")
	if done, _ := form.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}); !done {
		t.Fatal("form did not submit")
	}
	if got == nil || *got != 7500 {
		t.Fatalf("Envelope = %v, want explicit 7500", got)
	}
}

// TestBugFormEnvelopeNegativeBlocksSubmit: a negative value is rejected
// and blocks submission.
func TestBugFormEnvelopeNegativeBlocksSubmit(t *testing.T) {
	submitted := false
	form := newBugForm(nil, 1000, func(bugFormResult) tea.Cmd {
		submitted = true
		return nil
	})
	form.desc.SetValue("crash on empty diff")
	form.env.SetValue("-100")
	if done, _ := form.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}); done {
		t.Fatal("negative envelope submitted")
	}
	if submitted {
		t.Fatal("onSubmit ran with a negative envelope")
	}
	if form.errText == "" {
		t.Fatal("expected an envelope error message")
	}
}

// TestBugFormEnvelopeNonNumericBlocksSubmit: non-numeric input is
// rejected the same way.
func TestBugFormEnvelopeNonNumericBlocksSubmit(t *testing.T) {
	submitted := false
	form := newBugForm(nil, 1000, func(bugFormResult) tea.Cmd {
		submitted = true
		return nil
	})
	form.desc.SetValue("crash on empty diff")
	form.env.SetValue("abc")
	if done, _ := form.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}); done {
		t.Fatal("non-numeric envelope submitted")
	}
	if submitted {
		t.Fatal("onSubmit ran with a non-numeric envelope")
	}
	if form.errText == "" {
		t.Fatal("expected an envelope error message")
	}
}

// TestBugFormEnvelopeEmpty: clearing the input yields Envelope nil, the
// use-default signal.
func TestBugFormEnvelopeEmpty(t *testing.T) {
	var got *int
	form := newBugForm(nil, 1000, func(res bugFormResult) tea.Cmd {
		got = res.Envelope
		return nil
	})
	form.desc.SetValue("crash on empty diff")
	form.env.SetValue("")
	if done, _ := form.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}); !done {
		t.Fatal("form did not submit")
	}
	if got != nil {
		t.Fatalf("Envelope = %v, want nil (use default)", got)
	}
}

// TestBugFormEnvelopeTabOrder: tab cycles description → envelope →
// options in the bug form too.
func TestBugFormEnvelopeTabOrder(t *testing.T) {
	form := newBugForm(nil, 0, func(bugFormResult) tea.Cmd { return nil })
	for _, want := range []int{fieldDesc, fieldEnvelope, fieldOpts} {
		if form.focus != want {
			t.Fatalf("focus = %d, want %d", form.focus, want)
		}
		form.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab})
	}
}
