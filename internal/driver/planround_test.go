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
// its resumed re-critique exhausts before any verdict, never re-mutates the
// store.
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
				st.mu.Lock()
				resumed := st.resumed
				st.mu.Unlock()
				if resumed {
					// the resumed leg is a re-critique (reviewer role):
					// exhaust it before any verdict, so the store is never
					// re-mutated after the persisted load.
					return []agent.Event{{Kind: agent.EventBudgetExhausted}}
				}
				return msgIdle(o.Model, "Missing check.\nVERDICT: changes")
			}
			st.mu.Lock()
			st.architect++
			a, resumed := st.architect, st.resumed
			st.mu.Unlock()
			if !resumed && a == 1 {
				return msgIdle(o.Model, "Plan written.") // run-1 writer
			}
			// run-1 replan exhausts, parking a finished writer session; the
			// resume never runs the writer (it routes to a re-critique).
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
	if got, err := h.store.Rounds(context.Background(), id, domain.RoundKindPlan); err != nil || got != 1 {
		t.Fatalf("run-1 Rounds(plan) = %d, %v; want 1 (one changes-round burned)", got, err)
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
	if got, err := h.store.Rounds(context.Background(), id, domain.RoundKindPlan); err != nil || got != 1 {
		t.Fatalf("post-resume Rounds(plan) = %d, %v; want 1 (never re-mutated)", got, err)
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
	if got, err := h.store.Rounds(context.Background(), h.only(), domain.RoundKindPlan); err != nil || got != 0 {
		t.Fatalf("Rounds(plan) after pass gate = %d, %v; want 0", got, err)
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
	if got, err := h.store.Rounds(context.Background(), h.only(), domain.RoundKindPlan); err != nil || got != 0 {
		t.Fatalf("Rounds(plan) after escalation = %d, %v; want 0", got, err)
	}
}

// failRoundStore fails reads, writes, or both on the keyed rounds seam —
// the fail-closed proof, shared by the plan and review round tests.
type failRoundStore struct {
	failLoad  bool
	failWrite bool
}

func (f *failRoundStore) Rounds(context.Context, domain.FeatureID, domain.RoundKind) (int, error) {
	if f.failLoad {
		return 0, errors.New("read failed")
	}
	return 0, nil
}

func (f *failRoundStore) IncrementRounds(context.Context, domain.FeatureID, domain.RoundKind) error {
	if f.failWrite {
		return errors.New("bump failed")
	}
	return nil
}

func (f *failRoundStore) ClearRounds(context.Context, domain.FeatureID, domain.RoundKind) error {
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
		d.roundStore = &failRoundStore{failLoad: true}
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
		d.roundStore = &failRoundStore{failWrite: true}
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

// A mid-replan interruption must resume the critique of the already-revised
// plan, not re-invoke the plan writer: run 1 burns a changes-round and parks
// with a finished replan writer (revised plan on disk, count 1); the resumed
// driver routes that finished writer to a re-critique and never re-runs the
// writer.
func TestPlanRoundsResumeReCritiquesRevisedPlan(t *testing.T) {
	st := struct {
		mu              sync.Mutex
		architects      int
		resumed         bool
		resumeCritiques int
		resumeWriters   int
		resumeKickoff   string
	}{}
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageBrainstorm: idleTurn,
		domain.StageSpec:       idleTurn,
		domain.StagePlan: func(_ *harness, _ int, o agent.SessionOpts, msg string) []agent.Event {
			if o.Role == agent.RoleReviewer {
				st.mu.Lock()
				resumed := st.resumed
				if resumed {
					st.resumeCritiques++
					st.resumeKickoff = msg
				}
				st.mu.Unlock()
				if !resumed {
					return msgIdle(o.Model, "Missing check.\nVERDICT: changes")
				}
				// the resumed re-critique exhausts before any verdict.
				return []agent.Event{{Kind: agent.EventBudgetExhausted}}
			}
			st.mu.Lock()
			st.architects++
			n, resumed := st.architects, st.resumed
			if resumed {
				st.resumeWriters++
			}
			st.mu.Unlock()
			if !resumed && n == 1 {
				return msgIdle(o.Model, "Plan written.") // run-1 writer
			}
			// run-1 replan exhausts, parking a finished writer session.
			return []agent.Event{{Kind: agent.EventBudgetExhausted}}
		},
	})

	out, err := h.driver(Options{Full: true}).Run(context.Background(), "Add JSON export\n\nUsers need JSON export.")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Status != StatusExhausted {
		t.Fatalf("run-1 status = %q, want exhausted; stream=%v", out.Status, h.eventKinds())
	}
	id := h.only()
	if got, err := h.store.Rounds(context.Background(), id, domain.RoundKindPlan); err != nil || got != 1 {
		t.Fatalf("run-1 Rounds(plan) = %d, %v; want 1", got, err)
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
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.resumeCritiques != 1 {
		t.Errorf("resume critiques = %d, want 1 (the re-critique)", st.resumeCritiques)
	}
	if st.resumeWriters != 0 {
		t.Errorf("resume writer runs = %d, want 0 (the revised plan is already written)", st.resumeWriters)
	}
	if !strings.Contains(st.resumeKickoff, "re-critique") {
		t.Errorf("resume critique kickoff missing the re-critique note: %q", st.resumeKickoff)
	}
}

// With no round burned (count 0), a finished-writer resume still critiques
// the freshly written plan, with the plain kickoff (not the re-critique
// note), and never re-invokes the writer.
func TestPlanRoundsResumeCritiquesFreshPlan(t *testing.T) {
	st := struct {
		mu        sync.Mutex
		writers   int
		critiques int
		kickoff   string
	}{}
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageBrainstorm: idleTurn,
		domain.StageSpec:       idleTurn,
		domain.StagePlan: func(_ *harness, _ int, o agent.SessionOpts, msg string) []agent.Event {
			if o.Role == agent.RoleReviewer {
				st.mu.Lock()
				st.critiques++
				st.kickoff = msg
				st.mu.Unlock()
				return []agent.Event{{Kind: agent.EventBudgetExhausted}}
			}
			st.mu.Lock()
			st.writers++
			st.mu.Unlock()
			// the run-1 writer exhausts, parking a finished writer (count 0);
			// the resume never runs the writer.
			return []agent.Event{{Kind: agent.EventBudgetExhausted}}
		},
	})

	out, err := h.driver(Options{Full: true}).Run(context.Background(), "Add JSON export\n\nUsers need JSON export.")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Status != StatusExhausted {
		t.Fatalf("run-1 status = %q, want exhausted; stream=%v", out.Status, h.eventKinds())
	}
	id := h.only()
	if got, err := h.store.Rounds(context.Background(), id, domain.RoundKindPlan); err != nil || got != 0 {
		t.Fatalf("run-1 Rounds(plan) = %d, %v; want 0", got, err)
	}

	h.buf.Reset()
	out2, err := h.driver(Options{}).Resume(context.Background(), id, ResumeInput{})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if out2.Status != StatusExhausted {
		t.Fatalf("resume status = %q, want exhausted; stream=%v", out2.Status, h.eventKinds())
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.critiques != 1 {
		t.Errorf("critiques = %d, want 1 (the resumed critique)", st.critiques)
	}
	if st.writers != 1 {
		t.Errorf("writers = %d, want 1 (only the run-1 writer, none on resume)", st.writers)
	}
	if strings.Contains(st.kickoff, "re-critique") {
		t.Errorf("fresh-plan critique kickoff carries the re-critique note: %q", st.kickoff)
	}
}

// A finished-critique resume routes to the judge, which replans via the
// writer on a changes verdict — the critique itself is never re-run.
func TestPlanRoundsResumeFinishedCritiqueReplans(t *testing.T) {
	st := struct {
		mu         sync.Mutex
		architects int
		reviewers  int
		replanMsg  string
	}{}
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageBrainstorm: idleTurn,
		domain.StageSpec:       idleTurn,
		domain.StagePlan: func(_ *harness, _ int, o agent.SessionOpts, msg string) []agent.Event {
			if o.Role == agent.RoleReviewer {
				st.mu.Lock()
				st.reviewers++
				st.mu.Unlock()
				// the run-1 critique: a changes verdict, then the session
				// exhausts, so it parks as a finished critique session.
				return []agent.Event{
					{Kind: agent.EventMessage, Text: "Missing check.\nVERDICT: changes"},
					{Kind: agent.EventBudgetExhausted},
				}
			}
			st.mu.Lock()
			st.architects++
			n := st.architects
			st.mu.Unlock()
			if n == 1 {
				return msgIdle(o.Model, "Plan written.") // run-1 writer
			}
			// the resumed replan writer: exhaust so the resume resolves here.
			st.mu.Lock()
			st.replanMsg = msg
			st.mu.Unlock()
			return []agent.Event{{Kind: agent.EventBudgetExhausted}}
		},
	})

	out, err := h.driver(Options{Full: true}).Run(context.Background(), "Add JSON export\n\nUsers need JSON export.")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Status != StatusExhausted {
		t.Fatalf("run-1 status = %q, want exhausted; stream=%v", out.Status, h.eventKinds())
	}
	id := h.only()

	h.buf.Reset()
	out2, err := h.driver(Options{}).Resume(context.Background(), id, ResumeInput{})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if out2.Status != StatusExhausted {
		t.Fatalf("resume status = %q, want exhausted; stream=%v", out2.Status, h.eventKinds())
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.architects != 2 {
		t.Errorf("architect sessions = %d, want 2 (run-1 writer + resumed replan)", st.architects)
	}
	if st.reviewers != 1 {
		t.Errorf("critique sessions = %d, want 1 (run-1 only; the finished critique is judged, not re-run)", st.reviewers)
	}
	if !strings.Contains(st.replanMsg, "plan critique found issues") {
		t.Errorf("resumed replan kickoff missing the replan note: %q", st.replanMsg)
	}
}

