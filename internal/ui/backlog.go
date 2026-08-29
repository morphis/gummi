package ui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
)

// ViewMode selects the board's shape. It is an ephemeral, in-memory
// toggle (never persisted), like SortMode: the split board is what a
// fresh shell starts on, and `L` swaps between the two.
type ViewMode int

const (
	// ModeSplit is the original layout: a kanban column on the left, the
	// selected card's detail in the main pane on the right. Both are on
	// screen at once, so the arrow keys have two regions to belong to.
	ModeSplit ViewMode = iota
	// ModeBacklog drops the column: the whole width is a sorted backlog
	// you select in, and enter opens the card on a page of its own. One
	// list at a time, so focus never has to be inferred — at the cost of
	// not seeing the board and the card together.
	ModeBacklog
)

// SetViewMode selects the layout before the first frame (for a caller
// that wants to start in the backlog).
func (m *Shell) SetViewMode(v ViewMode) {
	m.viewMode = v
	m.cardOpen = false
	if m.width > 0 && m.height > 0 {
		m.layout = m.computeLayout()
	}
}

// toggleLayout swaps the two board shapes, closing the card page so the
// switch always lands somewhere that exists in the other mode.
func (m *Shell) toggleLayout() {
	if m.viewMode == ModeBacklog {
		m.viewMode = ModeSplit
		m.notice = noticeMsg{text: "layout: split board"}
	} else {
		m.viewMode = ModeBacklog
		m.notice = noticeMsg{text: "layout: backlog (enter opens a card)"}
	}
	m.cardOpen = false
	m.blurActions()
	if m.width > 0 && m.height > 0 {
		m.layout = m.computeLayout()
	}
}

// layoutCommandLabel names the layout the toggle would switch to, so the
// command menu says what pressing it does rather than what is already on
// screen.
func (m *Shell) layoutCommandLabel() string {
	if m.viewMode == ModeBacklog {
		return "Switch to the split board layout"
	}
	return "Switch to the backlog layout"
}

// openCard opens the selected card's page. The action list is the only
// list on that page, so it takes the arrow keys on arrival — there is no
// second region to hand them to.
func (m *Shell) openCard() {
	if _, ok := m.selected(); !ok {
		return
	}
	m.cardOpen = true
	m.actionCursor = 0
	m.actionsExpanded = false
}

// closeCard returns to the backlog list.
func (m *Shell) closeCard() {
	m.cardOpen = false
	m.blurActions()
}

// stepCard moves to the previous/next card without leaving the page —
// J/K, so j/k stay on the action list where the eye already is.
func (m *Shell) stepCard(delta int) {
	m.moveSel(delta)
	m.actionCursor = 0
	m.actionsExpanded = false
}

// actionsOwnArrows reports whether the card's action list owns ↑↓ and
// enter. On the split board that is a focus the user moves into with →;
// on a card page it is simply true, because the page has one list.
func (m *Shell) actionsOwnArrows() bool {
	if m.viewMode == ModeBacklog {
		return m.cardOpen
	}
	return m.actionFocused
}

// backlogKey answers the keys whose meaning differs in the backlog
// layout — movement, enter, and the way in and out of a card page. It
// reports whether it handled the key; everything it does not handle
// falls through to boardVerb, so the card verbs (g, v, m, …) keep
// working from the list exactly as they do on the split board.
func (m *Shell) backlogKey(key string) (tea.Cmd, bool) {
	// the ingest feed takes the foreground in either layout; its own keys
	// (esc backgrounds it) must reach boardVerb unintercepted.
	if m.ingestRun != nil && !m.ingestRun.hidden {
		return nil, false
	}
	if !m.cardOpen {
		switch key {
		case "enter", "right", "l":
			m.openCard()
			return nil, true
		}
		return nil, false
	}
	switch key {
	case "esc", "left":
		m.closeCard()
		return nil, true
	case "j", "down":
		m.moveAction(1)
		return nil, true
	case "k", "up":
		m.moveAction(-1)
		return nil, true
	case "J":
		m.stepCard(1)
		return nil, true
	case "K":
		m.stepCard(-1)
		return nil, true
	case "right":
		return nil, true
	case "enter":
		if a, ok := m.cardActions().Selected(); ok {
			m.clearTransientNotice()
			// straight to the verb, never back through boardKey: the run
			// action's own accelerator IS enter, so re-entering there would
			// pick this same row and recurse until the stack blew.
			return m.runCardAction(a), true
		}
		return nil, true
	}
	return nil, false
}

