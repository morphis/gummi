package ui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
)

// BG-046 repro: once a stage becomes history, its message label must
// still show the stage's speaking role (e.g. "reviewer"), the same role
// the live pane showed for the same turn — not the raw persisted
// author, which engine/persist.go always writes as one of the engine's
// four fixed constants (user/system/assistant/tool) and never as the
// stage role.
func TestBG046ReproHistoryLabelUsesRawAuthorNotRole(t *testing.T) {
	s := m0Styles()
	answered := map[string]bool{}
	payload, _ := json.Marshal(messagePayload{
		Author:  string(engine.AuthorAssistant),
		Content: "Repo checks clean; verification plan satisfied.\n\nVERDICT: pass",
	})
	ev := state.CardEvent{Kind: state.EventMessage, Payload: string(payload)}

	got := ansi.Strip(stageEventLine(s, ev, 80, "reviewer", answered))
	t.Logf("got=%q", got)
	if strings.HasPrefix(got, "assistant") {
		t.Fatalf("history label fell back to the raw author %q; want the reviewer stage role, as the live pane shows for the same turn", "assistant")
	}
	if !strings.HasPrefix(got, "reviewer") {
		t.Errorf("history label = %q, want it to start with the stage role %q", got, "reviewer")
	}
}
