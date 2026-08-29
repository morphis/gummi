package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/ui/theme"
)

// mdSource styles markdown source *in place*: every byte of the input
// survives into the output, only wearing a color.
//
// That constraint is the whole point. The spec surface addresses
// annotations by source line number (spec.AddComment takes a line), so
// the view a user puts a cursor on has to be the source — which is why
// the surface used to carry a second, glamour-rendered read mode and a
// key to toggle between them. Glamour re-wraps and re-flows, so there is
// no mapping back from a rendered row to the line it came from; the two
// could never be one view.
//
// Styling without reflowing gets most of what the rendered mode was for
// — headings that read as headings, code that reads as verbatim — while
// staying line-for-line the file on disk. What it deliberately does not
// do is re-wrap prose or lay out tables: those move text between lines,
// and a spec's line numbers are load-bearing here.
type mdSource struct {
	// fence is the delimiter run that opened the current fenced block
	// ("```" or "~~~"), empty outside one. Held as the exact opener
	// because a ``` inside a ~~~ block is content, not a close.
	fence string
}

// fenceDelim returns the fence delimiter a line opens or closes with,
// or "" when the line is not a fence. CommonMark allows three or more
// backticks or tildes, optionally indented up to three spaces.
func fenceDelim(raw string) string {
	t := strings.TrimLeft(raw, " ")
	if len(raw)-len(t) > 3 {
		return ""
	}
	for _, d := range []string{"```", "~~~"} {
		if strings.HasPrefix(t, d) {
			return d
		}
	}
	return ""
}

// line styles one source line, advancing the fence state. It returns the
// line with the same characters it went in with.
func (ms *mdSource) line(s *theme.Styles, raw string) string {
	if d := fenceDelim(raw); d != "" && (ms.fence == "" || ms.fence == d) {
		// the opener carries an info string ("```gummi-checks"), the
		// closer is bare; both are chrome rather than content.
		if ms.fence == "" {
			ms.fence = d
		} else {
			ms.fence = ""
		}
		return s.Faint.Render(raw)
	}
	if ms.fence != "" {
		// verbatim: no inline spans, since backticks and asterisks inside
		// a code block are code, not markup.
		return s.Subtle.Render(raw)
	}

	trimmed := strings.TrimLeft(raw, " ")
	switch {
	case isSetextRule(trimmed):
		return s.Separator.Render(raw)
	case strings.HasPrefix(trimmed, "#"):
		if n := headingLevel(trimmed); n > 0 {
			// the outline is what a reader skims for, so the top two
			// levels get the loud style and the rest a quieter bold.
			if n <= 2 {
				return s.Title.Render(raw)
			}
			return s.Subtitle.Render(raw)
		}
	case strings.HasPrefix(trimmed, ">"):
		return s.Faint.Render(raw)
	}
	return styleInline(s, raw, s.Base)
}

// headingLevel counts the leading #s of an ATX heading, or 0 when the
// run is not one (seven #s, or no space after the run).
func headingLevel(trimmed string) int {
	n := 0
	for n < len(trimmed) && trimmed[n] == '#' {
		n++
	}
	if n == 0 || n > 6 {
		return 0
	}
	if n == len(trimmed) || trimmed[n] == ' ' {
		return n
	}
	return 0
}

// isSetextRule reports whether the line is a thematic break — three or
// more of -, * or _ and nothing else.
func isSetextRule(trimmed string) bool {
	t := strings.TrimRight(trimmed, " ")
	if len(t) < 3 {
		return false
	}
	c := t[0]
	if c != '-' && c != '*' && c != '_' {
		return false
	}
	return strings.Count(t, string(c)) == len(t)
}

// styleInline renders one prose line, giving `code` spans and **bold**
// spans their own style and everything else base. A span whose closing
// delimiter is missing on this line is left as plain text rather than
// styled to end of line — an unbalanced backtick is far more often a
// typo than a span, and swallowing the rest of the line would hide it.
func styleInline(s *theme.Styles, raw string, base lipgloss.Style) string {
	var b strings.Builder
	var plain strings.Builder
	flush := func() {
		if plain.Len() > 0 {
			b.WriteString(base.Render(plain.String()))
			plain.Reset()
		}
	}
	for i := 0; i < len(raw); {
		if raw[i] == '`' {
			if j := strings.IndexByte(raw[i+1:], '`'); j >= 0 {
				flush()
				end := i + 1 + j + 1
				b.WriteString(s.Info.Render(raw[i:end]))
				i = end
				continue
			}
		}
		if strings.HasPrefix(raw[i:], "**") {
			if j := strings.Index(raw[i+2:], "**"); j >= 0 {
				flush()
				end := i + 2 + j + 2
				b.WriteString(s.Subtitle.Render(raw[i:end]))
				i = end
				continue
			}
		}
		plain.WriteByte(raw[i])
		i++
	}
	flush()
	return b.String()
}

// wrapStyled wraps an already-styled line to w columns. Styling has to
// happen before wrapping, since an inline span can straddle the break;
// ansi.Wrap is sequence-aware and re-opens the style on the continuation
// row, which is exactly why the styling is not deferred to the segments.
func wrapStyled(styled string, w int) []string {
	return strings.Split(ansi.Wrap(styled, max(w, 4), ""), "\n")
}
