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

// inboxJump switches to the board tab with the named feature selected and
// opens the card page: the decision is pinned above the composer there
// (decision.go's openDecisionBlock), so opening the page is opening the
// card at its decision — the inbox dialog's old onJump callback (openInbox,
// pre-tab), now called directly by the tab's own enter handler instead of
// through a pushed-dialog closure.
//
// It does NOT clear the attention item. It used to, on the grounds that the
// pinned decision makes the queue row redundant — true while the reader is
// on the page, false the moment they press esc. Round 3 §1.2 walked it:
// enter on an unanswered question, read it, esc to think about it, and the
// board row carried no marker, the status bar said "running" and the inbox
// said "nothing needs you" while the agent sat blocked on a human. That is
// verbatim the state shell.go's EventAsk arm says must never happen; that
// fix closed the raise path and this one reopened it a keypress later.
//
// Reading a question is not answering it, and every kind has a real clearing
// path already — a gate on Advance (msgs.go), a question on the answer
// (decision.go), a budget on the top-up (shell.go's topUpBudget), a failure
// on the retry — so the row now survives until the thing it is asking about
// is actually done. x is still there for "not now".
func (m *Shell) inboxJump(id domain.FeatureID) tea.Cmd {
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
	default:
		return m.inboxSuggestedKey(key, items)
	}
	return nil
}

// inboxSuggestedKey runs the selected row's own suggested action when the
// key pressed is the key that row advertises.
//
// The ↳ line under the selected row is rendered from the same nextAction
// the card page builds, key column included — "g approve — moves the card
// into implement", "g land on main — verify passed". Those keys worked on
// the card page and nowhere else, so the inbox spent a line telling the
// reader to press something that did nothing at all: no movement, no
// notice, not even a refusal (round 3 §2.1). Rather than strip the key
// from the hint — it is the most useful thing on the row — the surface
// now honours whatever it printed, by construction: the same suggestFor
// the renderer reads decides what the key does, so the two cannot drift.
//
// The card is selected first because runCardAction (boardactions.go)
// works against the board's selection, the way it does when the same row
// is activated from the card page.
func (m *Shell) inboxSuggestedKey(key string, items []attnItem) tea.Cmd {
	if key == "" || m.inboxSel >= len(items) {
		return nil
	}
	id := items[m.inboxSel].Feature
	acts := m.suggestFor(id)
	if len(acts) == 0 || acts[0].key != key {
		return nil
	}
	for i, r := range m.rows {
		if r.F.ID != id {
			continue
		}
		m.sel = i
		m.syncActionFocus()
		a := acts[0]
		return m.runCardAction(cardAction{
			id: a.id, key: a.key, label: a.label, why: a.detail, danger: a.danger,
		})
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
		{key: "u", label: "top up", help: "raise the budget and resume (budget items only)"},
		{key: "alt+1/2/3", label: "tab", help: "jump straight to board / inbox / agent"},
		{key: "i", label: "inbox", help: "stay on the needs-attention queue"},
		// The inbox is a tab, not a modal, so cycling away IS its way out —
		// there is no esc to hold the position. It therefore goes last, for
		// the reason esc does elsewhere: the status bar drops hints from
		// the second-to-last backwards, so whatever a surface puts last is
		// the row that survives (statusbar.Render).
		{key: "?", label: "help", bar: true},
		{key: "tab", label: "next tab", help: "cycle the tabs (board, inbox, agent)", bar: true},
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

	// "N open decision(s)" used to label the header no matter what sat in
	// the queue — a live drive caught it reading "1 open decision" over a
	// RUN FAILURE row, which nobody decides, it just gets fixed. The
	// queue mixes four kinds (attnGate, attnFailure, attnQuestion,
	// attnBudget in inbox.go) and only some of them are actually a
	// decision, so the header names the umbrella the row labels
	// themselves (inboxRowLabel) already use — "item" — rather than
	// asserting a kind that may not hold for the row on screen.
	line(" " + s.PaneTitleActive.Render("NEEDS YOU") + "  " +
		s.Faint.Render("·  "+strconv.Itoa(len(items))+" open item"+plural(len(items))+" · oldest first"))
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
