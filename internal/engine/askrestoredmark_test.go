package engine

import (
	"context"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// A question re-armed from its durable decision says so: the process that
// asked it is gone and its options with it (decision rows never store
// them, DESIGN §10 D18), so the surfaces offering it can say why only the
// chat row is left instead of presenting it as all the agent offered.
func TestARestoredAskIsMarkedRestored(t *testing.T) {
	ctx := context.Background()
	e, _, store, _ := advanceEngine(t)
	f := feature(1, "Greeting prefix", domain.StagePlan)
	putFeature(t, store, f)
	if err := store.OpenDecision(ctx, f.ID, domain.StagePlan, state.DecisionPayload{
		ID: "call:1:mcp-5", Kind: state.DecisionKindAsk, Question: "Flag or config file?",
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	ask := e.openAskFor(ctx, f.ID, domain.StagePlan)
	if ask == nil {
		t.Fatal("the open ask was not re-armed")
	}
	if !ask.Restored || len(ask.Options) != 0 || ask.Question != "Flag or config file?" || ask.DecisionID != "call:1:mcp-5" {
		t.Errorf("restored ask = %+v, want the question, its id, no options, marked restored", ask)
	}
}
