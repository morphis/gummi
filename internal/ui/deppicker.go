package ui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui/theme"
)

// Inline reasons shown for a dependency edge the picker refuses to write.
// The same rules the store enforces (no self-loop, no cycle, no attachment
// onto a card at/past coding) are surfaced client-side first so a rejected
// edge never reaches the store with a raw error.
const (
	depReasonSelf  = "self-dependency — a card can't depend on itself"
	depReasonCycle = "would close a dependency cycle"
	depReasonLate  = "this card is already coding — dependencies are settled"
)

// depCandState classifies a candidate target in the dependency picker.
type depCandState int

const (
	candOK       depCandState = iota
	candSelf                  // the card itself — shown dimmed, never navigable
	candAttached              // already a forward dep — navigable, enter no-op, x removes
	candCycle                 // would close a cycle — selectable, enter rejected inline
	candLate                  // a would-be target in removal-only mode (add disabled)
	candRemove                // an existing forward dep in removal-only mode — x removes
)

// depCandidate is one board card in the picker's candidate list.
type depCandidate struct {
	f      domain.Feature
	state  depCandState
	reason string // inline reason shown in the detail panel for rejected edges
}

// navigable reports whether the cursor may land on this row. Self rows are
// never navigable (a self-edge can never be a forward dep to remove);
// attached and cycle rows are, so remove stays reachable and a cycle shows
// its reason inline.
func (c depCandidate) navigable() bool { return c.state != candSelf }

// depPicker is the add/remove dependency surface (opened by p on a selected
// card). It reuses the line-cursor-list-plus-detail-panel interaction from
// the ingest surface: a candidate list on the left, the selected target's
// one-liner and outcome on the right.
type depPicker struct {
	f          domain.Feature
	cands      []depCandidate
	cursor     int
	removeOnly bool // the source is at/past coding: add is disabled, removal works
}

// buildCands classifies every board card into the source's forward
// dependency set: self, already-attached, would-cycle, or addable targets.
// When the source is at/past coding it lists only its existing forward deps
// (removal-only). It reads the live store; it never writes.
func (dp *depPicker) buildCands(ctx context.Context, store *state.Store, f domain.Feature) error {
	all, err := store.ListFeatures(ctx)
	if err != nil {
		return err
	}
	deps, err := store.ListDependencies(ctx, f.ID)
	if err != nil {
		return err
	}
	depSet := make(map[domain.FeatureID]bool, len(deps))
	for _, id := range deps {
		depSet[id] = true
	}
	removeOnly := domain.AtOrPastCoding(f.Stage)
	dp.f = f
	dp.removeOnly = removeOnly
	dp.cands = dp.cands[:0]
	for _, cand := range all {
		switch {
		case cand.ID == f.ID:
			// the self card is shown (dimmed, non-navigable) so the user
			// sees why it isn't a target — but only in add mode; in
			// removal-only the list is strictly its forward deps.
			if !removeOnly {
				dp.cands = append(dp.cands, depCandidate{f: cand, state: candSelf, reason: depReasonSelf})
			}
		case depSet[cand.ID]:
			if removeOnly {
				dp.cands = append(dp.cands, depCandidate{f: cand, state: candRemove})
			} else {
				dp.cands = append(dp.cands, depCandidate{f: cand, state: candAttached})
			}
		case removeOnly:
			// a card this source could have depended on, but its coding
			// stage has passed — the late-attachment reason is rendered as
			// a banner rather than a row, so nothing here.
		default:
			cyc, err := store.WouldCycle(ctx, f.ID, cand.ID)
			if err != nil {
				return err
			}
			if cyc {
				dp.cands = append(dp.cands, depCandidate{f: cand, state: candCycle, reason: depReasonCycle})
			} else {
				dp.cands = append(dp.cands, depCandidate{f: cand, state: candOK})
			}
		}
	}
	dp.setCursor(0)
	return nil
}

// setCursor clamps n into range and snaps it onto a navigable row (moving
// down, wrapping), so the cursor never rests on a self row. Falls back to n
// when every row is non-navigable.
func (dp *depPicker) setCursor(n int) {
	if len(dp.cands) == 0 {
		dp.cursor = 0
		return
	}
	n = min(max(n, 0), len(dp.cands)-1)
	dp.cursor = n
	if dp.cands[n].navigable() {
		return
	}
	for i := 0; i < len(dp.cands); i++ {
		dp.cursor = (dp.cursor + 1) % len(dp.cands)
		if dp.cands[dp.cursor].navigable() {
			return
		}
	}
	dp.cursor = n
}