// A resume landing on a still-in-flight plan session is a no-op: neither the
// writer nor a critique is spawned. With a short stage timeout the resume
// resolves deterministically (the restored session never proceeds on its
// own), and the per-role invocation counts are unchanged on the resume leg.
func TestPlanRoundsResumeRunningSessionNoOp(t *testing.T) {
	st := struct {
		mu         sync.Mutex
		architects int
		reviewers  int
	}{}
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageBrainstorm: idleTurn,
		domain.StageSpec:       idleTurn,
		domain.StagePlan: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			if o.Role == agent.RoleReviewer {
				st.mu.Lock()
				st.reviewers++
				st.mu.Unlock()
			} else {
				st.mu.Lock()
				st.architects++
				st.mu.Unlock()
			}
			// stall: a single message with no idle keeps the session in
			// flight so a run resolves via StageTimeout.
			return []agent.Event{{Kind: agent.EventMessage, Text: "still working"}}
		},
	})
	opts := Options{Full: true, StageTimeout: 300 * time.Millisecond}
	out, err := h.driver(opts).Run(context.Background(), "Add JSON export\n\nUsers need JSON export.")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Status != StatusTimeout {
		t.Fatalf("run-1 status = %q, want timeout; stream=%v", out.Status, h.eventKinds())
	}
	id := h.only()
	if st := h.stageOf(id); st != domain.StagePlan {
		t.Fatalf("feature at %s, want Plan (parked mid-flight)", st)
	}
	st.mu.Lock()
	a0, r0 := st.architects, st.reviewers
	st.mu.Unlock()

	h.buf.Reset()
	out2, err := h.driver(Options{StageTimeout: 300 * time.Millisecond}).Resume(context.Background(), id, ResumeInput{})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if out2.Status != StatusTimeout {
		t.Fatalf("resume status = %q, want timeout; stream=%v", out2.Status, h.eventKinds())
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.architects != a0 || st.reviewers != r0 {
		t.Errorf("session spawned on a non-done resume: architects %d→%d, reviewers %d→%d",
			a0, st.architects, r0, st.reviewers)
	}
}
