package driver

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// An interactive stage (Spec) whose backend dies mid-turn persists at
// state='interactive' — failRun only flips non-Interactive sessions to
// paused. The dead session's kickoff message alone makes its transcript
// non-empty, so reattachSilent misreads it as a finished interview and
// resume must not cross the gate without ever re-dispatching the writer
// (BG-092, sibling of BG-091's paused-writer/critique cases above).
func TestPausedInteractiveResumeReDispatches(t *testing.T) {
	st := struct {
		mu        sync.Mutex
		specCalls int
		resumed   bool
	}{}
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageSpec: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			st.mu.Lock()
			st.specCalls++
			resumed := st.resumed
			st.mu.Unlock()
			if !resumed {
				// run-1: the backend dies mid-turn.
				return []agent.Event{{Kind: agent.EventError, Err: errors.New("backend died")}}
			}
			return msgIdle(o.Model, "Spec drafted.")
		},
		domain.StageImplement: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return msgIdle(o.Model, "Implemented.")
		},
		domain.StageReview: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return toolVerdict(o.Model, "pass")
		},
		domain.StageVerify: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return toolVerdict(o.Model, "pass")
		},
	})

	out, err := h.driver(Options{}).Run(context.Background(), "add a json export")
	if err == nil || out.Status != StatusError {
		t.Fatalf("run-1 = %+v, err=%v; want a StatusError from the failed spec interview", out, err)
	}
	id := h.only()
	if stg := h.stageOf(id); stg != domain.StageSpec {
		t.Fatalf("feature at %s, want Spec (parked on the failed interview)", stg)
	}
	st.mu.Lock()
	calls := st.specCalls
	st.mu.Unlock()
	if calls != 1 {
		t.Fatalf("specCalls after run-1 = %d, want 1", calls)
	}

	h.buf.Reset()
	st.mu.Lock()
	st.resumed = true
	st.mu.Unlock()
	out2, err := h.driver(Options{Autonomous: true, StageTimeout: 2 * time.Second}).
		Resume(context.Background(), id, ResumeInput{})
	if err != nil {
		t.Fatalf("Resume: %v; stream=%v", err, h.eventKinds())
	}
	st.mu.Lock()
	calls = st.specCalls
	st.mu.Unlock()
	if out2.Status == StatusDone && calls == 1 {
		t.Fatalf("resume reported done without ever re-dispatching the dead interview: "+
			"specCalls=%d; stream=%v", calls, h.eventKinds())
	}
	if calls != 2 {
		t.Fatalf("resume spec writer runs = %d, want 2 (the dead interview re-dispatched); "+
			"status=%v stream=%v", calls, out2.Status, h.eventKinds())
	}
}
