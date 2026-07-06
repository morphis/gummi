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
		Agent: &agent.Fake{Responder: func(agent.SessionOpts, string) []agent.Event {
			return []agent.Event{{Kind: agent.EventMessage, Text: "Sizeable.\nESTIMATE: 200"}, {Kind: agent.EventIdle}}
		}},
		Store: m.store, Worktrees: m.wt, Workspace: m.ws, MaxActive: 1,
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

func TestScribeEstimateNoEngineIsNoop(t *testing.T) {
	m, _ := newWorkspace(t)
	if cmd := m.scribeEstimate("FD-001"); cmd != nil {
		t.Error("scribeEstimate without an engine should be a no-op")
	}
}
