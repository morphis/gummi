package ui

import (
	"context"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/ui/theme"
)

// The card thread's half of "write a spec" (DESIGN §19.8): the web face's
// dialog is session.js's modal, and this is the terminal's — the same
// offer, the same seam. Opening the dialog fetches the handoff brief once,
// off the loop: on a live session the fetch IS the brief turn, gummi
// asking the session to write what the next card's architect will read,
// so the dialog shows its own drafting state while it runs and the brief
// arrives as an editable field pre-filled with it. Nothing mints without
// the confirm, and the edited text — not the raw draft — is what the
// minted card carries.

// writespec dialog fields, in tab order. fieldButtons is the last stop,
// so tab from it wraps back to the first field.
const (
	specFieldTitle = iota
	specFieldBrief
	specFieldProfile
	specFieldEnvelope
	specFieldButtons
	specFieldCount
)

// writespecDraftMsg carries the fetched handoff brief to the dialog that
// asked for it. err is the refusal any send rides (a mid-turn session, an
// unanswered question) or the backend failing to answer — the dialog says
// so instead of minting from nothing.
type writespecDraftMsg struct {
	f      domain.FeatureID
	brief  string
	source engine.BriefSource
	err    error
}

// freeformSpecDialog is the terminal's writespec dialog: the brief, the
// spec's title, profile and budget, each its own field, and the confirm
// that ends the session and mints the feature.
type freeformSpecDialog struct {
	f        domain.Feature
	title    textinput.Model
	brief    textarea.Model
	profiles []string
	profile  int
	envelope textinput.Model
	buttons  *buttonRow
	focus    int
	drafting bool
	source   engine.BriefSource
	errText  string

	onSubmit func(f domain.Feature, title, brief, profile string, envelope int) tea.Cmd
}

// openWritespec opens the dialog and starts the fetch — the brief turn on
// a live session — as one command. The dialog shows its drafting state
// from the first frame; the arriving message fills the field or reports
// why the draft could not be had.
func (m *Shell) openWritespec(f domain.Feature) tea.Cmd {
	eng, fid := m.engine, f.ID
	var profiles []string
	if eng != nil {
		for _, p := range eng.CardProfiles(domain.StagePlan) {
			profiles = append(profiles, p.Name)
		}
	}
	m.Overlay.Push(newFreeformSpecDialog(f, profiles, m.envelopePrefill(), m.specFromSession))
	return func() tea.Msg {
		brief, source, err := eng.SessionHandoffBrief(context.Background(), fid)
		return writespecDraftMsg{f: fid, brief: brief, source: source, err: err}
	}
}

// handleWritespecDraftMsg folds the fetch's outcome into the dialog that
// asked for it. The card may have moved on between the open and the fetch
// (the dialog dismissed, the card closed): anything else on the overlay
// gets nothing.
func (m *Shell) handleWritespecDraftMsg(msg writespecDraftMsg) {
	d, ok := m.Overlay.Top().(*freeformSpecDialog)
	if !ok || d.f.ID != msg.f {
		return
	}
	d.apply(msg)
}

func newFreeformSpecDialog(f domain.Feature, profiles []string, envelope int, onSubmit func(domain.Feature, string, string, string, int) tea.Cmd) *freeformSpecDialog {
	title := textinput.New()
	title.Placeholder = "the spec's title"
	title.CharLimit = 200
	title.SetWidth(44)
	title.SetValue(f.Title)
	title.Focus()

	brief := textarea.New()
	brief.Placeholder = "the handoff brief — what was asked, decided, done, and what remains"
	brief.CharLimit = engine.SpecBriefMax + 2000
	brief.ShowLineNumbers = false
	brief.SetWidth(60)
	brief.SetHeight(9)

	env := textinput.New()
	env.Placeholder = "credits for the whole spec; 0 is uncapped"
	env.CharLimit = 9
	env.SetWidth(12)
	env.SetValue(strconv.Itoa(envelope))

	if len(profiles) == 0 {
		profiles = defaultProfilePresets
	}
	return &freeformSpecDialog{
		f: f, title: title, brief: brief, profiles: profiles, envelope: env,
		drafting: true,
		buttons:  newButtonRow(button{label: "Cancel"}, button{label: "Start the spec"}),
		onSubmit: onSubmit,
	}
}

// apply folds the fetch's outcome into the dialog: the brief lands in the
// editable field, or the refusal is said where the drafting line was.
func (d *freeformSpecDialog) apply(msg writespecDraftMsg) {
	d.drafting = false
	if msg.err != nil {
		d.errText = sanitize(msg.err.Error())
		return
	}
	d.errText = ""
	d.source = msg.source
	if d.brief.Value() == "" {
		d.brief.SetValue(msg.brief)
	}
}

