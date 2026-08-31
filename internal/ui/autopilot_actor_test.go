package ui

import (
	"context"
	"encoding/json"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// newestGateActor is the actor on the most recent gate event in id's
// log, whatever stage it was raised in — the question these tests ask is
// who crossed, not where.
func newestGateActor(t *testing.T, m *Shell, id domain.FeatureID) string {
	t.Helper()
	evs, err := m.store.Events(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	actor := ""
	for _, ev := range evs {
		if ev.Kind != state.EventGate {
			continue
		}
		var p state.GatePayload
		if err := json.Unmarshal([]byte(ev.Payload), &p); err != nil {
			t.Fatal(err)
		}
		actor = p.Actor
	}
	if actor == "" {
		t.Fatalf("%s has no gate event with an actor", id)
	}
	return actor
}

// oneCardWorkspace is a workspace holding a single freshly minted card,
// sitting at todo with nothing run.
func oneCardWorkspace(t *testing.T) *Shell {
	t.Helper()
	m, _ := newWorkspace(t)
	m = pump(t, m, m.Init())
	m = press(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	m = typeString(t, m, "Actor")
	return press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
}

// TestAutopilotStepRecordsAutopilotActor: a crossing made because a
// person handed the card to autopilot is filed under autopilot, not
// under the review loop.
//
// autoStep and autoStepStage used to hardcode "review" for every caller,
// which was true of the only caller they originally had and false of the
// one startAutopilot added later. The cost was not cosmetic: the actor
// is what every reader downstream uses to tell a crossing the machine
// made on its own from one a person made, so a mislabelled crossing
// misleads the audit trail and the card's own history line alike.
func TestAutopilotStepRecordsAutopilotActor(t *testing.T) {
	m := oneCardWorkspace(t)
	id := m.rows[0].F.ID

	m = pump(t, m, m.autoStepStage(id, domain.StageBrainstorm, "entering brainstorm", state.ActorAutopilot))

	if got := newestGateActor(t, m, id); got != state.ActorAutopilot {
		t.Fatalf("crossing actor = %q, want %q — a handover recorded as the review loop's own work",
			got, state.ActorAutopilot)
	}
}

// TestReviewLoopKeepsItsOwnActor is the other half of the same rule, and
// the reason the actor is a parameter rather than a swapped constant:
// the review→fix loop's continuations really are the loop's, and must go
// on saying so. A card can run its review loop with autopilot switched
// off entirely, so relabelling these would claim a handover that never
// happened.
func TestReviewLoopKeepsItsOwnActor(t *testing.T) {
	m := oneCardWorkspace(t)
	id := m.rows[0].F.ID

	m = pump(t, m, m.autoStepStage(id, domain.StageBrainstorm, "re-shaping", "review"))

	if got := newestGateActor(t, m, id); got != "review" {
		t.Fatalf("review loop crossing actor = %q, want \"review\"", got)
	}
}
