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

// dialogDescSize sizes have their own clamps: a small terminal keeps
// today's fixed 46×4 box (the floor), a large one stops well short of
// spanning edge to edge (the ceiling).
const (
	descWidthMin  = 46
	descWidthMax  = 104
	descHeightMin = 4
	descHeightMax = 20
)

// dialogFrameChrome is DialogFrame's own border+padding, and
// dialogStaticRows is the fixed non-description row count shared by the
// feature and bug forms (title+blank, blank-after-desc, envelope+blank,
// options row, blank+hint) — both forms' View is line-for-line parallel,
// so one shared helper sizes both.
const (
	dialogFrameChromeW = 6
	dialogFrameChromeH = 4
	dialogStaticRows   = 8
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
func dialogDescSize(w, h int) (width, height int) {
	width = clamp(w-dialogFrameChromeW, descWidthMin, descWidthMax)
	height = clamp(h-dialogFrameChromeH-dialogStaticRows-dialogStatusBarMargin, descHeightMin, descHeightMax)
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
	// repos are the configured selectable managed repositories; repoIdx
	// indexes a list whose first (0) entry is the workspace default (empty
	// name) and whose rest are the configured names.
	repos   []string
	repoIdx int
	skip    domain.SkipFlags
	focus   int
	errText string

	onSubmit func(formResult) tea.Cmd
}

// newFeatureForm builds the dialog; profiles are the selectable profile
// names in display order, first selected (falling back to the built-in
// presets when empty), repos are the configured managed repository names
// (empty = only the workspace default), defaultEnvelope is the global
// default pre-filled into the envelope input, and onSubmit receives the
// validated fields.
func newFeatureForm(profiles []string, repos []string, defaultEnvelope int, onSubmit func(formResult) tea.Cmd) *featureForm {
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
	return &featureForm{desc: desc, env: env, profiles: profiles, repos: repos, onSubmit: onSubmit}
}

// repoName returns the currently selected repository name: "" for the
// workspace default, else the configured name.
func (d *featureForm) repoName() string {
	if d.repoIdx == 0 || len(d.repos) == 0 {
		return ""
	}
	return d.repos[d.repoIdx-1]
}

// repoLabel is the display label for the selected repository.
func (d *featureForm) repoLabel() string {
	if name := d.repoName(); name != "" {
		return name
	}
	return "default"
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
			Repo:     d.repoName(),
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
	descW, descH := dialogDescSize(w, h)
	d.desc.SetWidth(descW)
	d.desc.SetHeight(descH)

	var b strings.Builder
	b.WriteString(s.DialogTitle.Render("new feature") + "\n\n")
	b.WriteString(d.desc.View() + "\n\n")
	b.WriteString(d.env.View() + "\n\n")

	// the options row: quiet until focused, skips flagged when set
	marker := "  "
	profile := s.Faint.Render(d.profiles[d.profile])
	skips := s.Faint.Render(d.skipLabel())
	repo := s.Faint.Render("[" + d.repoLabel() + "]")
	if d.focus == fieldOpts {
		marker = s.Cursor.Render("▸ ")
		profile = s.Subtle.Render(d.profiles[d.profile])
		repo = s.Subtle.Render("[" + d.repoLabel() + "]")
	}
	if d.skip.Brainstorm || d.skip.Plan {
		skips = s.Warning.Render(d.skipLabel())
	}
	row := marker + profile + s.Faint.Render(" · ") + skips
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
		hint = "numeric credits (0 = uncapped) · enter create · esc cancel"
	case fieldOpts:
		hint = "←/→ profile · q quick · b/p toggle skips"
		if len(d.repos) > 0 {
			hint = "←/→ profile · q quick · b/p skips · r repo"
		}
		hint += " · enter create · esc cancel"
	}
	b.WriteString("\n" + s.Faint.Render(hint))
	return s.DialogFrame.Render(b.String())
}
