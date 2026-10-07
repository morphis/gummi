package ui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/ui/theme"
)

// settingsDialog is the settings overlay: the workspace's own knobs, which
// today is the one — the name this instance goes by, so boards running
// side by side can be told apart in a status bar and a browser tab. The
// web face's counterpart is views/settings.js, and both write through
// Shell.setName.
type settingsDialog struct {
	input    textinput.Model
	buttons  *buttonRow
	focus    int
	problem  string
	onSubmit func(name string) error
}

const (
	settingsFieldName = iota
	settingsFieldButtons
)

func newSettingsDialog(current string, onSubmit func(string) error) *settingsDialog {
	in := textinput.New()
	in.Placeholder = "e.g. staging, laptop, east"
	in.CharLimit = config.MaxNameLen
	in.SetWidth(32)
	in.SetValue(current)
	in.CursorEnd()
	in.Focus()
	return &settingsDialog{
		input: in, onSubmit: onSubmit,
		buttons: newButtonRow(button{label: "Cancel"}, button{label: "Save"}),
	}
}

// ID implements overlay.Dialog.
func (d *settingsDialog) ID() string { return "settings" }

func (d *settingsDialog) submit() (bool, tea.Cmd) {
	if err := d.onSubmit(d.input.Value()); err != nil {
		d.problem = err.Error()
		return false, nil
	}
	return true, nil
}

func (d *settingsDialog) setFocus(f int) {
	d.focus = f
	if f == settingsFieldName {
		d.input.Focus()
	} else {
		d.input.Blur()
	}
}

// HandleKey implements overlay.Dialog.
func (d *settingsDialog) HandleKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	switch key.String() {
	case "esc":
		return true, nil
	case "tab", "shift+tab":
		d.setFocus((d.focus + 1) % 2)
		return false, nil
	}
	if d.focus == settingsFieldButtons {
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
	d.problem = ""
	d.input, _ = d.input.Update(key)
	return false, nil
}

// HandlePaste implements overlay.Paster.
func (d *settingsDialog) HandlePaste(msg tea.PasteMsg) tea.Cmd {
	if d.focus == settingsFieldName {
		d.problem = ""
		d.input, _ = d.input.Update(msg)
	}
	return nil
}

// View implements overlay.Dialog.
func (d *settingsDialog) View(s *theme.Styles, w, h int) string {
	var b strings.Builder
	b.WriteString(s.DialogTitle.Render("settings") + "\n\n")
	b.WriteString(s.Faint.Render("name — how this gummi is told apart from the others (empty clears it)") + "\n\n")
	b.WriteString(d.input.View() + "\n")
	if d.problem != "" {
		b.WriteString(s.Error.Render(d.problem) + "\n")
	}
	b.WriteString("\n" + d.buttons.View(s, d.focus == settingsFieldButtons) + "\n")
	b.WriteString("\n" + s.Faint.Render("enter save · tab buttons · esc cancel"))
	return s.DialogFrame.Render(b.String())
}

// openSettings pushes the settings dialog.
func (m *Shell) openSettings() {
	m.Overlay.Push(newSettingsDialog(m.name, m.setName))
}

// setName stores the instance name in the workspace config and shows it
// at once. It is the one write both faces make.
func (m *Shell) setName(name string) error {
	name, err := config.ValidateName(name)
	if err != nil {
		return err
	}
	if err := config.SetName(m.ws.ConfigFile(), name); err != nil {
		return err
	}
	m.name = name
	return nil
}

// loadName reads the instance name the workspace config holds.
func (m *Shell) loadName() {
	if c, err := config.Load(m.ws.ConfigFile()); err == nil {
		m.name = c.Name
	}
}
