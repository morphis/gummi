package driver

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// A Plan-stage writer session that dies on a recoverable backend failure
// (e.g. Claude's session-limit error) persists paused with its error stored.
// Resume must re-dispatch a fresh writer instead of awaiting a pass that
// already ended — awaiting it burns the whole --stage-timeout window and
// misreports the stall as a gate park (BG-091).
func TestPausedPlanWriterResumeReDispatches(t *testing.T) {
	st := struct {
		mu      sync.Mutex
		writers int
		resumed bool
	}{}
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageBrainstorm: idleTurn,
		domain.StageSpec:       idleTurn,
		domain.StagePlan: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			if o.Role == agent.RoleScribe {
				return msgIdle(o.Model, "Plan written.")
			}
			if o.Role == agent.RoleReviewer {
				return msgIdle(o.Model, "Looks good.\nVERDICT: pass")
			}
			st.mu.Lock()
			st.writers++
			resumed := st.resumed
			st.mu.Unlock()
			if !resumed {
				// run-1 writer: a recoverable backend failure mid-turn.
				return []agent.Event{{Kind: agent.EventError, Err: errors.New("session limit reached")}}
			}
			return msgIdle(o.Model, "Plan written.")
		},
	})

	out, err := h.driver(Options{Full: true}).Run(context.Background(), "Add JSON export\n\nUsers need JSON export.")
	if err == nil || out.Status != StatusError {
		t.Fatalf("run-1 = %+v, err=%v; want a StatusError from the failed writer", out, err)
	}
	id := h.only()
	if stg := h.stageOf(id); stg != domain.StagePlan {
		t.Fatalf("feature at %s, want Plan (parked on the failed writer)", stg)
	}

	h.buf.Reset()
	st.mu.Lock()
	st.resumed = true
	st.mu.Unlock()
	out2, err := h.driver(Options{StageTimeout: 2 * time.Second}).Resume(context.Background(), id, ResumeInput{})
	if err != nil {
		t.Fatalf("Resume: %v; stream=%v", err, h.eventKinds())
	}
	if out2.Status == StatusTimeout {
		t.Fatalf("resume timed out instead of re-dispatching the paused writer; stream=%v", h.eventKinds())
	}
	st.mu.Lock()
	writers := st.writers
	st.mu.Unlock()
	if writers != 2 {
		t.Fatalf("resume writer runs = %d, want 2 (the paused writer re-dispatched); stream=%v", writers, h.eventKinds())
	}
}

// A Plan-stage critique session that dies on a recoverable backend failure
// persists paused (Critique=true) with its error stored. Resume must
// re-dispatch a fresh critique instead of awaiting a pass that already
// ended (mirrors the TUI's StatePaused+Critique branch).
func TestPausedPlanCritiqueResumeReDispatches(t *testing.T) {
	st := struct {
		mu        sync.Mutex
		critiques int
		resumed   bool
	}{}
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageBrainstorm: idleTurn,
		domain.StageSpec:       idleTurn,
		domain.StagePlan: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			if o.Role == agent.RoleReviewer {
				st.mu.Lock()
				st.critiques++
				resumed := st.resumed
				st.mu.Unlock()
				if !resumed {
					// run-1 critique: a recoverable backend failure mid-turn.
					return []agent.Event{{Kind: agent.EventError, Err: errors.New("session limit reached")}}
				}
				return msgIdle(o.Model, "Looks good.\nVERDICT: pass")
			}
			return msgIdle(o.Model, "Plan written.")
		},
	})

	out, err := h.driver(Options{Full: true}).Run(context.Background(), "Add JSON export\n\nUsers need JSON export.")
	if err == nil || out.Status != StatusError {
		t.Fatalf("run-1 = %+v, err=%v; want a StatusError from the failed critique", out, err)
	}
	id := h.only()
	if stg := h.stageOf(id); stg != domain.StagePlan {
		t.Fatalf("feature at %s, want Plan (parked on the failed critique)", stg)
	}

	h.buf.Reset()
	st.mu.Lock()
	st.resumed = true
	st.mu.Unlock()
	out2, err := h.driver(Options{StageTimeout: 2 * time.Second}).Resume(context.Background(), id, ResumeInput{})
	if err != nil {
		t.Fatalf("Resume: %v; stream=%v", err, h.eventKinds())
	}
	if out2.Status == StatusTimeout {
		t.Fatalf("resume timed out instead of re-dispatching the paused critique; stream=%v", h.eventKinds())
	}
	st.mu.Lock()
	critiques := st.critiques
	st.mu.Unlock()
	if critiques != 2 {
		t.Fatalf("resume critique runs = %d, want 2 (the paused critique re-dispatched); stream=%v", critiques, h.eventKinds())
	}
}

// The re-dispatched critique must set d.sentTurn, or a stall on the
// re-dispatched turn itself is misdiagnosed the same way the original bug
// was: a second timeout after the re-dispatch should blame the backend
// (timeoutHintStalled), not tell the operator to advance a nonexistent gate
// (timeoutHintParked).
func TestPausedPlanCritiqueResumeThenStallHintsStalled(t *testing.T) {
	var resumed bool
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageBrainstorm: idleTurn,
		domain.StageSpec:       idleTurn,
		domain.StagePlan: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			if o.Role == agent.RoleReviewer {
				if !resumed {
					// run-1 critique: a recoverable backend failure mid-turn.
					return []agent.Event{{Kind: agent.EventError, Err: errors.New("session limit reached")}}
				}
				// re-dispatched critique stalls: no idle, so the leg-2
				// resume itself must time out rather than resolve.
				return []agent.Event{{Kind: agent.EventMessage, Text: "still working"}}
			}
			return msgIdle(o.Model, "Plan written.")
		},
	})

	out, err := h.driver(Options{Full: true}).Run(context.Background(), "Add JSON export\n\nUsers need JSON export.")
	if err == nil || out.Status != StatusError {
		t.Fatalf("run-1 = %+v, err=%v; want a StatusError from the failed critique", out, err)
	}
	id := h.only()

	h.buf.Reset()
	resumed = true
	out2, err := h.driver(Options{StageTimeout: 300 * time.Millisecond}).Resume(context.Background(), id, ResumeInput{})
	if err != nil {
		t.Fatalf("Resume: %v; stream=%v", err, h.eventKinds())
	}
	if out2.Status != StatusTimeout {
		t.Fatalf("resume-2 status = %q, want timeout (the re-dispatched critique stalls); stream=%v", out2.Status, h.eventKinds())
	}
	ev := lastEvent(h, "timeout")
	if ev == nil {
		t.Fatalf("no timeout event; stream=%v", h.eventKinds())
	}
	hint, _ := ev["hint"].(string)
	if !strings.Contains(hint, "went silent") {
		t.Fatalf("timeout hint = %q, want a stalled-backend diagnosis (a turn was sent this stage); "+
			"a %q hint means d.sentTurn wasn't set on the re-dispatch", hint, hint)
	}
}
