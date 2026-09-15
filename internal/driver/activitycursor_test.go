package driver

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// toolTurn is a turn that calls n named tools before idling — the shape
// that fills a session's activity feed.
func toolTurn(model, prefix string, n int, text string) []agent.Event {
	out := make([]agent.Event, 0, n+3)
	for i := 0; i < n; i++ {
		out = append(out, agent.Event{Kind: agent.EventToolCall, Tool: prefix, CallID: prefix})
	}
	out = append(out,
		agent.Event{Kind: agent.EventMessage, Text: text},
		agent.Event{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 1, Model: model}},
		agent.Event{Kind: agent.EventIdle},
	)
	return out
}

// TestVerboseStreamsEverySessionInAStage: a stage is not one session. The
// plan writer, its critique and each replan round are separate sessions,
// each with an Activity feed that starts at zero. The verbose cursor used
// to reset only at the stage boundary, so it stayed parked at the writer's
// high-water mark and every shorter session that followed streamed
// nothing — on a three-round plan that silently swallowed the critique and
// both replans, which is where most of the stage's spend goes.
//
// The writer here is deliberately the longest session (6 tool calls) and
// every session after it is shorter (2), which is exactly the case the old
// cursor could not emit.
func TestVerboseStreamsEverySessionInAStage(t *testing.T) {
	var mu sync.Mutex
	var critiques int
	h := newHarness(t, false, map[domain.Stage]stageFn{
		domain.StagePlan: func(_ *harness, n int, o agent.SessionOpts, _ string) []agent.Event {
			if o.Role == agent.RoleReviewer {
				mu.Lock()
				i := critiques
				critiques++
				mu.Unlock()
				if i == 0 {
					return toolTurn(o.Model, "critique-tool", 2, "Finding.\nVERDICT: changes")
				}
				return toolTurn(o.Model, "recritique-tool", 2, "Better.\nVERDICT: pass")
			}
			if n == 0 {
				return toolTurn(o.Model, "writer-tool", 6, "Plan written.")
			}
			return toolTurn(o.Model, "replan-tool", 2, "Plan revised.")
		},
	})

	d := h.driver(Options{Verbose: true})
	if _, err := d.Run(context.Background(), "a feature"); err != nil {
		t.Fatalf("run: %v", err)
	}

	stream := h.buf.String()
	for _, want := range []string{"writer-tool", "critique-tool", "replan-tool", "recritique-tool"} {
		if !strings.Contains(stream, want) {
			t.Errorf("verbose stream never mentions %q — that session ran silent:\n%s", want, stream)
		}
	}
}
