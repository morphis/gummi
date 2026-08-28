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

// View renders "[ Label ]  [ Label ]". The focused button is *filled* —
// accent ink for an ordinary one, the destructive color for a danger one
// — while every other button stays an unfilled legend: faint for plain,
// destructive-tinted for danger.
//
// The fill is the point. Focus used to be a hue swap (s.Destructive →
// s.Error on the danger button), which said nothing: the two reds read
// as the same red on the dark theme, and on the light theme they are
// literally the same color, so the most consequential button in the app
// — merge, delete — looked identical whether or not enter would fire it.
// A fill is a shape change, so it survives both a red-on-red palette and
// a colorblind reader.
//
// focused reports whether the row itself holds input focus; false leaves
// every button unfilled (e.g. while a sibling text input has it), so a
// filled button always means "enter presses this".
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
		style := s.Button
		switch on := focused && i == r.cursor; {
		case on && b.danger:
			style = s.ButtonDangerFocus
		case on:
			style = s.ButtonFocus
		case b.danger:
			style = s.ButtonDanger
		}
		parts[i] = style.Render("[ " + label + " ]")
	}
	return strings.Join(parts, "  ")
}
