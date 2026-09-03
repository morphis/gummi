// Package statusbar renders the single-line status bar: a leading mode
// pill, informational pills (active/paused/needs-you counts, spend),
// and right-aligned key hints. Quiet, one line, always accurate.
package statusbar

import (
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/ui/theme"
)

// Kind selects a pill's visual weight.
type Kind int

const (
	// KindMode is the leading accent pill naming the current mode/view.
	KindMode Kind = iota
	// KindNeutral is a quiet informational pill.
	KindNeutral
	// KindAlert is the needs-your-attention pill.
	KindAlert
)

// Pill is one segment of the status bar.
type Pill struct {
	Text string
	Kind Kind
}

// Hint is one key-binding hint rendered right-aligned.
type Hint struct {
	Key   string
	Label string
	// Sticky marks a hint Render must not drop while shedding for width —
	// every non-sticky hint (besides the trailing escape hatch, which was
	// already exempt) goes first. Set by a surface that has something
	// consequential enough riding on one row that silently dropping it
	// would mislead rather than merely declutter (keymap.go's binding.sticky
	// is where a caller declares this; F15 is the row that motivated it —
	// a pinned decision's "enter <option>" hint, which can attach an agent
	// and spend credits).
	Sticky bool
}

// Render draws the bar at exactly the given width.
func Render(s *theme.Styles, width int, pills []Pill, hints []Hint) string {
	if width <= 0 {
		return ""
	}
	var left []string
	for _, p := range pills {
		if p.Text == "" {
			continue
		}
		switch p.Kind {
		case KindMode:
			left = append(left, s.PillMode.Render(p.Text))
		case KindAlert:
			left = append(left, s.PillAlert.Render(p.Text))
		default:
			left = append(left, s.Pill.Render(p.Text))
		}
	}
	leftStr := strings.Join(left, " ")

	lw := ansi.StringWidth(leftStr)
	// keep the pills; when the hints don't fit, drop whole hints rather
	// than truncating mid-word. Hints arrive most-important-first except
	// the last (help / the surface's escape hatch), which survives
	// longest — so drop from the second-to-last backwards. A hint marked
	// Sticky is excluded from that pool entirely: everything else sheds
	// before a sticky row is even considered (lastSheddable), the same
	// protection the trailing escape hatch always had, generalized to
	// whichever row a surface declared load-bearing.
	hs := append([]Hint(nil), hints...)
	rightStr := joinHints(s, hs)
	for lw+ansi.StringWidth(rightStr)+1 > width {
		i := lastSheddable(hs)
		if i < 0 {
			break // nothing left we're allowed to drop
		}
		hs = append(hs[:i], hs[i+1:]...)
		rightStr = joinHints(s, hs)
	}
	if lw+ansi.StringWidth(rightStr)+1 > width {
		if hasSticky(hs) {
			// Only sticky hints (and maybe the escape hatch) are left, and
			// even those don't fit — including, as here, when the pills
			// alone already ate the whole width (BG-064): truncating keeps
			// SOME sign of what a sticky row promises rather than erasing
			// it outright, which is exactly the silent failure Sticky
			// exists to prevent. Reserve room for it first, truncating the
			// pills if that's what it takes, rather than letting an
			// overlong pill row crowd out the one hint Render must not
			// silently drop.
			rw := min(ansi.StringWidth(rightStr), max(width-1, 0))
			rightStr = ansi.Truncate(rightStr, rw, "…")
			rw = ansi.StringWidth(rightStr)
			leftStr = ansi.Truncate(leftStr, max(width-rw-1, 0), "…")
			lw = ansi.StringWidth(leftStr)
		} else if lw+1 > width {
			// the pills alone don't fit and nothing sheddable survived to
			// need room anyway: unchanged from before Sticky existed, show
			// as much of the pills as fits and drop hints entirely.
			return s.StatusBase.Render(ansi.Truncate(leftStr, width, "…"))
		} else {
			// unchanged from before Sticky existed: a plain hint row that
			// still doesn't fit with nothing left to drop just goes blank.
			rightStr = ""
		}
	}
	gap := max(width-lw-ansi.StringWidth(rightStr), 1)
	return s.StatusBase.Render(leftStr + strings.Repeat(" ", gap) + rightStr)
}

// joinHints renders a hint list exactly as Render always has: "key label"
// per hint, "·" between them.
func joinHints(s *theme.Styles, hs []Hint) string {
	if len(hs) == 0 {
		return ""
	}
	parts := make([]string, len(hs))
	for i, h := range hs {
		parts[i] = s.KeyHint.Render(h.Key) + " " + s.KeyLabel.Render(h.Label)
	}
	return strings.Join(parts, s.Faint.Render(" · "))
}

// lastSheddable finds the rightmost hint Render is allowed to drop next:
// the same "second-to-last backwards" scan the shedding loop always used,
// skipping the trailing escape hatch (index len-1, never a candidate) and
// now also skipping any hint marked Sticky. -1 means nothing is left to
// drop — either only the escape hatch remains (the original contract) or
// every remaining hint besides it is sticky (the new one).
func lastSheddable(hs []Hint) int {
	for i := len(hs) - 2; i >= 0; i-- {
		if !hs[i].Sticky {
			return i
		}
	}
	return -1
}

// hasSticky reports whether any hint in the (already-shed-down) list is
// marked Sticky — Render's signal to truncate rather than blank when even
// the survivors don't fit.
func hasSticky(hs []Hint) bool {
	for _, h := range hs {
		if h.Sticky {
			return true
		}
	}
	return false
}
