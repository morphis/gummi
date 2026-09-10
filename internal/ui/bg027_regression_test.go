package ui

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/engine"
)

// TestBG027FooterLabelMatchesBadgedPopulation locks in that the footer's
// second lane count and the board's own ⚡ badge describe the same
// population.
//
// BG-027 was the two disagreeing: lanePoolFor used to pool every card
// whose GateApproval was not GateAttended — the empty default every
// TUI-created card stores included — while the card line badged only the
// explicit autopilot value, so "autopilot 2/2" counted two cards a
// reader could not find badged anywhere on the board. The fix at the
// time was to stop the footer saying "autopilot" at all; it said
// "unattended" instead, which was accurate about the pool and meaningless
// to a reader, since no other surface used that word.
//
// The pool itself was corrected since (lanePoolForMode: only
// GateAutopilot is autopilot), so the word is now the honest one, and the
// footer says it again. What must be true is not "never say autopilot" —
// that was a workaround — but that the word and the badge agree.
func TestBG027FooterLabelMatchesBadgedPopulation(t *testing.T) {
	got := laneCountsText(engine.LaneCounts{
		AttendedRunning: 1, AttendedMax: 1,
		AutopilotRunning: 2, AutopilotMax: 2,
	})
	if !strings.Contains(got, "autopilot 2/2") {
		t.Fatalf("footer text %q does not name the autopilot pool the board badges", got)
	}
	if strings.Contains(got, "unattended") {
		t.Fatalf("footer text %q still uses \"unattended\", a word no other surface says", got)
	}
}
