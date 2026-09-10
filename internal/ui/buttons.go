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

// Move shifts focus by delta, clamping at the ends. It used to wrap — the
// reasoning was that a two-item row cycles rather than clamping, so ←/→
// always lands on a button — but clamping lands on a button just as
// reliably, and wrapping made the row's most tempting gesture dangerous:
// on a two-item confirm/cancel row with the confirm focused (the common
// case — see newAutopilotDialog), pressing → once is the obvious "move to
// the affirmative button" reflex, and wrapping sends it past the far end
// and back onto Cancel instead. Clamping means an extra press in a
// direction that has nowhere left to go is a no-op, never a surprise
// trip to the other side of the row.
func (r *buttonRow) Move(delta int) {
	r.SetCursor(r.cursor + delta)
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

// View renders "  [ Label ]  ▸ [ Label ]". The focused button carries a
// leading ▸ in the two-column margin every button reserves, and is also
// *filled* — accent ink for an ordinary one, the destructive color for a
// danger one — while every other button stays an unfilled legend: faint
// for plain, destructive-tinted for danger.
//
// The ▸ exists because the fill alone was not enough: driving a real
// dialog over a pty, the confirm button read bold-white-on-blue and
// Cancel read dim, and that contrast was the *only* cue — a capture with
// no color (or a reader who can't rely on one) saw two identical-looking
// buttons and a stray keypress could cancel a confirmation that looked
// like it would confirm. The marker always occupies the same two columns
// whether or not it is drawn, so which button is focused never changes
// the row's width.
//
// The fill is still the point for color-capable terminals. Focus used to
// be a hue swap (s.Destructive → s.Error on the danger button), which
// said nothing: the two reds read as the same red on the dark theme, and
// on the light theme they are literally the same color, so the most
// consequential button in the app — merge, delete — looked identical
// whether or not enter would fire it. A fill is a shape change, so it
// survives both a red-on-red palette and a colorblind reader.
//
// focused reports whether the row itself holds input focus; false leaves
// every button unfilled (e.g. while a sibling text input has it), so a
// filled, marked button always means "enter presses this".
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
		style := s.Button
		switch {
		case on && b.danger:
			style = s.ButtonDangerFocus
		case on:
			style = s.ButtonFocus
		case b.danger:
			style = s.ButtonDanger
		}
		marker := "  "
		if on {
			marker = "▸ "
		}
		parts[i] = marker + style.Render("[ "+label+" ]")
	}
	return strings.Join(parts, "  ")
}
