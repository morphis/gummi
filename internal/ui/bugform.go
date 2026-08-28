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

// bugRoute is one state of the bug workflow-route field, mirroring
// featureRoute: a display label paired with the domain.SkipFlags it
// selects.
type bugRoute struct {
	label string
	skip  domain.SkipFlags
}

var bugRoutes = []bugRoute{
	{"full workflow", domain.SkipFlags{}},
	{"skip triage", domain.SkipFlags{Triage: true}},
	{"skip diagnose", domain.SkipFlags{Diagnose: true}},
	{"skip triage+diagnose", domain.SkipFlags{Triage: true, Diagnose: true}},
}

// bugSeverityChoices are the severities the form cycles through; the
// first ("") means unset — triage classifies it later.
var bugSeverityChoices = []domain.Severity{"", domain.SeverityCritical, domain.SeverityHigh, domain.SeverityMedium, domain.SeverityLow}

// bug form fields, in tab order. fieldRepo is skipped when the repo
// picker has nothing to choose (see advanceFocus); fieldButtons is the
// last stop, so tab from it wraps back to the first field.
const (
	bugFieldRepo = iota
	bugFieldDesc
	bugFieldEnvelope
	bugFieldProfile
	bugFieldSeverity
	bugFieldRoute
	bugFieldButtons
	bugFieldCount
)

// bugForm is the new-bug dialog: a title line, an envelope line, plus
// repo/profile/severity/route each its own tab stop, cycled with ←/→ —
// no mnemonic keys. It mirrors featureForm — the triage stage develops
// reproduction and root cause, just as brainstorm develops a feature —
// but carries bug-shaped options.
type bugForm struct {
	desc     textarea.Model
	env      textinput.Model
	profiles []string
	profile  int
	sev      int
	repo     repoPicker
	route    int
	focus    int
	errText  string
	buttons  *buttonRow

	onSubmit func(bugFormResult) tea.Cmd
}

func newBugForm(profiles []string, repos []string, hasDefault bool, defaultEnvelope int, onSubmit func(bugFormResult) tea.Cmd) *bugForm {
	if len(profiles) == 0 {
		profiles = defaultProfilePresets
	}
	desc := textarea.New()
	desc.Placeholder = "describe the bug…"
	desc.CharLimit = 4096
	desc.ShowLineNumbers = false
	desc.SetWidth(descWidthMin)
	desc.SetHeight(descHeightMin)
	desc.Focus()
	env := textinput.New()
	env.Placeholder = "credits (0 = uncapped)"
	env.SetWidth(46)
	env.CharLimit = 12
	env.SetValue(strconv.Itoa(defaultEnvelope))
	return &bugForm{
		desc: desc, env: env, profiles: profiles,
		repo: newRepoPicker(repos, hasDefault), focus: bugFieldDesc,
		buttons:  newButtonRow(button{label: "Cancel"}, button{label: "Create"}),
		onSubmit: onSubmit,
	}
}

// ID implements overlay.Dialog.
func (d *bugForm) ID() string { return "new-bug" }

// submit validates and fires onSubmit, matching enter's own handling from
// any other field exactly — the button row's Create button is just another
// way to reach the same action.
func (d *bugForm) submit() (bool, tea.Cmd) {
	desc := strings.TrimSpace(d.desc.Value())
	if desc == "" {
		d.errText = "description must not be empty"
		return false, nil
	}
	// validate the slug of the derived title (what creation will use),
	// not the whole description
	title, oneLiner, seed := domain.SplitFreeform(desc)
	if _, err := domain.Slugify(title); err != nil {
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
		Title:    title,
		OneLiner: oneLiner,
		Seed:     seed,
		Severity: bugSeverityChoices[d.sev],
		Profile:  d.profiles[d.profile],
		Skip:     bugRoutes[d.route].skip,
		Envelope: env,
		Repo:     d.repo.name(),
	})
}

