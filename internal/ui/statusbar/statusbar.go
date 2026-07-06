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

	var hs []string
	for _, h := range hints {
		hs = append(hs, s.KeyHint.Render(h.Key)+" "+s.KeyLabel.Render(h.Label))
	}
	rightStr := strings.Join(hs, s.Faint.Render(" · "))

	lw, rw := ansi.StringWidth(leftStr), ansi.StringWidth(rightStr)
	switch {
	case lw+rw+1 <= width:
		gap := width - lw - rw
		return s.StatusBase.Render(leftStr + strings.Repeat(" ", gap) + rightStr)
	case lw+1 <= width:
		// keep the pills, drop hints from the right
		rightStr = ansi.Truncate(rightStr, width-lw-1, "…")
		gap := max(width-lw-ansi.StringWidth(rightStr), 1)
		return s.StatusBase.Render(leftStr + strings.Repeat(" ", gap) + rightStr)
	default:
		return s.StatusBase.Render(ansi.Truncate(leftStr, width, "…"))
	}
}
