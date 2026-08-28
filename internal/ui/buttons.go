package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/ui/theme"
)

// button is one labelled control in a dialog's button row.
type button struct {
	label  string
	danger bool // destructive: rendered in the destructive style
}

// buttonRow is a dialog's focusable row of buttons — the last tab stop.
// Every dialog now ends in one of these, so "enter activates the focused
// control" holds with no exceptions: confirming, submitting, or cancelling
// is always something the user can see highlighted, not something a key
// binding told them.
type buttonRow struct {
	buttons []button
	cursor  int
}

// newButtonRow builds a row focused on its first button.
func newButtonRow(buttons ...button) *buttonRow {
	return &buttonRow{buttons: buttons}
}

// Move shifts focus by delta, wrapping — a two-item row cycles rather than
// clamping, so ←/→ (or tab/shift+tab) always lands on a button.
func (r *buttonRow) Move(delta int) {
	n := len(r.buttons)
	if n == 0 {
		return
	}
	r.cursor = ((r.cursor+delta)%n + n) % n
}

// Cursor returns the focused button's index.
func (r *buttonRow) Cursor() int { return r.cursor }

// SetCursor moves focus directly to index i, clamped to the row.
func (r *buttonRow) SetCursor(i int) {
	if len(r.buttons) == 0 {
		return
	}
	switch {
	case i < 0:
		i = 0
	case i > len(r.buttons)-1:
		i = len(r.buttons) - 1
	}
	r.cursor = i
}

// Selected returns the focused button, or the zero button when the row is
// empty — a dialog built with no buttons should render nothing, not panic
// on the first keypress.
func (r *buttonRow) Selected() button {
	if r.cursor < 0 || r.cursor >= len(r.buttons) {
		return button{}
	}
	return r.buttons[r.cursor]
}

// View renders "[ Label ]  [ Label ]": the focused button highlighted
// (bracket in s.Cursor, label in s.Subtle — the same marker/text split
// form.go's options row uses), the rest s.Faint. A danger button inverts
// that: s.Destructive while unfocused, s.Error once it takes focus, so
// the destructive choice reads as alarmed only when it's actually about
// to fire. focused reports whether the row itself holds input focus
// (false dims every button, e.g. while a sibling text input has it).
func (r *buttonRow) View(s *theme.Styles, focused bool) string {
	width := 0
	for _, b := range r.buttons {
		width = max(width, ansi.StringWidth(b.label))
	}
	parts := make([]string, len(r.buttons))
	for i, b := range r.buttons {
		label := b.label
		if pad := width - ansi.StringWidth(label); pad > 0 {
			label += strings.Repeat(" ", pad)
		}
		on := focused && i == r.cursor
		switch {
		case on && b.danger:
			parts[i] = s.Error.Render("[ " + label + " ]")
		case on:
			parts[i] = s.Cursor.Render("[ ") + s.Subtle.Render(label) + s.Cursor.Render(" ]")
		case b.danger:
			parts[i] = s.Destructive.Render("[ " + label + " ]")
		default:
			parts[i] = s.Faint.Render("[ " + label + " ]")
		}
	}
	return strings.Join(parts, "  ")
}
