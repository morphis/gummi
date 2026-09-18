package domain

import (
	"strings"
	"testing"
	"time"
)

func TestGoalIDRoundTrips(t *testing.T) {
	id, err := NewID(KindGoal, 4)
	if err != nil {
		t.Fatal(err)
	}
	if id != "GL-004" {
		t.Fatalf("NewID(goal, 4) = %q, want GL-004", id)
	}
	if _, err := ParseFeatureID("GL-004"); err != nil {
		t.Fatalf("GL-004 should parse: %v", err)
	}
	if id.Kind() != KindGoal {
		t.Fatalf("GL-004 kind = %q", id.Kind())
	}
	f := Feature{ID: id, Num: 4, Kind: KindGoal, Title: "t", Slug: "t", Stage: StageTodo}
	if got := f.ArtifactPath(); got != ".gummi/goals/GL-004-t.md" {
		t.Fatalf("goal artifact path = %q", got)
	}
	if !f.IsGoal() || f.InGoal() {
		t.Fatalf("a goal is a goal and belongs to none")
	}
}

func TestFeatureValidateGoalLinks(t *testing.T) {
	base := func() Feature {
		return Feature{ID: "FD-002", Num: 2, Kind: KindFeature, Title: "t", Slug: "t", Stage: StageTodo}
	}
	f := base()
	f.GoalID = "GL-001"
	if err := f.Validate(); err != nil {
		t.Fatalf("a feature in a goal is valid: %v", err)
	}
	f.GoalID = "FD-001"
	if err := f.Validate(); err == nil {
		t.Fatalf("a goal id that is not a goal must be refused")
	}
	g := Feature{ID: "GL-003", Num: 3, Kind: KindGoal, Title: "t", Slug: "t", Stage: StageTodo, GoalID: "GL-001"}
	if err := g.Validate(); err == nil || !strings.Contains(err.Error(), "do not nest") {
		t.Fatalf("a goal inside a goal must be refused, got %v", err)
	}
	f = base()
	f.FoundBy = "GL-001"
	if err := f.Validate(); err != nil {
		t.Fatalf("found-by a goal is valid: %v", err)
	}
	f.Goal.Lanes = -1
	if err := f.Validate(); err == nil {
		t.Fatalf("negative lanes must be refused")
	}
}

func TestDoneWhenValidate(t *testing.T) {
	cases := []struct {
		in   DoneWhen
		want string
	}{
		{DoneWhen{ID: "DW-1", Says: "export runs offline", Check: "make offline-test"}, ""},
		{DoneWhen{ID: "DW-2", Says: "the docs explain it", Judge: true}, ""},
		{DoneWhen{ID: "1", Says: "x", Check: "true"}, "form DW-N"},
		{DoneWhen{ID: "DW-3", Check: "true"}, "says nothing"},
		{DoneWhen{ID: "DW-4", Says: "x"}, "no way to check"},
		{DoneWhen{ID: "DW-5", Says: "x", Check: "true", Judge: true}, "pick one"},
	}
	for _, c := range cases {
		err := c.in.Validate()
		if c.want == "" && err != nil {
			t.Errorf("%+v: unexpected %v", c.in, err)
		}
		if c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%+v: want error containing %q, got %v", c.in, c.want, err)
		}
	}
	if id, ok := DoneWhenIDForCheck(DoneWhen{ID: "DW-7"}.CheckName()); !ok || id != "DW-7" {
		t.Fatalf("check name does not round-trip: %q %v", id, ok)
	}
	if _, ok := DoneWhenIDForCheck("go test"); ok {
		t.Fatalf("a repo check is not a done-when item")
	}
}

