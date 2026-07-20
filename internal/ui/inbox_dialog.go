package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/ui/theme"
)

// inboxDialog lists the needs-attention queue and lets the user jump to
// or dismiss an item. It reads a snapshot of items taken at open time;
// enter/x resolve via callbacks so the Shell owns the mutations.
type inboxDialog struct {
	items   []attnItem
	sel     int
	onJump  func(domain.FeatureID) tea.Cmd
	onClear func(domain.FeatureID)
	onTopUp func(domain.FeatureID) tea.Cmd
	// suggest returns the selected item's ranked next actions
	// (nextsteps.go); the top one renders under the list so the inbox
	// says what to do, not just what happened. Optional.
	suggest func(domain.FeatureID) []nextAction
}

func newInboxDialog(items []attnItem, onJump func(domain.FeatureID) tea.Cmd, onClear func(domain.FeatureID), onTopUp func(domain.FeatureID) tea.Cmd, suggest func(domain.FeatureID) []nextAction) *inboxDialog {
	return &inboxDialog{items: items, onJump: onJump, onClear: onClear, onTopUp: onTopUp, suggest: suggest}
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
	case "pgup":
		d.sel = 0
	case "pgdown":
		d.sel = max(len(d.items)-1, 0)
	case "enter":
		if d.sel < len(d.items) {
			return true, d.onJump(d.items[d.sel].Feature)
		}
	case "u":
		// top up: raise the envelope and resume an exhausted stage. Only
		// budget gates offer it; other items ignore the key.
		if d.sel < len(d.items) && d.items[d.sel].Kind == attnBudget && d.onTopUp != nil {
			return true, d.onTopUp(d.items[d.sel].Feature)
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
			cursor = s.Cursor.Render("▸ ")
			row = s.Subtle
		}
		icon := attnIcon(s, it.Kind)
		line := cursor + icon + " " + s.CardID.Render(string(it.Feature)) + " " +
			row.Render(ansi.Truncate(sanitize(it.Text), max(width-14, 6), "…"))
		b.WriteString(line + "\n")
	}
	// the selected item's recommended action: what to press once there
	if d.suggest != nil && d.sel < len(d.items) {
		if acts := d.suggest(d.items[d.sel].Feature); len(acts) > 0 {
			a := acts[0]
			b.WriteString("\n  " + s.Faint.Render("↳ ") + s.KeyHint.Render(a.key) + " " +
				s.Subtle.Render(a.label) + s.Faint.Render(ansi.Truncate(" — "+sanitize(a.why), max(width-len(a.key)-len(a.label)-6, 6), "…")) + "\n")
		}
	}
	hint := "\n" + s.KeyHint.Render("enter") + s.KeyLabel.Render(" go")
	if d.sel < len(d.items) && d.items[d.sel].Kind == attnBudget {
		hint += s.Faint.Render(" · ") + s.KeyHint.Render("u") + s.KeyLabel.Render(" top up")
	}
	hint += s.Faint.Render(" · ") + s.KeyHint.Render("x") + s.KeyLabel.Render(" dismiss") +
		s.Faint.Render(" · ") + s.KeyHint.Render("esc") + s.KeyLabel.Render(" close")
	b.WriteString(hint)
	return s.DialogFrame.Render(b.String())
}

func attnIcon(s *theme.Styles, k attnKind) string {
	switch k {
	case attnFailure:
		return s.Error.Render("✗")
	case attnQuestion:
		return s.Info.Render("?")
	case attnBudget:
		return s.Warning.Render("$")
	default:
		return s.Warning.Render("✉")
	}
}
