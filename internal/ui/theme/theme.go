// Package theme is gummi's design system: semantic color tokens, the
// style builder that derives every component style from them exactly
// once, and the default charmtone-based theme. Components never touch
// raw colors — they consume *Styles.
package theme

import (
	"image/color"
	"math"

	"github.com/morphis/gummi/internal/domain"
)

// Theme is the complete set of semantic color slots. Everything the UI
// renders derives from these via New; adding a component style means
// extending the builder, never sprinkling colors in components.
type Theme struct {
	Name string

	// Brand.
	Primary   color.Color // wordmark ramp start, focused accents
	Secondary color.Color // wordmark ramp end
	Accent    color.Color // interactive highlights (selection, links)

	// Foreground tiers, brightest → faintest.
	FgBase   color.Color
	FgSubtle color.Color
	FgMuted  color.Color
	FgFaint  color.Color

	// Background tiers, base → most raised/visible.
	BgBase    color.Color
	BgSubtle  color.Color
	BgSurface color.Color
	BgRaised  color.Color

	// OnAccent is the foreground for text sitting on the Accent fill
	// (the mode pill). Other fills — warning, stage accents — vary too
	// much in brightness for one slot; they use OnFill instead.
	OnAccent color.Color

	// Separator is for low-contrast rules and pane borders.
	Separator color.Color

	// Statuses.
	Error       color.Color
	Warning     color.Color
	Success     color.Color
	Info        color.Color
	Destructive color.Color // irreversible actions (delete feature, force remove)

	// StageAccents give every workflow stage its gummy hue; a card's
	// stage must be readable by color alone (DESIGN §6.2).
	StageAccents map[domain.Stage]color.Color
}

// StageAccent returns the stage's accent, falling back to FgMuted for
// anything unmapped so a rendering bug degrades visibly but safely.
func (t Theme) StageAccent(s domain.Stage) color.Color {
	if c, ok := t.StageAccents[s]; ok {
		return c
	}
	return t.FgMuted
}

// OnFill returns readable ink for text on the given fill. FgBase/BgBase
// always form a dark/light pair (whichever theme polarity), so the one
// with more contrast against the fill is legible on it — bright fills
// like Zest get dark ink instead of the near-white OnAccent.
func (t Theme) OnFill(fill color.Color) color.Color {
	if contrast(fill, t.FgBase) >= contrast(fill, t.BgBase) {
		return t.FgBase
	}
	return t.BgBase
}

// contrast is the WCAG contrast ratio between two colors (1–21).
func contrast(a, b color.Color) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// luminance is WCAG relative luminance.
func luminance(c color.Color) float64 {
	r, g, b, _ := c.RGBA()
	lin := func(v uint32) float64 {
		s := float64(v) / 0xFFFF
		if s <= 0.04045 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(r) + 0.7152*lin(g) + 0.0722*lin(b)
}
