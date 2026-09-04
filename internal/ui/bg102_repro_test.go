package ui

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/ui/theme"
)

// TestBG102NoticeNamesTheStopThatWasSet is BG-102's regression test. The
// only confirmation the board gives after the switch is its notice, and
// it still spoke the two-state vocabulary the three stops replaced: one
// stop was recognised and the other two shared a sentence describing the
// first of them. So choosing full — the stop that runs a card to a
// verified branch on its own — was confirmed with the words for off.
//
// Driven over autopilotStops rather than the one mode the drive typed:
// the defect was a switch that had fallen behind the list it switches
// on, and the assertion that catches that is one every entry has to
// pass. The empty mode is included because that is what a card written
// before the field existed carries, and it reads as gates everywhere
// else the field is interpreted.
func TestBG102NoticeNamesTheStopThatWasSet(t *testing.T) {
	ws, store, wt := uiRepo(t)
	m := NewShell(theme.GummiDark(), "v0-test")
	m.Attach(store, wt, ws)
	f := mkFeature(t, store, 1, "rate limits", domain.StagePlan)

	seen := map[string]string{}
	for _, mode := range []string{domain.GateOff, domain.GateGates, domain.GateFull, ""} {
		msg := m.setGateApproval(f.ID, mode)()
		nm, ok := msg.(noticeMsg)
		if !ok || nm.isErr {
			t.Fatalf("%q: setting the stop failed: %#v", mode, msg)
		}
		want := autopilotStops[autopilotCursorFor(mode)]
		if !strings.Contains(nm.text, want.label) {
			t.Errorf("%q: notice %q never names the stop %q", mode, nm.text, want.label)
		}
		// and it must not name a different one: "off" and "full" reading
		// alike is the whole defect.
		for _, other := range autopilotStops {
			if other.label == want.label {
				continue
			}
			if strings.Contains(nm.text, " "+other.label+" ") {
				t.Errorf("%q: notice %q names %q as well", mode, nm.text, other.label)
			}
		}
		if prev, dup := seen[nm.text]; dup && prev != want.label {
			t.Errorf("stops %q and %q are confirmed with the identical sentence %q", prev, want.label, nm.text)
		}
		seen[nm.text] = want.label
	}
}
