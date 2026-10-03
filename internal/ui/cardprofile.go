package ui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
)

// The card-scoped profile switch: switching which profile drives the
// selected card. The card thread's composer has no live-typing popup at
// all, so this is two chained commandMenu overlays (the command tier's
// "profile" row, then this file's value tier) sitting on the composer's
// already-intricate chip/decision/ask key routing.

// labelBackendModel words an empty Backend or Model coming back from the
// engine as the thing it falls back to, never left blank — a blank field
// reads as missing data, not as "the default". Shared by the card-scoped
// /profile picker (openCardProfilePicker, here) so its wording can't
// drift from any other picker's.
func labelBackendModel(backend, model string) (string, string) {
	if backend == "" {
		backend = "engine default"
	}
	if model == "" {
		model = "backend default"
	}
	return backend, model
}

// openCardProfilePicker pushes the value tier: one row per profile
// engine.CardProfiles(r.F.Stage) declares, labeled with what the card's
// own current role would actually resolve to under it, and marked
// "current" against the selected card's own Feature.Profile. No gotoTab:
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
// when the card has nothing live to lose, else confirms first, since
// restarting a live session ends its in-flight turn.
func (m *Shell) confirmCardProfileChange(id domain.FeatureID, profile string) tea.Cmd {
	if m.engine == nil {
		return nil
	}
	if !m.engine.Get(id).Live() {
		return m.applyCardProfileChange(id, profile)
	}
	m.Overlay.Push(&confirmDialog{
		card:         id,
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
