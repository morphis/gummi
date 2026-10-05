package driver

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
)

// freeformWithWork mints a freeform card, cuts its branch and worktree, and
// leaves one real commit on it — the state a card is in after a turn or two,
// and the only state from which landing means anything.
func freeformWithWork(t *testing.T, h *harness, num int) domain.Feature {
	t.Helper()
	ctx := context.Background()
	id, _ := domain.NewID(domain.KindFreeform, num)
	now := time.Now()
	f := domain.Feature{
		ID: id, Num: num, Kind: domain.KindFreeform, Title: "drop the leaked fd",
		Slug: "drop-the-leaked-fd", Stage: domain.StageOpen,
		Budget: domain.Budget{Envelope: 400}, BranchScheme: domain.BranchSchemeKind,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := h.store.CreateFeature(ctx, &f); err != nil {
		t.Fatal(err)
	}
	tree, err := h.wt.Ensure(ctx, &f)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, "sketch.txt"), []byte("hand-rolled\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := h.wt.CommitAll(ctx, &f, string(f.ID)+": turn checkpoint"); err != nil {
		t.Fatal(err)
	}
	return f
}

// TestAFreeformCardLandsWithoutVerifying: the second landing floor, end to
// end. There is no verify stamp and no stage to be at, and the branch still
// reaches main through the same squash merge every card lands by — which is
// the whole of what "a freeform card can land" had to mean.
func TestAFreeformCardLandsWithoutVerifying(t *testing.T) {
	h := newHarness(t, true, nil)
	f := freeformWithWork(t, h, 21)
	before := gitHead(t, h.root)

	out, err := h.driver(Options{}).Merge(context.Background(), f.ID,
		"fix(copilot): drop the pty fd leaked on idle timeout")
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if out.Status != StatusVerified {
		t.Fatalf("status = %q, want done", out.Status)
	}
	if got := gitHead(t, h.root); got == before {
		t.Fatal("main HEAD did not move")
	}
	// It closes like any other card, through the one method that may take a
	// freeform card out of StageOpen.
	stored, err := h.store.GetFeature(context.Background(), f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Stage != domain.StageDone {
		t.Errorf("stage after landing = %q, want done", stored.Stage)
	}
	merged := lastEvent(h, "merged")
	if merged == nil {
		t.Fatalf("no merged event; got %v", h.eventKinds())
	}
	if merged["branch"] != f.BranchName() {
		t.Errorf("merged.branch = %v, want %s", merged["branch"], f.BranchName())
	}
}

// TestUnresolvedCommentsHoldAFreeformLanding: the diff surface IS this
// card's floor. A freeform card has no verify and no gate, so its own
// unresolved comments are the one thing between it and main — and they hold
// it through exactly the check every other landing crosses.
func TestUnresolvedCommentsHoldAFreeformLanding(t *testing.T) {
	h := newHarness(t, true, nil)
	f := freeformWithWork(t, h, 22)
	ctx := context.Background()
	if _, err := h.store.AddDiffAnnotation(ctx, domain.DiffAnnotation{
		Feature: f.ID, File: "sketch.txt", Anchor: "abc123",
		Excerpt: "hand-rolled", Comment: "this leaks on the error path too",
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	before := gitHead(t, h.root)

	out, err := h.driver(Options{}).Merge(ctx, f.ID, "fix(copilot): drop the leaked fd")
	if err == nil {
		t.Fatal("a freeform card landed with an unresolved diff comment")
	}
	if out.Status != StatusError {
		t.Errorf("status = %q, want error", out.Status)
	}
	if !strings.Contains(err.Error(), "diff annotation") {
		t.Errorf("the refusal does not name the comments: %v", err)
	}
	if got := gitHead(t, h.root); got != before {
		t.Errorf("main HEAD moved by a refused merge: %s -> %s", before, got)
	}
}

// TestRunRefusesAFreeformCard: `run`/`resume` drive stages, and a freeform
// card has none. The refusal has to be explicit, because StageOpen is
// terminal — so without it the loop would report a card that is very much
// open as done.
func TestRunRefusesAFreeformCard(t *testing.T) {
	h := newHarness(t, true, nil)
	f := freeformWithWork(t, h, 23)

	out, err := h.driver(Options{}).Drive(context.Background(), f)
	if err == nil {
		t.Fatal("the driver drove a freeform card")
	}
	if out.Status != StatusError {
		t.Errorf("status = %q, want error", out.Status)
	}
	if !strings.Contains(err.Error(), "freeform") {
		t.Errorf("the refusal does not say why: %v", err)
	}
	if lastEvent(h, "done") != nil {
		t.Error("the driver reported a freeform card done")
	}
	stored, _ := h.store.GetFeature(context.Background(), f.ID)
	if stored.Stage != domain.StageOpen {
		t.Errorf("stage = %q, want it left open", stored.Stage)
	}
}

// TestAFreeformHandOffClosesTheCardAndKeepsItsBranch: the ending that lands
// nothing. It crosses no gate, because there is none — and it still leaves
// the branch exactly where it was, which is the whole point of the verb.
func TestAFreeformHandOffClosesTheCardAndKeepsItsBranch(t *testing.T) {
	h := newHarness(t, true, nil)
	f := freeformWithWork(t, h, 24)
	ctx := context.Background()
	before := gitHead(t, h.root)

	out, err := h.driver(Options{}).HandOff(ctx, f.ID)
	if err != nil {
		t.Fatalf("HandOff: %v", err)
	}
	if out.Status != StatusVerified {
		t.Fatalf("status = %q, want done", out.Status)
	}
	stored, err := h.store.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Stage != domain.StageDone {
		t.Errorf("stage = %q, want done", stored.Stage)
	}
	if stored.HandedOffAt.IsZero() {
		t.Error("no hand-off stamp: nothing would badge how this card ended")
	}
	if got := gitHead(t, h.root); got != before {
		t.Errorf("a hand-off moved main: %s -> %s", before, got)
	}
	// The branch is still there — kept on purpose, which is what a hand-off
	// means and why cleanup refuses one.
	if err := exec.CommandContext(ctx, "git", "-C", h.root,
		"rev-parse", "--verify", "--quiet", "refs/heads/"+f.BranchName()).Run(); err != nil {
		t.Errorf("the handed-off branch %s is gone: %v", f.BranchName(), err)
	}
}

// TestASessionContinuedAsASpecIsNotLanded: `gummi merge` lands a handed-off
// card after all — except a session whose work went on as a spec, which
// lands it on a verified branch. Landing the session too would put the same
// work on main a second way, past the floor it was sent to.
func TestASessionContinuedAsASpecIsNotLanded(t *testing.T) {
	h := newHarness(t, true, nil)
	f := freeformWithWork(t, h, 25)
	ctx := context.Background()
	if _, err := h.driver(Options{}).HandOff(ctx, f.ID); err != nil {
		t.Fatalf("HandOff: %v", err)
	}
	if err := h.store.SetContinuedAs(ctx, f.ID, "FD-026"); err != nil {
		t.Fatal(err)
	}
	before := gitHead(t, h.root)

	out, err := h.driver(Options{}).Merge(ctx, f.ID, "fix(copilot): drop the leaked fd")
	if err == nil {
		t.Fatal("a session continued as a spec landed on its own")
	}
	if out.Status != StatusError {
		t.Errorf("status = %q, want error", out.Status)
	}
	if !strings.Contains(err.Error(), "continues as FD-026") {
		t.Errorf("the refusal does not name the spec that lands the work: %v", err)
	}
	if got := gitHead(t, h.root); got != before {
		t.Errorf("main HEAD moved by a refused merge: %s -> %s", before, got)
	}
}
