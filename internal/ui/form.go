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

// dialogDescSize sizes have their own clamps: a small terminal keeps
// today's fixed 46×4 box (the floor), a large one stops well short of
// spanning edge to edge (the ceiling).
const (
	descWidthMin  = 46
	descWidthMax  = 104
	descHeightMin = 4
	descHeightMax = 20
)

// dialogFrameChrome is DialogFrame's own border+padding.
const (
	dialogFrameChromeW = 6
	dialogFrameChromeH = 4
)

// dialogStatusBarMargin reserves the persistent status bar's row: the
// overlay draws dialogs into the full terminal area, status bar included
// (layout.Compute carves the status row out of that same height rather
// than excluding it), so without this the dialog frame can grow to
// exactly fill the draw area and paint over the status bar.
const dialogStatusBarMargin = 1

// dialogDescSize returns the description editor's width and height for a
// dialog drawn in a w×h area, clamped so it stays usable on a small
// terminal and doesn't span an ultra-wide/ultra-tall one edge to edge.
// staticRows is the caller's own fixed non-description row count (it
// varies per dialog and by whether optional fields like repo render), so
// each View computes and passes its own rather than sharing one constant.
func dialogDescSize(w, h, staticRows int) (width, height int) {
	width = clamp(w-dialogFrameChromeW, descWidthMin, descWidthMax)
	height = clamp(h-dialogFrameChromeH-staticRows-dialogStatusBarMargin, descHeightMin, descHeightMax)
	return width, height
}

