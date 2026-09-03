package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestBG049ReproMastheadDropsBudgetBeforeTitle(t *testing.T) {
	m := populatedShell(140, 40)
	f := &m.rows[m.sel].F
	f.Title = "lxc storage volume: --format json misses snapshot expiry"
	f.Budget.Envelope = 2400
	f.Spend.Credits = 36

	out := ansi.Strip(m.threadView(62, 40))
	if !strings.Contains(out, "credits") {
		t.Fatalf("62-col masthead = %q, want it to still contain the credit budget", strings.Split(out, "\n")[1])
	}
}

// TestBG049ReproMastheadHeightTrimDropsBudget is the height-axis mirror
// of the width-axis repro above. composeThread trims the head to a
// prefix when a frame is too short for all of it (thread.go's own
// comment on head ordering), so whatever the masthead puts on its
// first row is what survives a short terminal. A card with an open
// decision on a frame just short enough to leave room for exactly one
// head row must still show the credit budget on it — a masthead split
// across a title row and a separate badge row loses the badge row
// outright here, even though the badges alone fit the row's width with
// room to spare.
func TestBG049ReproMastheadHeightTrimDropsBudget(t *testing.T) {
	m := reviewGateWorkspace(t)
	m.rows[m.sel].F.Profile = "" // isolate the case from profile-tag shedding
	out := ansi.Strip(m.threadView(61, 7))
	if !strings.Contains(out, "credits") {
		t.Fatalf("61x7 masthead with an open decision = %q, want it to still contain the credit budget", strings.Split(out, "\n")[0])
	}
}
