// Package logo renders the gummi wordmark: chunky half-block
// letterforms with a berry→lemon gradient. The splash is the first
// thing a user sees — it carries the visual identity on its own.
package logo

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphia/gummi/internal/ui/theme"
)

// letterforms are gummi's own 3-row half-block glyphs.
var letterforms = map[rune]string{
	'g': "▄▀▀▀▄\n█   █\n▀▀▀▀█\n▄▄▄▄▀",
	'u': "█   █\n█   █\n▀▄▄▄▀\n     ",
	'm': "▄▀▄▀▄\n█ █ █\n█ █ █\n     ",
	'i': "▀\n█\n█\n ",
}

const wordmarkRows = 4

// Wordmark renders the plain (uncolored) gummi wordmark.
func Wordmark() string {
	letters := make([]string, 0, 9)
	for i, r := range "gummi" {
		if i > 0 {
			letters = append(letters, " ")
		}
		letters = append(letters, letterforms[r])
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, letters...)
}

// Render renders the wordmark with the theme's brand gradient, one
// ramp per row, truncated to width.
func Render(s *theme.Styles, width int) string {
	rows := strings.Split(Wordmark(), "\n")
	for i, row := range rows {
		row = ansi.Truncate(row, width, "")
		rows[i] = theme.Grad(s.Base, row, s.Theme.Primary, s.Theme.Secondary)
	}
	return strings.Join(rows, "\n")
}

// Splash renders the full first-run screen block: wordmark, tagline,
// and version, horizontally centered in width and vertically centered
// in height.
func Splash(s *theme.Styles, version string, width, height int) string {
	var b strings.Builder
	b.WriteString(Render(s, width))
	b.WriteString("\n\n")
	b.WriteString(s.Subtle.Render("a meta-harness for coding agents"))
	b.WriteString("\n")
	b.WriteString(s.Faint.Render(version))
	block := b.String()
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, block)
}
