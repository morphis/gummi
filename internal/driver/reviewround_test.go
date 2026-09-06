package driver

import (
	"context"
	"sync"
	"testing"
	"time"

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
	var implements int
	return map[domain.Stage]stageFn{
		domain.StagePlan: idleTurn,
		domain.StageImplement: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			if o.Role == agent.RoleScribe {
				return msgIdle(o.Model, "Implemented.")
			}
			mu.Lock()
			n := implements
			implements++
			mu.Unlock()
			if n == 0 {
				return msgIdle(o.Model, "Implemented.")
			}
			return []agent.Event{{Kind: agent.EventBudgetExhausted}}
		},
		stageCritique: func(h *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			// the design stage has a critique too now; this script is
			// about the WORK stage's, so let the design one pass.
			if h.stageOf(h.only()) == domain.StagePlan {
				return toolVerdict(o.Model, "pass")
			}
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
	if got, err := h.store.Rounds(context.Background(), id, domain.RoundKindReview); err != nil || got != 1 {
		t.Fatalf("run-1 Rounds(review) = %d, %v; want 1 (one changes-round burned)", got, err)
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
	// 2, not 1: the resume seeded run-1's burned round (asserted above, via
	// the first work-stage event) and then burned a second one legitimately.
	// It is NOT a re-grant — a re-grant would have restarted at 0 and left
	// this at 1. The count reaching 2 rather than stopping at 1 is a real
	// consequence of folding Review into the work stage: the critique now
	// runs inside implement's budget instead of behind a stage transition,
	// so an exhausted implement no longer stops short of being reviewed.
	if got, err := h.store.Rounds(context.Background(), id, domain.RoundKindReview); err != nil || got != 2 {
		t.Fatalf("post-resume Rounds(review) = %d, %v; want 2 (seeded 1, then one more burned — not a fresh grant)", got, err)
	}
}

// A passing review clears the persisted count, so the next review loop
// starts a fresh budget. Here the first review requests changes (burning a
// round, which the fix leg carries forward) and the second passes.
func TestReviewRoundsClearedOnPassGate(t *testing.T) {
	var mu sync.Mutex
	var reviews int
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StagePlan:      idleTurn,
		domain.StageImplement: idleTurn,
		domain.StageVerify: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return prosePass(o.Model)
		},
		stageCritique: func(h *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			// the design stage has a critique too now; this script is
			// about the WORK stage's, so let the design one pass.
			if h.stageOf(h.only()) == domain.StagePlan {
				return toolVerdict(o.Model, "pass")
			}
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
	if got, err := h.store.Rounds(context.Background(), h.only(), domain.RoundKindReview); err != nil || got != 0 {
		t.Fatalf("Rounds(review) after pass gate = %d, %v; want 0", got, err)
	}
}

// Store failures are never silently ignored: a failed read aborts review-
// loop entry and a failed write aborts the round.
func TestReviewRoundsStoreFailureFailsClosed(t *testing.T) {
	t.Run("read aborts review entry", func(t *testing.T) {
		h := newHarness(t, true, reviewLoopScript("changes"))
		d := h.driver(Options{})
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
		h := newHarness(t, true, reviewLoopScript("changes"))
		d := h.driver(Options{})
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

// TestRSReviewLegLanding proves the research review-loop routing this
// feature adds: a research card that was bounced review→investigate (one
// review round already burned) parks/resumes at Investigate with the
// review counter seeded from the store — round 1, not a fresh grant — and
// the driver steps a research card's work stage forward to verify like
// any other card's, rather than hitting an "unexpected autonomous stage"
// default. One graph means research has no special leg to land on.
func TestRSReviewLegLanding(t *testing.T) {
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageImplement: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return msgIdle(o.Model, "Investigated further.")
		},

		// the work stage ends with a critique now; a drive that is not
		// about the critique still needs it to pass.
		stageCritique: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return toolVerdict(o.Model, "pass")
		},
		domain.StageVerify: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return toolVerdict(o.Model, "pass")
		},
	})
	// research stages run read-only in the main checkout; only a backend
	// that can structurally enforce that is allowed to drive them.
	h.fake.Caps.ReadOnlyEnforce = true
	id, err := domain.NewID(domain.KindResearch, 1)
	if err != nil {
		t.Fatal(err)
	}
	slug, _ := domain.Slugify("research card")
	now := time.Now()
	f := domain.Feature{
		ID: id, Num: 1, Kind: domain.KindResearch, Title: "research card", Slug: slug,
		Stage: domain.StageImplement, CreatedAt: now, UpdatedAt: now,
	}
	putDraft(t, h, &f, "# RS-001: research card\n\n## Findings\n\nNothing yet.\n")
	if err := h.store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	// simulate the prior review→investigate bounce: one round already burned.
	if err := h.store.SetReviewRounds(context.Background(), f.ID, 1); err != nil {
		t.Fatal(err)
	}

	_, err = h.driver(Options{}).drive(context.Background(), f.ID)
	if err != nil {
		t.Fatalf("drive: %v", err)
	}
	// The card walks its work leg forward and lands on research's own
	// decompose gate, which this fixture has no proposals for — that gate
	// is decompose's business, tested there. What matters here is that the
	// leg moved at all and carried its burned round with it.
	if got := h.stageOf(f.ID); got == domain.StageImplement {
		t.Fatalf("feature stuck at %s; the work leg never stepped forward", got)
	}
	if r := investigateStageRound(h); r != 1 {
		t.Fatalf("work-stage event round = %d, want 1 (the burned round, not a fresh grant)", r)
	}
}

// investigateStageRound returns the round of the investigate stage event
// in the stream, or -1 if none was emitted.
func investigateStageRound(h *harness) int {
	for _, e := range h.events() {
		if e["event"] == "stage" && e["stage"] == "implement" {
			if r, ok := e["round"].(float64); ok {
				return int(r)
			}
		}
	}
	return -1
}
