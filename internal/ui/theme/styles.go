package theme

import (
	"image/color"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/exp/charmtone"

	"github.com/morphis/gummi/internal/domain"
)

// Styles holds every derived component style. Built exactly once per
// theme by New (the quickStyle pattern): components receive *Styles and
// never construct colors themselves.
type Styles struct {
	Theme Theme

	// Text tiers.
	Base   lipgloss.Style
	Subtle lipgloss.Style
	Muted  lipgloss.Style
	Faint  lipgloss.Style

	// Headings.
	Title    lipgloss.Style
	Subtitle lipgloss.Style

	// Chrome.
	Separator       lipgloss.Style
	PaneTitle       lipgloss.Style // section headers in panes (TODO, IN PROGRESS…)
	PaneTitleActive lipgloss.Style // the same header in the pane that holds focus
	KeyHint         lipgloss.Style // "enter" in key hints
	KeyLabel        lipgloss.Style // "attach" in key hints
	Cursor          lipgloss.Style // the ▸ selection/focus marker

	// Selection bands. A selected row wears a full-width background band
	// (Band, band.go) so the eye finds it without hunting for a one-glyph
	// marker; the accent-tinted band marks the pane that owns the arrow
	// keys, the quiet grey one a pane whose selection is only remembered.
	Selection    lipgloss.Style // selected row, focused pane
	SelectionDim lipgloss.Style // selected row, unfocused pane
	SelMarker    lipgloss.Style // the ▸ on a focused band
	SelMarkerDim lipgloss.Style // the ▸ on an unfocused band

	// A band costs contrast: against it FgMuted lands near 2:1 and
	// FgFaint near 1.2:1, so the four-tier text ramp collapses to two on
	// a highlighted row. Rows swap their Base/Faint pair for these, which
	// is why a selected card's cost tick and profile tag don't disappear
	// on exactly the row the eye was sent to.
	BandText    lipgloss.Style // primary text on a band
	BandTextDim lipgloss.Style // secondary text on a band

	// band/bandDim are the two band backgrounds Band paints with; they
	// are the backgrounds inside Selection/SelectionDim, kept as raw
	// slots because Band assembles its own SGR (a nested lipgloss
	// background cannot survive the row's own resets).
	band    color.Color
	bandDim color.Color

	// Buttons (a dialog's focusable row, buttons.go). Focus reads as a
	// fill, not a hue: two reds cannot say "selected" to each other, and
	// on the light theme Error and Destructive are the same color.
	Button            lipgloss.Style
	ButtonFocus       lipgloss.Style
	ButtonDanger      lipgloss.Style
	ButtonDangerFocus lipgloss.Style

	CardID         lipgloss.Style // FD-042
	CardIDResearch lipgloss.Style // RS-### cool/info tint, distinct from CardID and Warning
	CardTitle      lipgloss.Style
	ProfileTag     lipgloss.Style // [thrifty]
	RepoBadge      lipgloss.Style // [lxd] / [default] — a card's managed repository

	// Severity badges (bug impact) on board cards, tinted to read
	// by color: coral for critical, mustard, julep, oyster for low.
	SeverityCritical lipgloss.Style
	SeverityHigh     lipgloss.Style
	SeverityMedium   lipgloss.Style
	SeverityLow      lipgloss.Style

	// Status bar.
	StatusBase lipgloss.Style // the bar's base text (quiet, no fill)
	PillMode   lipgloss.Style // leading mode pill
	Pill       lipgloss.Style // neutral pill
	PillAlert  lipgloss.Style // needs-you pill

	// Dialogs.
	DialogFrame lipgloss.Style
	DialogTitle lipgloss.Style

	// Statuses.
	Error       lipgloss.Style
	Warning     lipgloss.Style
	Success     lipgloss.Style
	Info        lipgloss.Style
	Destructive lipgloss.Style

	stagePill map[domain.Stage]lipgloss.Style
	stageFg   map[domain.Stage]lipgloss.Style
}

