package ui

import (
	"os"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/ui/theme"
)

// textPromptDialog is a one-field inline editor (rename, one-liner). An
// optional validate rejects a value and keeps the dialog open; an empty
// value cancels.
type textPromptDialog struct {
	title    string
	input    textinput.Model
	validate func(string) error
	onSubmit func(string) tea.Cmd
	errText  string
}

func newTextPrompt(title, initial, placeholder string, validate func(string) error, onSubmit func(string) tea.Cmd) *textPromptDialog {
	in := textinput.New()
	in.Placeholder = placeholder
	in.CharLimit = 200
	in.SetWidth(48)
	in.SetValue(initial)
	in.Focus()
	return &textPromptDialog{title: title, input: in, validate: validate, onSubmit: onSubmit}
}

// ID implements overlay.Dialog.
func (d *textPromptDialog) ID() string { return "text-prompt" }

// HandleKey implements overlay.Dialog.
func (d *textPromptDialog) HandleKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	switch key.String() {
	case "esc":
		return true, nil
	case "enter":
		text := strings.TrimSpace(d.input.Value())
		if text == "" {
			return true, nil // empty cancels, leaving the value unchanged
		}
		if d.validate != nil {
			if err := d.validate(text); err != nil {
				d.errText = err.Error()
				return false, nil
			}
		}
		return true, d.onSubmit(text)
	}
	d.input, _ = d.input.Update(key)
	d.errText = ""
	return false, nil
}

// HandlePaste implements overlay.Paster.
func (d *textPromptDialog) HandlePaste(msg tea.PasteMsg) tea.Cmd {
	d.input, _ = d.input.Update(msg)
	d.errText = ""
	return nil
}

// View implements overlay.Dialog.
func (d *textPromptDialog) View(s *theme.Styles, w, h int) string {
	var b strings.Builder
	b.WriteString(s.DialogTitle.Render(d.title) + "\n\n")
	b.WriteString(d.input.View() + "\n")
	if d.errText != "" {
		b.WriteString("\n" + s.Error.Render(d.errText))
	}
	b.WriteString("\n" + s.Faint.Render("enter save · esc cancel"))
	return s.DialogFrame.Render(b.String())
}

// ingest form fields, in tab order. fieldRepo is skipped when the repo
// picker has nothing to choose (see advanceFocus) — the row itself may
// still render read-only there, see repoPicker.shown; fieldButtons is the
// last stop, so tab from it wraps back to the first field.
const (
	ingestFieldRepo = iota
	ingestFieldPath
	ingestFieldProfile
	ingestFieldButtons
	ingestFieldCount
)

// ingestForm collects the managed repository, the source document, and
// the profile for an ingest pass (DESIGN §11.4): the architect decomposes
// the file, then the review surface opens on the result. The repository
// is a batch-level choice for the whole pass, like the profile.
type ingestForm struct {
	path     textinput.Model
	profiles []string
	profile  int
	repo     repoPicker
	focus    int
	errText  string
	buttons  *buttonRow

	onSubmit func(path, profile, repo string) tea.Cmd
}

func newIngestForm(profiles, repos []string, hasDefault bool, onSubmit func(path, profile, repo string) tea.Cmd) *ingestForm {
	if len(profiles) == 0 {
		profiles = defaultProfilePresets
	}
	path := textinput.New()
	path.Placeholder = "path to a spec/PRD to decompose…"
	path.CharLimit = 300
	path.SetWidth(46)
	path.Focus()
	return &ingestForm{
		path: path, profiles: profiles,
		repo: newRepoPicker(repos, hasDefault), focus: ingestFieldPath,
		buttons:  newButtonRow(button{label: "Cancel"}, button{label: "Decompose"}),
		onSubmit: onSubmit,
	}
}

// ID implements overlay.Dialog.
func (d *ingestForm) ID() string { return "ingest-spec" }

// submit validates and fires onSubmit, matching enter's own handling from
// any other field exactly — the button row's Decompose button is just
// another way to reach the same action.
func (d *ingestForm) submit() (bool, tea.Cmd) {
	if d.repo.needsChoice() {
		d.errText = repoUnchosenErr
		d.setFocus(ingestFieldRepo)
		return false, nil
	}
	path := strings.TrimSpace(d.path.Value())
	if path == "" {
		d.errText = "give a path to a spec file"
		return false, nil
	}
	if fi, err := os.Stat(path); err != nil || fi.IsDir() {
		d.errText = "no such file: " + path
		return false, nil
	}
	return true, d.onSubmit(path, d.profiles[d.profile], d.repo.name())
}

// HandleKey implements overlay.Dialog.
func (d *ingestForm) HandleKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	switch key.String() {
	case "esc":
		return true, nil
	case "tab":
		d.advanceFocus(1)
		return false, nil
	case "shift+tab":
		d.advanceFocus(-1)
		return false, nil
	}

	if d.focus == ingestFieldButtons {
		switch key.String() {
		case "left", "h":
			d.buttons.Move(-1)
		case "right", "l":
			d.buttons.Move(1)
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

	switch d.focus {
	case ingestFieldRepo:
		if delta, ok := selectCycleDelta(key.String()); ok {
			d.repo.cycle(delta)
			// clear a pending "choose a repo" refusal the moment they do
			d.errText = ""
		}
	case ingestFieldProfile:
		if delta, ok := selectCycleDelta(key.String()); ok {
			n := len(d.profiles)
			d.profile = ((d.profile+delta)%n + n) % n
		}
	case ingestFieldPath:
		d.path, _ = d.path.Update(key)
		d.errText = ""
	}
	return false, nil
}

// HandlePaste implements overlay.Paster: pasted text goes into the
// path while it's focused.
func (d *ingestForm) HandlePaste(msg tea.PasteMsg) tea.Cmd {
	if d.focus == ingestFieldPath {
		d.path, _ = d.path.Update(msg)
		d.errText = ""
	}
	return nil
}

// advanceFocus moves focus by dir (±1), wrapping, and skips the repo stop
// when there's nothing to choose there.
func (d *ingestForm) advanceFocus(dir int) {
	f := d.focus
	for {
		f = (f + dir + ingestFieldCount) % ingestFieldCount
		if f != ingestFieldRepo || d.repo.multi() {
			break
		}
	}
	d.setFocus(f)
}

func (d *ingestForm) setFocus(f int) {
	d.focus = f
	if f == ingestFieldPath {
		d.path.Focus()
	} else {
		d.path.Blur()
	}
}

// View implements overlay.Dialog.
func (d *ingestForm) View(s *theme.Styles, w, h int) string {
	var b strings.Builder
	b.WriteString(s.DialogTitle.Render("ingest spec") + "\n\n")
	if d.repo.shown() {
		b.WriteString(fieldRow(s, d.focus == ingestFieldRepo, "repo: "+d.repo.label()) + "\n\n")
	}
	b.WriteString(d.path.View() + "\n\n")
	b.WriteString(fieldRow(s, d.focus == ingestFieldProfile, "profile: "+d.profiles[d.profile]) + "\n")
	b.WriteString("\n" + d.buttons.View(s, d.focus == ingestFieldButtons) + "\n")

	if d.errText != "" {
		b.WriteString("\n" + s.Error.Render(d.errText))
	}
	hint := "tab next · ←/→ change · enter decompose · esc cancel"
	if d.focus == ingestFieldButtons {
		hint = "←/→ buttons · enter activate · tab next · esc cancel"
	}
	b.WriteString("\n" + s.Faint.Render(hint))
	return s.DialogFrame.Render(b.String())
}
