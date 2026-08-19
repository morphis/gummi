package ui

import (
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/ui/theme"
)

// bugForm is the new-bug dialog: a title line, an envelope line, plus a
// demoted options row (profile · severity · triage/diagnose skips). It
// mirrors the feature form — the triage stage develops reproduction and
// root cause, just as brainstorm develops a feature — but carries
// bug-shaped options.
type bugForm struct {
	desc     textinput.Model
	env      textinput.Model
	profiles []string
	profile  int
	sevs     []domain.Severity
	sev      int
	// repos are the configured selectable managed repositories; repoIdx
	// indexes a list whose first (0) entry is the workspace default (empty
	// name) and whose rest are the configured names.
	repos   []string
	repoIdx int
	skip    domain.SkipFlags
	focus   int
	errText string

	onSubmit func(bugFormResult) tea.Cmd
}

// bugSeverityChoices are the severities the form cycles through; the
// first ("") means unset — triage classifies it later.
var bugSeverityChoices = []domain.Severity{"", domain.SeverityCritical, domain.SeverityHigh, domain.SeverityMedium, domain.SeverityLow}

func newBugForm(profiles []string, repos []string, defaultEnvelope int, onSubmit func(bugFormResult) tea.Cmd) *bugForm {
	if len(profiles) == 0 {
		profiles = defaultProfilePresets
	}
	desc := textinput.New()
	desc.Placeholder = "describe the bug…"
	desc.CharLimit = 120
	desc.SetWidth(46)
	desc.Focus()
	env := textinput.New()
	env.Placeholder = "credits (0 = uncapped)"
	env.SetWidth(46)
	env.CharLimit = 12
	env.SetValue(strconv.Itoa(defaultEnvelope))
	return &bugForm{desc: desc, env: env, profiles: profiles, sevs: bugSeverityChoices, repos: repos, onSubmit: onSubmit}
}

// repoName returns the currently selected repository name: "" for the
// workspace default, else the configured name.
func (d *bugForm) repoName() string {
	if d.repoIdx == 0 || len(d.repos) == 0 {
		return ""
	}
	return d.repos[d.repoIdx-1]
}

// repoLabel is the display label for the selected repository.
func (d *bugForm) repoLabel() string {
	if name := d.repoName(); name != "" {
		return name
	}
	return "default"
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
		var env *int
		if trimmed := strings.TrimSpace(d.env.Value()); trimmed != "" {
			n, err := strconv.Atoi(trimmed)
			if err != nil || n < 0 {
				d.errText = "envelope must be a non-negative number of credits"
				return false, nil
			}
			env = &n
		}
		return true, d.onSubmit(bugFormResult{
			Title:    desc,
			Severity: d.sevs[d.sev],
			Profile:  d.profiles[d.profile],
			Skip:     d.skip,
			Envelope: env,
			Repo:     d.repoName(),
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
		case "r":
			// cycle the managed repository (default + configured names); no
			// repos configured means a no-op on a single-entry list.
			total := len(d.repos) + 1
			if total > 1 {
				d.repoIdx = (d.repoIdx + 1) % total
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
// description while it's focused.
func (d *bugForm) HandlePaste(msg tea.PasteMsg) tea.Cmd {
	if d.focus == fieldDesc {
		d.desc, _ = d.desc.Update(msg)
		d.errText = ""
	}
	return nil
}

func (d *bugForm) setFocus(f int) {
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
	b.WriteString(d.env.View() + "\n\n")

	marker := "  "
	profile := s.Faint.Render(d.profiles[d.profile])
	sev := s.Faint.Render(d.sevLabel())
	skips := s.Faint.Render(d.skipLabel())
	repo := s.Faint.Render("[" + d.repoLabel() + "]")
	if d.focus == fieldOpts {
		marker = s.Cursor.Render("▸ ")
		profile = s.Subtle.Render(d.profiles[d.profile])
		repo = s.Subtle.Render("[" + d.repoLabel() + "]")
	}
	if d.sevs[d.sev] != "" {
		sev = s.Warning.Render(d.sevLabel())
	}
	if d.skip.Triage || d.skip.Diagnose {
		skips = s.Warning.Render(d.skipLabel())
	}
	row := marker + profile + s.Faint.Render(" · ") + sev + s.Faint.Render(" · ") + skips
	if len(d.repos) > 0 {
		// only when there is more than the default to choose among
		row += s.Faint.Render(" · ") + repo
	}
	b.WriteString(row + "\n")

	if d.errText != "" {
		b.WriteString("\n" + s.Error.Render(d.errText))
	}
	hint := "enter create · tab envelope · esc cancel"
	if len(d.repos) > 0 {
		hint += " · r repo"
	}
	switch d.focus {
	case fieldEnvelope:
		hint = "numeric credits (0 = uncapped) · enter create · esc cancel"
	case fieldOpts:
		hint = "←/→ profile · s severity · t/d toggle skips"
		if len(d.repos) > 0 {
			hint = "←/→ profile · s severity · t/d skips · r repo"
		}
		hint += " · enter create · esc cancel"
	}
	b.WriteString("\n" + s.Faint.Render(hint))
	return s.DialogFrame.Render(b.String())
}
