package engine

import (
	"context"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// TestRestoredAskKeepsItsSpecAnchor locks the anchor across a restore.
// DecisionPayload records Anchor for exactly one reason — "so a re-armed
// answer lands where the agent asked for it to land" — and openAskFor
// dropped it, so captureAnswer had nothing to match and a restored
// answer never reached the spec.
func TestRestoredAskKeepsItsSpecAnchor(t *testing.T) {
	ctx := context.Background()
	f := feature(1, "Greeting prefix", domain.StagePlan)
	e := newEngine(t, agent.NewFake("ack"))

	if err := e.cfg.Store.CreateFeature(ctx, &f); err != nil {
		t.Fatal(err)
	}
	want := "which configuration source should we use"
	err := e.cfg.Store.OpenDecision(ctx, f.ID, domain.StagePlan, state.DecisionPayload{
		ID:       "call:1:mcp-5",
		Kind:     state.DecisionKindAsk,
		Question: "How should the greeting prefix be configured?",
		Anchor:   want,
	}, e.now())
	if err != nil {
		t.Fatal(err)
	}

	got := e.openAskFor(ctx, f.ID, domain.StagePlan)
	if got == nil {
		t.Fatal("no ask re-armed from the open decision row")
	}
	if got.SpecAnchor != want {
		t.Errorf("restored ask SpecAnchor = %q, want %q — the answer cannot "+
			"land on the spec line the agent named", got.SpecAnchor, want)
	}
}
