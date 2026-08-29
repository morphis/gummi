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
	// TabAgent hosts a pty running the user's own coding CLI, composited
	// straight into the screen buffer (stage 3; a placeholder until then).
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
		{TabBoard, "board"},
		{TabInbox, "inbox"},
		{TabAgent, "agent"},
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
	if m.tab == TabBoard && t != TabBoard {
		m.cardOpen = false
	}
	m.tab = t
}

// nextTab cycles every tab in tabDefs, the agent tab included. It used
// to skip that one: tab belongs to the hosted pty once you are there, so
// cycling onto it was a one-way door. That is no longer true — handleKey
// answers alt+1/2/3 above every surface, the hosted CLI included, so the
// way back out is always one keystroke. Landing there is a visit, not a
// trap, and a cycle that quietly omits a third of the tab bar is its own
// small lie.
func (m *Shell) nextTab() tea.Cmd {
	defs := m.tabDefs()
	// Tab is an index into tabDefs by construction and setTab keeps it in
	// range, so the successor is plain modular arithmetic over the same
	// slice the bar draws from — a fourth tab needs no edit here.
	next := defs[(int(m.tab)+1)%len(defs)].id
	m.setTab(next)
	if next == TabAgent {
		// same deferred spawn as alt+3: the first arrival pays for the
		// pty, and a board that never cycles that far never does.
		return m.ensureAgent()
	}
	return nil
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
		// stage 3 wires unread-output tracking (a "·" once the pty has
		// produced output the user hasn't looked at); nothing to show
		// before there is an agent view to watch.
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
	// reach a tab belongs beside the tabs. It still names alt+1/2/3
	// explicitly even though tab now reaches all three: the hosted CLI on
	// the agent tab keeps tab for itself, so alt+N is the only way back
	// out, and that is exactly where a user has no other hint to read.
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
