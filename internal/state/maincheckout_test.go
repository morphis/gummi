package state

import (
	"context"
	"testing"
)

// TestAMainCheckoutCardRoundTrips: the mint-time choice survives the row —
// a card minted into the main checkout reads back as one, and a card
// minted before the column existed still reads as a worktree card.
func TestAMainCheckoutCardRoundTrips(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := freeformFeat(1, "poke at the pty leak")
	f.MainCheckout = true
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.MainCheckout {
		t.Error("the mint-time choice did not survive the row")
	}

	// A card with nothing set reads as a worktree card — every row written
	// before the column existed, and every card minted into a worktree.
	plain := freeformFeat(2, "tidy the cli help text")
	if err := s.CreateFeature(ctx, plain); err != nil {
		t.Fatal(err)
	}
	again, err := s.GetFeature(ctx, plain.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.MainCheckout {
		t.Error("an ordinary card read back as a main-checkout card")
	}
}
