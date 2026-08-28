package ui

import (
	tea "charm.land/bubbletea/v2"
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
	if n := l.Len(); n > 0 {
		l.cursor = clamp(m.actionCursor, 0, n-1)
	}
	return l
}

// moveAction steps the action cursor, clamping through the live list so
// a shrunk list (an action that stopped applying) can't strand it past
// the end.
func (m *Shell) moveAction(delta int) {
	l := m.cardActions()
	l.Move(delta)
	m.actionCursor = l.cursor
}

// resetActionFocus returns focus to the cards and rewinds the action
// cursor. Called whenever the selected card changes: the list is a
// property of the card, so carrying a cursor across cards would land the
// user on an unrelated action.
func (m *Shell) resetActionFocus() {
	m.actionFocused = false
	m.actionCursor = 0
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
	return m.boardKey(id)
}
