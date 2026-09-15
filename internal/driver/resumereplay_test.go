package driver

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// TestResumeDoesNotReplayEarlierActivity: a resume used to re-emit the
// card's whole activity history before adding anything new, so an agent
// driving gummi through several stops re-read what it had already consumed
// on every one of them — growing with each stop, and distinguishable from
// new output only by remembering the previous line count.
func TestResumeDoesNotReplayEarlierActivity(t *testing.T) {
	var mu sync.Mutex
	var resumed bool
	h := newHarness(t, false, map[domain.Stage]stageFn{
		domain.StagePlan: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			mu.Lock()
			done := resumed
			mu.Unlock()
			if !done {
				// run 1: work, then stop on a question
				out := toolTurn(o.Model, "first-run-tool", 3, "")
				return append(out[:3], convAsk(o.Model, "Which seam?", "A", "B")...)
			}
			return toolTurn(o.Model, "second-run-tool", 2, "Plan written.")
		},
	})

	out, err := h.driver(Options{Verbose: true, GateApproval: GateAttended}).
		Run(context.Background(), "add a json export")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if out.Status != StatusQuestion {
		t.Fatalf("run 1 status = %q, want question", out.Status)
	}
	if !strings.Contains(h.buf.String(), "first-run-tool") {
		t.Fatal("run 1 never streamed its own activity; the test proves nothing")
	}

	// a second process picks the card up. Everything it prints is what the
	// caller has not already been shown.
	h.buf.Reset()
	mu.Lock()
	resumed = true
	mu.Unlock()
	answer := "A"
	if _, err := h.driver(Options{Verbose: true, StageTimeout: 3 * time.Second}).
		Resume(context.Background(), domain.FeatureID(out.ID), ResumeInput{Answer: &answer}); err != nil {
		t.Fatalf("resume: %v", err)
	}

	stream := h.buf.String()
	if strings.Contains(stream, "first-run-tool") {
		t.Errorf("the resume replayed run 1's activity:\n%s", stream)
	}
	if !strings.Contains(stream, "second-run-tool") {
		t.Errorf("the resume streamed none of its own activity:\n%s", stream)
	}
}
