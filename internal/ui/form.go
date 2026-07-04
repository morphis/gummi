package ui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/morphia/gummi/internal/domain"
	"github.com/morphia/gummi/internal/ui/theme"
)

// defaultProfilePresets is the fallback profile list when no
// profiles.yaml is loaded.
var defaultProfilePresets = []string{"thrifty", "premium", "local-heavy"}

// form fields, in tab order.
const (
	fieldTitle = iota
	fieldOneLiner
	fieldProfile
	fieldSkipBrainstorm
	fieldSkipPlan
	fieldCount
)

// featureForm is the new-feature dialog: title, one-liner, profile,
// and the two skip flags (the only workflow flexibility, fixed at
// creation).
type featureForm struct {
	title    textinput.Model
	oneLiner textinput.Model
	profiles []string
	profile  int
	skip     domain.SkipFlags
	focus    int
	errText  string

	onSubmit func(formResult) tea.Cmd
}

// newFeatureForm builds the dialog; profiles are the selectable profile
// names (falling back to the built-in presets when empty), and onSubmit
// receives the validated fields.
func newFeatureForm(profiles []string, onSubmit func(formResult) tea.Cmd) *featureForm {
	if len(profiles) == 0 {
		profiles = defaultProfilePresets
	}
	title := textinput.New()
	title.Placeholder = "feature title"
	title.CharLimit = 80
	title.SetWidth(38)
	title.Focus()
	one := textinput.New()
	one.Placeholder = "one-liner (optional)"
	one.CharLimit = 120
	one.SetWidth(38)
	return &featureForm{title: title, oneLiner: one, profiles: profiles, onSubmit: onSubmit}
}

// ID implements overlay.Dialog.
func (d *featureForm) ID() string { return "new-feature" }

// HandleKey implements overlay.Dialog.
func (d *featureForm) HandleKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	switch key.String() {
	case "esc":
		return true, nil
	case "enter":
		title := strings.TrimSpace(d.title.Value())
		if title == "" {
			d.errText = "title must not be empty"
			return false, nil
		}
		if _, err := domain.Slugify(title); err != nil {
			d.errText = err.Error()
			return false, nil
		}
		res := formResult{
			Title:    title,
			OneLiner: strings.TrimSpace(d.oneLiner.Value()),
			Profile:  d.profiles[d.profile],
			Skip:     d.skip,
		}
		return true, d.onSubmit(res)
	case "tab", "down":
		d.setFocus((d.focus + 1) % fieldCount)
		return false, nil
	case "shift+tab", "up":
		d.setFocus((d.focus + fieldCount - 1) % fieldCount)
		return false, nil
	}

	switch d.focus {
	case fieldProfile:
		switch key.String() {
		case "left", "h":
			d.profile = (d.profile + len(d.profiles) - 1) % len(d.profiles)
		case "right", "l", "space":
			d.profile = (d.profile + 1) % len(d.profiles)
		}
	case fieldSkipBrainstorm:
		if key.String() == "space" {
			d.skip.Brainstorm = !d.skip.Brainstorm
		}
	case fieldSkipPlan:
		if key.String() == "space" {
			d.skip.Plan = !d.skip.Plan
		}
	case fieldTitle:
		d.title, _ = d.title.Update(key)
		d.errText = ""
	case fieldOneLiner:
		d.oneLiner, _ = d.oneLiner.Update(key)
	}
	return false, nil
}

func (d *featureForm) setFocus(f int) {
	d.focus = f
	d.title.Blur()
	d.oneLiner.Blur()
	switch f {
	case fieldTitle:
		d.title.Focus()
	case fieldOneLiner:
		d.oneLiner.Focus()
	}
}

// View implements overlay.Dialog.
func (d *featureForm) View(s *theme.Styles, w, h int) string {
	label := func(field int, text string) string {
		if d.focus == field {
			return s.KeyHint.Render("▸ " + text)
		}
		return s.Muted.Render("  " + text)
	}
	check := func(on bool) string {
		if on {
			return s.Success.Render("[x]")
		}
		return s.Faint.Render("[ ]")
	}

	var b strings.Builder
	b.WriteString(s.DialogTitle.Render("new feature") + "\n\n")
	b.WriteString(label(fieldTitle, "title    ") + d.title.View() + "\n")
	b.WriteString(label(fieldOneLiner, "one-liner") + d.oneLiner.View() + "\n")
	b.WriteString(label(fieldProfile, "profile  "))
	for i, p := range d.profiles {
		if i == d.profile {
			b.WriteString(s.PillMode.Render(p) + " ")
		} else {
			b.WriteString(s.Faint.Render(p) + " ")
		}
	}
	b.WriteString("\n")
	b.WriteString(label(fieldSkipBrainstorm, "skip brainstorm ") + check(d.skip.Brainstorm) + "\n")
	b.WriteString(label(fieldSkipPlan, "skip plan       ") + check(d.skip.Plan) + "\n")
	if d.errText != "" {
		b.WriteString("\n" + s.Error.Render(d.errText))
	}
	b.WriteString("\n" + s.Faint.Render("enter create · tab next · space toggle · esc cancel"))
	return s.DialogFrame.Render(b.String())
}