func clamp(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

// selectCycleDelta maps a keypress to a step for a left/right-style select
// field: -1/+1, or ok=false if the key doesn't drive one. Every creation
// dialog's cycle fields (repo, profile, severity, route) share this so a
// field never needs a memorized letter to operate — arrows, vi h/l, and
// space (a generic "next") all just work.
func selectCycleDelta(key string) (delta int, ok bool) {
	switch key {
	case "left", "h":
		return -1, true
	case "right", "l", "space":
		return 1, true
	}
	return 0, false
}

// fieldRow renders one stacked field line: a cursor marker and the label,
// styled by focus — the shared look for every tab-stop field (repo,
// profile, severity, route) across the creation dialogs.
//
// The focused field wears the same band a selected list row does, so
// "where am I" answers the same way on every surface. It bands the label
// only (w=0), not a full column: these rows sit inside a dialog frame
// whose width they don't own.
func fieldRow(s *theme.Styles, focused bool, label string) string {
	if focused {
		return s.Band(s.BandMarker(true)+s.Base.Render(label), 0, true)
	}
	return "  " + s.Faint.Render(label)
}

// repoUnset is repoPicker.idx while no repository has been chosen. A
// picker with a real choice to make starts here and can never return:
// choosing is one-way, because there is nothing sensible to go back to.
const repoUnset = -1

// repoUnsetLabel is what the field reads before a choice is made. It is
// deliberately not the name of any repository — a label that named one
// would look like a selection that had already happened.
const repoUnsetLabel = "choose one"

// repoUnchosenErr is the refusal every creation dialog shows when it has a
// repository choice to make and none has been made.
const repoUnchosenErr = "select a repository — a workspace with `repos:` has no default"

// repoPicker is the repo-selector field shared by every managed-repo
// creation dialog (feature, bug, research, ingest). It offers exactly one
// of two things, never a mix:
//
//   - the configured `repos:` names, when there are any. Such a workspace
//     has no default repository at all (config.ResolveRepos leaves the
//     default root empty whenever `repos:` is set, and setting `repo:`
//     alongside it is a config error), so "default" is not among the
//     options — offering it would submit a choice that only fails later
//     at worktree creation (worktree/pool.go ManagerForName).
//   - the lone workspace default, when no names are configured. There is
//     no name to report and nothing to choose, so the dialogs skip the
//     field entirely.
//
// hasDefault (worktree.Pool.Known("")) therefore only matters in the
// second case. With more than one option the picker starts unselected and
// the dialogs refuse to submit until the user picks one: a card must name
// its repository outright rather than inherit a silent default.
//
// A single configured name is not a choice either, but it is still a
// name the card is about to be created under, so the dialogs render it
// (shown) as a read-only row rather than hiding where the work lands.
// Only multi decides what is interactive: the tab stop and ←/→.
type repoPicker struct {
	names      []string
	hasDefault bool
	idx        int
}

func newRepoPicker(names []string, hasDefault bool) repoPicker {
	p := repoPicker{names: names, hasDefault: hasDefault}
	if len(p.options()) > 1 {
		p.idx = repoUnset
	}
	return p
}

// options are the labels offered, in cycle order: the configured names
// when there are any, else the lone default when one resolves.
func (p *repoPicker) options() []string {
	if len(p.names) > 0 {
		return p.names
	}
	if p.hasDefault {
		return []string{"default"}
	}
	return nil
}

// multi reports whether there's an actual choice to make; dialogs give
// the field a tab stop and ←/→ only then.
func (p *repoPicker) multi() bool { return len(p.options()) > 1 }

// shown reports whether the dialogs render the field at all. Any
// configured `repos:` name qualifies, a lone one included: with one
// repository configured there is nothing to pick, but the row is the
// only place the dialog says which repository the card will be created
// in. The unconfigured workspace default has no name worth a row.
func (p *repoPicker) shown() bool { return len(p.names) > 0 }

// chosen reports whether a repository has actually been selected. It is
// false only while a multi-option picker sits at its initial unset state.
func (p *repoPicker) chosen() bool { return p.idx != repoUnset }

// needsChoice reports that this dialog cannot submit yet: there is a
// repository to choose and the user has not chosen one.
func (p *repoPicker) needsChoice() bool { return p.multi() && !p.chosen() }

// name returns the currently selected repository name: "" for the
// workspace default (and while nothing is selected), else the configured
// name. Callers that could act on an unselected picker must check
// needsChoice first — "" would otherwise read as the default.
func (p *repoPicker) name() string {
	opts := p.options()
	if len(opts) == 0 || !p.chosen() || len(p.names) == 0 {
		return ""
	}
	return opts[p.idx%len(opts)]
}

// label is the display label for the selected repository.
func (p *repoPicker) label() string {
	opts := p.options()
	if len(opts) == 0 {
		return "default"
	}
	if !p.chosen() {
		return repoUnsetLabel
	}
	return opts[p.idx%len(opts)]
}

// cycle steps the selection by delta (wrapping in either direction). The
// first step off the unset state lands on an end of the list, so → picks
// the first repository and ← the last.
func (p *repoPicker) cycle(delta int) {
	total := len(p.options())
	if total <= 1 {
		return
	}
	if !p.chosen() {
		if delta > 0 {
			p.idx = 0
		} else {
			p.idx = total - 1
		}
		return
	}
	p.idx = ((p.idx+delta)%total + total) % total
}

// featureRoute is one state of the workflow-route field: a display label
// paired with the domain.SkipFlags it selects. The five states are the
// same combinations featureForm has always supported (independently
// toggled Brainstorm/Plan, plus the Quick preset) — collapsed into one
// cycling field instead of three letter-keyed toggles.
type featureRoute struct {
	label string
	skip  domain.SkipFlags
}

var featureRoutes = []featureRoute{
	{"full workflow", domain.SkipFlags{}},
	{"skip brainstorm", domain.SkipFlags{Brainstorm: true}},
	{"skip plan", domain.SkipFlags{Plan: true}},
	{"skip brainstorm+plan", domain.SkipFlags{Brainstorm: true, Plan: true}},
	{"quick — spec in one pass, then implement", domain.QuickRoute()},
}

// feature form fields, in tab order. fieldRepo is skipped when the repo
// picker has nothing to choose (see advanceFocus) — the row itself may
// still render read-only there, see repoPicker.shown; fieldButtons is the
// last stop, so tab from it wraps back to the first field.
const (
	featureFieldRepo = iota
	featureFieldDesc
	featureFieldEnvelope
	featureFieldProfile
	featureFieldRoute
	featureFieldButtons
	featureFieldCount
)

// featureForm is the new-feature dialog: a free-form description — the
// first line becomes the card title, anything beyond it seeds the
// draft's Problem section for the brainstorm stage to develop. Every
// other choice (repo, profile, route) is its own tab stop, cycled with
// ←/→ — no mnemonic keys.
type featureForm struct {
	desc     textarea.Model
	env      textinput.Model
	profiles []string
	profile  int
	repo     repoPicker
	route    int
	focus    int
	errText  string
	buttons  *buttonRow

	onSubmit func(formResult) tea.Cmd
}

// newFeatureForm builds the dialog; profiles are the selectable profile
// names in display order, first selected (falling back to the built-in
// presets when empty), repos are the configured managed repository names
// (empty = only the workspace default) and hasDefault reports whether the
// workspace default actually resolves, defaultEnvelope is the global
// default pre-filled into the envelope input, and onSubmit receives the
// validated fields.
func newFeatureForm(profiles []string, repos []string, hasDefault bool, defaultEnvelope int, onSubmit func(formResult) tea.Cmd) *featureForm {
	if len(profiles) == 0 {
		profiles = defaultProfilePresets
	}
	desc := textarea.New()
	desc.Placeholder = "describe the feature…"
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
	return &featureForm{
		desc: desc, env: env, profiles: profiles,
		repo: newRepoPicker(repos, hasDefault), focus: featureFieldDesc,
		buttons:  newButtonRow(button{label: "Cancel"}, button{label: "Create"}),
		onSubmit: onSubmit,
	}
}

// ID implements overlay.Dialog.
func (d *featureForm) ID() string { return "new-feature" }

// submit validates and fires onSubmit, matching enter's own handling from
// any other field exactly — the button row's Create button is just another
// way to reach the same action.
func (d *featureForm) submit() (bool, tea.Cmd) {
	// the repository is the first field and the one choice with no
	// default, so it is the first thing refused — and focus moves there,
	// since the field is what the message is asking about.
	if d.repo.needsChoice() {
		d.errText = repoUnchosenErr
		d.setFocus(featureFieldRepo)
		return false, nil
	}
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
		Skip:     featureRoutes[d.route].skip,
		Envelope: env,
		Repo:     d.repo.name(),
	}
	return true, d.onSubmit(res)
}

