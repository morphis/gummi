package driver

import (
	"context"
	"sync"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// TestAQuestionInAReworkRoundStopsTheDrive: a rework pass is a session like
// any other, and the replan after a critique asked for changes is exactly
// where a question gets asked. The loop that waits on a rework read the
// question as a finished turn and went straight on to the re-critique,
// leaving the ask open against a session that could take nothing else —
// which on a real backend is a stage that says nothing at all until its
// whole timeout has run out.
func TestAQuestionInAReworkRoundStopsTheDrive(t *testing.T) {
	var mu sync.Mutex
	var critiques int
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StagePlan: func(_ *harness, n int, o agent.SessionOpts, _ string) []agent.Event {
			if n == 1 { // the replan, after the critique asked for changes
				return toolAsk(o.Model, "Problem", "Guard in the handler or in the caller?",
					"in the handler (recommended)", "in the caller")
			}
			return msgIdle(o.Model, "Plan written.")
		},
		stageCritique: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			mu.Lock()
			n := critiques
			critiques++
			mu.Unlock()
			if n == 0 {
				return toolVerdict(o.Model, "changes")
			}
			return toolVerdict(o.Model, "pass")
		},
		domain.StageImplement: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return msgIdle(o.Model, "Implemented.")
		},
		domain.StageVerify: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return toolVerdict(o.Model, "pass")
		},
	})

	out, err := h.driver(Options{}).Run(context.Background(), "add export")
	if err != nil {
		t.Fatalf("Run: %v; stream=%v", err, h.eventKinds())
	}
	if out.Status != StatusQuestion {
		t.Fatalf("a question asked in a replan ended the drive as %q; it is a stop like any other question, not a finished turn (stream=%v)",
			out.Status, h.eventKinds())
	}
	if q := lastEvent(h, "question"); q == nil || q["q"] == "" {
		t.Fatalf("no question reached the caller: %v; stream=%v", q, h.eventKinds())
	}
}