func TestGoalCardRowValidate(t *testing.T) {
	items := map[string]bool{"DW-1": true}
	ok := GoalCardRow{Title: "cache", Serves: []string{"DW-1"}}
	if err := ok.Validate(items); err != nil {
		t.Fatalf("valid row refused: %v", err)
	}
	if ok.EffectiveKind() != KindFeature {
		t.Fatalf("row kind defaults to feature")
	}
	bad := []GoalCardRow{
		{Title: "no serves"},
		{Title: "wrong serves", Serves: []string{"DW-9"}},
		{Title: "nested", Kind: string(KindGoal), Serves: []string{"DW-1"}},
		{Title: "not a type", Kind: "chore", Serves: []string{"DW-1"}},
		{Title: "nested id", ID: "GL-002", Serves: []string{"DW-1"}},
		{Serves: []string{"DW-1"}},
	}
	for _, r := range bad {
		if err := r.Validate(items); err == nil {
			t.Errorf("row %+v should be refused", r)
		}
	}
}

func TestGoalReserveAndLanes(t *testing.T) {
	if got := DefaultGoalReserve(4000); got != 600 {
		t.Fatalf("15%% of 4000 = %d, want 600", got)
	}
	if got := DefaultGoalReserve(300); got != GoalReserveFloor {
		t.Fatalf("small budget reserve = %d, want the floor", got)
	}
	if got := DefaultGoalReserve(50); got != 50 {
		t.Fatalf("reserve never exceeds the budget, got %d", got)
	}
	f := Feature{Kind: KindGoal, Budget: Budget{Envelope: 4000}}
	if f.ReserveCredits() != 600 {
		t.Fatalf("default reserve not used")
	}
	f.Goal.Reserve = 250
	if f.ReserveCredits() != 250 {
		t.Fatalf("the lead's estimate wins")
	}
	if (GoalSettings{}).LaneCount() != DefaultGoalLanes {
		t.Fatalf("zero lanes reads as the default")
	}
	if (GoalSettings{WrapUpAt: time.Now()}).WrappingUp() != true {
		t.Fatalf("wrap-up stamp not read")
	}
}

func TestGoalMintPoolKeepsRoomForTheLead(t *testing.T) {
	f := Feature{Kind: KindGoal, Budget: Budget{Envelope: 1500}} // reserve 225
	// few cards: the 10% floor
	if got := f.GoalMintPool(0, 1); got != 1275-127.5 {
		t.Fatalf("one card keeps the 10%% headroom: %v", got)
	}
	// two cards: four lead turns each
	if got := f.GoalMintPool(0, 2); got != 1275-240 {
		t.Fatalf("two cards keep room for eight lead turns: %v", got)
	}
	// many cards: capped at 30%
	if got := f.GoalMintPool(0, 20); got != 1275-382.5 {
		t.Fatalf("many cards cap the headroom: %v", got)
	}
}

// TestAGoalConductsItsOwnCards: a card a goal is working is driven by
// that goal's lead, so no one else drives it. A card the goal dropped is
// closed where it stands and a done card has nothing left to drive —
// both wear the goal id and neither is conducted — and a card in no goal
// never was.
func TestAGoalConductsItsOwnCards(t *testing.T) {
	held := Feature{ID: "FD-011", Kind: KindFeature, Stage: StageImplement, GoalID: "GL-010"}
	if !held.Conducted() {
		t.Fatal("a card its goal is working is conducted by the goal's lead")
	}
	dropped := held
	dropped.GoalDroppedAt = time.Now()
	if dropped.Conducted() {
		t.Fatal("a dropped card left the goal's hands — adopting it back is the reader's move")
	}
	done := held
	done.Stage = StageDone
	if done.Conducted() {
		t.Fatal("a done card has nothing left to conduct")
	}
	own := Feature{ID: "FD-012", Kind: KindFeature, Stage: StageImplement}
	if own.Conducted() {
		t.Fatal("a card outside every goal is the board's own")
	}
	goal := Feature{ID: "GL-010", Kind: KindGoal, Stage: StageImplement}
	if goal.Conducted() {
		t.Fatal("goals do not nest: a goal is never conducted by one")
	}
}
