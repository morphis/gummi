package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphia/gummi/internal/domain"
	"github.com/morphia/gummi/internal/ui/theme"
)

// inboxDialog lists the needs-attention queue and lets the user jump to
// or dismiss an item. It reads a snapshot of items taken at open time;
// enter/x resolve via callbacks so the Shell owns the mutations.
type inboxDialog struct {
	items   []attnItem
	sel     int
	onJump  func(domain.FeatureID) tea.Cmd
	onClear func(domain.FeatureID)
}

func newInboxDialog(items []attnItem, onJump func(domain.FeatureID) tea.Cmd, onClear func(domain.FeatureID)) *inboxDialog {
	return &inboxDialog{items: items, onJump: onJump, onClear: onClear}
}

func (d *inboxDialog) ID() string { return "inbox" }

func (d *inboxDialog) HandleKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	switch key.String() {
	case "esc", "i", "q":
		return true, nil
	case "j", "down":
		if d.sel < len(d.items)-1 {
			d.sel++
		}
	case "k", "up":
		if d.sel > 0 {
			d.sel--
		}
	case "enter":
		if d.sel < len(d.items) {
			return true, d.onJump(d.items[d.sel].Feature)
		}
	case "x":
		if d.sel < len(d.items) {
			d.onClear(d.items[d.sel].Feature)
			d.items = append(d.items[:d.sel], d.items[d.sel+1:]...)
			if d.sel >= len(d.items) && d.sel > 0 {
				d.sel--
			}
			if len(d.items) == 0 {
				return true, nil
			}
		}
	}
	return false, nil
}

func (d *inboxDialog) View(s *theme.Styles, w, h int) string {
	var b strings.Builder
	b.WriteString(s.DialogTitle.Render("needs you") + "\n\n")
	if len(d.items) == 0 {
		b.WriteString(s.Faint.Render("nothing needs your attention") + "\n")
		return s.DialogFrame.Render(b.String())
	}
	width := max(min(w-8, 60), 20)
	for i, it := range d.items {
		cursor := "  "
		row := s.Base
		if i == d.sel {
			cursor = s.KeyHint.Render("▸ ")
			row = s.Subtle
		}
		icon := attnIcon(s, it.Kind)
		line := cursor + icon + " " + s.CardID.Render(string(it.Feature)) + " " +
			row.Render(ansi.Truncate(sanitize(it.Text), max(width-14, 6), "…"))
		b.WriteString(line + "\n")
	}
	b.WriteString("\n" + s.KeyHint.Render("enter") + s.KeyLabel.Render(" go") +
		s.Faint.Render(" · ") + s.KeyHint.Render("x") + s.KeyLabel.Render(" dismiss") +
		s.Faint.Render(" · ") + s.KeyHint.Render("esc") + s.KeyLabel.Render(" close"))
	return s.DialogFrame.Render(b.String())
}

func attnIcon(s *theme.Styles, k attnKind) string {
	switch k {
	case attnFailure:
		return s.Error.Render("✗")
	case attnQuestion:
		return s.Info.Render("?")
	default:
		return s.Warning.Render("✉")
	}
}
