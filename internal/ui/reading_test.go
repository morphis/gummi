package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/reentry"
)

// The read is a model call, and these tests are all about the seconds it
// takes. Every one of them drives Update directly rather than through
// press(): press pumps the command it gets back to completion, which is
// exactly the interval being tested away.

const separateWork = "the retry path should back off, but that is its own job"

// separateWorkStop is a card at the verify stop — where a sentence about
// work that is not this card's has somewhere else to go — with the page
// open, the composer focused and a classifier that says so.
func separateWorkStop(t *testing.T) *Shell {
	t.Helper()
	m := verifyCard(t, classifyingAgent("separate_card"))
	m = openCardPage(t, m)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
	m = model.(*Shell)
	_ = m.View()
	return m
}

// While the line is out being read the conversation says so, the stop's
// own answers stay exactly where they were, and the card is marked busy
// so every other surface animates for it too.
func TestReadInFlightIsInTheThread(t *testing.T) {
	m := separateWorkStop(t)
	m = typeString(t, m, separateWork)

	model, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = model.(*Shell)

	p := m.reentryRead
	if p == nil {
		t.Fatal("enter sent the line to be read and left no trace of it on the shell")
	}
	if p.line != separateWork {
		t.Errorf("the read carries %q, want the line that was typed", p.line)
	}
	if !m.cardBusy(m.rows[0]) {
		t.Error("a card with a read out is not marked busy — no spinner anywhere shows it")
	}
	if got := m.cardBusyWord(m.rows[0]); got != "reading" {
		t.Errorf("busy word = %q, want \"reading\"", got)
	}
	if !m.spinnerActive() {
		t.Error("nothing animates while a read is out, so the wait reads as a frozen UI")
	}
	view := m.View().Content
	if !strings.Contains(view, "reading your line") {
		t.Errorf("the page does not say a read is running:\n%s", view)
	}
	if !strings.Contains(view, "run verify") {
		t.Errorf("the picker went away under a read — its rows are still the answers to an unanswered question:\n%s", view)
	}

	// A second enter must not buy a second read.
	model, again := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = model.(*Shell)
	if m.reentryRead != p {
		t.Error("enter while a read was out started another one")
	}
	if again != nil {
		t.Error("enter while a read was out returned a command")
	}
	if !strings.Contains(m.notice.text, "still reading") {
		t.Errorf("enter while reading said nothing back: %+v", m.notice)
	}

	// and the bar says so rather than naming a destination enter no
	// longer has
	var barred bool
	for _, b := range m.threadInputBindings() {
		if b.key == "enter" && b.label == "reading…" {
			barred = true
		}
		if b.key == "esc" && b.label != "stop reading" {
			t.Errorf("esc reads %q while a line is out being read", b.label)
		}
	}
	if !barred {
		t.Error("the bar still claims enter commits something")
	}

	m = pump(t, m, cmd)
	if m.reentryRead != nil {
		t.Error("the read stayed on screen after its answer landed")
	}
	if len(m.scribing) != 0 {
		t.Errorf("the read leaked its busy count: %+v", m.scribing)
	}
	if p := m.reentryPending; p == nil || p.out.Action != reentry.NewCard {
		t.Fatalf("the answer did not become a chip: %+v", p)
	}
}

// esc during the read stops the read and nothing else: nothing has been
// proposed yet, so there is nothing to decline and nothing to send. The
// line stays where it was typed, the page stays open, and the answer that
// was already on its way raises nothing when it lands.
func TestEscWhileReadingStopsItAndKeepsTheLine(t *testing.T) {
	m := separateWorkStop(t)
	m = typeString(t, m, separateWork)

	model, classify := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = model.(*Shell)
	model, after := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = model.(*Shell)

	if m.reentryRead != nil {
		t.Error("esc left the read running")
	}
	if len(m.scribing) != 0 {
		t.Errorf("a cancelled read left the card busy: %+v", m.scribing)
	}
	if !m.cardOpen {
		t.Error("esc closed the page instead of stopping the read")
	}
	if got := m.threadInput.Value(); got != separateWork {
		t.Errorf("composer = %q, want the line still there — stopping a read sends nothing", got)
	}
	id := m.rows[0].F.ID
	if got := m.consultSending[id]; got != "" {
		t.Errorf("stopping a read sent the line to the consult session: %q", got)
	}
	if after != nil {
		t.Error("stopping a read returned a command")
	}
	if !strings.Contains(m.notice.text, "stopped reading") {
		t.Errorf("esc said nothing back: %+v", m.notice)
	}

	// the answer to the cancelled read arrives late and is dropped
	m = pump(t, m, classify)
	if m.reentryPending != nil {
		t.Errorf("a withdrawn read still raised a chip: %+v", m.reentryPending)
	}
}

// The chip's own esc is the other one, and it still sends: an act has
// been proposed, declining it still owes the line a destination, and
// getting it there is a model call the page shows too.
func TestEscOnTheChipSendsTheLineAndSaysSo(t *testing.T) {
	m := separateWorkStop(t)
	m = typeAndSend(t, m, separateWork)
	if m.reentryPending == nil {
		t.Fatal("no chip to decline")
	}
	model, sent := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = model.(*Shell)

	id := m.rows[0].F.ID
	if got := m.consultSending[id]; got != separateWork {
		t.Errorf("the line in flight to the consult session = %q, want the typed line", got)
	}
	if view := m.View().Content; !strings.Contains(view, "asking…") {
		t.Errorf("the line went somewhere the page does not show:\n%s", view)
	}
	m = pump(t, m, sent)
	if got := m.consultSending[id]; got != "" {
		t.Errorf("the asking marker outlived its send: %q", got)
	}
}

// Backing out of the new-card form backs out of the whole route: nothing
// is created, and the line is where the reader typed it.
func TestAbandoningTheSeededCardFormGivesTheLineBack(t *testing.T) {
	m := separateWorkStop(t)
	m = typeAndSend(t, m, separateWork)
	if p := m.reentryPending; p == nil || p.out.Action != reentry.NewCard {
		t.Fatalf("no new-card chip: %+v", p)
	}
	before := len(m.rows)

	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.Overlay.Contains("new-card") {
		t.Fatal("taking the chip did not open the new-card form")
	}
	if got := m.threadInput.Value(); got != "" {
		t.Errorf("the composer kept the line the form was seeded with: %q", got)
	}

	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.Overlay.HasDialogs() {
		t.Fatal("esc did not close the form")
	}
	if got := m.threadInput.Value(); got != separateWork {
		t.Errorf("composer = %q, want the line back — declining the card must not cost the reader what they wrote", got)
	}
	m = pump(t, m, m.loadRows)
	if len(m.rows) != before {
		t.Errorf("rows = %d, want %d — an abandoned form created a card", len(m.rows), before)
	}
}
