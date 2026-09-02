package ui

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/engine"
)

// TestBG027FooterLabelMatchesBadgedPopulation locks in that the footer's
// second lane count never claims the word "autopilot" for a population
// the card line refuses to badge as such. lanePoolFor (internal/engine)
// deliberately pools every card whose GateApproval isn't GateOff —
// including the empty default every TUI-created card stores — but the
// card line's own badge (this file's row rendering, above) lights up
// only for the explicit domain.GateGates value. Before this fix the
// footer reused "autopilot" for that wider pool, so a user reading
// "autopilot 2/2" could never find those two cards badged on the board.
func TestBG027FooterLabelMatchesBadgedPopulation(t *testing.T) {
	got := laneCountsText(engine.LaneCounts{
		AttendedRunning: 1, AttendedMax: 1,
		AutopilotRunning: 2, AutopilotMax: 2,
	})
	if strings.Contains(got, "autopilot") {
		t.Fatalf("BG-027: footer text %q still says \"autopilot\" for a pool that includes cards the board never badges as autopilot", got)
	}
}
