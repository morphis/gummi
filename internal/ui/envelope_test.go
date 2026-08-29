package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

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

// parkedAutopilotFeature is a card whose stored envelope is already
// fully spent while an autopilot mode governs it — the one shape
// offersResume can recognize as "parked for lack of budget" without
// reaching into live session state it does not have.
func parkedAutopilotFeature() domain.Feature {
	return domain.Feature{
		ID: "FD-047", GateApproval: domain.GateFull,
		Budget: domain.Budget{Envelope: 2400},
		Spend:  domain.Spend{Credits: 2400},
	}
}

// TestEnvelopeDialogOffersResumeOnParkedAutopilotRaise: raising the
// envelope past an already-exhausted one on an autopilot card sets the
// envelope (onSubmit still fires) but keeps the dialog open on the
// resume question instead of closing it — the top-up itself must never
// silently restart the run.
func TestEnvelopeDialogOffersResumeOnParkedAutopilotRaise(t *testing.T) {
	got := -1
	d := newEnvelopeDialog(parkedAutopilotFeature(), func(to int) tea.Cmd { got = to; return nil })

	typeInto(d, "4000")
	closed, _ := d.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if got != 4000 {
		t.Fatalf("onSubmit got %d, want 4000 — the raise must fire regardless of the resume answer", got)
	}
	if closed {
		t.Fatal("dialog closed immediately instead of asking whether to resume")
	}
	if !d.askResume {
		t.Fatal("d.askResume = false, want true after raising a parked autopilot card's envelope")
	}
	if !strings.Contains(ansi.Strip(d.View(m0Styles(), 60, 20)), "resume FD-047 on autopilot?") {
		t.Errorf("resume view = %q, missing the expected question", ansi.Strip(d.View(m0Styles(), 60, 20)))
	}
}

// TestEnvelopeDialogNoResumeOffGateApproval: a card driven entirely by
// hand (GateOff) never gets the resume question — there is no
// autopilot loop to hand it back to.
func TestEnvelopeDialogNoResumeOffGateApproval(t *testing.T) {
	f := parkedAutopilotFeature()
	f.GateApproval = domain.GateOff
	d := newEnvelopeDialog(f, func(int) tea.Cmd { return nil })
	typeInto(d, "4000")
	d.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if d.askResume {
		t.Fatal("askResume = true on a GateOff card, want false")
	}
}

// TestEnvelopeDialogNoResumeWhenNotExhausted: raising the envelope on an
// autopilot card that was not actually out of budget (still under its
// old envelope) is an ordinary proactive top-up, not a parked-card
// pickup — no resume question.
func TestEnvelopeDialogNoResumeWhenNotExhausted(t *testing.T) {
	f := parkedAutopilotFeature()
	f.Spend.Credits = 100 // nowhere near the 2400 envelope
	d := newEnvelopeDialog(f, func(int) tea.Cmd { return nil })
	typeInto(d, "4000")
	d.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if d.askResume {
		t.Fatal("askResume = true on a card that was not exhausted, want false")
	}
}

// TestEnvelopeDialogResumeAnswerYes: saying yes to the resume question
// closes the dialog and fires a notice — this file has no reach into
// live session state to actually restart the run, so it says so rather
// than pretending the run resumed.
func TestEnvelopeDialogResumeAnswerYes(t *testing.T) {
	d := newEnvelopeDialog(parkedAutopilotFeature(), func(int) tea.Cmd { return nil })
	typeInto(d, "4000")
	d.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !d.askResume {
		t.Fatal("setup: expected the resume question to be showing")
	}

	closed, cmd := d.HandleKey(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if !closed {
		t.Fatal("'y' on the resume question should close the dialog")
	}
	if cmd == nil {
		t.Fatal("'y' on the resume question should fire a notice command")
	}
	msg, ok := cmd().(noticeMsg)
	if !ok || !strings.Contains(msg.text, "FD-047") {
		t.Fatalf("resume notice = %+v, want it to name the card", msg)
	}
}

// TestEnvelopeDialogResumeAnswerNo: saying no just closes the dialog —
// the envelope stays raised (already set), nothing else happens.
func TestEnvelopeDialogResumeAnswerNo(t *testing.T) {
	d := newEnvelopeDialog(parkedAutopilotFeature(), func(int) tea.Cmd { return nil })
	typeInto(d, "4000")
	d.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})

	closed, cmd := d.HandleKey(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if !closed || cmd != nil {
		t.Fatalf("'n' on the resume question: closed=%v cmd=%v, want true/nil", closed, cmd)
	}
}
