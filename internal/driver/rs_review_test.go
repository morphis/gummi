package driver

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/verdict"
)

// TestRSReviewCapEscalates drives a research card through its review loop
// (review → investigate → shape → review, the existing research edges)
// with a changes verdict every round, proving the research slice's review
// loop reuses the review round kind and cap verbatim — no research-specific
// counter or escalation wording. The work leg is investigate→shape, not a
// direct bounce back to review (research has no investigate→review edge),
// so the script drives an investigate turn and a shape turn per round.
func TestRSReviewCapEscalates(t *testing.T) {
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageInvestigate: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return msgIdle(o.Model, "Investigated.")
		},
		domain.StageShape: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return msgIdle(o.Model, "Shaped.")
		},
		domain.StageReview: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return msgIdle(o.Model, "Issues.\nVERDICT: changes")
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
		Stage: domain.StageReview, CreatedAt: now, UpdatedAt: now,
	}
	putDraft(t, h, &f, "# RS-001: research card\n\n## Findings\n\nNothing yet.\n")
	if err := h.store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	out, err := h.driver(Options{}).drive(ctx, f.ID)
	if err != nil {
		t.Fatalf("drive: %v", err)
	}
	if out.Status != StatusEscalation {
		t.Fatalf("status = %q, want escalation; stream=%v", out.Status, h.eventKinds())
	}
	max := verdict.MaxRounds(domain.RoundKindReview)
	want := fmt.Sprintf("review still requesting changes after %d rounds", max)
	esc := lastEvent(h, "escalation")
	if esc == nil || esc["reason"] != want {
		t.Fatalf("escalation reason = %v, want %q", esc, want)
	}
	// the cap escalation clears the persisted count, so a later resume
	// starts a fresh review budget.
	if got, err := h.store.Rounds(ctx, f.ID, domain.RoundKindReview); err != nil || got != 0 {
		t.Fatalf("Rounds(review) after escalation = %d, %v; want 0 (reset)", got, err)
	}
}
