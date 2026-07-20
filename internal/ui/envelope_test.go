package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
)

func typeInto(d *envelopeDialog, text string) {
	for _, r := range text {
		d.HandleKey(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func TestEnvelopeDialogSubmitsFigure(t *testing.T) {
	f := domain.Feature{ID: "FD-001", Budget: domain.Budget{Envelope: 300}}
	got := -1
	d := newEnvelopeDialog(f, func(to int) tea.Cmd { got = to; return nil })

	typeInto(d, "450")
	closed, _ := d.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !closed || got != 450 {
		t.Fatalf("submit: closed=%v got=%d, want true/450", closed, got)
	}
}

func TestEnvelopeDialogRejectsNonNumbers(t *testing.T) {
	f := domain.Feature{ID: "FD-001"}
	called := false
	d := newEnvelopeDialog(f, func(int) tea.Cmd { called = true; return nil })

	typeInto(d, "lots")
	closed, _ := d.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if closed || called {
		t.Fatalf("bad input submitted: closed=%v called=%v, want false/false", closed, called)
	}
	if d.problem == "" {
		t.Fatal("no problem message after a non-numeric submit")
	}
	// the message clears as soon as the figure is edited
	typeInto(d, "4")
	if d.problem != "" {
		t.Fatalf("problem %q survived an edit", d.problem)
	}
}

func TestEnvelopeDialogEmptyEnterCancels(t *testing.T) {
	f := domain.Feature{ID: "FD-001"}
	called := false
	d := newEnvelopeDialog(f, func(int) tea.Cmd { called = true; return nil })

	closed, _ := d.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !closed || called {
		t.Fatalf("empty enter: closed=%v called=%v, want true/false", closed, called)
	}
}
