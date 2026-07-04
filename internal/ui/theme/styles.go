package theme

import (
	"charm.land/lipgloss/v2"

	"github.com/morphia/gummi/internal/domain"
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
	Separator  lipgloss.Style
	PaneTitle  lipgloss.Style // section headers in panes (TODO, IN PROGRESS…)
	KeyHint    lipgloss.Style // "enter" in key hints
	KeyLabel   lipgloss.Style // "attach" in key hints
	Cursor     lipgloss.Style // the ▸ selection/focus marker
	Selection  lipgloss.Style // selected row highlight
	CardID     lipgloss.Style // FD-042
	CardTitle  lipgloss.Style
	ProfileTag lipgloss.Style // [thrifty]

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
	s := &Styles{
		Theme: t,

		Base:   base,
		Subtle: base.Foreground(t.FgSubtle),
		Muted:  base.Foreground(t.FgMuted),
		Faint:  base.Foreground(t.FgFaint),

		Title:    base.Foreground(t.Primary).Bold(true),
		Subtitle: base.Foreground(t.FgSubtle).Bold(true),

		Separator: base.Foreground(t.Separator),
		PaneTitle: base.Foreground(t.FgFaint).Bold(true),
		// key hints stay quiet greys (the crush help pattern); the
		// accent is reserved for the cursor and interactive pills.
		KeyHint:   base.Foreground(t.FgSubtle),
		KeyLabel:  base.Foreground(t.FgMuted),
		Cursor:    base.Foreground(t.Accent),
		Selection: base.Foreground(t.FgBase).Background(t.BgRaised),

		CardID:     base.Foreground(t.FgSubtle).Bold(true),
		CardTitle:  base,
		ProfileTag: base.Foreground(t.FgFaint),

		StatusBase: base,
		PillMode:   lipgloss.NewStyle().Foreground(t.OnAccent).Background(t.Accent).Bold(true).Padding(0, 1),
		Pill:       lipgloss.NewStyle().Foreground(t.FgSubtle).Background(t.BgRaised).Padding(0, 1),
		PillAlert:  lipgloss.NewStyle().Foreground(t.OnAccent).Background(t.Warning).Bold(true).Padding(0, 1),

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
		s.stagePill[st] = lipgloss.NewStyle().Foreground(t.OnAccent).Background(accent).Padding(0, 1)
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
