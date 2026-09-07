package ui

import (
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
)

// TestBG076NoTranscriptRowOnTheCardPage is BG-076's regression test. The
// action inventory carried a "transcript" row promising "the session
// transcript (tool calls and their outputs)" — a separate view that no
// longer exists. Its key runs openThread, which on the card page opens
// the page the reader is already on: a row that describes something gone
// and does nothing.
//
// The row is gone outright now rather than only on the card page. The
// thread IS the transcript, so the row could never be anything but a
// second name for it, and reading surfaces are the card page's tabs
// rather than rows in the option list (cardtabs.go). The way in from the
// board is unaffected: t still opens the card's thread, which is a
// navigation key on the board's own table, not a card action.
func TestBG076NoTranscriptRowOnTheCardPage(t *testing.T) {
	in := nextInput{
		stage: domain.StageImplement, kind: domain.KindFeature,
		sess: engine.StateDone,
	}
	row := featureRow{F: domain.Feature{ID: "FD-001", Kind: domain.KindFeature, Stage: domain.StageImplement}}

	for _, cardOpen := range []bool{false, true} {
		in.cardOpen = cardOpen
		for _, a := range cardActionsFor(in, row) {
			if a.id == "transcript" {
				t.Errorf("cardOpen=%v: the inventory still offers %q — the thread is the transcript", cardOpen, a.label)
			}
		}
	}

	// the board's way into the thread is a navigation key and stays
	m := populatedShell(120, 34)
	var found bool
	for _, b := range m.boardBindings() {
		if b.key == "t" {
			found = true
		}
	}
	if !found {
		t.Error("the board lost its way into a card's thread")
	}
}