// backlogEntry is one rendered line of the backlog list: a super-state
// header, a card, or the blank line that separates two groups (a spacer
// is an entry rather than a rendering flourish so that one entry is
// always one row, which is what makes the window arithmetic below
// honest). Building the list as entries first is
// what lets it scroll — the window is taken over these, so a card can
// never be half-shown and a group header can be re-emitted at the top of
// a window that starts mid-group.
type backlogEntry struct {
	header   string // group header text; empty on a spacer
	card     bool
	row      int // index into m.rows
	shortcut int // 1-based, the printed jump number
}

// backlogEntries flattens the display order into headers, spacers and
// cards.
func (m *Shell) backlogEntries() []backlogEntry {
	var out []backlogEntry
	var last domain.SuperState
	for i, idx := range m.displayOrder(m.sortMode) {
		if super := m.rows[idx].F.Stage.SuperState(); i == 0 || super != last {
			if i > 0 {
				out = append(out, backlogEntry{})
			}
			out = append(out, backlogEntry{header: strings.ToUpper(string(super))})
			last = super
		}
		out = append(out, backlogEntry{card: true, row: idx, shortcut: i + 1})
	}
	return out
}

// backlogView renders the full-width backlog: the same super-state
// grouping the kanban column has, given the whole terminal instead of a
// third of it, and scrolled to keep the selection on screen.
func (m *Shell) backlogView(w, h int) string {
	s := m.styles
	if len(m.rows) == 0 {
		var b strings.Builder
		b.WriteString("\n " + s.PaneTitleActive.Render("BACKLOG") + "\n\n")
		b.WriteString(" " + s.Faint.Render("nothing on the board yet") + "\n")
		b.WriteString(" " + s.Muted.Render("press ") + s.KeyHint.Render("n") + s.Muted.Render(" new feature · ") +
			s.KeyHint.Render("B") + s.Muted.Render(" new bug · ") + s.KeyHint.Render("R") + s.Muted.Render(" new research") + "\n")
		return b.String()
	}

	entries := m.backlogEntries()
	sel := 0
	for i, e := range entries {
		if e.card && e.row == m.sel {
			sel = i
			break
		}
	}
	// the title and the blank under it are the fixed chrome. A list that
	// overflows also spends a row at each end on its "N more" marker —
	// always both, even at the ends where one of them is blank, so the
	// rows don't shift under the eye as the list scrolls.
	body := max(h-2, 1)
	scrolls := len(entries) > body
	if scrolls {
		body = max(h-4, 1)
	}
	start, end := backlogWindow(len(entries), sel, body)
	// a window that opens mid-group spends one of its rows repeating that
	// group's header. The row has to be taken out of the budget before the
	// window is placed, not after: trimming the end afterwards can clip
	// off the very card the window was centred on.
	sticky := ""
	if start > 0 && entries[start].card {
		start, end = backlogWindow(len(entries), sel, max(body-1, 1))
		if start > 0 && entries[start].card {
			for i := start; i >= 0; i-- {
				if e := entries[i]; !e.card && e.header != "" {
					sticky = e.header
					break
				}
			}
		}
	}

	var b strings.Builder
	line := func(str string) { b.WriteString(ansi.Truncate(str, w, "…") + "\n") }

	sortLabel := "creation order"
	if m.sortMode == SortSeverity {
		sortLabel = "severity (todo)"
	}
	line(" " + s.PaneTitleActive.Render("BACKLOG") + "  " +
		s.Faint.Render(strconv.Itoa(len(m.rows))+" cards  ·  sort: "+sortLabel))
	line("")

	if scrolls {
		line(scrollNote(s.Faint.Render, "↑", start))
	}
	if sticky != "" {
		line(" " + s.PaneTitleActive.Render(sticky) + s.Faint.Render(" (cont.)"))
	}
	for _, e := range entries[start:end] {
		if !e.card {
			if e.header == "" {
				line("")
				continue
			}
			line(" " + s.PaneTitleActive.Render(e.header))
			continue
		}
		b.WriteString(m.cardLine(m.rows[e.row], e.shortcut, e.row == m.sel, true, w) + "\n")
	}
	if scrolls {
		line(scrollNote(s.Faint.Render, "↓", len(entries)-end))
	}
	return b.String()
}

