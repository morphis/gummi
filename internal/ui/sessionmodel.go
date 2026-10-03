package ui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
)

// The card thread's half of the session model picker (DESIGN §19.8): the
// web face's picker is the popover beside Send (session.js), and the TUI's
// is this card page's "model" action — the same offer in both surfaces,
// read from the same engine seam. A freeform card runs on the agent and
// model its person picked, not on a profile role, so its menu switches
// the model instead of the profile; a card in the workflow never reaches
// any of it.
//
// The offer is open on purpose: what a session's model picker lists is
// the agent's own answer about the models it provides
// (engine.SessionModelChoices — its catalog where it can say one, merged
// with the ids the workspace's profiles run there), and any other id may
// still be typed. That answer is a subprocess or RPC read, so it is
// fetched off the loop in the probe a command returns — the prLinkDialog
// pattern — and the model tier opens on its message, never inline.

// openCardModelPicker pushes the agent tier: one row per session backend,
// dimmed when this host cannot start it, the one this card's session runs
// on marked. Choosing one probes its models and opens the model tier on
// the probe's message.
func (m *Shell) openCardModelPicker() tea.Cmd {
	r, ok := m.selected()
	if !ok {
		return nil
	}
	if m.engine == nil {
		m.notice = noticeMsg{text: m.noAgent(" (set a model/provider to enable sessions)"), isErr: true}
		return nil
	}
	cur, _ := m.engine.SessionModel(r.F)
	var rows []command
	for _, name := range engine.SessionBackends {
		installed := m.engine.HasAgent(name) || agentInstalled(name)
		label := name
		if name == cur {
			label += " · current"
		} else if !installed {
			label += " — not installed"
		}
		rows = append(rows, command{
			id:        "session-backend:" + name,
			label:     label,
			available: installed,
		})
	}
	m.Overlay.Push(newCommandMenu(rows, m.runSessionBackend))
	return nil
}

// runSessionBackend answers the agent tier's pick: the probe for that
// backend's models, off the loop, whose message opens the model tier.
func (m *Shell) runSessionBackend(id string) tea.Cmd {
	if m.engine == nil {
		return nil
	}
	r, ok := m.selected()
	if !ok {
		return nil
	}
	backend, ok := strings.CutPrefix(id, "session-backend:")
	if !ok {
		return nil
	}
	eng, fid := m.engine, r.F.ID
	return func() tea.Msg {
		return sessionModelsMsg{
			id:      fid,
			backend: backend,
			models:  eng.SessionModelChoices(context.Background(), backend),
		}
	}
}

// sessionModelsMsg is the probe's outcome: the merged model list for one
// backend (empty when the agent cannot enumerate and no profile names
// one), for the card the tier was opened on.
type sessionModelsMsg struct {
	id      domain.FeatureID
	backend string
	models  []string
}

// handleSessionModelsMsg opens the model tier. The card may have moved on
// between the pick and the probe (a backend switch elsewhere, the card
// deleted); a card that is no longer an open session gets nothing, since
// the picker's answer would land on a menu it cannot change.
func (m *Shell) handleSessionModelsMsg(msg sessionModelsMsg) {
	r, ok := m.rowByID(msg.id)
	if !ok || !r.F.IsFreeform() || r.F.Stage != domain.StageOpen {
		return
	}
	m.Overlay.Push(m.newSessionModelMenu(r.F, msg.backend, msg.models))
}

// newSessionModelMenu builds the model tier: the agent's own models and
// the workspace's ids, the pair this session runs on marked, the agent's
// default model where it takes none, and — last, growing out of the
// filter itself — the row that runs the id nobody offered. A typed id
// that the backend would refuse is refused by checkSessionPick at the
// pick, in the engine's own words, the same courtesy rule the web
// picker's pattern row follows.
func (m *Shell) newSessionModelMenu(f domain.Feature, backend string, models []string) *commandMenu {
	curBackend, curModel := m.engine.SessionModel(f)
	needs, _, _ := engine.SessionModelRule(backend)
	var rows []command
	if !needs {
		rows = append(rows, m.sessionModelRow(backend, "", curBackend, curModel, "the agent's default model"))
	}
	for _, model := range models {
		rows = append(rows, m.sessionModelRow(backend, model, curBackend, curModel, model))
	}
	menu := newCommandMenu(rows, m.runSessionModel(f.ID, backend))
	menu.dynamic = func(q string) []command {
		return []command{{
			id:        "session-model-typed:" + q,
			label:     "use " + q + " on " + backend,
			available: true,
		}}
	}
	return menu
}

// sessionModelRow is one model tier row: the id (or the default-model
// stand-in) as the label, " · current" on the pair this session already
// runs on.
func (m *Shell) sessionModelRow(backend, model, curBackend, curModel, label string) command {
	if backend == curBackend && model == curModel {
		label += " · current"
	}
	return command{
		id:        "session-model:" + model,
		label:     label,
		available: true,
	}
}

// runSessionModel answers the model tier's pick — a listed id, or the
// typed one the dynamic row carries in its own id. The pick is checked
// the way the web face's is (checkSessionPick) and switched the same way
// both faces switch (switchSessionModel): off the loop, with the
// engine's refusals — a turn in flight, an id the backend would refuse —
// in the notice where the picker was.
func (m *Shell) runSessionModel(id domain.FeatureID, backend string) func(string) tea.Cmd {
	return func(rowID string) tea.Cmd {
		model, ok := strings.CutPrefix(rowID, "session-model:")
		if !ok {
			if t, ok := strings.CutPrefix(rowID, "session-model-typed:"); ok {
				model = t
			} else {
				return nil
			}
		}
		if problem := m.checkSessionPick(backend, model); problem != "" {
			m.notice = noticeMsg{text: sanitize(problem), isErr: true}
			return nil
		}
		return m.switchSessionModel(id, backend, model)
	}
}
