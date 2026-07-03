package theme

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/rivo/uniseg"
)

// Grad renders input with a horizontal foreground gradient from c1 to
// c2, one blend step per grapheme cluster. This is the signature move
// of the wordmark and the single animated status line — use sparingly
// (DESIGN §6.2: motion and gradients mark exactly one thing).
func Grad(base lipgloss.Style, input string, c1, c2 color.Color) string {
	if input == "" {
		return ""
	}
	var clusters []string
	gr := uniseg.NewGraphemes(input)
	for gr.Next() {
		clusters = append(clusters, gr.Str())
	}
	if len(clusters) == 1 {
		return base.Foreground(c1).Render(input)
	}
	ramp := lipgloss.Blend1D(len(clusters), c1, c2)
	var b strings.Builder
	for i, cl := range clusters {
		// Gradient whitespace is invisible; skip the escape codes.
		if strings.TrimSpace(cl) == "" {
			b.WriteString(cl)
			continue
		}
		b.WriteString(base.Foreground(ramp[i]).Render(cl))
	}
	return b.String()
}
