package domain

import (
	"strings"
	"testing"
	"time"
)

func mainCheckoutCard(num int) *Feature {
	id, _ := NewID(KindFreeform, num)
	slug, _ := Slugify("poke at the pty leak")
	now := time.Now()
	return &Feature{
		ID: id, Num: num, Kind: KindFreeform, Title: "poke at the pty leak", Slug: slug,
		Stage: StageOpen, BranchScheme: BranchSchemeKind,
		MainCheckout: true,
		CreatedAt:    now, UpdatedAt: now,
	}
}

// TestAMainCheckoutCardValidates: a freeform card minted into the main
// checkout carries no branch-shaped field at all, and the row reads back
// exactly as it was minted.
func TestAMainCheckoutCardValidates(t *testing.T) {
	f := mainCheckoutCard(1)
	if err := f.Validate(); err != nil {
		t.Fatalf("a main-checkout freeform card does not validate: %v", err)
	}
}

// TestMainCheckoutIsAFreeformCardsProperty: every other kind exists to end
// as a branch, so the choice is refused on one before it can be stored —
// and a freeform card carrying a branch-shaped field beside it is a mint
// that lost track of what it was making.
func TestMainCheckoutIsAFreeformCardsProperty(t *testing.T) {
	cases := []struct {
		name string
		mut  func(f *Feature)
		want string
	}{
		{"a feature", func(f *Feature) { f.Kind, f.ID, f.Stage = KindFeature, "FD-002", StageTodo }, "freeform card's"},
		{"a bug", func(f *Feature) { f.Kind, f.ID, f.Stage = KindBug, "BG-002", StageTodo }, "freeform card's"},
		{"a base", func(f *Feature) { f.Base = "feat/other" }, "forks from no branch"},
		{"a carried branch", func(f *Feature) { f.Branch, f.BranchScheme = "feat/other", BranchSchemeAdopted }, "holds no branch"},
		{"a stack", func(f *Feature) { f.StackID, f.StackPos = "st-1", 0 }, "no branch to stack"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := mainCheckoutCard(2)
			tc.mut(f)
			err := f.Validate()
			if err == nil {
				t.Fatalf("validated a main-checkout card with %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal does not say %q: %v", tc.want, err)
			}
		})
	}
}
