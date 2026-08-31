package ui

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// handoverEvents is id's log filtered to the rows that mark a card
// changing hands — the took-over/handed-back boundaries, never the
// mode-change rows SetGateApproval writes on every switch move, which
// share the kind and mean something else entirely (AutopilotPayload's
// doc comment, state/cardevents.go).
func handoverEvents(t *testing.T, m *Shell, id domain.FeatureID) []state.AutopilotPayload {
	t.Helper()
	evs, err := m.store.Events(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var out []state.AutopilotPayload
	for _, ev := range evs {
		if ev.Kind != state.EventAutopilot {
			continue
		}
		var p state.AutopilotPayload
		if err := json.Unmarshal([]byte(ev.Payload), &p); err != nil {
			t.Fatal(err)
		}
		if p.Event == "" {
			continue // a mode change, not a boundary
		}
		out = append(out, p)
	}
	return out
}

// TestSwitchRecordsTheHandover: pointing the switch at a card that it
// actually moves records the takeover, and the mode it was handed over
// under travels with the row — the same card can be handed over twice
// under different stops, and this row is the only place that survives.
func TestSwitchRecordsTheHandover(t *testing.T) {
	m := oneCardWorkspace(t)
	f := m.rows[0].F
	plan := m.planAutopilot(f)
	if plan.to == "" {
		t.Fatalf("a todo card's plan moves it somewhere: %+v", plan)
	}

	m = pump(t, m, m.startAutopilot(f, domain.GateFull, plan))

	got := handoverEvents(t, m, f.ID)
	if len(got) != 1 || got[0].Event != state.AutopilotTookOver {
		t.Fatalf("handover rows = %+v, want exactly one took-over", got)
	}
	if got[0].Mode != domain.GateFull {
		t.Fatalf("took-over mode = %q, want %q", got[0].Mode, domain.GateFull)
	}
}

// TestSwitchingOffRecordsTheHandback: off is the one mode that gives a
// card back rather than taking it, and it is the gesture that would
// otherwise leave no trace at all — nothing parks, nothing is typed, so
// without this row the period would stay open over everything that
// followed.
func TestSwitchingOffRecordsTheHandback(t *testing.T) {
	m := oneCardWorkspace(t)
	f := m.rows[0].F
	plan := m.planAutopilot(f)

	m = pump(t, m, m.startAutopilot(f, domain.GateFull, plan))
	m = pump(t, m, m.startAutopilot(m.rows[0].F, domain.GateOff, m.planAutopilot(m.rows[0].F)))

	got := handoverEvents(t, m, f.ID)
	if len(got) != 2 {
		t.Fatalf("handover rows = %+v, want a took-over then a handed-back", got)
	}
	if got[1].Event != state.AutopilotHandedBack {
		t.Fatalf("second row = %q, want %q", got[1].Event, state.AutopilotHandedBack)
	}
}

// TestModeAloneIsNotAHandover is the restraint that keeps the record
// honest. A card sitting at a gate autopilot may never cross on its own
// — review and verify, where the choice stays a person's under every
// mode — is not moved by the switch: only the stored mode changes. There
// is no period to mark, and marking one would draw a stretch around a
// card that sat still the whole time.
func TestModeAloneIsNotAHandover(t *testing.T) {
	m := reviewGateWorkspace(t)
	f := m.rows[m.sel].F
	plan := m.planAutopilot(f)
	if plan.to != "" {
		t.Fatalf("a review gate is not autopilot's to cross: %+v", plan)
	}

	m = pump(t, m, m.startAutopilot(f, domain.GateFull, plan))

	if got := handoverEvents(t, m, f.ID); len(got) != 0 {
		t.Fatalf("handover rows = %+v, want none — nothing changed hands", got)
	}
}
