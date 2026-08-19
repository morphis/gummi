package ui

import (
	"context"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/engine"
)

func TestScribeEstimateBlendsAndPersists(t *testing.T) {
	m, _ := diffWorkspace(t) // FD-001 at review, worktree exists
	ctx := context.Background()

	// a historical envelope is already in place
	f, _ := m.store.GetFeature(ctx, "FD-001")
	f.Budget.Envelope = 100
	if err := m.store.UpdateFeature(ctx, &f); err != nil {
		t.Fatal(err)
	}

	// a scribe that estimates 200; blended with the historical 100 → 150
	eng := engine.New(engine.Config{
		Agents: singleAgent(&agent.Fake{Responder: func(agent.SessionOpts, string) []agent.Event {
			return []agent.Event{{Kind: agent.EventMessage, Text: "Sizeable.\nESTIMATE: 200"}, {Kind: agent.EventIdle}}
		}}),
		Store: m.store, Pool: m.wt, Workspace: m.ws, MaxActive: 1,
	})
	t.Cleanup(func() { eng.Close() })
	m.AttachEngine(eng)

	m = pump(t, m, m.scribeEstimate("FD-001"))
	got, _ := m.store.GetFeature(ctx, "FD-001")
	if got.Budget.Envelope != 150 {
		t.Errorf("envelope = %d, want 150 (blend of historical 100 + scribe 200)", got.Budget.Envelope)
	}
	if !strings.Contains(m.notice.text, "scribe sized") {
		t.Errorf("notice = %q, want a scribe refinement", m.notice.text)
	}
}

// scribeEngine attaches an engine whose scribe replies with the given
// estimate line.
func scribeEngine(t *testing.T, m *Shell, reply string) {
	t.Helper()
	eng := engine.New(engine.Config{
		Agents: singleAgent(&agent.Fake{Responder: func(agent.SessionOpts, string) []agent.Event {
			return []agent.Event{{Kind: agent.EventMessage, Text: reply}, {Kind: agent.EventIdle}}
		}}),
		Store: m.store, Pool: m.wt, Workspace: m.ws, MaxActive: 1,
	})
	t.Cleanup(func() { eng.Close() })
	m.AttachEngine(eng)
}

func TestScribeEstimateFloorsTinyGuess(t *testing.T) {
	// no history and a lowball scribe: the blend still lands on the
	// MinEnvelope floor rather than an envelope that gates instantly.
	m, _ := diffWorkspace(t)
	scribeEngine(t, m, "Small.\nESTIMATE: 40")

	m = pump(t, m, m.scribeEstimate("FD-001"))
	got, _ := m.store.GetFeature(context.Background(), "FD-001")
	if got.Budget.Envelope != 150 {
		t.Errorf("envelope = %d, want 150 (MinEnvelope floor over a 40 guess)", got.Budget.Envelope)
	}
}

func TestScribeEstimateRespectsUserEnvelopeFloor(t *testing.T) {
	// GUMMI_ENVELOPE is a floor: a scribe blend below the user's chosen
	// envelope must not silently undercut it.
	m, _ := diffWorkspace(t)
	ctx := context.Background()
	m.SetEnvelope(300)
	f, _ := m.store.GetFeature(ctx, "FD-001")
	f.Budget.Envelope = 300 // set at creation from GUMMI_ENVELOPE
	if err := m.store.UpdateFeature(ctx, &f); err != nil {
		t.Fatal(err)
	}
	scribeEngine(t, m, "Small.\nESTIMATE: 40") // blend(300,40) = 170 < 300

	m = pump(t, m, m.scribeEstimate("FD-001"))
	got, _ := m.store.GetFeature(ctx, "FD-001")
	if got.Budget.Envelope != 300 {
		t.Errorf("envelope = %d, want 300 (user envelope is a floor)", got.Budget.Envelope)
	}
}

func TestScribeEstimateNoEngineIsNoop(t *testing.T) {
	m, _ := newWorkspace(t)
	if cmd := m.scribeEstimate("FD-001"); cmd != nil {
		t.Error("scribeEstimate without an engine should be a no-op")
	}
}
