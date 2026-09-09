package ui

import (
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
// dialog's cycle fields (repo, profile, severity) share this so a
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
// profile, severity) across the creation dialogs.
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

// The envelope hints: the label drawn beside the new-card dialog's
// envelope input, and the unit on its collapsed readout.
//
// The number is a credit figure and 0 is a meaningful value, and the only
// thing that ever said so was the input's Placeholder — which a text input
// renders only while it is empty, and this field is prefilled from
// envelopePrefill() every single time it opens. So the modal a first-time
// user meets showed a bare "> 2400": no unit, and no sign that 0 means
// anything but a rejected entry. A caption is drawn whatever the field
// holds, which is the whole reason it is one and not a placeholder.
//
// The three creation dialogs share it rather than each writing their own,
// which is how the feature form and the bug form drift. Research passes
// the other hint: an RS card carries no default budget, so 0 is refused
// there rather than meaning uncapped (rsForm.submit).
const (
	envelopeHintCapped   = "credits · 0 = uncapped"
	envelopeHintRequired = "credits · required"
)

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

// bugSeverityChoices are the severities the new-card dialog cycles
// through for a bug; the first ("") means unset — triage classifies it.
var bugSeverityChoices = []domain.Severity{"", domain.SeverityCritical, domain.SeverityHigh, domain.SeverityMedium, domain.SeverityLow}