// HandleKey implements overlay.Dialog.
func (d *bugForm) HandleKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	switch key.String() {
	case "esc":
		return true, nil
	case "alt+enter", "ctrl+j":
		if d.focus == bugFieldDesc {
			d.desc.InsertString("\n")
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

	if d.focus == bugFieldButtons {
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
	case bugFieldRepo:
		if delta, ok := selectCycleDelta(key.String()); ok {
			d.repo.cycle(delta)
		}
	case bugFieldProfile:
		if delta, ok := selectCycleDelta(key.String()); ok {
			n := len(d.profiles)
			d.profile = ((d.profile+delta)%n + n) % n
		}
	case bugFieldSeverity:
		if delta, ok := selectCycleDelta(key.String()); ok {
			n := len(bugSeverityChoices)
			d.sev = ((d.sev+delta)%n + n) % n
		}
	case bugFieldRoute:
		if delta, ok := selectCycleDelta(key.String()); ok {
			n := len(bugRoutes)
			d.route = ((d.route+delta)%n + n) % n
		}
	case bugFieldDesc:
		d.desc, _ = d.desc.Update(key)
		d.errText = ""
	case bugFieldEnvelope:
		d.env, _ = d.env.Update(key)
		d.errText = ""
	}
	return false, nil
}

// HandlePaste implements overlay.Paster: pasted text goes into the
// description while it's focused.
func (d *bugForm) HandlePaste(msg tea.PasteMsg) tea.Cmd {
	if d.focus == bugFieldDesc {
		d.desc, _ = d.desc.Update(msg)
		d.errText = ""
	}
	return nil
}

// advanceFocus moves focus by dir (±1), wrapping, and skips the repo stop
// when there's nothing to choose there.
func (d *bugForm) advanceFocus(dir int) {
	f := d.focus
	for {
		f = (f + dir + bugFieldCount) % bugFieldCount
		if f != bugFieldRepo || d.repo.multi() {
			break
		}
	}
	d.setFocus(f)
}

func (d *bugForm) setFocus(f int) {
	d.focus = f
	d.desc.Blur()
	d.env.Blur()
	switch f {
	case bugFieldDesc:
		d.desc.Focus()
	case bugFieldEnvelope:
		d.env.Focus()
	}
}

func (d *bugForm) sevLabel() string {
	if bugSeverityChoices[d.sev] == "" {
		return "severity: unset"
	}
	return "severity: " + string(bugSeverityChoices[d.sev])
}

// View implements overlay.Dialog.
func (d *bugForm) View(s *theme.Styles, w, h int) string {
	// base static rows: title+blank(2), blank-after-desc(1),
	// envelope+blank(2), profile+severity+route(3), blank+buttons(2),
	// blank+hint(2); +2 more when the repo field renders (repo+blank).
	staticRows := 12
	if d.repo.multi() {
		staticRows += 2
	}
	descW, descH := dialogDescSize(w, h, staticRows)
	d.desc.SetWidth(descW)
	d.desc.SetHeight(descH)

	var b strings.Builder
	b.WriteString(s.DialogTitle.Render("new bug") + "\n\n")
	if d.repo.multi() {
		b.WriteString(fieldRow(s, d.focus == bugFieldRepo, "repo: "+d.repo.label()) + "\n\n")
	}
	b.WriteString(d.desc.View() + "\n\n")
	b.WriteString(d.env.View() + "\n\n")
	b.WriteString(fieldRow(s, d.focus == bugFieldProfile, "profile: "+d.profiles[d.profile]) + "\n")
	b.WriteString(fieldRow(s, d.focus == bugFieldSeverity, d.sevLabel()) + "\n")
	b.WriteString(fieldRow(s, d.focus == bugFieldRoute, "route: "+bugRoutes[d.route].label) + "\n")
	b.WriteString("\n" + d.buttons.View(s, d.focus == bugFieldButtons) + "\n")

	if d.errText != "" {
		b.WriteString("\n" + s.Error.Render(d.errText))
	}
	hint := "tab next · ←/→ change · enter create · esc cancel"
	switch d.focus {
	case bugFieldDesc:
		hint = "alt+enter newline · " + hint
	case bugFieldButtons:
		hint = "←/→ buttons · enter activate · tab next · esc cancel"
	}
	b.WriteString("\n" + s.Faint.Render(hint))
	return s.DialogFrame.Render(b.String())
}
