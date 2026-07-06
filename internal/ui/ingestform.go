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

// ingest form fields, in tab order.
const (
	ingestFieldPath = iota
	ingestFieldProfile
	ingestFieldCount
)

// ingestForm collects the source document and the profile for an ingest
// pass (DESIGN §11.4): the architect decomposes the file, then the review
// surface opens on the result.
type ingestForm struct {
	path     textinput.Model
	profiles []string
	profile  int
	focus    int
	errText  string

	onSubmit func(path, profile string) tea.Cmd
}

func newIngestForm(profiles []string, onSubmit func(path, profile string) tea.Cmd) *ingestForm {
	if len(profiles) == 0 {
		profiles = defaultProfilePresets
	}
	path := textinput.New()
	path.Placeholder = "path to a spec/PRD to decompose…"
	path.CharLimit = 300
	path.SetWidth(46)
	path.Focus()
	return &ingestForm{path: path, profiles: profiles, onSubmit: onSubmit}
}

// ID implements overlay.Dialog.
func (d *ingestForm) ID() string { return "ingest-spec" }

// HandleKey implements overlay.Dialog.
func (d *ingestForm) HandleKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	switch key.String() {
	case "esc":
		return true, nil
	case "enter":
		path := strings.TrimSpace(d.path.Value())
		if path == "" {
			d.errText = "give a path to a spec file"
			return false, nil
		}
		if fi, err := os.Stat(path); err != nil || fi.IsDir() {
			d.errText = "no such file: " + path
			return false, nil
		}
		return true, d.onSubmit(path, d.profiles[d.profile])
	case "tab", "down":
		d.setFocus((d.focus + 1) % ingestFieldCount)
		return false, nil
	case "shift+tab", "up":
		d.setFocus((d.focus + ingestFieldCount - 1) % ingestFieldCount)
		return false, nil
	}
	switch d.focus {
	case ingestFieldProfile:
		switch key.String() {
		case "left", "h":
			d.profile = (d.profile + len(d.profiles) - 1) % len(d.profiles)
		case "right", "l", "space":
			d.profile = (d.profile + 1) % len(d.profiles)
		}
	case ingestFieldPath:
		d.path, _ = d.path.Update(key)
		d.errText = ""
	}
	return false, nil
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
	b.WriteString(d.path.View() + "\n\n")

	marker := "  "
	profile := s.Faint.Render(d.profiles[d.profile])
	if d.focus == ingestFieldProfile {
		marker = s.Cursor.Render("▸ ")
		profile = s.Subtle.Render(d.profiles[d.profile])
	}
	b.WriteString(marker + profile + "\n")

	if d.errText != "" {
		b.WriteString("\n" + s.Error.Render(d.errText))
	}
	hint := "enter decompose · tab profile · esc cancel"
	if d.focus == ingestFieldProfile {
		hint = "←/→ profile · enter decompose · esc cancel"
	}
	b.WriteString("\n" + s.Faint.Render(hint))
	return s.DialogFrame.Render(b.String())
}
