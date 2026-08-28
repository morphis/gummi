package ui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/ui/theme"
)

// comment dialog fields, in tab order: the input submits on enter (a
// single-line field, so that's the natural gesture), the button row after
// it is the dialog's other tab stop and its own enter activates whichever
// button is focused.
const (
	commentFieldInput = iota
	commentFieldButtons
)

// commentDialog is the inline popover collecting one annotation for
// the spec's cursor line.
type commentDialog struct {
	input    textinput.Model
	buttons  *buttonRow
	focus    int
	onSubmit func(text string) tea.Cmd
}

func newCommentDialog(onSubmit func(string) tea.Cmd) *commentDialog {
	in := textinput.New()
	in.Placeholder = "your comment"
	in.CharLimit = 200
	in.SetWidth(48)
	in.Focus()
	return &commentDialog{
		input: in, onSubmit: onSubmit,
		buttons: newButtonRow(button{label: "Cancel"}, button{label: "Save"}),
	}
}

// ID implements overlay.Dialog.
func (d *commentDialog) ID() string { return "spec-comment" }

// submit fires onSubmit, matching the input's own enter handling exactly
// — an empty value is a cancel, not an error, and the button row's Save
// button is just another way to reach the same action.
func (d *commentDialog) submit() (bool, tea.Cmd) {
	text := strings.TrimSpace(d.input.Value())
	if text == "" {
		return true, nil
	}
	return true, d.onSubmit(text)
}

// HandleKey implements overlay.Dialog.
func (d *commentDialog) HandleKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	switch key.String() {
	case "esc":
		return true, nil
	case "tab", "shift+tab":
		// only two stops, so tab and shift+tab are the same toggle
		d.setFocus((d.focus + 1) % 2)
		return false, nil
	}
	if d.focus == commentFieldButtons {
		switch key.String() {
		case "left", "h":
			d.buttons.Move(-1)
			return false, nil
		case "right", "l":
			d.buttons.Move(1)
			return false, nil
		case "enter":
			if d.buttons.Cursor() == 0 {
				return true, nil
			}
			return d.submit()
		}
		return false, nil
	}
	if key.String() == "enter" {
		return d.submit()
	}
	d.input, _ = d.input.Update(key)
	return false, nil
}

// setFocus moves focus between the input and the button row, keeping the
// textinput's own focus/blur in sync with which one is drawn active.
func (d *commentDialog) setFocus(f int) {
	d.focus = f
	if f == commentFieldInput {
		d.input.Focus()
	} else {
		d.input.Blur()
	}
}

// HandlePaste implements overlay.Paster.
func (d *commentDialog) HandlePaste(msg tea.PasteMsg) tea.Cmd {
	if d.focus == commentFieldInput {
		d.input, _ = d.input.Update(msg)
	}
	return nil
}

// View implements overlay.Dialog.
func (d *commentDialog) View(s *theme.Styles, w, h int) string {
	var b strings.Builder
	b.WriteString(s.DialogTitle.Render("comment") + "\n\n")
	b.WriteString(d.input.View() + "\n\n")
	b.WriteString(d.buttons.View(s, d.focus == commentFieldButtons) + "\n\n")
	b.WriteString(s.Faint.Render("enter save · tab buttons · esc cancel"))
	return s.DialogFrame.Render(b.String())
}
