package ui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/morphia/gummi/internal/ui/theme"
)

// commentDialog is the inline popover collecting one annotation for
// the spec's cursor line.
type commentDialog struct {
	input    textinput.Model
	onSubmit func(text string) tea.Cmd
}

func newCommentDialog(onSubmit func(string) tea.Cmd) *commentDialog {
	in := textinput.New()
	in.Placeholder = "your comment"
	in.CharLimit = 200
	in.SetWidth(48)
	in.Focus()
	return &commentDialog{input: in, onSubmit: onSubmit}
}

// ID implements overlay.Dialog.
func (d *commentDialog) ID() string { return "spec-comment" }

// HandleKey implements overlay.Dialog.
func (d *commentDialog) HandleKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	switch key.String() {
	case "esc":
		return true, nil
	case "enter":
		text := strings.TrimSpace(d.input.Value())
		if text == "" {
			return true, nil
		}
		return true, d.onSubmit(text)
	}
	d.input, _ = d.input.Update(key)
	return false, nil
}

// View implements overlay.Dialog.
func (d *commentDialog) View(s *theme.Styles, w, h int) string {
	var b strings.Builder
	b.WriteString(s.DialogTitle.Render("comment") + "\n\n")
	b.WriteString(d.input.View() + "\n\n")
	b.WriteString(s.Faint.Render("enter save · esc cancel"))
	return s.DialogFrame.Render(b.String())
}
