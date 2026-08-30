package ui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/ui/theme"
)

// attnIcon names the glyph a needs-attention item wears by kind — split
// out of the row renderer so tabBadge (tabs.go) and the inbox tab agree
// on exactly what each kind looks like.
func attnIcon(s *theme.Styles, k attnKind) string {
	switch k {
	case attnFailure:
		return s.Error.Render("✗")
	case attnQuestion:
		return s.Info.Render("?")
	case attnBudget:
		return s.Warning.Render("$")
	default:
		return s.Warning.Render("✉")
	}
}

// clampInboxSel keeps the inbox cursor inside [0, n-1] (0 when n <= 0).
// Called on every entry into inboxKey/inboxView rather than only on the
// moves that shrink the queue, because the queue can lose an item out
// from under the tab — an engine event clearing a gate while the inbox
// tab merely sits on screen — without any key press to clamp on.
func (m *Shell) clampInboxSel(n int) {
	m.inboxSel = clamp(m.inboxSel, 0, max(n-1, 0))
}

// moveInboxSel steps the inbox cursor by delta, clamped to the queue.
func (m *Shell) moveInboxSel(delta, n int) {
	m.inboxSel = clamp(m.inboxSel+delta, 0, max(n-1, 0))
}

// inboxJump switches to the board tab with the named feature selected
// and clears its attention item — the inbox dialog's old onJump
// callback (openInbox, pre-tab), now called directly by the tab's own
// enter handler instead of through a pushed-dialog closure.
func (m *Shell) inboxJump(id domain.FeatureID) {
	m.inbox.remove(id)
	m.setTab(TabBoard)
	for i, r := range m.rows {
		if r.F.ID == id {
			m.sel = i
			m.syncActionFocus()
			return
		}
	}
}

// inboxKey answers the inbox tab's own keys: j/k walk the queue, enter
// jumps to a card, x dismisses it, and u tops up a budget item in place
// — nextsteps.go's budget suggestion ("top up (u) ... from there") names
// this exact key. tab, alt+1/2/3 and ? never reach here: handleKey
// answers them above every surface.
func (m *Shell) inboxKey(key string) tea.Cmd {
	items := m.inbox.list()
	m.clampInboxSel(len(items))
	switch key {
	case "j", "down":
		m.moveInboxSel(1, len(items))
	case "k", "up":
		m.moveInboxSel(-1, len(items))
	case "i":
		// already here; i is idempotent on its own tab rather than a
		// close-toggle the way it closed the old modal (esc/i/q).
		m.setTab(TabInbox)
	case "enter":
		if m.inboxSel < len(items) {
			m.inboxJump(items[m.inboxSel].Feature)
		}
	case "x":
		if m.inboxSel < len(items) {
			m.inbox.remove(items[m.inboxSel].Feature)
			m.clampInboxSel(len(items) - 1)
		}
	case "u":
		if m.inboxSel < len(items) && items[m.inboxSel].Kind == attnBudget {
			return m.topUpBudget(items[m.inboxSel].Feature)
		}
	}
	return nil
}

// inboxBindings is the inbox tab's key table (keymap.go's
// activeSurface), replacing the placeholder it wore before this queue
// had a real view.
func (m *Shell) inboxBindings() []binding {
	return []binding{
		{key: "j/k ↓↑", label: "select", help: "select item", bar: true},
		{key: "enter", label: "go", help: "jump to the card and clear this item", bar: true},
		{key: "x", label: "dismiss", help: "clear this item without acting on it", bar: true},
		{key: "u", label: "top up", help: "raise the envelope and resume (budget items only)"},
		{key: "tab", label: "next tab", help: "cycle the tabs (board, inbox, agent)", bar: true},
		{key: "alt+1/2/3", label: "tab", help: "jump straight to board / inbox / agent"},
		{key: "i", label: "inbox", help: "stay on the needs-attention queue"},
		{key: "?", label: "help", bar: true},
		{key: "q", label: "quit"},
	}
}

// inboxView renders the needs-attention queue full width: a header
// naming the count, one row per item (icon, card id, text), and — under
// the selected row only — the suggestion line m.suggestFor derives for
// it, mirroring backlogView's chrome (backlog.go) so the two tabs read
// as the same surface.
func (m *Shell) inboxView(w, h int) string {
	s := m.styles
	items := m.inbox.list()
	if len(items) == 0 {
		var b strings.Builder
		b.WriteString("\n " + s.PaneTitleActive.Render("NEEDS YOU") + "\n\n")
		b.WriteString(" " + s.Faint.Render("nothing needs you") + "\n")
		return b.String()
	}
	m.clampInboxSel(len(items))

	var b strings.Builder
	line := func(str string) { b.WriteString(ansi.Truncate(str, w, "…") + "\n") }

	line(" " + s.PaneTitleActive.Render("NEEDS YOU") + "  " +
		s.Faint.Render("·  "+strconv.Itoa(len(items))+" items"))
	line("")

	for i, it := range items {
		sel := i == m.inboxSel
		cursor := "  "
		row := s.Base
		if sel {
			cursor = s.BandMarker(true)
			row = s.Subtle
		}
		icon := attnIcon(s, it.Kind)
		text := ansi.Truncate(sanitize(it.Text), max(w-20, 6), "…")
		l := cursor + icon + " " + s.CardID.Render(string(it.Feature)) + " " + row.Render(text)
		if sel {
			l = s.Band(l, w, true)
		}
		b.WriteString(l + "\n")
		if sel {
			if acts := m.suggestFor(it.Feature); len(acts) > 0 {
				a := acts[0]
				line("  " + s.Faint.Render("↳ ") + s.KeyHint.Render(a.key) + " " +
					s.Subtle.Render(a.label) + s.Faint.Render(" — "+sanitize(a.detail)))
			}
		}
	}
	return b.String()
}
