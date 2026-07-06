package ui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/ui/theme"
)

// bugForm is the new-bug dialog: a title line plus a demoted options row
// (profile · severity · triage/diagnose skips). It mirrors the feature
// form — the triage stage develops reproduction and root cause, just as
// brainstorm develops a feature — but carries bug-shaped options.
type bugForm struct {
	desc     textinput.Model
	profiles []string
	profile  int
	sevs     []domain.Severity
	sev      int
	skip     domain.SkipFlags
	focus    int
	errText  string

	onSubmit func(bugFormResult) tea.Cmd
}

// bugSeverityChoices are the severities the form cycles through; the
// first ("") means unset — triage classifies it later.
var bugSeverityChoices = []domain.Severity{"", domain.SeverityCritical, domain.SeverityHigh, domain.SeverityMedium, domain.SeverityLow}

func newBugForm(profiles []string, onSubmit func(bugFormResult) tea.Cmd) *bugForm {
	if len(profiles) == 0 {
		profiles = defaultProfilePresets
	}
	desc := textinput.New()
	desc.Placeholder = "describe the bug…"
	desc.CharLimit = 120
	desc.SetWidth(46)
	desc.Focus()
	return &bugForm{desc: desc, profiles: profiles, sevs: bugSeverityChoices, onSubmit: onSubmit}
}

// ID implements overlay.Dialog.
func (d *bugForm) ID() string { return "new-bug" }

// HandleKey implements overlay.Dialog.
func (d *bugForm) HandleKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	switch key.String() {
	case "esc":
		return true, nil
	case "enter":
		desc := strings.TrimSpace(d.desc.Value())
		if desc == "" {
			d.errText = "description must not be empty"
			return false, nil
		}
		if _, err := domain.Slugify(desc); err != nil {
			d.errText = err.Error()
			return false, nil
		}
		return true, d.onSubmit(bugFormResult{
			Title:    desc,
			Severity: d.sevs[d.sev],
			Profile:  d.profiles[d.profile],
			Skip:     d.skip,
		})
	case "tab", "down":
		d.setFocus((d.focus + 1) % fieldCount)
		return false, nil
	case "shift+tab", "up":
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
		case "s":
			d.sev = (d.sev + 1) % len(d.sevs)
		case "t":
			d.skip.Triage = !d.skip.Triage
		case "d":
			d.skip.Diagnose = !d.skip.Diagnose
		}
	case fieldDesc:
		d.desc, _ = d.desc.Update(key)
		d.errText = ""
	}
	return false, nil
}

func (d *bugForm) setFocus(f int) {
	d.focus = f
	if f == fieldDesc {
		d.desc.Focus()
	} else {
		d.desc.Blur()
	}
}

// skipLabel names the workflow route the bug's skip flags select.
func (d *bugForm) skipLabel() string {
	switch {
	case d.skip.Triage && d.skip.Diagnose:
		return "skip triage+diagnose"
	case d.skip.Triage:
		return "skip triage"
	case d.skip.Diagnose:
		return "skip diagnose"
	}
	return "full workflow"
}

func (d *bugForm) sevLabel() string {
	if d.sevs[d.sev] == "" {
		return "severity: unset"
	}
	return "severity: " + string(d.sevs[d.sev])
}

// View implements overlay.Dialog.
func (d *bugForm) View(s *theme.Styles, w, h int) string {
	var b strings.Builder
	b.WriteString(s.DialogTitle.Render("new bug") + "\n\n")
	b.WriteString(d.desc.View() + "\n\n")

	marker := "  "
	profile := s.Faint.Render(d.profiles[d.profile])
	sev := s.Faint.Render(d.sevLabel())
	skips := s.Faint.Render(d.skipLabel())
	if d.focus == fieldOpts {
		marker = s.Cursor.Render("▸ ")
		profile = s.Subtle.Render(d.profiles[d.profile])
	}
	if d.sevs[d.sev] != "" {
		sev = s.Warning.Render(d.sevLabel())
	}
	if d.skip.Triage || d.skip.Diagnose {
		skips = s.Warning.Render(d.skipLabel())
	}
	b.WriteString(marker + profile + s.Faint.Render(" · ") + sev + s.Faint.Render(" · ") + skips + "\n")

	if d.errText != "" {
		b.WriteString("\n" + s.Error.Render(d.errText))
	}
	hint := "enter create · tab options · esc cancel"
	if d.focus == fieldOpts {
		hint = "←/→ profile · s severity · t/d toggle skips · enter create · esc cancel"
	}
	b.WriteString("\n" + s.Faint.Render(hint))
	return s.DialogFrame.Render(b.String())
}