// submit validates and fires onSubmit — the confirm that ends the session
// and mints the feature. It waits for the draft: the fetch is the brief
// turn, and a confirm fired while it runs would mint a brief nobody wrote.
func (d *freeformSpecDialog) submit() (bool, tea.Cmd) {
	if d.drafting {
		return false, nil
	}
	if d.errText != "" {
		return false, nil
	}
	env, err := strconv.Atoi(strings.TrimSpace(d.envelope.Value()))
	if err != nil || env < 0 {
		d.errText = "the budget is a number of credits, 0 for uncapped"
		return false, nil
	}
	return true, d.onSubmit(d.f, strings.TrimSpace(d.title.Value()), d.brief.Value(), d.profiles[d.profile], env)
}

// ID implements overlay.Dialog.
func (d *freeformSpecDialog) ID() string { return "freeform-writespec" }

// HandleKey implements overlay.Dialog.
func (d *freeformSpecDialog) HandleKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	switch key.String() {
	case "esc":
		return true, nil
	case "tab":
		d.cycleFocus(1)
		return false, nil
	case "shift+tab":
		d.cycleFocus(-1)
		return false, nil
	}
	if d.focus == specFieldButtons {
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
	if key.String() == "ctrl+s" {
		return d.submit()
	}
	switch d.focus {
	case specFieldTitle:
		d.title, _ = d.title.Update(key)
	case specFieldBrief:
		d.brief, _ = d.brief.Update(key)
	case specFieldProfile:
		if delta, ok := selectCycleDelta(key.String()); ok {
			n := len(d.profiles)
			d.profile = ((d.profile+delta)%n + n) % n
		}
	case specFieldEnvelope:
		d.envelope, _ = d.envelope.Update(key)
	}
	return false, nil
}

// HandlePaste implements overlay.Paster: pasted text goes into whichever
// text field is focused.
func (d *freeformSpecDialog) HandlePaste(msg tea.PasteMsg) tea.Cmd {
	switch d.focus {
	case specFieldTitle:
		d.title, _ = d.title.Update(msg)
	case specFieldBrief:
		d.brief, _ = d.brief.Update(msg)
	case specFieldEnvelope:
		d.envelope, _ = d.envelope.Update(msg)
	}
	return nil
}

func (d *freeformSpecDialog) setFocus(f int) {
	d.focus = f
	d.title.Blur()
	d.brief.Blur()
	d.envelope.Blur()
	switch f {
	case specFieldTitle:
		d.title.Focus()
	case specFieldBrief:
		d.brief.Focus()
	case specFieldEnvelope:
		d.envelope.Focus()
	}
}

// cycleFocus moves the tab focus one field over, skipping the brief field
// while the draft has not landed: the field is not rendered while the
// dialog drafts, and landing focus in an invisible textarea would let
// typed keys fill it — the fill only takes an empty field, so the draft
// would be lost.
func (d *freeformSpecDialog) cycleFocus(delta int) {
	for {
		f := ((d.focus+delta)%specFieldCount + specFieldCount) % specFieldCount
		if f != specFieldBrief || !d.drafting {
			d.setFocus(f)
			return
		}
		d.focus = f
	}
}

// View implements overlay.Dialog.
func (d *freeformSpecDialog) View(s *theme.Styles, w, h int) string {
	width, _ := dialogDescSize(w, h, 0)
	var b strings.Builder
	b.WriteString(s.DialogTitle.Render("write a spec · "+string(d.f.ID)) + "\n\n")
	if d.drafting {
		// the fetch is the brief turn on a live session: the dialog says
		// what is happening rather than opening on an empty field
		b.WriteString(s.Info.Render("drafting the handoff brief…") + "\n\n")
	}
	b.WriteString(fieldRow(s, d.focus == specFieldTitle, "title") + "\n")
	b.WriteString(d.title.View() + "\n\n")
	if !d.drafting {
		label := "brief"
		switch d.source {
		case engine.BriefAssembled:
			label += " — assembled from the conversation"
		case engine.BriefLive:
			label += " — the session's own words"
		}
		b.WriteString(fieldRow(s, d.focus == specFieldBrief, label) + "\n")
		b.WriteString(d.brief.View() + "\n\n")
	}
	b.WriteString(fieldRow(s, d.focus == specFieldProfile, "profile: "+d.profiles[d.profile]) + "\n")
	b.WriteString(fieldRow(s, d.focus == specFieldEnvelope, "budget") + "\n")
	b.WriteString(d.envelope.View() + "\n")
	b.WriteString("\n" + d.buttons.ViewWidth(s, d.focus == specFieldButtons, width) + "\n")
	if d.errText != "" {
		b.WriteString("\n" + s.Error.Render(ansi.Wrap(d.errText, width, " -")))
	}
	var hint string
	switch d.focus {
	case specFieldButtons:
		hint = "←/→ buttons · enter activate · tab next · esc cancel"
	case specFieldBrief:
		hint = "type to edit · tab next · ctrl+s start · esc cancel"
	default:
		hint = "tab next · ←/→ change · enter start · esc cancel"
	}
	b.WriteString("\n" + s.Faint.Render(strings.Join(wrapHint(hint, width), "\n")))
	return s.DialogFrame.Render(b.String())
}
