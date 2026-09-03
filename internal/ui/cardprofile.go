package ui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
)

// The card-scoped counterpart of boardcomplete.go's "/profile": switching
// which profile drives the selected card rather than the board's own
// hosted agent. The board's version lives inline in the composer's own
// "/" completion popup; the card thread's composer has no such live-typing
// popup at all, so this is the plan's reinterpretation of "the same kind
// of value-tier picker" for that surface: two chained commandMenu
// overlays (the command tier's "profile" row, then this file's value
// tier) rather than porting boardcomplete.go's popup into the composer's
// already-intricate chip/decision/ask key routing.

// openCardProfilePicker pushes the value tier: one row per profile
// engine.CardProfiles(r.F.Stage) declares, labeled with what the card's
// own current role would actually resolve to under it — never the
// board/architect fallback BoardProfiles reports — and marked "current"
// against the selected card's own Feature.Profile, never m.board.Profile()
// (the board's /profile writes a different field entirely). No gotoTab:
// this stays on the card's own thread page.
func (m *Shell) openCardProfilePicker() tea.Cmd {
	r, ok := m.selected()
	if !ok || m.engine == nil {
		return nil
	}
	var rows []command
	for _, p := range m.engine.CardProfiles(r.F.Stage) {
		backend, model := labelBackendModel(p.Backend, p.Model)
		label := p.Name + " — " + backend + " · " + model
		if p.Name == r.F.Profile {
			label += " · current"
		}
		rows = append(rows, command{id: "profile-value:" + p.Name, label: label, available: true})
	}
	m.Overlay.Push(newCommandMenu(rows, m.runCommand))
	return nil
}

// confirmCardProfileChange answers a value-tier pick. It applies at once
// when the card has nothing live to lose — the same idle gating
// confirmBoardReopen already uses for the board's own reopen — else
// confirms first, since restarting a live session ends its in-flight
// turn.
func (m *Shell) confirmCardProfileChange(id domain.FeatureID, profile string) tea.Cmd {
	if m.engine == nil {
		return nil
	}
	if !m.engine.Get(id).Live() {
		return m.applyCardProfileChange(id, profile)
	}
	m.Overlay.Push(&confirmDialog{
		id:           "confirm-card-profile",
		cancelLabel:  "Cancel",
		confirmLabel: "Switch",
		question:     "switch profile to " + profile + "?",
		detail:       "the live session restarts under it",
		onConfirm:    func() tea.Cmd { return m.applyCardProfileChange(id, profile) },
	})
	return nil
}

// applyCardProfileChange runs the engine mutation, mirroring
// sendThreadMessage's closure shape: an error surfaces as a visible
// notice, success says nothing further, since the engine's own
// session/store events carry the change through the same as every other
// Run-calling UI path.
func (m *Shell) applyCardProfileChange(id domain.FeatureID, profile string) tea.Cmd {
	eng := m.engine
	return func() tea.Msg {
		if err := eng.ChangeProfile(context.Background(), id, profile); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		return nil
	}
}
