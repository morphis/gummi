package ui

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
)

// TestBG076NoTranscriptRowOnTheCardPage is BG-076's regression test. The
// action inventory is raised from the card page, and it carried a
// "transcript" row promising "the session transcript (tool calls and
// their outputs)" — a separate view that no longer exists. Its key runs
// openThread, which on the card page opens the page the reader is
// already on: a row that describes something gone and does nothing,
// spending a line of the one list whose job is to say what can be done.
func TestBG076NoTranscriptRowOnTheCardPage(t *testing.T) {
	in := nextInput{
		stage: domain.StageImplement, kind: domain.KindFeature,
		sess: engine.StateDone,
	}
	row := featureRow{F: domain.Feature{ID: "FD-001", Kind: domain.KindFeature, Stage: domain.StageImplement}}

	// from the board the row is real: t opens the card's thread.
	board := cardActionsFor(in, row)
	var fromBoard *cardAction
	for i, a := range board {
		if a.id == "transcript" {
			fromBoard = &board[i]
		}
	}
	if fromBoard == nil {
		t.Fatal("the board lost its way into a card's thread")
	}
	if strings.Contains(fromBoard.why, "transcript (tool calls") {
		t.Errorf("the row still describes the removed transcript view: %q", fromBoard.why)
	}

	// on the card page it would only re-open the surface under the cursor
	in.cardOpen = true
	for _, a := range cardActionsFor(in, row) {
		if a.id == "transcript" {
			t.Errorf("the card page still offers %q — it re-opens the page the reader is on", a.label)
		}
	}
}
