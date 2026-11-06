package ui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/ui/theme"
	"github.com/morphis/gummi/internal/worktree"
)

// settingsDialog is the settings overlay: the workspace's own knobs — the
// name this instance goes by, so boards running side by side can be told
// apart in a status bar and a browser tab, and the co-authors named on
// every commit. The web face's counterpart is views/settings.js, and both
// write through Shell.setName and Shell.setCoAuthors.
type settingsDialog struct {
	input     textinput.Model
	coAuthors textarea.Model
	// coAuthorsWas is the box as it opened: an untouched box is not
	// written, so saving a name never copies a user-level list into the
	// workspace config.
	coAuthorsWas string
	buttons      *buttonRow
	focus        int
	problem      string
	onSubmit     func(name string) error
	onCoAuthors  func(list []string) error
	// workflows is the toggle as it stands; workflowsWas is how it opened,
	// so an untouched toggle is not written.
	workflows, workflowsWas bool
	onWorkflows             func(on bool) error
}

const (
	settingsFieldName = iota
	settingsFieldCoAuthors
	settingsFieldWorkflows
	settingsFieldButtons
	settingsFields
)

func newSettingsDialog(current string, coAuthors []string, onSubmit func(string) error, onCoAuthors func([]string) error, workflows bool, onWorkflows func(bool) error) *settingsDialog {
	in := textinput.New()
	in.Placeholder = "e.g. staging, laptop, east"
	in.CharLimit = config.MaxNameLen
	in.SetWidth(32)
	in.SetValue(current)
	in.CursorEnd()
	in.Focus()
	ca := textarea.New()
	ca.Placeholder = "Ada Lovelace <ada@example.com>"
	ca.ShowLineNumbers = false
	ca.SetWidth(56)
	ca.SetHeight(3)
	was := strings.Join(coAuthors, "\n")
	ca.SetValue(was)
	ca.Blur()
	return &settingsDialog{
		input: in, coAuthors: ca, coAuthorsWas: was,
		onSubmit: onSubmit, onCoAuthors: onCoAuthors,
		workflows: workflows, workflowsWas: workflows, onWorkflows: onWorkflows,
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
	if text := d.coAuthors.Value(); text != d.coAuthorsWas {
		if err := d.onCoAuthors(strings.Split(text, "\n")); err != nil {
			d.problem = "co-authors: " + err.Error()
			d.setFocus(settingsFieldCoAuthors)
			return false, nil
		}
	}
	if d.workflows != d.workflowsWas {
		if err := d.onWorkflows(d.workflows); err != nil {
			d.problem = "workflows: " + err.Error()
			d.setFocus(settingsFieldWorkflows)
			return false, nil
		}
	}
	return true, nil
}

func (d *settingsDialog) setFocus(f int) {
	d.focus = f
	d.input.Blur()
	d.coAuthors.Blur()
	switch f {
	case settingsFieldName:
		d.input.Focus()
	case settingsFieldCoAuthors:
		d.coAuthors.Focus()
	}
}

// HandleKey implements overlay.Dialog. Enter saves from the name field and
// starts a new line in the co-authors box, where ctrl+s saves.
func (d *settingsDialog) HandleKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	switch key.String() {
	case "esc":
		return true, nil
	case "tab":
		d.setFocus((d.focus + 1) % settingsFields)
		return false, nil
	case "shift+tab":
		d.setFocus((d.focus + settingsFields - 1) % settingsFields)
		return false, nil
	case "ctrl+s":
		return d.submit()
	}
	switch d.focus {
	case settingsFieldButtons:
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
	case settingsFieldCoAuthors:
		d.problem = ""
		d.coAuthors, _ = d.coAuthors.Update(key)
		return false, nil
	case settingsFieldWorkflows:
		switch key.String() {
		case " ", "space", "enter", "x":
			d.workflows = !d.workflows
			d.problem = ""
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
	switch d.focus {
	case settingsFieldName:
		d.problem = ""
		d.input, _ = d.input.Update(msg)
	case settingsFieldCoAuthors:
		d.problem = ""
		d.coAuthors, _ = d.coAuthors.Update(msg)
	}
	return nil
}

// View implements overlay.Dialog.
func (d *settingsDialog) View(s *theme.Styles, w, h int) string {
	var b strings.Builder
	b.WriteString(s.DialogTitle.Render("settings") + "\n\n")
	b.WriteString(s.Faint.Render("name — how this gummi is told apart from the others (empty clears it)") + "\n\n")
	b.WriteString(d.input.View() + "\n\n")
	b.WriteString(s.Faint.Render("co-authors — one \"Name <email>\" per line, named in a Co-authored-by") + "\n")
	b.WriteString(s.Faint.Render(fmt.Sprintf("trailer on every commit (at most %d; empty turns it off)", config.MaxCoAuthors)) + "\n\n")
	b.WriteString(d.coAuthors.View() + "\n\n")
	box := "[ ]"
	if d.workflows {
		box = "[x]"
	}
	label := box + " workflows — sessions are offered specs and workflow runs"
	if d.focus == settingsFieldWorkflows {
		label = s.DialogTitle.Render(label)
	}
	b.WriteString(label + "\n")
	b.WriteString(s.Faint.Render("off by default; a session's own switch wins. A session already running") + "\n")
	b.WriteString(s.Faint.Render("picks a change up when it next starts (space toggles)") + "\n")
	if d.problem != "" {
		b.WriteString(s.Error.Render(d.problem) + "\n")
	}
	b.WriteString("\n" + d.buttons.View(s, d.focus == settingsFieldButtons) + "\n")
	b.WriteString("\n" + s.Faint.Render("ctrl+s save · tab next field · esc cancel"))
	return s.DialogFrame.Render(b.String())
}

// openSettings pushes the settings dialog.
func (m *Shell) openSettings() {
	m.Overlay.Push(newSettingsDialog(m.name, worktree.CoAuthors(), m.setName, m.setCoAuthors, m.workflowsDefault(), m.setWorkflows))
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

// setCoAuthors stores the workspace's co-authors in its config and names
// them for every commit this process makes from here on: what the config
// now resolves to, which is a user-level list when the workspace's own is
// cleared.
func (m *Shell) setCoAuthors(list []string) error {
	list, err := config.ParseCoAuthors(list)
	if err != nil {
		return err
	}
	if err := config.SetCoAuthors(m.ws.ConfigFile(), list); err != nil {
		return err
	}
	userPath, err := config.UserConfigPath()
	if err != nil {
		userPath = ""
	}
	if c, _, err := config.LoadLayered(userPath, m.ws.ConfigFile()); err == nil {
		list = c.CoAuthors
	}
	return worktree.UseCoAuthors(list)
}

// workflowsDefault is the workspace's workflows setting: the live engine's,
// else the config's.
func (m *Shell) workflowsDefault() bool {
	if m.engine != nil {
		return m.engine.WorkflowsDefault()
	}
	c, _, err := m.layeredConfig()
	return err == nil && c.WorkflowsOn()
}

func (m *Shell) layeredConfig() (config.Config, map[string]string, error) {
	userPath, err := config.UserConfigPath()
	if err != nil {
		userPath = ""
	}
	return config.LoadLayered(userPath, m.ws.ConfigFile())
}

// setWorkflows stores the workspace's workflows setting in its config and
// makes it the live engine's. It is the one write both faces make.
func (m *Shell) setWorkflows(on bool) error {
	if err := config.SetWorkflows(m.ws.ConfigFile(), on); err != nil {
		return err
	}
	if c, _, err := m.layeredConfig(); err == nil {
		on = c.WorkflowsOn()
	}
	if m.engine != nil {
		m.engine.SetWorkflowsDefault(on)
	}
	return nil
}

// loadName reads the instance name the workspace config holds.
func (m *Shell) loadName() {
	if c, err := config.Load(m.ws.ConfigFile()); err == nil {
		m.name = c.Name
	}
}
