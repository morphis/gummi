package ui

import (
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/ui/theme"
)

// defaultProfilePresets is the fallback profile list when no
// profiles.yaml is loaded.
var defaultProfilePresets = []string{"thrifty", "premium", "local-heavy"}

// form fields, in tab order.
const (
	fieldDesc = iota
	fieldOpts
	fieldCount
)

// featureForm is the new-feature dialog: a free-form description — the
// first line becomes the card title, anything beyond it seeds the
// draft's Problem section for the brainstorm stage to develop. Profile
// and the skip flags (the only workflow flexibility, fixed at creation)
// share a single demoted options row.
type featureForm struct {
	desc     textarea.Model
	profiles []string
	profile  int
	skip     domain.SkipFlags
	focus    int
	errText  string

	onSubmit func(formResult) tea.Cmd
}

// newFeatureForm builds the dialog; profiles are the selectable profile
// names in display order, first selected (falling back to the built-in
// presets when empty), and onSubmit receives the validated fields.
func newFeatureForm(profiles []string, onSubmit func(formResult) tea.Cmd) *featureForm {
	if len(profiles) == 0 {
		profiles = defaultProfilePresets
	}
	desc := textarea.New()
	desc.Placeholder = "describe the feature…"
	desc.CharLimit = 4000
	desc.ShowLineNumbers = false
	desc.SetWidth(46)
	desc.SetHeight(4)
	desc.Focus()
	return &featureForm{desc: desc, profiles: profiles, onSubmit: onSubmit}
}

// ID implements overlay.Dialog.
func (d *featureForm) ID() string { return "new-feature" }

// HandleKey implements overlay.Dialog.
func (d *featureForm) HandleKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	switch key.String() {
	case "esc":
		return true, nil
	case "enter":
		desc := strings.TrimSpace(d.desc.Value())
		if desc == "" {
			d.errText = "description must not be empty"
			return false, nil
		}
		// validate the slug of the derived title (what creation will use),
		// not the whole description
		title, _, _ := domain.SplitFreeform(desc)
		if _, err := domain.Slugify(title); err != nil {
			d.errText = err.Error()
			return false, nil
		}
		res := formResult{
			Desc:    desc,
			Profile: d.profiles[d.profile],
			Skip:    d.skip,
		}
		return true, d.onSubmit(res)
	case "alt+enter", "ctrl+j":
		if d.focus == fieldDesc {
			d.desc.InsertString("\n")
			d.errText = ""
		}
		return false, nil
	case "tab":
		d.setFocus((d.focus + 1) % fieldCount)
		return false, nil
	case "shift+tab":
		d.setFocus((d.focus + fieldCount - 1) % fieldCount)
		return false, nil
	}

	switch d.focus {
	case fieldOpts:
		switch key.String() {
		case "left", "h":
			d.profile = (d.profile + len(d.profiles) - 1) % len(d.profiles)
		case "right", "l", "space":
			d.profile = (d.profile + 1) % len(d.profiles)
		case "b":
			d.skip.Brainstorm = !d.skip.Brainstorm
			d.skip.Quick = false
		case "p":
			d.skip.Plan = !d.skip.Plan
			d.skip.Quick = false
		case "q":
			// the quick route is a preset, not a fourth flag: one keystroke
			// in, one keystroke back to the full workflow
			if d.skip.Quick {
				d.skip = domain.SkipFlags{}
			} else {
				d.skip = domain.QuickRoute()
			}
		}
	case fieldDesc:
		d.desc, _ = d.desc.Update(key)
		d.errText = ""
	}
	return false, nil
}

// HandlePaste implements overlay.Paster: pasted text goes into the
// description while it's focused, newlines intact.
func (d *featureForm) HandlePaste(msg tea.PasteMsg) tea.Cmd {
	if d.focus == fieldDesc {
		d.desc, _ = d.desc.Update(msg)
		d.errText = ""
	}
	return nil
}

func (d *featureForm) setFocus(f int) {
	d.focus = f
	if f == fieldDesc {
		d.desc.Focus()
	} else {
		d.desc.Blur()
	}
}

// skipLabel names the workflow route the skip flags select.
func (d *featureForm) skipLabel() string {
	switch {
	case d.skip.Quick:
		return "quick — spec in one pass, then implement"
	case d.skip.Brainstorm && d.skip.Plan:
		return "skip brainstorm+plan"
	case d.skip.Brainstorm:
		return "skip brainstorm"
	case d.skip.Plan:
		return "skip plan"
	}
	return "full workflow"
}

// View implements overlay.Dialog.
func (d *featureForm) View(s *theme.Styles, w, h int) string {
	var b strings.Builder
	b.WriteString(s.DialogTitle.Render("new feature") + "\n\n")
	b.WriteString(d.desc.View() + "\n\n")

	// the options row: quiet until focused, skips flagged when set
	marker := "  "
	profile := s.Faint.Render(d.profiles[d.profile])
	skips := s.Faint.Render(d.skipLabel())
	if d.focus == fieldOpts {
		marker = s.Cursor.Render("▸ ")
		profile = s.Subtle.Render(d.profiles[d.profile])
	}
	if d.skip.Brainstorm || d.skip.Plan {
		skips = s.Warning.Render(d.skipLabel())
	}
	b.WriteString(marker + profile + s.Faint.Render(" · ") + skips + "\n")

	if d.errText != "" {
		b.WriteString("\n" + s.Error.Render(d.errText))
	}
	hint := "enter create · alt+enter newline · tab options · esc cancel"
	if d.focus == fieldOpts {
		hint = "←/→ profile · q quick · b/p toggle skips · enter create · esc cancel"
	}
	b.WriteString("\n" + s.Faint.Render(hint))
	return s.DialogFrame.Render(b.String())
}
