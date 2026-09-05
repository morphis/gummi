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
// Driven over every mode rather than the one the drive typed:
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

	// two modes, and every value resolves to one of them (empty is what a
	// card written before the field existed carries; it reads as
	// attended). Each must be confirmed in its OWN sentence — two modes
	// sharing one wording is the defect this guards.
	seen := map[string]string{}
	for _, mode := range []string{domain.GateAttended, domain.GateAutopilot, ""} {
		msg := m.setGateApproval(f.ID, mode)()
		nm, ok := msg.(noticeMsg)
		if !ok || nm.isErr {
			t.Fatalf("%q: setting the stop failed: %#v", mode, msg)
		}
		want := autopilotModeFor(mode)
		if !strings.Contains(nm.text, want) {
			t.Errorf("%q: notice %q never names the mode %q", mode, nm.text, want)
		}
		other := domain.GateAutopilot
		if want == domain.GateAutopilot {
			other = domain.GateAttended
		}
		if strings.Contains(nm.text, other) {
			t.Errorf("%q: notice %q names %q as well", mode, nm.text, other)
		}
		if prev, dup := seen[nm.text]; dup && prev != want {
			t.Errorf("modes %q and %q are confirmed with the identical sentence %q", prev, want, nm.text)
		}
		seen[nm.text] = want
	}
}
