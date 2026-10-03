package driver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
)

// mainCheckoutFreeform mints a freeform card running in the main checkout:
// no branch, no worktree, and one loose file standing in for a turn's work.
func mainCheckoutFreeform(t *testing.T, h *harness, num int) domain.Feature {
	t.Helper()
	ctx := context.Background()
	id, _ := domain.NewID(domain.KindFreeform, num)
	now := time.Now()
	f := domain.Feature{
		ID: id, Num: num, Kind: domain.KindFreeform, Title: "drop the leaked fd",
		Slug: "drop-the-leaked-fd", Stage: domain.StageOpen,
		Budget: domain.Budget{Envelope: 400}, BranchScheme: domain.BranchSchemeKind,
		MainCheckout: true,
		CreatedAt:    now, UpdatedAt: now,
	}
	if err := h.store.CreateFeature(ctx, &f); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.root, "sketch.txt"), []byte("hand-rolled\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

// TestMergeRefusesAMainCheckoutCard: there is no branch to squash and
// nothing this verb could do to work that lives loose in the person's
// checkout. The refusal names what to do instead, before any git runs.
func TestMergeRefusesAMainCheckoutCard(t *testing.T) {
	h := newHarness(t, true, nil)
	f := mainCheckoutFreeform(t, h, 31)
	before := gitHead(t, h.root)

	_, err := h.driver(Options{}).Merge(context.Background(), f.ID,
		"fix(copilot): drop the pty fd leaked on idle timeout")
	if err == nil {
		t.Fatal("a main-checkout card was merged")
	}
	if !strings.Contains(err.Error(), "main checkout") {
		t.Errorf("the refusal does not say why: %v", err)
	}
	if got := gitHead(t, h.root); got != before {
		t.Errorf("the refused merge moved main: %s -> %s", before, got)
	}
}

// TestAMainCheckoutHandOffKeepsTheWorkLoose: the ending such a card has.
// It closes the card, commits nothing — the loose work is the person's, in
// the checkout they work in — and cuts no branch to keep.
func TestAMainCheckoutHandOffKeepsTheWorkLoose(t *testing.T) {
	h := newHarness(t, true, nil)
	f := mainCheckoutFreeform(t, h, 32)
	ctx := context.Background()
	before := gitHead(t, h.root)

	out, err := h.driver(Options{}).HandOff(ctx, f.ID)
	if err != nil {
		t.Fatalf("HandOff: %v", err)
	}
	if out.Status != StatusVerified {
		t.Errorf("status = %q, want done", out.Status)
	}
	stored, err := h.store.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Stage != domain.StageDone || stored.HandedOffAt.IsZero() {
		t.Errorf("stage %q, handedOff %v; want done and stamped", stored.Stage, stored.HandedOffAt)
	}
	if got := gitHead(t, h.root); got != before {
		t.Errorf("the hand-off moved main: %s -> %s", before, got)
	}
	body, err := os.ReadFile(filepath.Join(h.root, "sketch.txt"))
	if err != nil {
		t.Fatalf("the loose work is gone: %v", err)
	}
	if string(body) != "hand-rolled\n" {
		t.Errorf("the loose work changed: %q", body)
	}
}
