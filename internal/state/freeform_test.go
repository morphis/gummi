package state

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
)

func freeformFeat(num int, title string) *domain.Feature {
	id, _ := domain.NewID(domain.KindFreeform, num)
	slug, _ := domain.Slugify(title)
	now := time.Now().UTC()
	return &domain.Feature{
		ID: id, Num: num, Kind: domain.KindFreeform, Title: title, Slug: slug,
		Stage: domain.StageOpen, CreatedAt: now, UpdatedAt: now,
	}
}

// TestCloseFreeformIsTheOnlyWayOutOfStageOpen: a freeform card's ending is
// not an edge in the graph, so the ordinary crossing refuses it and the
// method that exists for it records the same history any other ending does.
func TestCloseFreeformIsTheOnlyWayOutOfStageOpen(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := freeformFeat(1, "poke at the pty leak")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}

	// The graph refuses it, which is why CloseFreeform exists at all.
	if _, err := s.Transition(ctx, f.ID, domain.StageDone, "t"); err == nil {
		t.Fatal("Transition moved a freeform card; StageOpen has no outgoing edge")
	}

	closed, err := s.CloseFreeform(ctx, f.ID, "t")
	if err != nil {
		t.Fatal(err)
	}
	if closed.Stage != domain.StageDone {
		t.Errorf("stage after close = %q, want %q", closed.Stage, domain.StageDone)
	}
	got, err := s.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Stage != domain.StageDone {
		t.Errorf("stored stage = %q, want %q", got.Stage, domain.StageDone)
	}
	// The card's own history records the crossing, so a freeform ending is
	// as auditable as any other.
	ts, err := s.History(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, tr := range ts {
		if tr.From == domain.StageOpen && tr.To == domain.StageDone {
			found = true
		}
	}
	if !found {
		t.Errorf("no open → done transition recorded: %+v", ts)
	}
	// Closing again is a no-op rather than an error: a landing and a
	// hand-off can both reach it.
	if _, err := s.CloseFreeform(ctx, f.ID, "t"); err != nil {
		t.Errorf("closing an already-closed freeform card failed: %v", err)
	}
}

// TestCloseFreeformRefusesEveryOtherKind is the check that makes bypassing
// workflow.CanTransition safe: a store method able to write "done" onto any
// card would be a way to land a feature without verifying it.
func TestCloseFreeformRefusesEveryOtherKind(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := feat(2, "a real feature")
	f.Stage = domain.StageImplement
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	_, err := s.CloseFreeform(ctx, f.ID, "t")
	if err == nil {
		t.Fatal("CloseFreeform closed a feature card")
	}
	if !strings.Contains(err.Error(), string(f.ID)) {
		t.Errorf("the refusal does not name the card: %v", err)
	}
	got, err := s.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Stage != domain.StageImplement {
		t.Errorf("the refused close moved the card to %q", got.Stage)
	}
}

// TestASessionKeepsTheModelItWasGiven: the agent and model a person chose
// for a freeform card's session are the card's, read back as written and
// changeable afterwards — the switch a session offers mid-conversation.
func TestASessionKeepsTheModelItWasGiven(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := freeformFeat(1, "tidy the cli help text")
	f.SessionBackend, f.SessionModel = "codex", "gpt-5"
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionBackend != "codex" || got.SessionModel != "gpt-5" {
		t.Fatalf("read back %q/%q, want codex/gpt-5", got.SessionBackend, got.SessionModel)
	}
	got.SessionBackend, got.SessionModel = "claude", "claude-sonnet-5-5"
	if err := s.UpdateFeature(ctx, &got); err != nil {
		t.Fatal(err)
	}
	again, err := s.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.SessionBackend != "claude" || again.SessionModel != "claude-sonnet-5-5" {
		t.Errorf("after the switch read back %q/%q, want claude/claude-sonnet-5-5", again.SessionBackend, again.SessionModel)
	}
}

// TestACardKeepsTheSkillsItWasCreatedWith: the library skills picked for
// a card are read back as written, in order, and a card with none picked
// reads back with none.
func TestACardKeepsTheSkillsItWasCreatedWith(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := freeformFeat(1, "review the branch")
	f.Skills = []string{"skill-review", "skill-deploy"}
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Skills) != 2 || got.Skills[0] != "skill-review" || got.Skills[1] != "skill-deploy" {
		t.Fatalf("skills read back as %q, want [skill-review skill-deploy]", got.Skills)
	}
	plain := freeformFeat(2, "tidy the cli help text")
	if err := s.CreateFeature(ctx, plain); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetFeature(ctx, plain.ID); err != nil || got.Skills != nil {
		t.Fatalf("a card with no skills read back %q (%v), want none", got.Skills, err)
	}
}
