package ui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
)

// openCard opens the selected card's page. The action list is the only
// list on that page, so it takes the arrow keys on arrival — there is no
// second region to hand them to. It also kicks off the selected card's
// event-log load (shell.go's loadCardEvents): the thread's folded stage
// receipts need it, and the card page is the one place that reads it, so
// it is fetched on arrival rather than on every board refresh.
func (m *Shell) openCard() tea.Cmd {
	r, ok := m.selected()
	if !ok {
		return nil
	}
	m.cardOpen = true
	m.actionCursor = 0
	m.actionsExpanded = false
	return m.loadCardEvents(r.F.ID)
}

// closeCard returns to the backlog list.
func (m *Shell) closeCard() {
	m.cardOpen = false
	m.blurActions()
}

// stepCard moves to the previous/next card without leaving the page —
// J/K, so j/k stay on the action list where the eye already is. It
// re-fires the event-log load for the newly selected card, same as
// openCard: the thread's folded receipts belong to whichever card is on
// screen now.
func (m *Shell) stepCard(delta int) tea.Cmd {
	m.moveSel(delta)
	m.actionCursor = 0
	m.actionsExpanded = false
	r, ok := m.selected()
	if !ok {
		return nil
	}
	return m.loadCardEvents(r.F.ID)
}

// actionsOwnArrows reports whether the card's action list owns ↑↓ and
// enter — true exactly when a card page is open, because the page has
// one list and nothing else on it to hand the arrows to.
func (m *Shell) actionsOwnArrows() bool {
	return m.cardOpen
}

// backlogKey answers the board tab's keys — movement, enter, and the way
// in and out of a card page. It reports whether it handled the key;
// everything it does not handle falls through to boardVerb, so the card
// verbs (g, v, m, …) keep working from the list exactly as they do on
// the card page.
func (m *Shell) backlogKey(key string) (tea.Cmd, bool) {
	// the ingest feed takes the foreground over the board; its own keys
	// (esc backgrounds it) must reach boardVerb unintercepted.
	if m.ingestRun != nil && !m.ingestRun.hidden {
		return nil, false
	}
	if !m.cardOpen {
		switch key {
		case "enter", "right", "l":
			return m.openCard(), true
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
		return m.stepCard(1), true
	case "K":
		return m.stepCard(-1), true
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

// backlogView renders the full-width backlog: cards grouped by
// super-state, given the whole terminal to spend on them, and scrolled
// to keep the selection on screen.
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
// the way back and the card's position in the backlog, then the card's
// thread (thread.go) — with the whole terminal to spend on it.
//
// This delegates to threadView rather than the older dashboardView, which
// stays in the package untouched (board_test.go still goldens it
// directly).
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
	return ansi.Truncate(crumb, w, "…") + "\n" + m.threadView(w, max(h-1, 1))
}

// backlogBindings is the list level's key table: the board's own table
// with enter re-pointed at the card page.
//
// The one key this level has to teach is enter, so it leads: a narrow
// status bar fits two hints, and it should spend them on the way in
// rather than on arrow keys a list already implies.
func (m *Shell) backlogBindings() []binding {
	var out []binding
	var lead []binding
	for _, b := range m.boardBindings() {
		switch b.key {
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
		case "j/k ↓↑":
			b.label, b.help, b.bar = "move", "move the action cursor", true
		case "enter":
			b.label, b.help = "run action", "run the highlighted action"
		}
		out = append(out, b)
	}
	return out
}
