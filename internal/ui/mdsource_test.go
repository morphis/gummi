package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/ui/theme"
)

// mdLines is the sample document the tests below walk, covering every
// construct mdSource claims to know about plus the two it must leave
// alone (fenced content, an unbalanced delimiter).
var mdLines = []string{
	"# Dark mode",
	"",
	"## Problem",
	"The toggle uses `localStorage` and **must** survive a reload.",
	"",
	"> Depends on: FD-002",
	"",
	"### Verification plan",
	"```gummi-checks",
	"- name: unit",
	"  cmd: go test ./...   # ** not bold ** here",
	"```",
	"---",
	"- a list item with `code`",
	"unbalanced ` backtick stays plain",
	"####### seven hashes is not a heading",
	"#nospace is not a heading either",
}

// TestMdSourceKeepsEveryCharacter is the invariant the whole surface
// rests on: the spec view addresses annotations by source line, so the
// styler may add color and nothing else. A styler that trimmed, re-
// wrapped, or dropped a delimiter would silently move every comment
// anchor below it.
func TestMdSourceKeepsEveryCharacter(t *testing.T) {
	s := theme.New(theme.GummiDark())
	var md mdSource
	for _, raw := range mdLines {
		got := ansi.Strip(md.line(s, raw))
		if got != raw {
			t.Errorf("styling changed the text:\n  in  %q\n  out %q", raw, got)
		}
	}
}

// TestMdSourceFencedContentIsVerbatim: inside a fenced block, backticks
// and asterisks are code, not markup. The `**` pair on the cmd line must
// come out unstyled, and the heading-looking line after it too.
func TestMdSourceFencedContentIsVerbatim(t *testing.T) {
	s := theme.New(theme.GummiDark())
	var md mdSource
	styled := map[string]string{}
	for _, raw := range mdLines {
		styled[raw] = md.line(s, raw)
	}
	fenced := styled["  cmd: go test ./...   # ** not bold ** here"]
	if strings.Count(fenced, "\x1b[") > 2 {
		t.Errorf("fenced line carries inline styling: %q", fenced)
	}
	// the same text outside a fence does get the bold span, which is what
	// makes the check above meaningful rather than vacuous.
	var fresh mdSource
	loose := fresh.line(s, "  cmd: go test ./...   # ** not bold ** here")
	if strings.Count(loose, "\x1b[") <= 2 {
		t.Errorf("outside a fence the ** span should be styled: %q", loose)
	}
}

// TestMdSourceLeavesUnbalancedDelimitersPlain: an unmatched backtick is
// far more often a typo than a span, and styling to end of line would
// hide it.
func TestMdSourceLeavesUnbalancedDelimitersPlain(t *testing.T) {
	s := theme.New(theme.GummiDark())
	var md mdSource
	got := md.line(s, "unbalanced ` backtick stays plain")
	if strings.Count(got, "\x1b[") > 2 {
		t.Errorf("unbalanced backtick opened a span: %q", got)
	}
}

// TestMdSourceHeadingLevels: only one to six #s followed by a space (or
// nothing) is a heading.
func TestMdSourceHeadingLevels(t *testing.T) {
	for _, tc := range []struct {
		line string
		want int
	}{
		{"# one", 1},
		{"###### six", 6},
		{"####### seven", 0},
		{"#nospace", 0},
		{"#", 1},
		{"not a heading", 0},
	} {
		if got := headingLevel(tc.line); got != tc.want {
			t.Errorf("headingLevel(%q) = %d, want %d", tc.line, got, tc.want)
		}
	}
}

// TestWrapStyledStaysInsideThePane: styling happens before wrapping so a
// span can straddle the break, which only works if ansi.Wrap measures
// printable width rather than bytes.
func TestWrapStyledStaysInsideThePane(t *testing.T) {
	s := theme.New(theme.GummiDark())
	var md mdSource
	long := "The toggle uses `localStorage` and **must** survive a reload, " +
		"which is a long enough sentence to wrap several times at forty columns."
	const w = 40
	rows := wrapStyled(md.line(s, long), w)
	if len(rows) < 3 {
		t.Fatalf("expected the line to wrap, got %d rows", len(rows))
	}
	for _, row := range rows {
		if got := ansi.StringWidth(row); got > w {
			t.Errorf("row wider than the pane (%d > %d): %q", got, w, row)
		}
	}
	if got := ansi.Strip(strings.Join(rows, " ")); got != long {
		t.Errorf("wrapping lost text:\n  in  %q\n  out %q", long, got)
	}
}
