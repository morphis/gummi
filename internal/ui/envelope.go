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

// envelopeDialog is the inline popover setting a feature's budget
// envelope to an explicit credit figure — the proactive counterpart of
// the inbox's one-keystroke top-up, usable before a stage runs dry.
type envelopeDialog struct {
	feature  domain.Feature // snapshot for the envelope/spent readout
	input    textinput.Model
	onSubmit func(to int) tea.Cmd
	problem  string // parse error shown under the input, cleared on edit
}

func newEnvelopeDialog(f domain.Feature, onSubmit func(int) tea.Cmd) *envelopeDialog {
	in := textinput.New()
	in.Placeholder = "credits (0 = uncapped)"
	in.CharLimit = 8
	in.SetWidth(28)
	in.Focus()
	return &envelopeDialog{feature: f, input: in, onSubmit: onSubmit}
}

// ID implements overlay.Dialog.
func (d *envelopeDialog) ID() string { return "envelope" }

// HandleKey implements overlay.Dialog.
func (d *envelopeDialog) HandleKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	switch key.String() {
	case "esc":
		return true, nil
	case "enter":
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
	d.problem = ""
	d.input, _ = d.input.Update(key)
	return false, nil
}

// HandlePaste implements overlay.Paster.
func (d *envelopeDialog) HandlePaste(msg tea.PasteMsg) tea.Cmd {
	d.input, _ = d.input.Update(msg)
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
	b.WriteString("\n" + s.Faint.Render("enter set · esc cancel"))
	return s.DialogFrame.Render(b.String())
}
