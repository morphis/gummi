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
	// onResume restarts the card, for the second step submit() raises on a
	// parked autopilot run. Separate from onSubmit because raising an
	// envelope and resuming a run are two decisions, and topping up has
	// never implied the second.
	onResume func() tea.Cmd
	problem  string // parse error shown under the input, cleared on edit

	// askResume/resumeTo/resumeButtons carry the follow-up "resume it?"
	// step submit() raises for a parked autopilot card (see offersResume):
	// the envelope has already been set by the time this shows — raising
	// it must never itself restart a run — so this is a second, explicit
	// question the user answers on its own.
	askResume     bool
	resumeTo      int
	resumeButtons *buttonRow
}

func newEnvelopeDialog(f domain.Feature, onSubmit func(int) tea.Cmd, onResume func() tea.Cmd) *envelopeDialog {
	in := textinput.New()
	in.Placeholder = "credits (0 = uncapped)"
	in.CharLimit = 8
	in.SetWidth(28)
	in.Focus()
	return &envelopeDialog{
		feature: f, input: in, onSubmit: onSubmit, onResume: onResume,
		buttons:       newButtonRow(button{label: "Cancel"}, button{label: "Set"}),
		resumeButtons: newButtonRow(button{label: "Not now"}, button{label: "Resume"}),
	}
}

// ID implements overlay.Dialog.
func (d *envelopeDialog) ID() string { return "envelope" }

// submit validates and fires onSubmit, matching the input's own enter
// handling exactly — the button row's Set button is just another way to
// reach the same action. The envelope is set unconditionally; when the
// card looks like a parked autopilot run (offersResume), submit does not
// close the dialog — it swaps to the resume confirm step instead, a
// second and entirely separate question (the top-up must never restart
// a run on its own).
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
	cmd := d.onSubmit(to)
	if d.offersResume(to) {
		d.askResume, d.resumeTo = true, to
		return false, cmd
	}
	return true, cmd
}

// offersResume reports whether raising the envelope to `to` earns the
// follow-up "resume it?" question: an autopilot card (any gate-approval
// mode but domain.GateOff) whose envelope, before this raise, was
// already spent — the one shape of "parked for lack of budget" this
// dialog can tell from a card that's simply mid-run without reaching
// into live session state, which this dialog — built from a bare
// domain.Feature snapshot — has no access to.
func (d *envelopeDialog) offersResume(to int) bool {
	f := d.feature
	if f.GateApproval == domain.GateOff {
		return false
	}
	if f.Budget.Envelope <= 0 || to <= f.Budget.Envelope {
		return false // was already uncapped, or this isn't actually a raise
	}
	return f.Spend.CreditEquivalent() >= float64(f.Budget.Envelope)
}

// resumeNotice answers "yes" on the resume question: restart the card
// through whatever the constructing surface handed us. A dialog with no
// resume wired says so rather than reporting a restart that did not
// happen — the whole reason the question is a separate step is that the
// answer has to be true.
func (d *envelopeDialog) resumeNotice() tea.Cmd {
	id := d.feature.ID
	if d.onResume == nil {
		return func() tea.Msg {
			return noticeMsg{text: fmt.Sprintf(
				"%s: envelope raised — pick it back up from the inbox (i) or `gummi resume %s`", id, id)}
		}
	}
	return d.onResume()
}

// HandleKey implements overlay.Dialog.
func (d *envelopeDialog) HandleKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	if d.askResume {
		switch key.String() {
		case "left", "h":
			d.resumeButtons.Move(-1)
			return false, nil
		case "right", "l":
			d.resumeButtons.Move(1)
			return false, nil
		case "y":
			d.askResume = false
			return true, d.resumeNotice()
		case "n", "esc":
			d.askResume = false
			return true, nil
		case "enter":
			d.askResume = false
			if d.resumeButtons.Cursor() == 1 { // "Resume"
				return true, d.resumeNotice()
			}
			return true, nil
		}
		return false, nil
	}
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
	if d.askResume {
		var b strings.Builder
		b.WriteString(s.DialogTitle.Render("envelope · "+string(d.feature.ID)) + "\n\n")
		b.WriteString(fmt.Sprintf("raised to %d — resume %s on autopilot?", d.resumeTo, d.feature.ID) + "\n")
		b.WriteString("\n" + d.resumeButtons.View(s, true) + "\n")
		b.WriteString("\n" + s.Faint.Render("y/enter resume · n/esc not now"))
		return s.DialogFrame.Render(b.String())
	}
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
