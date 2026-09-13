package state

import (
	"context"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
)

func goalFeat(num int, title string) *domain.Feature {
	f := feat(num, title)
	f.ID, _ = domain.NewID(domain.KindGoal, num)
	f.Kind = domain.KindGoal
	f.Budget = domain.Budget{Envelope: 4000}
	return f
}

func TestGoalColumnsRoundTrip(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	g := goalFeat(1, "Export works offline")
	g.Goal = domain.GoalSettings{Lanes: 3, Reserve: 250, Partial: "budget"}
	g.Goal.WrapUpAt = time.Now().UTC().Truncate(time.Second)
	if err := s.CreateFeature(ctx, g); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetFeature(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Goal.Lanes != 3 || got.Goal.Reserve != 250 || got.Goal.Partial != "budget" || !got.Goal.WrapUpAt.Equal(g.Goal.WrapUpAt) {
		t.Fatalf("goal settings did not round-trip: %+v", got.Goal)
	}

	c := feat(2, "local cache")
	c.GoalID = g.ID
	if err := s.CreateFeature(ctx, c); err != nil {
		t.Fatal(err)
	}
	other := feat(3, "existing card")
	if err := s.CreateFeature(ctx, other); err != nil {
		t.Fatal(err)
	}
	if err := s.SetGoal(ctx, other.ID, g.ID, true); err != nil {
		t.Fatal(err)
	}
	cards, err := s.ListGoalCards(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 2 || cards[0].ID != c.ID || cards[1].ID != other.ID || !cards[1].GoalAttached {
		t.Fatalf("goal cards = %+v", cards)
	}

	g2 := goalFeat(4, "another goal")
	if err := s.CreateFeature(ctx, g2); err != nil {
		t.Fatal(err)
	}
	if err := s.SetGoal(ctx, other.ID, g2.ID, true); err == nil {
		t.Fatalf("a card in one goal must not be taken by another")
	}
	if err := s.SetGoal(ctx, g2.ID, g.ID, false); err == nil {
		t.Fatalf("goals do not nest")
	}

	now := time.Now()
	if err := s.SetGoalDropped(ctx, c.ID, now); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFoundBy(ctx, other.ID, g.ID); err != nil {
		t.Fatal(err)
	}
	gotC, _ := s.GetFeature(ctx, c.ID)
	if !gotC.GoalDropped() {
		t.Fatalf("dropped stamp not read back")
	}
	if err := s.ClearGoal(ctx, other.ID); err != nil {
		t.Fatal(err)
	}
	gotO, _ := s.GetFeature(ctx, other.ID)
	if gotO.InGoal() || gotO.GoalAttached || gotO.FoundBy != g.ID {
		t.Fatalf("cleared card = %+v", gotO)
	}

	if err := s.ClearGoalWrapUp(ctx, g.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetGoalWrapUp(ctx, g.ID, now); err != nil {
		t.Fatal(err)
	}
	later := now.Add(time.Hour)
	if err := s.SetGoalWrapUp(ctx, g.ID, later); err != nil {
		t.Fatal(err)
	}
	gotG, _ := s.GetFeature(ctx, g.ID)
	if gotG.Goal.WrapUpAt.Sub(now).Abs() > time.Millisecond {
		t.Fatalf("first wrap-up stamp must win, got %v want %v", gotG.Goal.WrapUpAt, now)
	}
	if gotG.Goal.Partial != "" {
		t.Fatalf("clearing wrap-up clears partial")
	}
}

func TestGoalLogNumbersDecisions(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	g := goalFeat(1, "Goal")
	if err := s.CreateFeature(ctx, g); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendGoalEvent(ctx, g.ID, GoalPayload{Action: GoalLanded, Card: "FD-002", By: "goal"}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	d1, err := s.AppendGoalEvent(ctx, g.ID, GoalPayload{Action: GoalDecision, Detail: "json by default", Alternative: "table by default"}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	d2, err := s.AppendGoalEvent(ctx, g.ID, GoalPayload{Action: GoalDecision, Detail: "cache in XDG dir"}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if d1.DecisionRef() != "D-1" || d2.DecisionRef() != "D-2" {
		t.Fatalf("decisions numbered %s, %s", d1.DecisionRef(), d2.DecisionRef())
	}
	log, err := s.GoalLog(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(log) != 3 || log[0].Action != GoalLanded || log[2].N != 2 || log[1].Alternative != "table by default" {
		t.Fatalf("log = %+v", log)
	}
	if _, err := s.AppendGoalEvent(ctx, "GL-099", GoalPayload{Action: GoalNote}, time.Time{}); err == nil {
		t.Fatalf("an entry for a missing goal must fail")
	}
}
