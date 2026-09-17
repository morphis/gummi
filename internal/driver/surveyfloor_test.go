package driver

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// TestSurveyThatWroteNothingDoesNotCross is the regression for the lxd
// autopilot drive's research card. Its survey stage wrote nothing into
// Findings — it had no tool that could, which is fixed in the engine —
// and its critique passed it anyway, citing a resolution the architect
// had recorded back at the PLAN stage ("Findings stays the seeded
// placeholder by design until that stage runs"). The crossing itself
// never asked: gatepolicy answers a work-stage critique pass with
// Advance, and both loops acted on it with a bare store.Transition, so
// the undrafted floor — which does demand Findings on this exact edge —
// was never consulted. Verify caught it 428.8 credits later.
func TestSurveyThatWroteNothingDoesNotCross(t *testing.T) {
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageImplement: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return msgIdle(o.Model, "Surveyed the repo.") // and wrote nothing
		},
		stageCritique: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return toolVerdict(o.Model, "pass")
		},
	})
	h.noDraft = true // the whole point: the survey wrote nothing

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
	// the document as the survey left it: every section still the template's
	// own `%%` prompt, plus the markers the plan critique exchanged.
	path := filepath.Join(h.root, f.ArtifactPath())
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	doc := "# RS-001: research card\n\n## Findings\n" +
		"%% @gummi: what the investigation learned — prose with inline citations\n" +
		"%% @reviewer(2026-09-16): Findings is still the empty placeholder.\n" +
		"%% @architect: resolved — that is the build stage's job; re-check it once it has run.\n" +
		"\n## Slices\n\nnone\n"
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := h.driver(Options{}).Resume(context.Background(), id, ResumeInput{})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if out.Status != StatusBlocked {
		t.Fatalf("status = %q, want %q — a survey that wrote nothing crossed its own gate",
			out.Status, StatusBlocked)
	}
	if got := h.stageOf(id); got != domain.StageImplement {
		t.Errorf("card advanced to %s with an empty Findings section", got)
	}
	b := lastEvent(h, "blocked")
	if b == nil {
		t.Fatalf("no blocked event; kinds=%v", h.eventKinds())
	}
	names, _ := b["undrafted"].([]any)
	if len(names) != 1 || names[0] != "Findings" {
		t.Errorf("blocked event undrafted = %v, want [Findings]", b["undrafted"])
	}
}
