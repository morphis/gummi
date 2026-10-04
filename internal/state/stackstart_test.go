package state

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
)

func TestStartStackNamesItAfterTheBottomCard(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	mk := func(id domain.FeatureID, num int, slug string, kind domain.Kind) {
		t.Helper()
		f := domain.Feature{ID: id, Num: num, Title: slug, Slug: slug, Kind: kind, Stage: domain.StageTodo}
		if err := s.CreateFeature(ctx, &f); err != nil {
			t.Fatal(err)
		}
	}
	mk("FD-001", 1, "theme", domain.KindFeature)
	mk("FD-002", 2, "theme", domain.KindFeature)
	mk("GL-003", 3, "ship-it", domain.KindGoal)

	st, err := s.StartStack(ctx, "FD-001", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if st.ID != "theme" || st.Name != "theme" {
		t.Fatalf("stack = %+v, want id and name from the slug", st)
	}
	// a second card with the same slug still gets a stack of its own
	st2, err := s.StartStack(ctx, "FD-002", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if st2.ID != "fd-002-theme" {
		t.Fatalf("collision id = %q, want the bottom card's id in front", st2.ID)
	}
	if _, err := s.StartStack(ctx, "FD-001", "again", time.Now()); err == nil {
		t.Fatal("a card already in a stack started another")
	}
	if _, err := s.StartStack(ctx, "GL-003", "", time.Now()); !errors.Is(err, ErrStackNotStackable) {
		t.Fatalf("a goal started a stack: %v", err)
	}
	stacks, err := s.ListStacks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(stacks) != 2 {
		t.Fatalf("stacks = %+v, want the two that started and nothing left by the refusals", stacks)
	}
}

// Deleting a card takes it out of its stack like `stack rm` does: the
// cards above move down a place, so positions stay contiguous.
func TestDeletingAStackedCardClosesTheGap(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	for i, slug := range []string{"a", "b", "c"} {
		f := domain.Feature{ID: domain.FeatureID("FD-00" + string(rune('1'+i))), Num: i + 1, Title: slug, Slug: slug, Kind: domain.KindFeature, Stage: domain.StageTodo}
		if err := s.CreateFeature(ctx, &f); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.StartStack(ctx, "FD-001", "chain", time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []domain.FeatureID{"FD-002", "FD-003"} {
		if err := s.AddToStack(ctx, "chain", id, 99); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteFeature(ctx, "FD-002"); err != nil {
		t.Fatal(err)
	}
	cards, err := s.ListStackCards(ctx, "chain")
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 2 || cards[0].StackPos != 0 || cards[1].ID != "FD-003" || cards[1].StackPos != 1 {
		t.Fatalf("stack after the delete = %+v, want FD-001@0 and FD-003@1", cards)
	}
}

// A card whose work has landed or been handed off holds its place: lifting
// it above open cards would name a closed card as what they fork from.
func TestAClosedCardCannotBeMovedInItsStack(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	for i, slug := range []string{"a", "b"} {
		f := domain.Feature{ID: domain.FeatureID("FD-00" + string(rune('1'+i))), Num: i + 1, Title: slug, Slug: slug, Kind: domain.KindFeature, Stage: domain.StageTodo}
		if err := s.CreateFeature(ctx, &f); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.StartStack(ctx, "FD-001", "chain", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.AddToStack(ctx, "chain", "FD-002", 99); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE features SET stage = 'done' WHERE id = 'FD-001'`); err != nil {
		t.Fatal(err)
	}
	if err := s.MoveInStack(ctx, "FD-001", 1); !errors.Is(err, ErrStackCardClosed) {
		t.Fatalf("moving a done card: %v, want ErrStackCardClosed", err)
	}
	if err := s.MoveInStack(ctx, "FD-002", 0); err != nil {
		t.Fatalf("moving an open card below a closed one: %v", err)
	}
}

// A freeform session may stack while it works in a worktree of its own
// (DESIGN §19.5), but not from the main checkout, which holds no branch,
// and not once it is closed: its branch is no longer anyone's work.
func TestAFreeformCardStacksOnlyWhileItHoldsABranch(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	mk := func(f domain.Feature) {
		t.Helper()
		if err := s.CreateFeature(ctx, &f); err != nil {
			t.Fatal(err)
		}
	}
	mk(domain.Feature{ID: "FD-001", Num: 1, Title: "a", Slug: "a", Kind: domain.KindFeature, Stage: domain.StageTodo})
	mk(domain.Feature{ID: "FF-002", Num: 2, Title: "main", Slug: "main", Kind: domain.KindFreeform, Stage: domain.StageOpen, MainCheckout: true})
	mk(domain.Feature{ID: "FF-003", Num: 3, Title: "closed", Slug: "closed", Kind: domain.KindFreeform, Stage: domain.StageOpen})
	mk(domain.Feature{ID: "FF-004", Num: 4, Title: "open", Slug: "open", Kind: domain.KindFreeform, Stage: domain.StageOpen})
	if _, err := s.db.ExecContext(ctx, `UPDATE features SET stage = 'done' WHERE id = 'FF-003'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartStack(ctx, "FD-001", "chain", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.AddToStack(ctx, "chain", "FF-002", -1); !errors.Is(err, ErrStackNotStackable) {
		t.Errorf("adding a main-checkout session: %v, want ErrStackNotStackable", err)
	}
	if err := s.AddToStack(ctx, "chain", "FF-003", -1); !errors.Is(err, ErrStackSessionClosed) {
		t.Errorf("adding a closed session: %v, want ErrStackSessionClosed", err)
	}
	if _, err := s.StartStack(ctx, "FF-003", "", time.Now()); !errors.Is(err, ErrStackSessionClosed) {
		t.Errorf("starting a stack on a closed session: %v, want ErrStackSessionClosed", err)
	}
	if err := s.AddToStack(ctx, "chain", "FF-004", -1); err != nil {
		t.Errorf("adding a session in its own worktree: %v", err)
	}
}

// Two stacks never share a name, ignoring case: a rename onto another
// stack's name, or a new stack named after one, is refused, and a stack
// whose bottom card's slug is already a name takes its id as its name.
func TestStackNamesAreUnique(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	for i, slug := range []string{"theme", "theme", "other"} {
		f := domain.Feature{ID: domain.FeatureID("FD-00" + string(rune('1'+i))), Num: i + 1, Title: slug, Slug: slug, Kind: domain.KindFeature, Stage: domain.StageTodo}
		if err := s.CreateFeature(ctx, &f); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.StartStack(ctx, "FD-001", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.StartStack(ctx, "FD-002", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if second.Name == first.Name {
		t.Fatalf("two stacks share the name %q", second.Name)
	}
	if _, err := s.StartStack(ctx, "FD-003", "THEME", time.Now()); !errors.Is(err, ErrStackNameTaken) {
		t.Errorf("a new stack named after another: %v, want ErrStackNameTaken", err)
	}
	if err := s.RenameStack(ctx, second.ID, "Theme"); !errors.Is(err, ErrStackNameTaken) {
		t.Errorf("a rename onto another stack's name: %v, want ErrStackNameTaken", err)
	}
	if err := s.RenameStack(ctx, first.ID, "Theme"); err != nil {
		t.Errorf("renaming a stack to its own name in another case: %v", err)
	}
}
