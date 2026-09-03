package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestBG051_ClearingComposerWithdrawsWordAim is the regression test for
// BG-051: wordAim moves decisionCursor onto the word-consuming option while
// the composer holds prose, but syncDecision had no branch withdrawing that
// aim once the composer emptied again, so the cursor stuck on the aimed
// option instead of returning to the position it held before the aim took
// over.
func TestBG051_ClearingComposerWithdrawsWordAim(t *testing.T) {
	m := reviewGateWorkspace(t)

	m = typeString(t, m, "the contrast is off in dark mode")
	_ = m.threadView(100, 30) // decisionCursor syncs lazily, on render
	if m.decisionCursor != 1 {
		t.Fatalf("precondition: typing did not aim the cursor, got %d", m.decisionCursor)
	}

	m.threadInput.Focus()
	m.handleThreadInputKey(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}) // ctrl+u clears the composer
	if m.threadInput.Value() != "" {
		t.Fatalf("precondition: ctrl+u should have emptied the composer, got %q", m.threadInput.Value())
	}
	_ = m.threadView(100, 30)

	if m.decisionCursor != 0 {
		t.Fatalf("BG-051: clearing the composer left decisionCursor at %d, want 0 (aim not withdrawn)", m.decisionCursor)
	}
}
