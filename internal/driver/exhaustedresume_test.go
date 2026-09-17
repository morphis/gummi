package driver

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// TestToppedUpSurveyRunsAgainRatherThanBeingCritiqued is the other half of
// the same drive failure. Case C ran out of credits with its Findings
// section still the seeded placeholder; the resume, given 500 more, spent
// them on a critique and a verify of a document nobody had written — the
// writer got 0.00. Engine.exhaust saves a budget-stopped session as
// StateDone like any finished one, and resumeCritiqueLoop read that as
// "the revised output is on disk: critique it".
func TestToppedUpSurveyRunsAgainRatherThanBeingCritiqued(t *testing.T) {
	writerRuns := 0
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageImplement: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			writerRuns++
			return msgIdle(o.Model, "Surveying.")
		},
		stageCritique: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return toolVerdict(o.Model, "pass")
		},
	})
	h.noDraft = true

	id, _ := domain.NewID(domain.KindResearch, 1)
	slug, _ := domain.Slugify("research card")
	now := time.Now()
	f := domain.Feature{
		ID: id, Num: 1, Kind: domain.KindResearch, Title: "research card", Slug: slug,
		Stage: domain.StageImplement, Budget: domain.Budget{Envelope: 500},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := h.store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(h.root, f.ArtifactPath())
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# RS-001: research card\n\n## Findings\n"+
		"%% @gummi: what the investigation learned\n\n## Slices\n\nnone\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// the survey's session as the budget stop left it: done, and cut off
	if err := h.store.SaveSession(context.Background(), state.SessionSnapshot{
		Feature: id, Stage: domain.StageImplement, Role: "architect", Flavor: "stage",
		State: "done", Exhausted: true,
	}); err != nil {
		t.Fatal(err)
	}

	writerRuns = 0
	if _, err := h.driver(Options{}).Resume(context.Background(), id, ResumeInput{}); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if writerRuns == 0 {
		t.Error("the topped-up resume never gave the survey another turn — it went " +
			"straight to critiquing a document that was never written")
	}
}
