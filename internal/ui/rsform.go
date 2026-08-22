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

// rsForm is the new-research dialog: a free-form brief textarea (first
// line becomes the card title, the rest seeds the doc's Brief section
// verbatim), an envelope input, and a demoted options row (profile ·
// repo). Unlike featureForm/bugForm there are no brainstorm/plan skip
// toggles — research has no such stages — and the envelope is required:
// RS carries no default budget of its own (SCOPE §Budget).
type rsForm struct {
	brief    textarea.Model
	env      textinput.Model
	profiles []string
	profile  int
	// repos are the configured selectable managed repositories; repoIdx
	// indexes a list whose first (0) entry is the workspace default (empty
	// name) and whose rest are the configured names.
	repos   []string
	repoIdx int
	focus   int
	errText string

	onSubmit func(rsFormResult) tea.Cmd
}

func newRSForm(profiles []string, repos []string, defaultEnvelope int, onSubmit func(rsFormResult) tea.Cmd) *rsForm {
	if len(profiles) == 0 {
		profiles = defaultProfilePresets
	}
	brief := textarea.New()
	brief.Placeholder = "the ask, in your own words…"
	brief.CharLimit = 4000
	brief.ShowLineNumbers = false
	brief.SetWidth(46)
	brief.SetHeight(4)
	brief.Focus()
	env := textinput.New()
	env.Placeholder = "credits (required)"
	env.SetWidth(46)
	env.CharLimit = 12
	env.SetValue(strconv.Itoa(defaultEnvelope))
	return &rsForm{brief: brief, env: env, profiles: profiles, repos: repos, onSubmit: onSubmit}
}

// repoName returns the currently selected repository name: "" for the
// workspace default, else the configured name.
func (d *rsForm) repoName() string {
	if d.repoIdx == 0 || len(d.repos) == 0 {
		return ""
	}
	return d.repos[d.repoIdx-1]
}

// repoLabel is the display label for the selected repository.
func (d *rsForm) repoLabel() string {
	if name := d.repoName(); name != "" {
		return name
	}
	return "default"
}

// ID implements overlay.Dialog.
func (d *rsForm) ID() string { return "new-research" }

// HandleKey implements overlay.Dialog.
func (d *rsForm) HandleKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	switch key.String() {
	case "esc":
		return true, nil
	case "enter":
		brief := strings.TrimSpace(d.brief.Value())
		if brief == "" {
			d.errText = "brief required"
			return false, nil
		}
		trimmedEnv := strings.TrimSpace(d.env.Value())
		if trimmedEnv == "" {
			d.errText = "envelope required"
			return false, nil
		}
		n, err := strconv.Atoi(trimmedEnv)
		if err != nil || n < 0 {
			d.errText = "envelope must be a non-negative number of credits"
			return false, nil
		}
		title, _, _ := domain.SplitFreeform(brief)
		if _, err := domain.Slugify(title); err != nil {
			d.errText = "brief must include a letter or digit"
			return false, nil
		}
		return true, d.onSubmit(rsFormResult{
			Brief:    brief,
			Profile:  d.profiles[d.profile],
			Repo:     d.repoName(),
			Envelope: &n,
		})
	case "alt+enter", "ctrl+j":
		if d.focus == fieldDesc {
			d.brief.InsertString("\n")
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
		case "r":
			// cycle the managed repository (default + configured names); no
			// repos configured means a no-op on a single-entry list.
			total := len(d.repos) + 1
			if total > 1 {
				d.repoIdx = (d.repoIdx + 1) % total
			}
		}
	case fieldDesc:
		d.brief, _ = d.brief.Update(key)
		d.errText = ""
	case fieldEnvelope:
		d.env, _ = d.env.Update(key)
		d.errText = ""
	}
	return false, nil
}

// HandlePaste implements overlay.Paster: pasted text goes into the brief
// while it's focused, newlines intact.
func (d *rsForm) HandlePaste(msg tea.PasteMsg) tea.Cmd {
	if d.focus == fieldDesc {
		d.brief, _ = d.brief.Update(msg)
		d.errText = ""
	}
	return nil
}

func (d *rsForm) setFocus(f int) {
	d.focus = f
	d.brief.Blur()
	d.env.Blur()
	switch f {
	case fieldDesc:
		d.brief.Focus()
	case fieldEnvelope:
		d.env.Focus()
	}
}

// View implements overlay.Dialog.
func (d *rsForm) View(s *theme.Styles, w, h int) string {
	var b strings.Builder
	b.WriteString(s.DialogTitle.Render("new research") + "\n\n")
	b.WriteString(d.brief.View() + "\n\n")
	b.WriteString(d.env.View() + "\n\n")

	marker := "  "
	profile := s.Faint.Render(d.profiles[d.profile])
	repo := s.Faint.Render("[" + d.repoLabel() + "]")
	if d.focus == fieldOpts {
		marker = s.Cursor.Render("▸ ")
		profile = s.Subtle.Render(d.profiles[d.profile])
		repo = s.Subtle.Render("[" + d.repoLabel() + "]")
	}
	row := marker + profile
	if len(d.repos) > 0 {
		// only when there is more than the default to choose among
		row += s.Faint.Render(" · ") + repo
	}
	b.WriteString(row + "\n")

	if d.errText != "" {
		b.WriteString("\n" + s.Error.Render(d.errText))
	}
	hint := "enter create · alt+enter newline · tab envelope · esc cancel"
	if len(d.repos) > 0 {
		hint += " · r repo"
	}
	switch d.focus {
	case fieldEnvelope:
		hint = "numeric credits (required) · enter create · esc cancel"
	case fieldOpts:
		hint = "←/→ profile"
		if len(d.repos) > 0 {
			hint = "←/→ profile · r repo"
		}
		hint += " · enter create · esc cancel"
	}
	b.WriteString("\n" + s.Faint.Render(hint))
	return s.DialogFrame.Render(b.String())
}