// move steps the cursor to the next navigable row in the given direction.
func (dp *depPicker) move(delta int) {
	if len(dp.cands) == 0 {
		return
	}
	n := len(dp.cands)
	pos := dp.cursor
	for i := 0; i < n; i++ {
		pos = (pos + delta + n) % n
		if dp.cands[pos].navigable() {
			dp.cursor = pos
			return
		}
	}
}

// attachedCount is how many forward deps the source currently has.
func (dp *depPicker) attachedCount() int {
	n := 0
	for _, c := range dp.cands {
		if c.state == candAttached || c.state == candRemove {
			n++
		}
	}
	return n
}

// bindings is the picker's key table (see keymap.go).
func (dp *depPicker) bindings() []binding {
	return []binding{
		{key: "j/k", label: "select", help: "move over the candidates"},
		{key: "enter", label: "add", help: "add the selected candidate as a dependency", bar: true},
		{key: "x", label: "remove", help: "remove the selected dependency", bar: true},
		{key: "esc", label: "back", help: "return to the board (also q)", bar: true},
		{key: "?", label: "help", bar: true},
	}
}

// handleDepsKey routes keys while the picker is open.
func (m *Shell) handleDepsKey(key string) tea.Cmd {
	dp := m.deps
	switch key {
	case "esc", "q":
		m.deps = nil
		return nil
	case "?":
		m.Overlay.Push(m.helpOverlay())
		return nil
	case "j", "down":
		dp.move(1)
	case "k", "up":
		dp.move(-1)
	case "enter":
		return dp.add(m)
	case "x":
		return dp.remove(m)
	}
	return nil
}

// add submits the selected candidate to the store's add op — only for a
// candOK row, so a rejected edge (self/cycle/late) never reaches the store.
func (dp *depPicker) add(m *Shell) tea.Cmd {
	if len(dp.cands) == 0 || dp.cands[dp.cursor].state != candOK {
		return nil
	}
	c := dp.cands[dp.cursor]
	return m.applyDep(dp.f, c.f.ID, true)
}

// remove submits the selected row to the store's remove op — only for a
// navigable forward dep (attached in add mode, or any listed row in
// removal-only mode).
func (dp *depPicker) remove(m *Shell) tea.Cmd {
	if len(dp.cands) == 0 {
		return nil
	}
	c := dp.cands[dp.cursor]
	switch c.state {
	case candAttached, candRemove:
		return m.applyDep(dp.f, c.f.ID, false)
	}
	return nil
}

// applyDep writes or removes one forward edge in a command (never in
// Update), then rebuilds the picker's candidates from the live store and
// reloads the board so the badge recomputes.
func (m *Shell) applyDep(src domain.Feature, dep domain.FeatureID, add bool) tea.Cmd {
	store := m.store
	if store == nil {
		return nil
	}
	return func() tea.Msg {
		ctx := context.Background()
		var err error
		if add {
			err = store.AddDependency(ctx, src.ID, dep)
		} else {
			err = store.RemoveDependency(ctx, src.ID, dep)
		}
		if err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		return m.rebuildDepsMsg(ctx, store, src)
	}
}

// rebuildDepsMsg rebuilds the candidate set for src after an edge change.
func (m *Shell) rebuildDepsMsg(ctx context.Context, store *state.Store, src domain.Feature) depsLoadedMsg {
	dp := &depPicker{}
	if err := dp.buildCands(ctx, store, src); err != nil {
		return depsLoadedMsg{f: src, err: err}
	}
	return depsLoadedMsg{f: src, cands: dp.cands, removeOnly: dp.removeOnly, reload: true}
}

// openDeps loads the picker's candidate set for f in a command and opens
// the surface when it lands.
func (m *Shell) openDeps(f domain.Feature) tea.Cmd {
	store := m.store
	if store == nil {
		return nil
	}
	return func() tea.Msg {
		dp := &depPicker{}
		if err := dp.buildCands(context.Background(), store, f); err != nil {
			return depsLoadedMsg{f: f, err: err}
		}
		return depsLoadedMsg{f: f, cands: dp.cands, removeOnly: dp.removeOnly}
	}
}