// backlogWindow places a body-row window over n entries, centred on the
// selected one and clamped to the ends.
func backlogWindow(n, sel, body int) (start, end int) {
	if n <= body {
		return 0, n
	}
	start = clamp(sel-body/2, 0, n-body)
	return start, start + body
}

// scrollNote renders the "N more" marker for a clipped end of the list,
// or a blank line when nothing is clipped — the row is always spent, so
// the list doesn't jitter vertically as it scrolls.
func scrollNote(render func(...string) string, arrow string, n int) string {
	if n <= 0 {
		return ""
	}
	return "  " + render(arrow+" "+strconv.Itoa(n)+" more")
}

// cardPageView renders one card on the full width: a breadcrumb naming
// the way back and the card's position in the backlog, then the same
// detail the split board's main pane shows — with roughly twice the
// width to spend on it.
func (m *Shell) cardPageView(w, h int) string {
	s := m.styles
	if _, ok := m.selected(); !ok {
		return m.backlogView(w, h)
	}
	order := m.displayOrder(m.sortMode)
	pos := 0
	for i, idx := range order {
		if idx == m.sel {
			pos = i + 1
			break
		}
	}
	crumb := " " + s.Faint.Render("‹ ") + s.KeyHint.Render("esc") + s.Faint.Render(" backlog") +
		s.Faint.Render("  ·  "+strconv.Itoa(pos)+" of "+strconv.Itoa(len(order))) +
		"  " + s.KeyHint.Render("J/K") + s.Faint.Render(" prev/next card")
	return ansi.Truncate(crumb, w, "…") + "\n" + m.dashboardView(w, max(h-1, 1))
}

// backlogBindings is the list level's key table: the board's own table
// with the two-region movement keys dropped (there is one list here) and
// enter re-pointed at the card page.
//
// The one key this level has to teach is enter, so it leads: a narrow
// status bar fits two hints, and it should spend them on the way in
// rather than on arrow keys a list already implies.
func (m *Shell) backlogBindings() []binding {
	var out []binding
	var lead []binding
	for _, b := range m.boardBindings() {
		switch b.key {
		case "→", "←":
			continue
		case "j/k ↓↑":
			b.label, b.help, b.bar = "select", "select card", false
		case "enter":
			b.label, b.help, b.bar = "open card", "open the card page — full width, with the card's actions", true
			lead = append(lead, b)
			continue
		}
		out = append(out, b)
	}
	return append(lead, out...)
}

// cardPageBindings is the card page's table: the board's verbs, plus the
// page's own way back and its prev/next, with the arrow keys documented
// as the action list's (it is the only list on the page). The way out
// leads, for the same reason enter leads on the list.
func (m *Shell) cardPageBindings() []binding {
	out := []binding{
		{key: "esc", label: "backlog", help: "back to the backlog list", bar: true},
		{key: "J/K", label: "prev/next", help: "previous / next card without leaving the page", bar: true},
	}
	for _, b := range m.boardBindings() {
		switch b.key {
		case "→", "←":
			continue
		case "j/k ↓↑":
			b.label, b.help, b.bar = "move", "move the action cursor", true
		case "enter":
			b.label, b.help = "run action", "run the highlighted action"
		}
		out = append(out, b)
	}
	return out
}
