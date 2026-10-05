package domain

import "testing"

func TestParseLandMethod(t *testing.T) {
	cases := map[string]LandMethod{"": LandSquash, "squash": LandSquash, "merge": LandMerge}
	for in, want := range cases {
		got, err := ParseLandMethod(in)
		if err != nil || got != want {
			t.Errorf("ParseLandMethod(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := ParseLandMethod("rebase"); err == nil {
		t.Error(`ParseLandMethod("rebase") accepted an unknown method`)
	}
}

func TestLandMethodsForGoalCards(t *testing.T) {
	card := &Feature{ID: "FD-001"}
	if got := card.LandMethods(); len(got) != 2 || !card.Offers(LandMerge) {
		t.Errorf("plain card methods = %v, want squash and merge", got)
	}
	inGoal := &Feature{ID: "FD-002", GoalID: "GL-001"}
	if inGoal.Offers(LandMerge) || !inGoal.Offers(LandSquash) {
		t.Errorf("card in a goal offers %v, want squash only", inGoal.LandMethods())
	}
	goal := &Feature{ID: "GL-001", Kind: KindGoal}
	if goal.Offers(LandMerge) {
		t.Errorf("goal offers %v, want squash only", goal.LandMethods())
	}
}