// depsLoadedMsg delivers a built candidate set (and, for a reload, the flag
// that the board should refresh its badge).
type depsLoadedMsg struct {
	f          domain.Feature
	cands      []depCandidate
	removeOnly bool
	reload     bool
	err        error
}

// depPickerView paints the picker into the main pane: header, candidate
// list, and the selected target's detail.
func (m *Shell) depPickerView(w, h int) string {
	dp := m.deps
	if dp == nil {
		return ""
	}
	s := m.styles
	var b strings.Builder
	head := s.Title.Render("dependencies") + " " + s.Base.Render("· "+string(dp.f.ID)) +
		"  " + s.Pill.Render(fmt.Sprintf("%d deps", dp.attachedCount()))
	b.WriteString("\n" + head + "\n")
	b.WriteString(s.Separator.Render(strings.Repeat("─", max(min(w, 76), 0))) + "\n")

	extra := 0
	if dp.removeOnly {
		b.WriteString(s.Warning.Render(depReasonLate) + "\n")
		extra = 1
	}
	if len(dp.cands) == 0 {
		b.WriteString(s.Faint.Render("  no dependencies") + "\n")
	} else {
		numW := len(fmt.Sprintf("%d", len(dp.cands)))
		rows := make([]string, len(dp.cands))
		for i, c := range dp.cands {
			rows[i] = dp.depRow(s, numW, i, c, w)
		}
		var tail strings.Builder
		if dp.cursor < len(dp.cands) {
			tail.WriteString("\n" + dp.renderDepDetail(s, w))
		}
		tailLines := strings.Count(tail.String(), "\n") + 1
		const headerLines = 3 // leading blank, head, separator
		listBudget := max(h-headerLines-extra-tailLines, 3)
		for _, line := range windowLines(rows, dp.cursor, listBudget) {
			b.WriteString(line + "\n")
		}
		b.WriteString(tail.String())
	}
	return clipLines(b.String(), h)
}

// depRow renders one candidate row: cursor marker, index, a state marker,
// and the target's ID, tinted by its state.
func (dp *depPicker) depRow(s *theme.Styles, numW, i int, c depCandidate, w int) string {
	marker := "  "
	style := s.Base
	switch c.state {
	case candSelf:
		style = s.Faint
	case candAttached, candRemove:
		style = s.Subtle
	case candCycle:
		style = s.Warning
	}
	if i == dp.cursor {
		marker = s.Cursor.Render("▸ ")
		style = s.Subtitle
	}
	num := s.Faint.Render(fmt.Sprintf("%*d.", numW, i+1))
	mark := "  "
	switch c.state {
	case candAttached, candRemove:
		mark = s.Success.Render("✓ ")
	case candSelf:
		mark = s.Faint.Render("· ")
	}
	id := style.Render(string(c.f.ID))
	line := marker + num + " " + mark + id
	if t := c.f.Title; t != "" {
		line += " " + s.Faint.Render(ansi.Truncate(t, max(w-numW-8, 4), "…"))
	}
	return ansi.Truncate(line, w, "…")
}

// renderDepDetail shows the selected target's one-liner and the outcome of
// attaching/removing it.
func (dp *depPicker) renderDepDetail(s *theme.Styles, w int) string {
	if len(dp.cands) == 0 {
		return ""
	}
	c := dp.cands[dp.cursor]
	var b strings.Builder
	if c.f.OneLiner != "" {
		b.WriteString(s.Subtle.Render(ansi.Truncate(c.f.OneLiner, max(w-2, 8), "…")) + "\n")
	}
	b.WriteString(s.Base.Render(ansi.Truncate(depOutcome(c), max(w-2, 8), "…")) + "\n")
	return b.String()
}

// depOutcome is the detail line for a candidate's state: the reason a
// rejected edge is rejected, or the action that will apply.
func depOutcome(c depCandidate) string {
	switch c.state {
	case candOK:
		return "press enter to add as a dependency"
	case candSelf:
		return depReasonSelf
	case candAttached:
		return "already a dependency — enter adds nothing, x removes"
	case candCycle:
		return depReasonCycle
	case candLate:
		return depReasonLate
	case candRemove:
		return "press x to remove this dependency"
	}
	return ""
}
