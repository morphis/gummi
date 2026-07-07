package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/ui/theme"
)

// helpDialog is the ? overlay: the active surface's full key table,
// built by helpOverlay (keymap.go) from the same bindings the status
// bar hints render from.
type helpDialog struct {
	title string
	rows  [][2]string
}

func (helpDialog) ID() string { return "help" }

func (helpDialog) HandleKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	switch key.String() {
	case "esc", "?", "q", "enter":
		return true, nil
	}
	return false, nil
}

func (d helpDialog) View(s *theme.Styles, w, h int) string {
	keyW := 0
	for _, r := range d.rows {
		keyW = max(keyW, ansi.StringWidth(r[0]))
	}
	var b strings.Builder
	b.WriteString(s.DialogTitle.Render(d.title) + "\n\n")
	for _, r := range d.rows {
		b.WriteString(s.KeyHint.Render(padRight(r[0], keyW)) + "  " + s.Subtle.Render(r[1]) + "\n")
	}
	b.WriteString("\n" + s.Faint.Render("esc close"))
	return s.DialogFrame.Render(b.String())
}

func padRight(str string, n int) string {
	if w := ansi.StringWidth(str); w < n {
		return str + strings.Repeat(" ", n-w)
	}
	return str
}

// confirmDialog asks a yes/no question before a destructive action.
type confirmDialog struct {
	id        string
	question  string
	detail    string
	onConfirm func() tea.Cmd
}

func (d *confirmDialog) ID() string { return d.id }

func (d *confirmDialog) HandleKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	switch key.String() {
	case "y", "Y":
		return true, d.onConfirm()
	case "n", "N", "esc":
		return true, nil
	}
	return false, nil
}

func (d *confirmDialog) View(s *theme.Styles, w, h int) string {
	var b strings.Builder
	b.WriteString(s.Destructive.Bold(true).Render(d.question) + "\n")
	if d.detail != "" {
		b.WriteString(s.Subtle.Render(d.detail) + "\n")
	}
	b.WriteString("\n" + s.KeyHint.Render("y") + s.KeyLabel.Render(" yes") +
		s.Faint.Render(" · ") + s.KeyHint.Render("n") + s.KeyLabel.Render(" no"))
	return s.DialogFrame.Render(b.String())
}
