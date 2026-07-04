package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/morphia/gummi/internal/ui/theme"
)

// helpDialog is the ? overlay listing every key binding.
type helpDialog struct{}

func (helpDialog) ID() string { return "help" }

func (helpDialog) HandleKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	switch key.String() {
	case "esc", "?", "q", "enter":
		return true, nil
	}
	return false, nil
}

func (helpDialog) View(s *theme.Styles, w, h int) string {
	rows := [][2]string{
		{"j/k ↓↑", "select feature"},
		{"1..9", "jump to feature"},
		{"enter", "chat (brainstorm/spec) · run (autonomous)"},
		{"p", "pause the running agent"},
		{"tab", "cycle needs-attention queue"},
		{"i", "open needs-attention inbox"},
		{"g", "advance stage (gate)"},
		{"b", "bounce back to implement"},
		{"r", "rebase branch onto main"},
		{"n", "new feature"},
		{"s", "spec (tab: read ⇄ annotate)"},
		{"d", "diff (tab: read ⇄ annotate)"},
		{"v", "run verify checks"},
		{"x", "delete feature"},
		{"?", "help"},
		{"q", "quit"},
	}
	var b strings.Builder
	b.WriteString(s.DialogTitle.Render("keys") + "\n\n")
	for _, r := range rows {
		b.WriteString(s.KeyHint.Render(padRight(r[0], 8)) + " " + s.Subtle.Render(r[1]) + "\n")
	}
	b.WriteString("\n" + s.Faint.Render("esc close"))
	return s.DialogFrame.Render(b.String())
}

func padRight(str string, n int) string {
	if len(str) >= n {
		return str
	}
	return str + strings.Repeat(" ", n-len(str))
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
