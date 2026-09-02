package ui

import (
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui/theme"
)

// TestSpinnerLifecycle drives the shared spinner clock through a full
// cycle: idle (no loop), activity appears (one loop starts, frames
// advance), activity ends (the next tick winds the loop down).
func TestSpinnerLifecycle(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0-test")

	// idle: an ordinary message must not start the clock
	_, cmd := m.Update(noticeMsg{text: "hi"})
	if m.spinning || cmd != nil {
		t.Fatal("spinner started with nothing animating")
	}

	// activity appears (an ingest pass is decomposing): the next update
	// starts exactly one tick loop
	m.ingestRun = newIngestRunView("spec.md")
	_, cmd = m.Update(noticeMsg{text: "ingesting"})
	if !m.spinning || cmd == nil {
		t.Fatal("spinner did not start when activity appeared")
	}
	_, cmd = m.Update(noticeMsg{text: "again"})
	if cmd != nil {
		t.Fatal("a second tick loop was started while one is live")
	}

	// ticks advance the frame and keep the loop alive
	before := m.spinner()
	_, cmd = m.Update(spinnerTickMsg{})
	if cmd == nil {
		t.Fatal("tick did not reschedule while active")
	}
	if m.spinner() == before {
		t.Error("tick did not advance the frame")
	}

	// activity ends: the next tick stops the loop without advancing
	m.ingestRun = nil
	frame := m.frame
	_, cmd = m.Update(spinnerTickMsg{})
	if m.spinning || cmd != nil {
		t.Fatal("tick loop kept running after activity stopped")
	}
	if m.frame != frame {
		t.Error("winding-down tick advanced the frame")
	}

	// activity returns: the clock restarts
	m.ingestRun = newIngestRunView("spec.md")
	_, cmd = m.Update(noticeMsg{text: "back"})
	if !m.spinning || cmd == nil {
		t.Fatal("spinner did not restart on new activity")
	}
}

// TestSpinnerActiveScribing covers the scribe-count OR arm: a card with
// no session and no baseline still keeps the shared tick loop alive
// while a one-shot scribe pass is in flight against it, and the loop
// goes quiet again once the map empties out.
func TestSpinnerActiveScribing(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0-test")
	if m.spinnerActive() {
		t.Fatal("spinnerActive true with nothing in flight")
	}

	m.scribing["FD-001"] = 1
	if !m.spinnerActive() {
		t.Error("spinnerActive false with a scribe pass in flight")
	}

	m.scribing["FD-001"] = 2
	if !m.spinnerActive() {
		t.Error("spinnerActive false with both scribe passes in flight")
	}

	delete(m.scribing, "FD-001")
	if m.spinnerActive() {
		t.Error("spinnerActive true after the scribe count settled")
	}
}

// TestSpinnerActiveForeignRow: a foreign-driven, busy row is the one
// source of activity spinnerActive can only read from m.rows — no local
// engine session covers another process's session — so without its own
// OR arm a board whose only busy thing is a foreign card would never
// start the frame-advance loop at all.
func TestSpinnerActiveForeignRow(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0-test")
	r := featureRow{F: domain.Feature{ID: "FD-001"}, DrivenAbroad: true, Foreign: state.ForeignDrive{Busy: true}}
	m.rows = []featureRow{r}

	if !m.spinnerActive() {
		t.Error("spinnerActive false with a driven-abroad, busy row and no other activity")
	}

	m.rows[0].Foreign.Busy = false
	if m.spinnerActive() {
		t.Error("spinnerActive true for a driven-abroad row that is not busy")
	}
}
