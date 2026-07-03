// Package theme is gummi's design system: semantic color tokens, the
// style builder that derives every component style from them exactly
// once, and the default charmtone-based theme. Components never touch
// raw colors — they consume *Styles.
package theme

import (
	"image/color"

	"github.com/morphia/gummi/internal/domain"
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

	// OnAccent is the foreground for text sitting on Accent/stage pills.
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
