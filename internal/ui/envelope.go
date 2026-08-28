package ui

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/ui/theme"
)

// envelope dialog fields, in tab order: the input submits on enter (a
// single-line field, so that's the natural gesture), the button row after
// it is the dialog's other tab stop and its own enter activates whichever
// button is focused.
const (
	envelopeFieldInput = iota
	envelopeFieldButtons
)

// envelopeDialog is the inline popover setting a feature's budget
// envelope to an explicit credit figure — the proactive counterpart of
// the inbox's one-keystroke top-up, usable before a stage runs dry.
type envelopeDialog struct {
	feature  domain.Feature // snapshot for the envelope/spent readout
	input    textinput.Model
	buttons  *buttonRow
	focus    int
	onSubmit func(to int) tea.Cmd
	problem  string // parse error shown under the input, cleared on edit
}

func newEnvelopeDialog(f domain.Feature, onSubmit func(int) tea.Cmd) *envelopeDialog {
	in := textinput.New()
	in.Placeholder = "credits (0 = uncapped)"
	in.CharLimit = 8
	in.SetWidth(28)
	in.Focus()
	return &envelopeDialog{
		feature: f, input: in, onSubmit: onSubmit,
		buttons: newButtonRow(button{label: "Cancel"}, button{label: "Set"}),
	}
}

// ID implements overlay.Dialog.
func (d *envelopeDialog) ID() string { return "envelope" }

// submit validates and fires onSubmit, matching the input's own enter
// handling exactly — the button row's Set button is just another way to
// reach the same action.
func (d *envelopeDialog) submit() (bool, tea.Cmd) {
	raw := strings.TrimSpace(d.input.Value())
	if raw == "" {
		return true, nil
	}
	to, err := strconv.Atoi(raw)
	if err != nil || to < 0 {
		d.problem = "a whole credit figure, please"
		return false, nil
	}
	return true, d.onSubmit(to)
}

// HandleKey implements overlay.Dialog.
func (d *envelopeDialog) HandleKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	switch key.String() {
	case "esc":
		return true, nil
	case "tab", "shift+tab":
		// only two stops, so tab and shift+tab are the same toggle
		d.setFocus((d.focus + 1) % 2)
		return false, nil
	}
	if d.focus == envelopeFieldButtons {
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
	d.problem = ""
	d.input, _ = d.input.Update(key)
	return false, nil
}

// setFocus moves focus between the input and the button row, keeping the
// textinput's own focus/blur in sync with which one is drawn active.
func (d *envelopeDialog) setFocus(f int) {
	d.focus = f
	if f == envelopeFieldInput {
		d.input.Focus()
	} else {
		d.input.Blur()
	}
}

// HandlePaste implements overlay.Paster.
func (d *envelopeDialog) HandlePaste(msg tea.PasteMsg) tea.Cmd {
	if d.focus == envelopeFieldInput {
		d.input, _ = d.input.Update(msg)
	}
	return nil
}

// View implements overlay.Dialog.
func (d *envelopeDialog) View(s *theme.Styles, w, h int) string {
	var b strings.Builder
	b.WriteString(s.DialogTitle.Render("envelope · "+string(d.feature.ID)) + "\n\n")
	spent := d.feature.Spend.CreditEquivalent()
	now := "uncapped"
	if d.feature.Budget.Envelope > 0 {
		now = fmt.Sprintf("%g credits", float64(d.feature.Budget.Envelope))
	}
	b.WriteString(s.Faint.Render(fmt.Sprintf("now %s · spent %s%g", now, estMark(d.feature.Spend), roundSpend(spent))) + "\n\n")
	b.WriteString(d.input.View() + "\n")
	if d.problem != "" {
		b.WriteString(s.Error.Render(d.problem) + "\n")
	}
	b.WriteString("\n" + d.buttons.View(s, d.focus == envelopeFieldButtons) + "\n")
	b.WriteString("\n" + s.Faint.Render("enter set · tab buttons · esc cancel"))
	return s.DialogFrame.Render(b.String())
}
