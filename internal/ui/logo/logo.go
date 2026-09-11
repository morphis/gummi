// Package logo renders the gummi wordmark: chunky half-block
// letterforms with a berry→lemon gradient. The splash is the first
// thing a user sees — it carries the visual identity on its own.
package logo

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/ui/theme"
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

// tagline is the one sentence under the wordmark.
//
// It used to read "a meta-harness for coding agents", which is what the
// program is to the people who build it and nothing at all to the person
// reading it for the first time: "meta-harness" is not a word, and
// nothing in the sentence says what happens if you keep going. This
// screen is shown on an EMPTY board — the reader has no cards and no
// context — so the sentence has to earn the next keystroke by naming the
// work, in the same three stage words the stage strip will show them
// thirty seconds later. It is kept under 80 columns so the narrowest
// terminal anyone drives this on still gets the sentence.
const tagline = "drives coding agents through plan, implement and verify — one card at a time"

// firstStep is the empty board's call to action.
//
// The splash is the whole screen when there are no cards, and it used to
// carry no instruction of any kind: a wordmark, a phrase and a version
// string, with the only way forward mentioned in a status bar hint at
// the bottom of the frame. Naming the key here, next to what the key is
// for, is the difference between a landing page and a first step.
const firstStep = "press n to describe the first thing you want built"

// Splash renders the full first-run screen block: wordmark, tagline,
// first step, and version, horizontally centered in width and vertically
// centered in height.
//
// The two prose lines are dropped rather than wrapped when the frame is
// too narrow to hold them — a half-sentence broken across a centered
// block reads worse than the wordmark alone, and the status bar carries
// the same "n new" hint regardless.
func Splash(s *theme.Styles, version string, width, height int) string {
	var b strings.Builder
	b.WriteString(Render(s, width))
	if ansi.StringWidth(tagline) <= width {
		b.WriteString("\n\n")
		b.WriteString(s.Subtle.Render(tagline))
	}
	if ansi.StringWidth(firstStep) <= width {
		b.WriteString("\n\n")
		b.WriteString(s.Base.Render(firstStep))
	}
	b.WriteString("\n")
	b.WriteString(s.Faint.Render(version))
	block := b.String()
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, block)
}