// HandleKey implements overlay.Dialog.
func (d *featureForm) HandleKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	switch key.String() {
	case "esc":
		return true, nil
	case "alt+enter", "ctrl+j":
		if d.focus == featureFieldDesc {
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

	if d.focus == featureFieldButtons {
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
	case featureFieldRepo:
		if delta, ok := selectCycleDelta(key.String()); ok {
			d.repo.cycle(delta)
			// clear a pending "choose a repo" refusal the moment they do
			d.errText = ""
		}
	case featureFieldProfile:
		if delta, ok := selectCycleDelta(key.String()); ok {
			n := len(d.profiles)
			d.profile = ((d.profile+delta)%n + n) % n
		}
	case featureFieldRoute:
		if delta, ok := selectCycleDelta(key.String()); ok {
			n := len(featureRoutes)
			d.route = ((d.route+delta)%n + n) % n
		}
	case featureFieldDesc:
		d.desc, _ = d.desc.Update(key)
		d.errText = ""
	case featureFieldEnvelope:
		d.env, _ = d.env.Update(key)
		d.errText = ""
	}
	return false, nil
}

// HandlePaste implements overlay.Paster: pasted text goes into the
// description while it's focused, newlines intact.
func (d *featureForm) HandlePaste(msg tea.PasteMsg) tea.Cmd {
	if d.focus == featureFieldDesc {
		d.desc, _ = d.desc.Update(msg)
		d.errText = ""
	}
	return nil
}

// advanceFocus moves focus by dir (±1), wrapping, and skips the repo stop
// when there's nothing to choose there.
func (d *featureForm) advanceFocus(dir int) {
	f := d.focus
	for {
		f = (f + dir + featureFieldCount) % featureFieldCount
		if f != featureFieldRepo || d.repo.multi() {
			break
		}
	}
	d.setFocus(f)
}

func (d *featureForm) setFocus(f int) {
	d.focus = f
	d.desc.Blur()
	d.env.Blur()
	switch f {
	case featureFieldDesc:
		d.desc.Focus()
	case featureFieldEnvelope:
		d.env.Focus()
	}
}

// View implements overlay.Dialog.
func (d *featureForm) View(s *theme.Styles, w, h int) string {
	// base static rows: title+blank(2), blank-after-desc(1),
	// envelope+blank(2), profile+route(2), blank+buttons(2), blank+hint(2);
	// +2 more when the repo field renders (repo+blank).
	staticRows := 11
	if d.repo.shown() {
		staticRows += 2
	}
	descW, descH := dialogDescSize(w, h, staticRows)
	d.desc.SetWidth(descW)
	d.desc.SetHeight(descH)

	var b strings.Builder
	b.WriteString(s.DialogTitle.Render("new feature") + "\n\n")
	if d.repo.shown() {
		b.WriteString(fieldRow(s, d.focus == featureFieldRepo, "repo: "+d.repo.label()) + "\n\n")
	}
	b.WriteString(d.desc.View() + "\n\n")
	b.WriteString(d.env.View() + "\n\n")
	b.WriteString(fieldRow(s, d.focus == featureFieldProfile, "profile: "+d.profiles[d.profile]) + "\n")
	b.WriteString(fieldRow(s, d.focus == featureFieldRoute, "route: "+featureRoutes[d.route].label) + "\n")
	b.WriteString("\n" + d.buttons.View(s, d.focus == featureFieldButtons) + "\n")

	if d.errText != "" {
		b.WriteString("\n" + s.Error.Render(d.errText))
	}
	hint := "tab next · ←/→ change · enter create · esc cancel"
	switch d.focus {
	case featureFieldDesc:
		hint = "alt+enter newline · " + hint
	case featureFieldButtons:
		hint = "←/→ buttons · enter activate · tab next · esc cancel"
	}
	b.WriteString("\n" + s.Faint.Render(hint))
	return s.DialogFrame.Render(b.String())
}
