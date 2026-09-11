package ui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Tab is one of gummi's top-level board views. It is a small int over a
// slice of tabDefs, not a hardcoded three-way switch: adding a second
// agent tab later (DESIGN's "out of scope for this pass" list) is then
// a config change to tabDefs, not a refactor of everything that walks
// the tab set.
type Tab int

const (
	// TabBoard is the full-width backlog (backlog.go): the only board
	// shape now that the split kanban+dashboard layout is gone. A fresh
	// shell always starts here.
	TabBoard Tab = iota
	// TabInbox is the needs-attention queue, promoted out of its modal
	// overlay onto a tab of its own (stage 2; a placeholder until then).
	TabInbox
	// TabAgent hosts gummi's own board conversation — an in-process
	// engine.BoardSession, not a hosted external program (boardthread.go).
	TabAgent
)

// tabDef names one tab in the bar: its identity and its label.
type tabDef struct {
	id    Tab
	label string
}

// tabDefs is the tab bar's contents, left to right after the wordmark.
func (m *Shell) tabDefs() []tabDef {
	return []tabDef{
		{id: TabBoard, label: "board"},
		{id: TabInbox, label: "inbox"},
		{id: TabAgent, label: "agent"},
	}
}

// setTab switches the active tab, clamping to a valid one. cardOpen
// belongs to the board tab alone (backlog.go); leaving the board closes
// it so it can never reappear stale on a tab that doesn't own it.
func (m *Shell) setTab(t Tab) {
	// bounds come from tabDefs, not a hardcoded upper tab: this type's
	// whole claim is that a fourth tab is a tabDefs edit, and a check
	// written against TabAgent would silently reject one.
	if int(t) < 0 || int(t) >= len(m.tabDefs()) {
		return
	}
	// A notice raised on one tab has no standing on another (BG-038's
	// rule, BG-087's missing wiring). Cleared first, so an action that
	// switches tabs AND reports something keeps its own message.
	m.clearTransientNotice()
	// An open card page is not closed on the way out. It is a board
	// surface like the spec view, the diff and the dependency picker, and
	// the rule for all of them is that leaving the tab hides them and
	// returning restores them — never discards (DESIGN §6). The card page
	// used to be the exception, which made it the one surface where a
	// glance at the inbox cost you your place: back on the board you were
	// at the list, and the card you were reading — with the draft still
	// sitting in its composer, since the input itself always survived the
	// trip — had to be found and opened again.
	//
	// Nothing here needs closing to stay honest. boardSurfacesLive gates
	// the page's rendering and its keyboard both, so while another tab is
	// up the page is neither drawn nor listening, and the rows and events
	// behind it are reloaded on the board's own cadence rather than
	// frozen at the moment you left.
	m.tab = t
}

// nextTab cycles every tab in tabDefs, the agent tab included. Every tab
// is gummi's own keymap now, so cycling through the agent tab is no
// different from cycling through any other — there is no hosted program
// underneath it that could hold tab and turn the cycle into a one-way
// door.
func (m *Shell) nextTab() tea.Cmd {
	defs := m.tabDefs()
	// Tab is an index into tabDefs by construction and setTab keeps it in
	// range, so the successor is plain modular arithmetic over the same
	// slice the bar draws from — a fourth tab needs no edit here.
	return m.gotoTab(defs[(int(m.tab)+1)%len(defs)].id)
}

// tabBadge names the small marker a tab wears next to its label, and
// whether it should read as an alert — one function so the bar and its
// tests agree on exactly what a badge means, rather than each caller
// reaching into m.inbox or the agent view on its own.
func (m *Shell) tabBadge(t Tab) (text string, alert bool) {
	switch t {
	case TabInbox:
		n := m.inbox.len()
		if n == 0 {
			return "", false
		}
		for _, it := range m.inbox.list() {
			if it.Kind == attnGate {
				alert = true
				break
			}
		}
		return "✉" + strconv.Itoa(n), alert
	case TabAgent:
		// nothing to show yet — a future unread-output marker (a "·" once
		// the board thread has produced output the user hasn't looked at)
		// belongs here, but no such tracking exists today.
		return "", false
	default:
		return "", false
	}
}

// tabBarView renders the one-line tab bar: the gummi wordmark pill
// exactly as the status bar renders it, then each tab from tabDefs. The
// active tab wears the same full band a selected card wears (Band,
// theme/band.go) — Band exists precisely to re-assert its background
// after a badge's own color resets, so the two compose the same way a
// banded card line and its badges already do (board.go's cardLine).
func (m *Shell) tabBarView(w int) string {
	s := m.styles
	segs := []string{s.PillMode.Render("gummi")}
	for _, td := range m.tabDefs() {
		active := td.id == m.tab
		text := s.Muted
		if active {
			text = s.BandText
		}
		seg := " " + text.Render(td.label)
		if badge, alert := m.tabBadge(td.id); badge != "" {
			bstyle := text
			if alert {
				bstyle = s.Warning
			}
			seg += " " + bstyle.Render(badge)
		}
		seg += " "
		if active {
			seg = s.Band(seg, 0, true)
		}
		segs = append(segs, seg)
	}
	sep := s.Separator.Render(" │ ")
	bar := strings.Join(segs, sep)

	// Right-align the navigation hint in the tab bar's own free space.
	// It cannot live in the status bar's hint row: that row is already
	// full at 120 columns, and it is the wrong place anyway — how to
	// reach a tab belongs beside the tabs.
	hint := s.Muted.Render("tab") + s.Faint.Render(" cycle · ") +
		s.Muted.Render("alt+1/2/3") + s.Faint.Render(" board/inbox/agent")
	if pad := w - ansi.StringWidth(bar) - ansi.StringWidth(hint) - 1; pad > 0 {
		bar += strings.Repeat(" ", pad) + hint
	}
	return ansi.Truncate(bar, w, "…")
}

// centeredNotice places an already-styled message in the middle of a
// w×h pane — the inbox and agent tabs' content until stage 2/3 give
// them real views (logo.Splash uses the same lipgloss.Place for the
// empty-board splash).
func centeredNotice(w, h int, msg string) string {
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, msg)
}
