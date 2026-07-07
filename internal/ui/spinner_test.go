package ui

import (
	"testing"

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
