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

// research form fields, in tab order. fieldRepo is skipped when the repo
// picker has nothing to choose (see advanceFocus); fieldButtons is the
// last stop, so tab from it wraps back to the first field.
const (
	rsFieldRepo = iota
	rsFieldBrief
	rsFieldEnvelope
	rsFieldProfile
	rsFieldButtons
	rsFieldCount
)

// rsForm is the new-research dialog: a free-form brief textarea (first
// line becomes the card title, the rest seeds the doc's Brief section
// verbatim), an envelope input, and repo/profile each their own tab stop.
// Unlike featureForm/bugForm there are no brainstorm/plan skip toggles —
// research has no such stages — and the envelope is required: RS carries
// no default budget of its own (SCOPE §Budget).
type rsForm struct {
	brief    textarea.Model
	env      textinput.Model
	profiles []string
	profile  int
	repo     repoPicker
	focus    int
	errText  string
	buttons  *buttonRow

	onSubmit func(rsFormResult) tea.Cmd
}

func newRSForm(profiles []string, repos []string, hasDefault bool, defaultEnvelope int, onSubmit func(rsFormResult) tea.Cmd) *rsForm {
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
	return &rsForm{
		brief: brief, env: env, profiles: profiles,
		repo: newRepoPicker(repos, hasDefault), focus: rsFieldBrief,
		buttons:  newButtonRow(button{label: "Cancel"}, button{label: "Create"}),
		onSubmit: onSubmit,
	}
}

// ID implements overlay.Dialog.
func (d *rsForm) ID() string { return "new-research" }

// submit validates and fires onSubmit, matching enter's own handling from
// any other field exactly — the button row's Create button is just another
// way to reach the same action.
func (d *rsForm) submit() (bool, tea.Cmd) {
	if d.repo.needsChoice() {
		d.errText = repoUnchosenErr
		d.setFocus(rsFieldRepo)
		return false, nil
	}
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
		Repo:     d.repo.name(),
		Envelope: &n,
	})
}

// HandleKey implements overlay.Dialog.
func (d *rsForm) HandleKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	switch key.String() {
	case "esc":
		return true, nil
	case "alt+enter", "ctrl+j":
		if d.focus == rsFieldBrief {
			d.brief.InsertString("\n")
			d.errText = ""
		}
		return false, nil
	case "tab":
		d.advanceFocus(1)
		return false, nil
	case "shift+tab":
		d.advanceFocus(-1)
		return false, nil
	}

	if d.focus == rsFieldButtons {
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
	case rsFieldRepo:
		if delta, ok := selectCycleDelta(key.String()); ok {
			d.repo.cycle(delta)
			// clear a pending "choose a repo" refusal the moment they do
			d.errText = ""
		}
	case rsFieldProfile:
		if delta, ok := selectCycleDelta(key.String()); ok {
			n := len(d.profiles)
			d.profile = ((d.profile+delta)%n + n) % n
		}
	case rsFieldBrief:
		d.brief, _ = d.brief.Update(key)
		d.errText = ""
	case rsFieldEnvelope:
		d.env, _ = d.env.Update(key)
		d.errText = ""
	}
	return false, nil
}

// HandlePaste implements overlay.Paster: pasted text goes into the brief
// while it's focused, newlines intact.
func (d *rsForm) HandlePaste(msg tea.PasteMsg) tea.Cmd {
	if d.focus == rsFieldBrief {
		d.brief, _ = d.brief.Update(msg)
		d.errText = ""
	}
	return nil
}

// advanceFocus moves focus by dir (±1), wrapping, and skips the repo stop
// when there's nothing to choose there.
func (d *rsForm) advanceFocus(dir int) {
	f := d.focus
	for {
		f = (f + dir + rsFieldCount) % rsFieldCount
		if f != rsFieldRepo || d.repo.multi() {
			break
		}
	}
	d.setFocus(f)
}

func (d *rsForm) setFocus(f int) {
	d.focus = f
	d.brief.Blur()
	d.env.Blur()
	switch f {
	case rsFieldBrief:
		d.brief.Focus()
	case rsFieldEnvelope:
		d.env.Focus()
	}
}

// View implements overlay.Dialog.
func (d *rsForm) View(s *theme.Styles, w, h int) string {
	// base static rows: title+blank(2), blank-after-brief(1),
	// envelope+blank(2), profile(1), blank+buttons(2), blank+hint(2); +2
	// more when the repo field renders (repo+blank).
	staticRows := 10
	if d.repo.multi() {
		staticRows += 2
	}
	briefW, briefH := dialogDescSize(w, h, staticRows)
	d.brief.SetWidth(briefW)
	d.brief.SetHeight(briefH)

	var b strings.Builder
	b.WriteString(s.DialogTitle.Render("new research") + "\n\n")
	if d.repo.multi() {
		b.WriteString(fieldRow(s, d.focus == rsFieldRepo, "repo: "+d.repo.label()) + "\n\n")
	}
	b.WriteString(d.brief.View() + "\n\n")
	b.WriteString(d.env.View() + "\n\n")
	b.WriteString(fieldRow(s, d.focus == rsFieldProfile, "profile: "+d.profiles[d.profile]) + "\n")
	b.WriteString("\n" + d.buttons.View(s, d.focus == rsFieldButtons) + "\n")

	if d.errText != "" {
		b.WriteString("\n" + s.Error.Render(d.errText))
	}
	hint := "tab next · ←/→ change · enter create · esc cancel"
	switch d.focus {
	case rsFieldBrief:
		hint = "alt+enter newline · " + hint
	case rsFieldButtons:
		hint = "←/→ buttons · enter activate · tab next · esc cancel"
	}
	b.WriteString("\n" + s.Faint.Render(hint))
	return s.DialogFrame.Render(b.String())
}
