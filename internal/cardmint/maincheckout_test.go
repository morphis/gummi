package cardmint

import (
	"context"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

// TestMintAMainCheckoutCard: the freeform kind's opt-out of the worktree.
// The card comes out marked, with no branch cut for it and no draft seeded
// — and two of them may slugify alike, because neither ever wants a ref.
func TestMintAMainCheckoutCard(t *testing.T) {
	store, ws := newTestWorkspace(t)
	ctx := context.Background()
	f, err := Mint(ctx, store, ws, Input{
		Kind:         domain.KindFreeform,
		Description:  "Poke at the pty leak\n\nThe copilot adapter keeps one per idle timeout.",
		MainCheckout: true,
		Envelope:     400,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !f.MainCheckout {
		t.Error("the mint did not mark the card")
	}
	if f.BranchName() == "" {
		t.Error("BranchName is empty — the derived spelling is still derivable")
	}
	if err := f.Validate(); err != nil {
		t.Errorf("the minted card does not validate: %v", err)
	}

	// The same title mints again without a slug refusal: no branch, no
	// ref to collide on.
	again, err := Mint(ctx, store, ws, Input{
		Kind:         domain.KindFreeform,
		Description:  "Poke at the pty leak",
		MainCheckout: true,
	})
	if err != nil {
		t.Fatalf("a second same-titled main-checkout card did not mint: %v", err)
	}
	if again.ID == f.ID {
		t.Error("the second mint reused the first card's id")
	}
	if !again.MainCheckout {
		t.Error("the second mint was not marked")
	}
}

// TestMainCheckoutIsRefusedOnEveryOtherKind: a feature or a bug exists to
// end as a branch, so the opt-out is refused at the mint — before a
// sequence number is spent.
func TestMainCheckoutIsRefusedOnEveryOtherKind(t *testing.T) {
	store, ws := newTestWorkspace(t)
	seqBefore := readSeq(t, ws.SeqFile())
	_, err := Mint(context.Background(), store, ws, Input{
		Kind:         domain.KindFeature,
		Description:  "a feature with nowhere to work",
		MainCheckout: true,
	})
	if err == nil {
		t.Fatal("a feature was minted into the main checkout")
	}
	if !strings.Contains(err.Error(), "freeform") {
		t.Errorf("the refusal does not say who may have it: %v", err)
	}
	if after := readSeq(t, ws.SeqFile()); after != seqBefore {
		t.Errorf("the refused mint spent a sequence number (%q → %q)", seqBefore, after)
	}
}

// TestAMainCheckoutCardCannotAdopt: it has no branch of its own, so there
// is nothing for an adoption to hand it and nothing to leave behind.
func TestAMainCheckoutCardCannotAdopt(t *testing.T) {
	store, ws := newTestWorkspace(t)
	_, err := Mint(context.Background(), store, ws, Input{
		Kind:           domain.KindFreeform,
		Description:    "finish their parser",
		MainCheckout:   true,
		Adopt:          "feat/their-parser",
		InspectAdopted: inspector(theirWork(), nil),
	})
	if err == nil {
		t.Fatal("a main-checkout card adopted a branch")
	}
	if !strings.Contains(err.Error(), "feat/their-parser") {
		t.Errorf("the refusal does not name the branch: %v", err)
	}
}