// New derives all component styles from the theme's semantic slots.
func New(t Theme) *Styles {
	base := lipgloss.NewStyle().Foreground(t.FgBase)
	// the focused band is the raised surface pulled a quarter of the way
	// toward the accent: it separates from the unfocused band by hue as
	// well as by lightness, which is what makes "which pane am I in?"
	// answerable at a glance rather than by comparing two greys.
	band := lipgloss.Blend1D(5, t.BgRaised, t.Accent)[1]
	s := &Styles{
		Theme: t,

		Base:   base,
		Subtle: base.Foreground(t.FgSubtle),
		Muted:  base.Foreground(t.FgMuted),
		Faint:  base.Foreground(t.FgFaint),

		Title:    base.Foreground(t.Primary).Bold(true),
		Subtitle: base.Foreground(t.FgSubtle).Bold(true),

		Separator:       base.Foreground(t.Separator),
		PaneTitle:       base.Foreground(t.FgFaint).Bold(true),
		PaneTitleActive: base.Foreground(t.Accent).Bold(true),
		// key hints stay quiet greys (the crush help pattern); the
		// accent is reserved for the cursor and interactive pills.
		KeyHint:  base.Foreground(t.FgSubtle),
		KeyLabel: base.Foreground(t.FgMuted),
		Cursor:   base.Foreground(t.Accent),

		Selection:    base.Foreground(t.FgBase).Background(band),
		SelectionDim: base.Foreground(t.FgSubtle).Background(t.BgSurface),
		// on a band the accent cursor loses most of its contrast, and the
		// band already carries the accent — so the marker goes bright and
		// bold instead, and dim-but-legible on the quiet band.
		SelMarker:    base.Foreground(t.FgBase).Bold(true),
		SelMarkerDim: base.Foreground(t.FgMuted),
		BandText:     base.Foreground(t.FgBase),
		BandTextDim:  base.Foreground(t.FgSubtle),
		band:         band,
		bandDim:      t.BgSurface,

		Button:            base.Foreground(t.FgFaint),
		ButtonFocus:       lipgloss.NewStyle().Foreground(t.OnAccent).Background(t.Accent).Bold(true),
		ButtonDanger:      base.Foreground(t.Destructive),
		ButtonDangerFocus: lipgloss.NewStyle().Foreground(t.OnFill(t.Destructive)).Background(t.Destructive).Bold(true),

		CardID:         base.Foreground(t.FgSubtle).Bold(true),
		CardIDResearch: base.Foreground(t.Info).Bold(true),
		CardTitle:      base,
		ProfileTag:     base.Foreground(t.FgFaint),
		RepoBadge:      base.Foreground(t.FgSubtle),

		SeverityCritical: base.Foreground(charmtone.Coral),
		SeverityHigh:     base.Foreground(charmtone.Mustard),
		SeverityMedium:   base.Foreground(charmtone.Julep),
		SeverityLow:      base.Foreground(charmtone.Oyster),

		StatusBase: base,
		PillMode:   lipgloss.NewStyle().Foreground(t.OnAccent).Background(t.Accent).Bold(true).Padding(0, 1),
		Pill:       lipgloss.NewStyle().Foreground(t.FgSubtle).Background(t.BgRaised).Padding(0, 1),
		PillAlert:  lipgloss.NewStyle().Foreground(t.OnFill(t.Warning)).Background(t.Warning).Bold(true).Padding(0, 1),

		// no fill: a dialog is its border on the base background, so
		// the panel reads as part of the canvas rather than a slab.
		DialogFrame: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(t.Accent).
			Padding(1, 2),
		DialogTitle: base.Foreground(t.Primary).Bold(true),

		Error:       base.Foreground(t.Error),
		Warning:     base.Foreground(t.Warning),
		Success:     base.Foreground(t.Success),
		Info:        base.Foreground(t.Info),
		Destructive: base.Foreground(t.Destructive),

		stagePill: map[domain.Stage]lipgloss.Style{},
		stageFg:   map[domain.Stage]lipgloss.Style{},
	}
	for _, st := range domain.Stages {
		accent := t.StageAccent(st)
		s.stagePill[st] = lipgloss.NewStyle().Foreground(t.OnFill(accent)).Background(accent).Padding(0, 1)
		s.stageFg[st] = base.Foreground(accent)
	}
	return s
}

// Stage returns the foreground style carrying a stage's accent color.
func (s *Styles) Stage(st domain.Stage) lipgloss.Style {
	if v, ok := s.stageFg[st]; ok {
		return v
	}
	return s.Muted
}

// StagePill returns the filled pill style for a stage badge.
func (s *Styles) StagePill(st domain.Stage) lipgloss.Style {
	if v, ok := s.stagePill[st]; ok {
		return v
	}
	return s.Pill
}

// SeverityBadgeStyle returns the badge style for a bug's severity level,
// or an empty style (renders as plain text) for an unclassified bug.
func (s *Styles) SeverityBadgeStyle(sev domain.Severity) lipgloss.Style {
	switch sev {
	case domain.SeverityCritical:
		return s.SeverityCritical
	case domain.SeverityHigh:
		return s.SeverityHigh
	case domain.SeverityMedium:
		return s.SeverityMedium
	case domain.SeverityLow:
		return s.SeverityLow
	default:
		return lipgloss.Style{}
	}
}
