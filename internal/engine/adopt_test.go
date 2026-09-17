package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// A drop used to be final in one direction only: an attached card went
// back to the board, and a card the goal had made was closed inside it
// with its commits unreachable. Adopt is the inverse of the drop.

// droppedCard puts a goal and one of its cards in the store, drops the
// card, and returns both.
func droppedCard(t *testing.T, e *Engine, at domain.Stage, reason string) (domain.Feature, domain.Feature) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	goal := domain.Feature{
		ID: "GL-001", Num: 1, Kind: domain.KindGoal, Title: "an objective", Slug: "an-objective",
		Stage: domain.StageImplement, CreatedAt: now, UpdatedAt: now,
	}
	putFeature(t, e.cfg.Store, goal)
	card := domain.Feature{
		ID: "FD-002", Num: 2, Kind: domain.KindFeature, Title: "a card", Slug: "a-card",
		Stage: at, GoalID: goal.ID, CreatedAt: now, UpdatedAt: now,
	}
	putFeature(t, e.cfg.Store, card)
	if err := e.goalDrop(ctx, goal, card, reason, ActorGoal); err != nil {
		t.Fatalf("drop: %v", err)
	}
	return goal, card
}

func TestAdoptReopensTheStageTheDropClosed(t *testing.T) {
	ctx := context.Background()
	e := newEngine(t, agent.NewFake("hi"))
	_, card := droppedCard(t, e, domain.StageImplement, "stuck and not recoverable")

	dropped, err := e.cfg.Store.GetFeature(ctx, card.ID)
	if err != nil {
		t.Fatal(err)
	}
	if dropped.Stage != domain.StageDone || !dropped.GoalDropped() {
		t.Fatalf("a dropped card is closed and stamped: stage %s dropped %v", dropped.Stage, dropped.GoalDropped())
	}
	// the drop must not borrow the hand-off stamp to clear the landing floor
	if dropped.HandedOff() {
		t.Fatal("a drop is not a hand-off")
	}
	if got := dropped.Ending(false); got != domain.EndingDropped {
		t.Fatalf("ending = %q, want dropped", got)
	}

	got, err := e.Adopt(ctx, card.ID, "user")
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if got.Stage != domain.StageImplement {
		t.Fatalf("adopted back to %s, want the stage the drop closed (implement)", got.Stage)
	}
	if got.InGoal() || got.GoalDropped() {
		t.Fatalf("an adopted card is on the open board: goal %q dropped %v", got.GoalID, got.GoalDropped())
	}
}

// The card's own thread has to say what happened to it. Its row wore one
// faint word and its page said nothing at all.
func TestDropAndAdoptAreRecordedOnTheCard(t *testing.T) {
	ctx := context.Background()
	e := newEngine(t, agent.NewFake("hi"))
	_, card := droppedCard(t, e, domain.StageImplement, "the goal is wrapping up")

	if !cardSays(t, e.cfg.Store, card.ID, "the goal is wrapping up") {
		t.Fatal("the drop's reason reached the goal's log and not the card's")
	}
	if _, err := e.Adopt(ctx, card.ID, "user"); err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if !cardSays(t, e.cfg.Store, card.ID, "adopted from GL-001") {
		t.Fatal("a card that comes back says where it came from")
	}
}

func TestAdoptRefusesACardNobodyDropped(t *testing.T) {
	ctx := context.Background()
	e := newEngine(t, agent.NewFake("hi"))
	now := time.Now()
	inGoal := domain.Feature{
		ID: "FD-003", Num: 3, Kind: domain.KindFeature, Title: "working", Slug: "working",
		Stage: domain.StageImplement, GoalID: "GL-001", CreatedAt: now, UpdatedAt: now,
	}
	putFeature(t, e.cfg.Store, inGoal)
	if _, err := e.Adopt(ctx, inGoal.ID, "user"); err == nil ||
		!strings.Contains(err.Error(), "still working it") {
		t.Fatalf("a card its goal is still working is not adoptable: %v", err)
	}

	open := domain.Feature{
		ID: "FD-004", Num: 4, Kind: domain.KindFeature, Title: "mine", Slug: "mine",
		Stage: domain.StageImplement, CreatedAt: now, UpdatedAt: now,
	}
	putFeature(t, e.cfg.Store, open)
	if _, err := e.Adopt(ctx, open.ID, "user"); err == nil ||
		!strings.Contains(err.Error(), "already on the open board") {
		t.Fatalf("an open-board card is already adopted: %v", err)
	}
}

// cardSays reports whether the card's own event log carries text.
func cardSays(t *testing.T, store *state.Store, id domain.FeatureID, want string) bool {
	t.Helper()
	evs, err := store.Events(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs {
		if strings.Contains(ev.Payload, want) {
			return true
		}
	}
	return false
}
