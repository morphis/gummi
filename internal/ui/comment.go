package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"

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
// the spec's or diff's cursor line.
//
// It shows the anchor — the line the comment will attach to — the way
// resolveDialog shows the thread it is closing. The dialog is drawn OVER
// the source pane, so without it the one thing the reader needs is the one
// thing the popover is covering: round 3 §3.4 wrote a comment about the
// gummi-checks block and it anchored to a "## Review" heading twelve lines
// away, because that is where the cursor happened to be and nothing on
// screen said so. The asymmetry with x — which had shown its target all
// along — is what made it obvious the information was available and simply
// not being offered on the write path.
type commentDialog struct {
	anchor   string
	input    textinput.Model
	buttons  *buttonRow
	focus    int
	onSubmit func(text string) tea.Cmd
}

func newCommentDialog(anchor string, onSubmit func(string) tea.Cmd) *commentDialog {
	in := textinput.New()
	in.Placeholder = "your comment"
	// 200 was under an ordinary review comment — the one round 3 typed ran
	// to ~190 and the field scrolls horizontally, so the writer could not
	// re-read their own first sentence. The field is still single-line
	// (enter is its submit gesture and a textarea would change that), so
	// this buys room rather than a second dimension.
	in.CharLimit = 600
	in.SetWidth(64)
	in.Focus()
	return &commentDialog{
		anchor: anchor, input: in, onSubmit: onSubmit,
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
	if d.anchor != "" {
		b.WriteString(s.Faint.Render("on: ") + s.Subtle.Render(ansi.Truncate(d.anchor, 60, "…")) + "\n\n")
	}
	b.WriteString(d.input.View() + "\n\n")
	b.WriteString(d.buttons.View(s, d.focus == commentFieldButtons) + "\n\n")
	b.WriteString(s.Faint.Render("enter save · tab buttons · esc cancel"))
	return s.DialogFrame.Render(b.String())
}
