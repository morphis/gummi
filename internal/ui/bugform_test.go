package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/ui/theme"
)

// TestBugFormSizing mirrors the feature form's sizing test: the
// description editor's rendered size matches dialogDescSize's output at
// a small and a large draw area.
func TestBugFormSizing(t *testing.T) {
	styles := theme.New(theme.GummiDark())
	for _, area := range []struct{ w, h int }{{60, 20}, {200, 60}} {
		form := newBugForm(nil, nil, 0, func(bugFormResult) tea.Cmd { return nil })
		form.View(styles, area.w, area.h)
		wantW, wantH := dialogDescSize(area.w, area.h)
		assertDescRendersAt(t, form.desc.View(), area.w, area.h, wantW, wantH)
	}
}

// TestBugFormCharLimit: the description accepts up to 4096 characters
// without truncation.
func TestBugFormCharLimit(t *testing.T) {
	form := newBugForm(nil, nil, 0, func(bugFormResult) tea.Cmd { return nil })
	long := strings.Repeat("a", 4096)
	form.desc.SetValue(long)
	if got := len(form.desc.Value()); got != 4096 {
		t.Fatalf("description length = %d, want 4096 (not truncated)", got)
	}
}

// TestBugFormAltEnterInsertsNewline: alt+enter (and ctrl+j) insert a
// newline into the description while it's focused, matching the feature
// form.
func TestBugFormAltEnterInsertsNewline(t *testing.T) {
	form := newBugForm(nil, nil, 0, func(bugFormResult) tea.Cmd { return nil })
	form.desc.SetValue("Crash on empty diff")
	form.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt})
	if got := form.desc.Value(); got != "Crash on empty diff\n" {
		t.Fatalf("desc after alt+enter = %q, want a trailing newline", got)
	}
	form.HandleKey(tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl})
	if got := form.desc.Value(); got != "Crash on empty diff\n\n" {
		t.Fatalf("desc after ctrl+j = %q, want a second trailing newline", got)
	}
}

// TestBugFormMultilinePasteKeepsNewlines: a multiline paste routes to the
// description and its newlines survive.
func TestBugFormMultilinePasteKeepsNewlines(t *testing.T) {
	form := newBugForm(nil, nil, 0, func(bugFormResult) tea.Cmd { return nil })
	form.HandlePaste(tea.PasteMsg{Content: "Crash on empty diff\n\nRepro: stage nothing, hit c."})
	if got := form.desc.Value(); got != "Crash on empty diff\n\nRepro: stage nothing, hit c." {
		t.Fatalf("desc after paste = %q, want newlines preserved", got)
	}
}

// TestBugFormDownMovesCursorNotFocus: with the description multiline,
// "down" is no longer a tab-cycle alias — it moves the in-editor cursor,
// same as the feature form. Only tab/shift+tab cycle focus.
func TestBugFormDownMovesCursorNotFocus(t *testing.T) {
	form := newBugForm(nil, nil, 0, func(bugFormResult) tea.Cmd { return nil })
	form.desc.SetValue("line one\nline two")
	if done, _ := form.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown}); done {
		t.Fatal("down should not submit or close the form")
	}
	if form.focus != fieldDesc {
		t.Fatalf("focus = %d after down, want it to stay on fieldDesc", form.focus)
	}
	if done, _ := form.HandleKey(tea.KeyPressMsg{Code: tea.KeyUp}); done {
		t.Fatal("up should not submit or close the form")
	}
	if form.focus != fieldDesc {
		t.Fatalf("focus = %d after up, want it to stay on fieldDesc", form.focus)
	}
}

// TestBugFormHintShowsNewlineKey: the bug form's hint mentions alt+enter,
// matching the feature form's phrasing.
func TestBugFormHintShowsNewlineKey(t *testing.T) {
	styles := theme.New(theme.GummiDark())
	form := newBugForm(nil, nil, 0, func(bugFormResult) tea.Cmd { return nil })
	out := form.View(styles, 80, 24)
	if !strings.Contains(out, "alt+enter newline") {
		t.Fatalf("bug form hint missing %q:\n%s", "alt+enter newline", out)
	}
}

// TestBugFormSubmitSplitsTitleOneLinerSeed is the plan's golden: a
// multiline description splits like a feature's, deriving Title from the
// first line and carrying the rest verbatim as Seed.
func TestBugFormSubmitSplitsTitleOneLinerSeed(t *testing.T) {
	var got bugFormResult
	form := newBugForm(nil, nil, 0, func(res bugFormResult) tea.Cmd {
		got = res
		return nil
	})
	form.desc.SetValue("Crash on empty diff\n\nRepro: stage nothing, hit c.")
	if done, _ := form.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}); !done {
		t.Fatal("form did not submit")
	}
	if got.Title != "Crash on empty diff" {
		t.Errorf("Title = %q, want %q", got.Title, "Crash on empty diff")
	}
	if got.OneLiner != "" {
		t.Errorf("OneLiner = %q, want empty", got.OneLiner)
	}
	want := "Crash on empty diff\n\nRepro: stage nothing, hit c."
	if got.Seed != want {
		t.Errorf("Seed = %q, want %q", got.Seed, want)
	}
}

// TestBugFormEnvelopeDefault: the bug form pre-fills its envelope from
// the global default, mirroring the feature form.
func TestBugFormEnvelopeDefault(t *testing.T) {
	form := newBugForm(nil, nil, 3000, func(bugFormResult) tea.Cmd { return nil })
	if got := form.env.Value(); got != "3000" {
		t.Fatalf("envelope pre-fill = %q, want \"3000\"", got)
	}
}

// TestBugFormEnvelopeCustom: a valid custom value parses into an
// explicit Envelope on submit.
func TestBugFormEnvelopeCustom(t *testing.T) {
	var got *int
	form := newBugForm(nil, nil, 1000, func(res bugFormResult) tea.Cmd {
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
	form := newBugForm(nil, nil, 1000, func(bugFormResult) tea.Cmd {
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
	form := newBugForm(nil, nil, 1000, func(bugFormResult) tea.Cmd {
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
	form := newBugForm(nil, nil, 1000, func(res bugFormResult) tea.Cmd {
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
	form := newBugForm(nil, nil, 0, func(bugFormResult) tea.Cmd { return nil })
	for _, want := range []int{fieldDesc, fieldEnvelope, fieldOpts} {
		if form.focus != want {
			t.Fatalf("focus = %d, want %d", form.focus, want)
		}
		form.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab})
	}
}
