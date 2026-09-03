package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// TestBG073StatusBarDoesNotAdvertiseTheSurfaceUnderAModal is BG-073's
// regression test. The status bar's job is to say what the next
// keystroke does, and while a dialog is open the next keystroke goes to
// the dialog — but the bar kept rendering the table of the surface
// underneath. Landing a card put "enter land on main" in the bar over a
// merge dialog whose enter activates the focused button, and "esc
// backlog" over one whose esc cancels the merge.
func TestBG073StatusBarDoesNotAdvertiseTheSurfaceUnderAModal(t *testing.T) {
	m := attachedBoard(t, 140, 40)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // open the card page
	if !m.cardOpen {
		t.Fatal("precondition: the card page did not open")
	}
	under := ansi.Strip(m.statusView(140))
	if !strings.Contains(under, "esc") {
		t.Fatalf("precondition: the card page's bar has no hints at all: %q", under)
	}

	// the action inventory is the card page's own modal, and the shortest
	// one to raise from here — every dialog behaves the same way from the
	// bar's point of view.
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	if !m.Overlay.HasDialogs() {
		t.Fatal("precondition: ↑ did not raise the action inventory")
	}

	bar := ansi.Strip(m.statusView(140))
	for _, leaked := range []string{"backlog", "choose", "scroll", "prev/next"} {
		if strings.Contains(bar, leaked) {
			t.Errorf("the bar still advertises the surface under the modal (%q): %q", leaked, bar)
		}
	}
	if !strings.Contains(bar, "esc") {
		t.Errorf("the bar dropped the one key every dialog answers to: %q", bar)
	}
}
