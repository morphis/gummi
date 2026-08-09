package ui

import (
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
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
	fieldEnvelope
	fieldOpts
	fieldCount
)

// featureForm is the new-feature dialog: a free-form description — the
// first line becomes the card title, anything beyond it seeds the
// draft's Problem section for the brainstorm stage to develop. Profile
// and the skip flags (the only workflow flexibility, fixed at creation)
// share a single demoted options row. The envelope line sits between
// them, pre-filled with the global default so the user can override it
// at creation time without a second trip to the detail view.
type featureForm struct {
	desc     textarea.Model
	env      textinput.Model
	profiles []string
	profile  int
	skip     domain.SkipFlags
	focus    int
	errText  string

	onSubmit func(formResult) tea.Cmd
}

// newFeatureForm builds the dialog; profiles are the selectable profile
// names in display order, first selected (falling back to the built-in
// presets when empty), defaultEnvelope is the global default pre-filled
// into the envelope input, and onSubmit receives the validated fields.
func newFeatureForm(profiles []string, defaultEnvelope int, onSubmit func(formResult) tea.Cmd) *featureForm {
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
	env := textinput.New()
	env.Placeholder = "credits (0 = uncapped)"
	env.SetWidth(46)
	env.CharLimit = 12
	env.SetValue(strconv.Itoa(defaultEnvelope))
	return &featureForm{desc: desc, env: env, profiles: profiles, onSubmit: onSubmit}
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
		// an empty envelope is the "use default" signal; a non-negative
		// integer becomes an explicit envelope (0 = uncapped)
		var env *int
		if trimmed := strings.TrimSpace(d.env.Value()); trimmed != "" {
			n, err := strconv.Atoi(trimmed)
			if err != nil || n < 0 {
				d.errText = "envelope must be a non-negative number of credits"
				return false, nil
			}
			env = &n
		}
		res := formResult{
			Desc:     desc,
			Profile:  d.profiles[d.profile],
			Skip:     d.skip,
			Envelope: env,
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
	case fieldEnvelope:
		d.env, _ = d.env.Update(key)
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
	d.desc.Blur()
	d.env.Blur()
	switch f {
	case fieldDesc:
		d.desc.Focus()
	case fieldEnvelope:
		d.env.Focus()
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
	b.WriteString(d.env.View() + "\n\n")

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
	hint := "enter create · alt+enter newline · tab envelope · esc cancel"
	switch d.focus {
	case fieldEnvelope:
		hint = "numeric credits (0 = uncapped) · enter create · esc cancel"
	case fieldOpts:
		hint = "←/→ profile · q quick · b/p toggle skips · enter create · esc cancel"
	}
	b.WriteString("\n" + s.Faint.Render(hint))
	return s.DialogFrame.Render(b.String())
}
