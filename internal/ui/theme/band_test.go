package theme

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestBandSurvivesNestedResets is the reason Band exists: a row is built
// from nested styles, each ending in an SGR reset, so a plain lipgloss
// background would stop at the first one and highlight only the first
// segment. Every reset in the banded row must be followed by the band's
// background again.
func TestBandSurvivesNestedResets(t *testing.T) {
	s := New(GummiDark())
	row := s.CardID.Render("FD-042") + " " + s.CardTitle.Render("dark mode") + " " + s.Faint.Render("[thrifty]")
	if strings.Count(row, ansi.ResetStyle) < 3 {
		t.Fatalf("fixture should carry several nested resets: %q", row)
	}

	banded := s.Band(row, 40, true)
	bg := ansi.Style{}.BackgroundColor(s.band).String()
	if !strings.HasPrefix(banded, bg) {
		t.Fatalf("banded row does not open with the band background: %q", banded)
	}
	// every reset except the final one re-opens the band.
	inner := strings.TrimSuffix(banded, ansi.ResetStyle)
	for _, seg := range strings.Split(inner, ansi.ResetStyle)[1:] {
		if !strings.HasPrefix(seg, bg) {
			t.Fatalf("a reset inside the row dropped the band: %q", banded)
		}
	}
}

// TestBandPadsToWidth: the band is a bar, not a tint on the text — a
// short row still has to reach the edge of the column it sits in, or the
// selection reads as ragged.
func TestBandPadsToWidth(t *testing.T) {
	s := New(GummiDark())
	banded := s.Band(s.Base.Render("short"), 30, true)
	if got := ansi.StringWidth(banded); got != 30 {
		t.Fatalf("banded width = %d, want 30", got)
	}
	if got := ansi.Strip(banded); got != "short"+strings.Repeat(" ", 25) {
		t.Fatalf("banded text = %q, want the row padded out to the column", got)
	}
}

// TestBandZeroWidthDoesNotPad: dialog field rows sit in a frame whose
// width they don't own, so they band their own text and no further.
func TestBandZeroWidthDoesNotPad(t *testing.T) {
	s := New(GummiDark())
	if got := ansi.Strip(s.Band(s.Base.Render("repo: default"), 0, true)); got != "repo: default" {
		t.Fatalf("banded text = %q, want no padding at w=0", got)
	}
}

// TestBandTruncatesOverlongRow: a row wider than its column must not
// spill the band into the neighbouring pane.
func TestBandTruncatesOverlongRow(t *testing.T) {
	s := New(GummiDark())
	banded := s.Band(s.Base.Render(strings.Repeat("x", 50)), 20, true)
	if got := ansi.StringWidth(banded); got != 20 {
		t.Fatalf("banded width = %d, want 20", got)
	}
}

// TestBandFocusedDiffersOnEveryTheme: the focused and unfocused bands are
// the board's answer to "which pane owns the arrow keys", so they have to
// be distinguishable on every shipped palette — and the focused one
// carries the accent, so the two differ in hue and not only in lightness.
func TestBandFocusedDiffersOnEveryTheme(t *testing.T) {
	for _, name := range Names() {
		th, ok := ByName(name)
		if !ok {
			t.Fatalf("theme %q not in the registry", name)
		}
		t.Run(name, func(t *testing.T) {
			s := New(th)
			row := s.Base.Render("FD-042 dark mode")
			if s.Band(row, 30, true) == s.Band(row, 30, false) {
				t.Error("focused and unfocused bands render identically")
			}
			if s.BandMarker(true) == s.BandMarker(false) {
				t.Error("focused and unfocused band markers render identically")
			}
			// the band must not be the pane background, or there is no band.
			if colorEq(s.band, th.BgBase) {
				t.Error("focused band is the pane background")
			}
			if colorEq(s.bandDim, th.BgBase) {
				t.Error("unfocused band is the pane background")
			}
		})
	}
}

func colorEq(a, b interface{ RGBA() (r, g, b, a uint32) }) bool {
	ar, ag, ab, aa := a.RGBA()
	br, bg, bb, ba := b.RGBA()
	return ar == br && ag == bg && ab == bb && aa == ba
}
