package driver

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// reviewLoopScript scripts a feature through to the review loop: spec and
// the first implement idle, every review returns the next verdict from the
// list (the last repeats), and every work-stage leg after the first
// (the fix round) exhausts so a run parks mid-loop at the work stage.
func reviewLoopScript(verdicts ...string) map[domain.Stage]stageFn {
	var mu sync.Mutex
	var reviews int
	return map[domain.Stage]stageFn{
		domain.StageSpec: idleTurn,
		domain.StageImplement: func(_ *harness, n int, o agent.SessionOpts, _ string) []agent.Event {
			if n == 0 {
				return msgIdle(o.Model, "Implemented.")
			}
			return []agent.Event{{Kind: agent.EventBudgetExhausted}}
		},
		domain.StageReview: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			mu.Lock()
			n := reviews
			reviews++
			mu.Unlock()
			if n >= len(verdicts) {
				n = len(verdicts) - 1
			}
			return msgIdle(o.Model, "Issues.\nVERDICT: "+verdicts[n])
		},
	}
}

// A resume carries the persisted review-round count into a fresh process:
// run 1 burns one changes-round (Bump persists 1) and parks the feature at
// the work stage; the resumed driver seeds the 1 before emitting its first
// work-stage event, so the cap is NOT re-granted. This is the bug: before
// persistence, the fresh process restarted the counter at zero.
func TestReviewRoundsResumeSurvivesFreshDriver(t *testing.T) {
	h := newHarness(t, true, reviewLoopScript("changes"))
	out, err := h.driver(Options{}).Run(context.Background(), "Add JSON export\n\nUsers need JSON export.")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Status != StatusExhausted {
		t.Fatalf("run-1 status = %q, want exhausted; stream=%v", out.Status, h.eventKinds())
	}
	id := h.only()
	if got, err := h.store.ReviewRounds(context.Background(), id); err != nil || got != 1 {
		t.Fatalf("run-1 ReviewRounds = %d, %v; want 1 (one changes-round burned)", got, err)
	}
	if st := h.stageOf(id); st != domain.StageImplement {
		t.Fatalf("feature at %s, want Implement (parked mid-loop)", st)
	}

	h.buf.Reset()
	out2, err := h.driver(Options{}).Resume(context.Background(), id, ResumeInput{})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if out2.Status != StatusExhausted {
		t.Fatalf("resume status = %q, want exhausted; stream=%v", out2.Status, h.eventKinds())
	}
	if r := firstWorkStageRound(h); r != 1 {
		t.Fatalf("first resumed work-stage event round = %d, want 1 (the round burned in run-1, not a fresh grant)", r)
	}
	if got, err := h.store.ReviewRounds(context.Background(), id); err != nil || got != 1 {
		t.Fatalf("post-resume ReviewRounds = %d, %v; want 1 (never re-mutated)", got, err)
	}
}

// A passing review clears the persisted count, so the next review loop
// starts a fresh budget. Here the first review requests changes (burning a
// round, which the fix leg carries forward) and the second passes.
func TestReviewRoundsClearedOnPassGate(t *testing.T) {
	var mu sync.Mutex
	var reviews int
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageSpec:      idleTurn,
		domain.StageImplement: idleTurn,
		domain.StageVerify: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return prosePass(o.Model)
		},
		domain.StageReview: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			mu.Lock()
			n := reviews
			reviews++
			mu.Unlock()
			if n == 0 {
				return msgIdle(o.Model, "Issues.\nVERDICT: changes")
			}
			return msgIdle(o.Model, "Clean.\nVERDICT: pass")
		},
	})
	out, err := h.driver(Options{}).Run(context.Background(), "Add JSON export\n\nUsers need JSON export.")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Status != StatusDone {
		t.Fatalf("status = %q, want done; stream=%v", out.Status, h.eventKinds())
	}
	if got, err := h.store.ReviewRounds(context.Background(), h.only()); err != nil || got != 0 {
		t.Fatalf("ReviewRounds after pass gate = %d, %v; want 0", got, err)
	}
}

// Store failures are never silently ignored: a failed read aborts review-
// loop entry and a failed write aborts the round.
func TestReviewRoundsStoreFailureFailsClosed(t *testing.T) {
	t.Run("read aborts review entry", func(t *testing.T) {
		h := newHarness(t, true, reviewLoopScript("changes"))
		d := h.driver(Options{})
		d.reviewStore = &failReviewStore{failLoad: true}
		out, err := d.Run(context.Background(), "Add JSON export\n\nUsers need JSON export.")
		if err == nil {
			t.Fatal("Run with a failing read: want error, got nil")
		}
		if out.Status != StatusError {
			t.Fatalf("status = %q, want error", out.Status)
		}
	})
	t.Run("write aborts the round", func(t *testing.T) {
		h := newHarness(t, true, reviewLoopScript("changes"))
		d := h.driver(Options{})
		d.reviewStore = &failReviewStore{failWrite: true}
		out, err := d.Run(context.Background(), "Add JSON export\n\nUsers need JSON export.")
		if err == nil {
			t.Fatal("Run with a failing write: want error, got nil")
		}
		if out.Status != StatusError {
			t.Fatalf("status = %q, want error", out.Status)
		}
	})
}

// failReviewStore fails reads, writes, or both — the fail-closed proof.
type failReviewStore struct {
	failLoad  bool
	failWrite bool
}

func (f *failReviewStore) ReviewRounds(context.Context, domain.FeatureID) (int, error) {
	if f.failLoad {
		return 0, errors.New("read failed")
	}
	return 0, nil
}

func (f *failReviewStore) IncrementReviewRounds(context.Context, domain.FeatureID) error {
	if f.failWrite {
		return errors.New("bump failed")
	}
	return nil
}

func (f *failReviewStore) ClearReviewRounds(context.Context, domain.FeatureID) error {
	if f.failWrite {
		return errors.New("clear failed")
	}
	return nil
}

// firstWorkStageRound returns the round of the first implement/fix stage
// event in the stream, or -1 if none was emitted.
func firstWorkStageRound(h *harness) int {
	for _, e := range h.events() {
		if e["event"] != "stage" {
			continue
		}
		s, ok := e["stage"].(string)
		if !ok || (s != "implement" && s != "fix") {
			continue
		}
		if r, ok := e["round"].(float64); ok {
			return int(r)
		}
	}
	return -1
}
