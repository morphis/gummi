package theme

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Band paints an already-rendered row as a selection band w columns wide:
// the whole row is highlighted, not just a marker glyph in front of it.
//
// It cannot be a plain lipgloss background. A row is assembled from a
// dozen nested styles — id, badges, title, tags — and every one of them
// ends in an SGR reset, so wrapping the finished row would tint the first
// segment and drop the band at the first reset. Band re-asserts the
// background after each reset it finds, then pads the row to w so the
// band reads as one continuous bar instead of ragged text. This is the
// one place in the package that assembles SGR by hand, and it does it
// through x/ansi rather than at byte level (DESIGN §6.2).
//
// focused picks the accent-tinted band — the pane that owns the arrow
// keys — over the quiet grey one worn by a pane whose selection is only
// remembered. The board's two focus regions are told apart by exactly
// this: both keep a selected row, only one of them wears the bright band.
//
// w <= 0 pads nothing, highlighting the row's own text and no further:
// dialog field rows sit in a box whose width they don't own.
func (s *Styles) Band(line string, w int, focused bool) string {
	bg := s.bandDim
	if focused {
		bg = s.band
	}
	if bg == nil {
		return line
	}
	open := ansi.Style{}.BackgroundColor(bg).String()
	if w > 0 {
		line = ansi.Truncate(line, w, "…")
		if pad := w - ansi.StringWidth(line); pad > 0 {
			line += strings.Repeat(" ", pad)
		}
	}
	// lipgloss emits the short reset; a hand-written or third-party
	// segment may use the long form, so normalize before re-asserting.
	line = strings.ReplaceAll(line, "\x1b[0m", ansi.ResetStyle)
	line = strings.ReplaceAll(line, ansi.ResetStyle, ansi.ResetStyle+open)
	return open + line + ansi.ResetStyle
}

// BandMarker is the ▸ marker to put in front of a banded row: bright and
// bold on a focused band, quietly legible on an unfocused one. Callers
// pair it with Band so the two always agree about which pane has focus.
func (s *Styles) BandMarker(focused bool) string {
	if focused {
		return s.SelMarker.Render("▸ ")
	}
	return s.SelMarkerDim.Render("▸ ")
}
