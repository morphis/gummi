package driver

import (
	"context"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// TestResumeHonoursAVerifyAlreadyPassed is the regression for case A of
// the lxd autopilot drive. Its verify returned pass — every gummi-check
// green, all five plan invariants confirmed, an 1,853-input differential
// fuzz against the merge-base clean — and the run exhausted four seconds
// later, before the card could be marked verified. A 60-credit top-up
// re-ran the whole verify stage from scratch (+42.8 credits) and ran out
// again without reaching a verdict at all.
//
// resumeCritiqueLoop is the only path that consults a stored verdict, and
// CritiqueRoundKind knows plan and implement only, so verify fell through
// to a fresh session every time.
func TestResumeHonoursAVerifyAlreadyPassed(t *testing.T) {
	verifyRuns := 0
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageVerify: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			verifyRuns++
			return toolVerdict(o.Model, "pass")
		},
	})

	id, _ := domain.NewID(domain.KindFeature, 1)
	slug, _ := domain.Slugify("export flag")
	f := domain.Feature{
		ID: id, Num: 1, Kind: domain.KindFeature, Title: "export flag", Slug: slug,
		Stage: domain.StageVerify, Budget: domain.Budget{Envelope: 900},
	}
	if err := h.store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	h.draftRequiredSections(f)
	// the verify session as the budget stop left it: done, verdict recorded
	if err := h.store.SaveSession(context.Background(), state.SessionSnapshot{
		Feature: f.ID, Stage: domain.StageVerify, Role: "reviewer", Flavor: "stage",
		State: "done", Verdict: "pass", Exhausted: true,
	}); err != nil {
		t.Fatal(err)
	}

	verifyRuns = 0
	out, err := h.driver(Options{}).Resume(context.Background(), f.ID, ResumeInput{})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if verifyRuns != 0 {
		t.Errorf("verify ran %d more time(s) — the resume paid again for a verdict "+
			"already in the store", verifyRuns)
	}
	if out.Status != StatusVerified {
		t.Errorf("status = %q, want %q — the card had already passed verify",
			out.Status, StatusVerified)
	}
}
