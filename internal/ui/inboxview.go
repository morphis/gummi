package ui

import (
	"sort"
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

// inboxOldestFirst orders items oldest-first by At, stably — items with
// an equal At (every decision seeded off one startup query, say) keep
// list()'s own order. It exists apart from list() itself because list()'s
// insertion order is a contract next() cycling and a chunk of the test
// suite already lean on (DESIGN doesn't touch that); the inbox tab's own
// render and its own key handler are the only two callers that need the
// display order, and both call this so a row's index here always names
// the same item the other is acting on.
func inboxOldestFirst(items []attnItem) []attnItem {
	sorted := make([]attnItem, len(items))
	copy(sorted, items)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].At.Before(sorted[j].At) })
	return sorted
}

// inboxJump switches to the board tab with the named feature selected,
// clears its attention item, and opens the card page: the decision is
// pinned above the composer there (decision.go's openDecisionBlock), so
// opening the page is opening the card at its decision — the inbox dialog's
// old onJump callback (openInbox, pre-tab), now called directly by the
// tab's own enter handler instead of through a pushed-dialog closure.
func (m *Shell) inboxJump(id domain.FeatureID) tea.Cmd {
	m.inbox.remove(id)
	m.setTab(TabBoard)
	for i, r := range m.rows {
		if r.F.ID == id {
			m.sel = i
			m.syncActionFocus()
			return m.openCard()
		}
	}
	return nil
}

// inboxKey answers the inbox tab's own keys: j/k walk the queue, enter
// jumps to a card, x dismisses it, and u tops up a budget item in place
// — nextsteps.go's budget suggestion ("top up (u) ... from there") names
// this exact key. tab, alt+1/2/3 and ? never reach here: handleKey
// answers them above every surface.
func (m *Shell) inboxKey(key string) tea.Cmd {
	items := inboxOldestFirst(m.inbox.list())
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
			return m.inboxJump(items[m.inboxSel].Feature)
		}
	case "x":
		// A dismissed row is only cleared from memory: if its decision is
		// still genuinely open (nobody acted on it, the stage hasn't moved
		// on), the next restart's seeding brings it right back. That is the
		// honest behavior, not a bug — the durable record outlives the
		// dismissal on purpose (DESIGN §10.18); x is for "not now", not
		// "forget this ever happened".
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
		{key: "enter", label: "go", help: "open the card at its decision, clearing this item", bar: true},
		{key: "x", label: "dismiss", help: "clear this item without acting on it", bar: true},
		{key: "u", label: "top up", help: "raise the envelope and resume (budget items only)"},
		{key: "tab", label: "next tab", help: "cycle the tabs (board, inbox, agent)", bar: true},
		{key: "alt+1/2/3", label: "tab", help: "jump straight to board / inbox / agent"},
		{key: "i", label: "inbox", help: "stay on the needs-attention queue"},
		{key: "?", label: "help", bar: true},
		{key: "q", label: "quit"},
	}
}

// inboxRowLabel names the stop a row is waiting on: the card's current
// stage plus a word for the kind. Derived at render time rather than
// stored on the item, so a live-raised item — which carries no label of
// its own, only a kind — reads exactly like a seeded one the moment
// m.stageOf can name the card's stage. An unknown stage (the card left
// the board mid-flight) drops the missing half rather than rendering a
// stray leading space.
func inboxRowLabel(stage domain.Stage, kind attnKind) string {
	word := "gate"
	switch kind {
	case attnQuestion:
		word = "question"
	case attnBudget:
		word = "budget"
	case attnFailure:
		word = "failure"
	}
	if stage == "" {
		return word
	}
	return string(stage) + " " + word
}

// inboxRowText is a row's question with the stage its own label just
// named trimmed off the front. The recorded question has to be
// self-describing everywhere else it is read — the card's history, the
// driver's NDJSON, a notification — so it names its stage there; here
// the label column has already said it, and printing "implement gate
// implement finished — review & advance" spends the row's width saying
// the same word twice.
func inboxRowText(stage domain.Stage, text string) string {
	if stage == "" {
		return text
	}
	if trimmed, ok := strings.CutPrefix(text, string(stage)+" "); ok {
		return trimmed
	}
	return text
}

// inboxView renders the needs-attention queue full width: a header naming
// the count, one row per item (icon, card id, stage+kind label, question,
// right-hand HH:MM), oldest first, and — under the selected row only —
// the suggestion line m.suggestFor derives for it, mirroring backlogView's
// chrome (backlog.go) so the two tabs read as the same surface.
func (m *Shell) inboxView(w, h int) string {
	s := m.styles
	items := inboxOldestFirst(m.inbox.list())
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
		s.Faint.Render("·  "+strconv.Itoa(len(items))+" open decisions · oldest first"))
	line("")

	// the label is a column, not a prefix: padded to the widest of them so
	// the questions start on one line and the queue scans down rather than
	// having to be read across.
	labelW := 0
	labels := make([]string, len(items))
	for i, it := range items {
		labels[i] = inboxRowLabel(m.stageOf(it.Feature), it.Kind)
		labelW = max(labelW, ansi.StringWidth(labels[i]))
	}

	for i, it := range items {
		sel := i == m.inboxSel
		cursor := "  "
		row := s.Base
		if sel {
			cursor = s.BandMarker(true)
			row = s.Subtle
		}
		icon := attnIcon(s, it.Kind)
		stage := m.stageOf(it.Feature)
		label := padRight(labels[i], labelW)
		ts := it.At.Format("15:04")
		// The id, label and time are never truncated — only the question
		// gives up width when the row is tight (DESIGN's row-shape rule).
		prefix := cursor + icon + " " + s.CardID.Render(string(it.Feature)) + "  " + s.Faint.Render(label) + "  "
		prefixW := ansi.StringWidth(prefix)
		tsW := ansi.StringWidth(ts)
		text := ansi.Truncate(sanitize(inboxRowText(stage, it.Text)), max(w-prefixW-tsW-1, 6), "…")
		pad := max(w-prefixW-ansi.StringWidth(text)-tsW, 1)
		l := prefix + row.Render(text) + strings.Repeat(" ", pad) + s.Faint.Render(ts)
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
