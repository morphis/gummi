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
func fieldRow(s *theme.Styles, focused bool, label string) string {
	if focused {
		return s.Cursor.Render("▸ ") + s.Subtle.Render(label)
	}
	return "  " + s.Faint.Render(label)
}

// repoPicker is the repo-selector field shared by every managed-repo
// creation dialog (feature, bug, research, ingest): index 0 selects the
// workspace default when one actually resolves, the rest are the
// configured `repos:` names in display order. hasDefault marks whether
// the empty name resolves (worktree.Pool.Known("")) — a repos:-only
// workspace with no `repo:` root never offers "default", so a dialog can
// never submit a choice that would only fail later at worktree creation
// (worktree/pool.go ManagerForName).
type repoPicker struct {
	names      []string
	hasDefault bool
	idx        int
}

func newRepoPicker(names []string, hasDefault bool) repoPicker {
	return repoPicker{names: names, hasDefault: hasDefault}
}

// options are the labels offered, in cycle order.
func (p *repoPicker) options() []string {
	opts := make([]string, 0, len(p.names)+1)
	if p.hasDefault {
		opts = append(opts, "default")
	}
	return append(opts, p.names...)
}

// multi reports whether there's an actual choice to make; dialogs render
// and tab into the field only then.
func (p *repoPicker) multi() bool { return len(p.options()) > 1 }

// name returns the currently selected repository name: "" for the
// workspace default, else the configured name.
func (p *repoPicker) name() string {
	opts := p.options()
	if len(opts) == 0 {
		return ""
	}
	i := p.idx % len(opts)
	if p.hasDefault && i == 0 {
		return ""
	}
	return opts[i]
}

// label is the display label for the selected repository.
func (p *repoPicker) label() string {
	opts := p.options()
	if len(opts) == 0 {
		return "default"
	}
	return opts[p.idx%len(opts)]
}

// cycle steps the selection by delta (wrapping in either direction).
func (p *repoPicker) cycle(delta int) {
	total := len(p.options())
	if total > 1 {
		p.idx = ((p.idx+delta)%total + total) % total
	}
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
// picker has nothing to choose (see advanceFocus).
const (
	featureFieldRepo = iota
	featureFieldDesc
	featureFieldEnvelope
	featureFieldProfile
	featureFieldRoute
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
		onSubmit: onSubmit,
	}
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
			Skip:     featureRoutes[d.route].skip,
			Envelope: env,
			Repo:     d.repo.name(),
		}
		return true, d.onSubmit(res)
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

	switch d.focus {
	case featureFieldRepo:
		if delta, ok := selectCycleDelta(key.String()); ok {
			d.repo.cycle(delta)
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
	// envelope+blank(2), profile+route(2), blank+hint(2); +2 more when the
	// repo field renders (repo+blank).
	staticRows := 9
	if d.repo.multi() {
		staticRows += 2
	}
	descW, descH := dialogDescSize(w, h, staticRows)
	d.desc.SetWidth(descW)
	d.desc.SetHeight(descH)

	var b strings.Builder
	b.WriteString(s.DialogTitle.Render("new feature") + "\n\n")
	if d.repo.multi() {
		b.WriteString(fieldRow(s, d.focus == featureFieldRepo, "repo: "+d.repo.label()) + "\n\n")
	}
	b.WriteString(d.desc.View() + "\n\n")
	b.WriteString(d.env.View() + "\n\n")
	b.WriteString(fieldRow(s, d.focus == featureFieldProfile, "profile: "+d.profiles[d.profile]) + "\n")
	b.WriteString(fieldRow(s, d.focus == featureFieldRoute, "route: "+featureRoutes[d.route].label) + "\n")

	if d.errText != "" {
		b.WriteString("\n" + s.Error.Render(d.errText))
	}
	hint := "tab next · ←/→ change · enter create · esc cancel"
	if d.focus == featureFieldDesc {
		hint = "alt+enter newline · " + hint
	}
	b.WriteString("\n" + s.Faint.Render(hint))
	return s.DialogFrame.Render(b.String())
}
