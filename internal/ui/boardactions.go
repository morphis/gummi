package ui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
)

// This file joins the two action surfaces to the board's key handler.
// Both a card action and a command carry the board key they stand for,
// so invoking one is boardKey(key) — the guards each case already
// carries (a research card refusing a merge, a card with no worktree
// refusing a diff) run exactly once, in one place, whichever surface
// the user came through.

// cardActions builds the selected card's action list, positioned at the
// stored cursor. It is rebuilt on every use rather than cached: the
// board reloads rows often, and a cached list would keep offering
// actions for a stage the card has already left.
func (m *Shell) cardActions() *cardActionList {
	r, ok := m.selected()
	if !ok {
		return newCardActionList(nil)
	}
	l := newCardActionList(cardActionsFor(m.nextInputFor(r), r))
	l.expanded = m.actionsExpanded
	if n := l.Len(); n > 0 {
		l.cursor = clamp(m.actionCursor, 0, n-1)
	}
	return l
}

// blurActions hands the arrow keys back to the cards and refolds the
// list. The fold exists so the pane stays short while you are reading
// it, so an expansion is scoped to the visit that asked for it.
func (m *Shell) blurActions() {
	m.actionFocused = false
	m.actionsExpanded = false
}

// moveAction steps the action cursor, clamping through the live list so
// a shrunk list (an action that stopped applying) can't strand it past
// the end.
func (m *Shell) moveAction(delta int) {
	l := m.cardActions()
	l.Move(delta)
	m.actionCursor = l.cursor
}

// syncActionFocus returns focus to the cards and rewinds the action
// cursor whenever the selected card changed. It keys off the card's
// identity rather than the selection index, because a board reload can
// move the selection onto a different card with no keypress at all —
// and a cursor left on "delete" then belongs to the wrong card.
func (m *Shell) syncActionFocus() {
	var id domain.FeatureID
	if r, ok := m.selected(); ok {
		id = r.F.ID
	}
	if id == m.actionCard {
		return
	}
	m.actionCard = id
	m.actionCursor = 0
	m.blurActions()
}

// globalCommands is the space menu's contents on the board: the actions
// that belong to no particular card. Per-card actions are already on
// screen in the dashboard's list, so repeating them here would only make
// the menu longer without making anything more reachable.
//
// A command's id is the board key it stands for, so onRun is boardKey
// itself and there is no second mapping to drift.
func (m *Shell) globalCommands() []command {
	attached := m.attached()
	return []command{
		{id: "n", label: "New feature", key: "n", available: attached},
		{id: "B", label: "New bug", key: "B", available: attached},
		{id: "R", label: "New research card", key: "R", available: attached},
		{id: "I", label: "Ingest a spec into features", key: "I", available: attached && m.engine != nil},
		{id: "G", label: "Import bugs from GitHub", key: "G", available: attached && m.engine != nil},
		{id: "i", label: "Open the needs-you inbox", key: "i", available: attached},
		{id: "S", label: "Sort todo by severity", key: "S", available: attached},
		{id: "agent-cli", label: agentChooseCommandLabel, key: "", available: attached},
		{id: "?", label: "Show the keys for this surface", key: "?", available: true},
		{id: "q", label: "Quit gummi", key: "q", available: true},
	}
}

// runCommand is the space menu's invoke path. q and ? are answered by
// handleKey above the attached check, so they never reach boardKey and
// have to be routed here explicitly — everything else is a board key.
func (m *Shell) runCommand(id string) tea.Cmd {
	switch id {
	case "q":
		return m.quitCmd()
	case "?":
		m.Overlay.Push(m.helpOverlay())
		return nil
	}
	return m.boardVerb(id)
}

// confirmDuplicate raises the duplicate confirm. Duplicating used to sit
// on the board's `y`, which is also "yes" in the confirm dialog `y`
// itself raises — one letter, two meanings, one keystroke apart. It has
// no accelerator now: it is a rare action, the action list and the
// command menu both reach it, and `y` gets to mean exactly one thing.
func (m *Shell) confirmDuplicate() tea.Cmd {
	r, ok := m.selected()
	if !ok {
		return nil
	}
	f := r.F
	m.Overlay.Push(&confirmDialog{
		id:           "confirm-duplicate",
		cancelLabel:  "Cancel",
		confirmLabel: "Duplicate",
		question:     "duplicate " + string(f.ID) + "?",
		detail:       f.Title + " — fresh copy in todo (same skips, profile, envelope); this card stays",
		onConfirm:    func() tea.Cmd { return m.duplicateFeature(f.ID) },
	})
	return nil
}

// runCardAction performs one entry from the card's action list. A keyed
// action goes through boardVerb so it hits the same guarded case body as
// its accelerator; a keyless one (duplicate, gate, and the fold row) is
// handled here, since there is no key to route it by.
func (m *Shell) runCardAction(a cardAction) tea.Cmd {
	if a.key != "" {
		return m.boardVerb(a.key)
	}
	switch a.id {
	case expandID:
		// the cursor stays on the fold row, so enter toggles in place and
		// the newly revealed actions start one ↓ away.
		m.actionsExpanded = !m.actionsExpanded
		return nil
	case "duplicate":
		return m.confirmDuplicate()
	case "changes":
		// this option IS the composer's words: typing aims at it and enter
		// delivers the line as the turn asking for the changes
		// (decision.go's deliverDecisionWords). Reached with nothing typed
		// there is nothing to send, so it says what it wants rather than
		// answering with silence — the bar named it, so enter owes a
		// response.
		m.notice = noticeMsg{text: "type what should change — your line goes back with it"}
		return nil
	case "gate":
		// the two-state toggle (tighten applies immediately, loosen
		// confirms first) is superseded by the autopilot overlay
		// (autopilot.go): its own confirm button is the deliberate act
		// that protects a loosening move now, so there is no second
		// confirm layered on top of it here.
		if r, ok := m.selected(); ok {
			return m.openAutopilot(r.F)
		}
	}
	return nil
}
