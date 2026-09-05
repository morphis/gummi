package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestThreadInputKeystrokeCommandIsSubscription: the composer textarea
// restarts its cursor's blink timer on every keystroke that moves the
// cursor (bubbles/v2/textarea, on a cursor move), and that timer's command
// is exactly the kind flow_test.go's pump is built to skip — a re-arming
// command with no finite end. Left untagged, pump ran it to completion for
// real (bubbles' default blink speed, 530ms) on every character typed into
// this composer, and a decision test typing one ordinary sentence took 17s
// instead of milliseconds; multiplied across the suite's composer-typing
// tests that is what made a full internal/ui run blow past any timeout
// meant for it.
//
// This checks the seam directly rather than typing a long string and
// timing it: a live keystroke's command must be nil or already registered
// as a subscription, which is true immediately (no 530ms wait needed) and
// was false before updateThreadInput tagged it.
func TestThreadInputKeystrokeCommandIsSubscription(t *testing.T) {
	m := attachedBoard(t, 120, 34)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // open the card page
	if !m.threadInput.Focused() {
		t.Fatal("composer is not focused — this test needs a live keystroke into it")
	}

	model, cmd := m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	m = model.(*Shell)
	if m.threadInput.Value() != "x" {
		t.Fatalf("composer = %q, want the typed key to land", m.threadInput.Value())
	}
	if cmd != nil && !isSubscription(cmd) {
		t.Fatal("a composer keystroke returned a command flow_test.go's pump will run to completion " +
			"instead of treating as a subscription — this is the seam the 530ms-per-keystroke hang came from")
	}
}

// TestChipDefaultKeyCommandIsSubscription covers the fourth call site:
