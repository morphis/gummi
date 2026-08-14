package driver

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// idleTurn is a plain finished turn with a token of spend — the writer's
// contribution to any stage it is asked to drive.
func idleTurn(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
	return msgIdle(o.Model, "drafted.")
}

// planLoopScript scripts the full route into the plan stage and then drives
// the plan-critique loop: each architect session (writer / replan) idles,
// and each reviewer (critique) returns the next verdict from the list (the
// last one repeats).
func planLoopScript(verdicts ...string) map[domain.Stage]stageFn {
	var mu sync.Mutex
	var critiques int
	return map[domain.Stage]stageFn{
		domain.StageBrainstorm: idleTurn,
		domain.StageSpec:       idleTurn,
		domain.StagePlan: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			if o.Role == agent.RoleReviewer {
				mu.Lock()
				n := critiques
				critiques++
				mu.Unlock()
				if n >= len(verdicts) {
					n = len(verdicts) - 1
				}
				return msgIdle(o.Model, "Finding.\nVERDICT: "+verdicts[n])
			}
			return msgIdle(o.Model, "Plan written.")
		},
	}
}

// A resume carries the persisted round count into a fresh process: run 1
// burns one changes-round (Bump persists 1) and parks the feature at Plan;
// the resumed driver loads the 1 before emitting the stage event and, since
// its plan session exhausts before any verdict, never re-mutates the store.
func TestPlanRoundsResumeSurvivesFreshDriver(t *testing.T) {
	st := struct {
		mu        sync.Mutex
		architect int
		resumed   bool
	}{}
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageBrainstorm: idleTurn,
		domain.StageSpec:       idleTurn,
		domain.StagePlan: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			if o.Role == agent.RoleReviewer {
				return msgIdle(o.Model, "Missing check.\nVERDICT: changes")
			}
			st.mu.Lock()
			st.architect++
			a, resumed := st.architect, st.resumed
			st.mu.Unlock()
			if !resumed && a == 1 {
				return msgIdle(o.Model, "Plan written.") // run-1 writer
			}
			// run-1 replan and the resumed session both exhaust, so no
			// verdict is ever judged after the persisted load.
			return []agent.Event{{Kind: agent.EventBudgetExhausted}}
		},
	})

	out, err := h.driver(Options{Full: true}).Run(context.Background(), "Add JSON export\n\nUsers need to export data as JSON.")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Status != StatusExhausted {
		t.Fatalf("run-1 status = %q, want exhausted; stream=%v", out.Status, h.eventKinds())
	}
	id := h.only()
	if got, err := h.store.PlanRounds(context.Background(), id); err != nil || got != 1 {
		t.Fatalf("run-1 PlanRounds = %d, %v; want 1 (one changes-round burned)", got, err)
	}
	if st := h.stageOf(id); st != domain.StagePlan {
		t.Fatalf("feature at %s, want Plan (parked mid-cycle)", st)
	}

	h.buf.Reset()
	st.mu.Lock()
	st.resumed = true
	st.mu.Unlock()
	out2, err := h.driver(Options{}).Resume(context.Background(), id, ResumeInput{})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if out2.Status != StatusExhausted {
		t.Fatalf("resume status = %q, want exhausted; stream=%v", out2.Status, h.eventKinds())
	}
	if r := planStageRound(h); r != 1 {
		t.Fatalf("resumed plan stage-event round = %d, want 1 (the persisted count)", r)
	}
	if got, err := h.store.PlanRounds(context.Background(), id); err != nil || got != 1 {
		t.Fatalf("post-resume PlanRounds = %d, %v; want 1 (never re-mutated)", got, err)
	}
}

// A passing critique clears the persisted count, so the next plan cycle
// starts a fresh two-round budget.
func TestPlanRoundsClearedOnPassGate(t *testing.T) {
	h := newHarness(t, true, planLoopScript("changes", "pass"))
	out, err := h.driver(Options{Full: true, Until: domain.StagePlan}).Run(context.Background(), "Add JSON export\n\nUsers need JSON export.")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Status != StatusStopped {
		t.Fatalf("status = %q, want stopped; stream=%v", out.Status, h.eventKinds())
	}
	if got, err := h.store.PlanRounds(context.Background(), h.only()); err != nil || got != 0 {
		t.Fatalf("PlanRounds after pass gate = %d, %v; want 0", got, err)
	}
}

// Escalation at the cap clears the persisted count, so a later resume does
// not inherit a stale exhausted count.
func TestPlanRoundsClearedOnEscalation(t *testing.T) {
	h := newHarness(t, true, planLoopScript("changes"))
	out, err := h.driver(Options{Full: true}).Run(context.Background(), "Add JSON export\n\nUsers need JSON export.")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Status != StatusEscalation {
		t.Fatalf("status = %q, want escalation; stream=%v", out.Status, h.eventKinds())
	}
	if got, err := h.store.PlanRounds(context.Background(), h.only()); err != nil || got != 0 {
		t.Fatalf("PlanRounds after escalation = %d, %v; want 0", got, err)
	}
}

// failPlanStore fails reads, writes, or both — the fail-closed proof.
type failPlanStore struct {
	failLoad  bool
	failWrite bool
}

func (f *failPlanStore) PlanRounds(context.Context, domain.FeatureID) (int, error) {
	if f.failLoad {
		return 0, errors.New("read failed")
	}
	return 0, nil
}

func (f *failPlanStore) IncrementPlanRounds(context.Context, domain.FeatureID) error {
	if f.failWrite {
		return errors.New("bump failed")
	}
	return nil
}

func (f *failPlanStore) ClearPlanRounds(context.Context, domain.FeatureID) error {
	if f.failWrite {
		return errors.New("clear failed")
	}
	return nil
}

// Store failures are never silently ignored: a failed read aborts
// plan-stage entry and a failed write aborts the round.
func TestPlanRoundsStoreFailureFailsClosed(t *testing.T) {
	t.Run("read aborts plan entry", func(t *testing.T) {
		h := newHarness(t, true, map[domain.Stage]stageFn{
			domain.StageBrainstorm: idleTurn,
			domain.StageSpec:       idleTurn,
			domain.StagePlan:       idleTurn,
		})
		d := h.driver(Options{Full: true})
		d.planStore = &failPlanStore{failLoad: true}
		out, err := d.Run(context.Background(), "Add JSON export\n\nUsers need JSON export.")
		if err == nil {
			t.Fatal("Run with a failing read: want error, got nil")
		}
		if out.Status != StatusError {
			t.Fatalf("status = %q, want error", out.Status)
		}
	})
	t.Run("write aborts the round", func(t *testing.T) {
		h := newHarness(t, true, planLoopScript("changes"))
		d := h.driver(Options{Full: true})
		d.planStore = &failPlanStore{failWrite: true}
		out, err := d.Run(context.Background(), "Add JSON export\n\nUsers need JSON export.")
		if err == nil {
			t.Fatal("Run with a failing write: want error, got nil")
		}
		if out.Status != StatusError {
			t.Fatalf("status = %q, want error", out.Status)
		}
	})
}

// planStageRound returns the round of the latest plan-stage event in the
// stream, or -1 if none was emitted.
func planStageRound(h *harness) int {
	round := -1
	for _, e := range h.events() {
		if e["event"] == "stage" && e["stage"] == "plan" {
			if r, ok := e["round"].(float64); ok {
				round = int(r)
			}
		}
	}
	return round
}
