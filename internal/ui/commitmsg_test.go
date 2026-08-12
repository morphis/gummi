package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
)

func newTestCommitMsgDialog(t *testing.T) *commitMsgDialog {
	t.Helper()
	return newCommitMsgDialog(
		domain.Feature{ID: "FD-001", Slug: "dark-mode"},
		func(string) tea.Cmd { return nil },
		nil,
	)
}

func TestCommitMsgDialogNavigationKeyDoesNotMarkModified(t *testing.T) {
	const draft = "feat(ui): add dark mode"

	navKeys := []tea.Key{
		{Code: tea.KeyUp},
		{Code: tea.KeyDown},
		{Code: tea.KeyLeft},
		{Code: tea.KeyRight},
		{Code: tea.KeyHome},
		{Code: tea.KeyEnd},
		{Code: tea.KeyPgUp},
		{Code: tea.KeyPgDown},
	}

	for _, key := range navKeys {
		t.Run(key.String(), func(t *testing.T) {
			d := newTestCommitMsgDialog(t)

			// A pure cursor-movement key, before the draft arrives.
			if _, _ = d.HandleKey(tea.KeyPressMsg{Code: key.Code}); d.modified {
				t.Fatalf("HandleKey(%s) set modified on empty dialog; want false", key)
			}

			// A draft arriving afterwards must still fill the box.
			d.gen = 1
			d.apply(commitDraftMsg{f: d.feature, gen: 1, draft: draft})
			if got := d.input.Value(); got != draft {
				t.Fatalf("apply after %s: value = %q, want draft %q", key, got, draft)
			}
		})
	}
}

func TestCommitMsgDialogModifyingKeyMarksModifiedAndSkipsDraft(t *testing.T) {
	const draft = "feat(ui): replace the modified flag"

	d := newTestCommitMsgDialog(t)

	// A printable character changes the value, so it must mark modified.
	if _, _ = d.HandleKey(tea.KeyPressMsg{Code: 'x', Text: "x"}); !d.modified {
		t.Fatal("HandleKey(printable char) did not set modified; want true")
	}
	typed := d.input.Value()
	if typed == "" {
		t.Fatal("typing a character left the textarea empty")
	}

	// A draft arriving afterwards must not clobber what was typed.
	d.gen = 1
	d.apply(commitDraftMsg{f: d.feature, gen: 1, draft: draft})
	if got := d.input.Value(); got != typed {
		t.Fatalf("apply clobbered typed text: got %q, want %q", got, typed)
	}
}

func TestCommitMsgDialogApplyHonorsGenerationAndEmpty(t *testing.T) {
	d := newTestCommitMsgDialog(t)

	// A stale-generation draft must be dropped.
	d.gen = 2
	d.apply(commitDraftMsg{f: d.feature, gen: 1, draft: "stale"})
	if got := d.input.Value(); got != "" {
		t.Fatalf("stale-gen apply filled box with %q; want empty", got)
	}

	// An empty draft leaves the box empty even for the current gen.
	d.apply(commitDraftMsg{f: d.feature, gen: 2, draft: ""})
	if got := d.input.Value(); got != "" {
		t.Fatalf("empty draft filled box with %q; want empty", got)
	}
}

func TestCommitMsgDialogPasteAlwaysMarksModified(t *testing.T) {
	d := newTestCommitMsgDialog(t)

	d.HandlePaste(tea.PasteMsg{Content: "feat: pasted note"})
	if !d.modified {
		t.Fatal("HandlePaste did not set modified; want true")
	}
	if got := d.input.Value(); got != "feat: pasted note" {
		t.Fatalf("HandlePaste inserted %q, want pasted content", got)
	}
}
