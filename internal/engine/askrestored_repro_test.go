package engine

import (
	"context"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// TestAnswerRestoredAskWithNoAgentMustNotSwallowIt locks the same contract
// TestAnswerAbandonedResolverMustNotReturnNilSilently locks, for the one
// path that reaches it without a resolver at all: a restored ask.
//
// A card parked on an ask_user question keeps the question (the durable
// decision_open row) but not the process. On restore, openAskFor re-arms
// the ask with no CallID, so AnswerAs falls through to the convention
// path — deliverTurn — which refuses a session whose agent died:
//
//	if s.agent() == nil { return "<id> is queued, not yet running" }
//
// By then AnswerAs has already taken the pending ask and recorded the
// answer. Every other failing branch calls trySetPendingAsk before
// returning; this one is a bare tail call, so the question is consumed,
// the answer goes nowhere, and the card is left with nothing open to
// answer and no agent to answer it — the user's answer vanishes.
func TestAnswerRestoredAskWithNoAgentMustNotSwallowIt(t *testing.T) {
	ctx := context.Background()
	f := feature(1, "Greeting prefix", domain.StagePlan)

	e := newEngine(t, agent.NewFake("ack"))
	s, err := e.Attach(ctx, f)
	if err != nil {
		t.Fatal(err)
	}

	// the shape openAskFor produces for a restored ask: no CallID (the
	// blocked call died with the process), free-form (options are never
	// stored), and no live agent behind it.
	s.setPendingAsk(&Ask{
		Question:   "How should the greeting prefix be configured?",
		FreeForm:   true,
		DecisionID: "call:1:mcp-5",
	})
	s.agent().Close()
	s.clearAgent()

	err = e.Answer(ctx, f.ID, "CLI flag")
	if err == nil {
		t.Fatal("Answer succeeded with no agent to deliver to; want a loud failure")
	}
	t.Logf("Answer returned: %v", err)

	if s.Snapshot().PendingAsk == nil {
		t.Errorf("pending ask not restored after a failed delivery (err=%v): "+
			"the question was consumed and the answer delivered nowhere", err)
	}
}
