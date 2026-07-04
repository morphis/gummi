package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// sanitize strips terminal escape sequences and control characters from
// untrusted text (model output, provider error strings) before it is
// rendered. Neither lipgloss nor ultraviolet neutralizes embedded
// escapes, so raw model bytes could otherwise smuggle OSC 52 (clipboard
// write), title-spoofing, or cursor/screen sequences to the terminal.
// Only newline and tab survive; gummi's own styling is applied after
// this, so nothing legitimate is lost.
func sanitize(s string) string {
	s = ansi.Strip(s) // remove recognized escape sequences (CSI/OSC/…)
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || (r >= 0x7f && r < 0xa0) {
			return -1 // drop remaining C0/C1 control runes
		}
		return r
	}, s)
}
