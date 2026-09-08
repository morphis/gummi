package ui

import (
	"context"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
)

// TestCleanVerifyStampsVerifiedInTheTUI: a card driven to its verify gate
// in the TUI reports `verified` exactly as one driven by `gummi run`
// does.
//
// The marker used to be stamped only inside engine.Advance, on the branch
// that returns StatusNeedsMerge, and a stage completion in the TUI never
// passes through that code — so two cards in identical states answered
// `gummi status --json … .verified` differently depending only on which
// surface had driven them, and a script polling it to open a PR never
// fired for the one a human drove.
func TestCleanVerifyStampsVerifiedInTheTUI(t *testing.T) {
	m := runVerify(t, "All checks green.\nVERDICT: pass")
	f, err := m.store.GetFeature(context.Background(), "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	if f.VerifiedAt.IsZero() {
		t.Fatal("a clean verify in the TUI left VerifiedAt zero")
	}
	if f.Stage != domain.StageVerify {
		t.Errorf("stamping moved the stage to %s; verified is a marker, not a transition", f.Stage)
	}
}

// TestFailedVerifyLeavesVerifiedUnstamped: only the clean-pass arm
// stamps. An escalation is the loop saying verification did not hold up,
// and a `verified` marker there would be the same lie in the other
// direction.
func TestFailedVerifyLeavesVerifiedUnstamped(t *testing.T) {
	for _, reply := range []string{
		"Build broken.\nVERDICT: fail",
		"I had a look around.", // no verdict at all: the shrug arm
	} {
		m := runVerify(t, reply)
		f, err := m.store.GetFeature(context.Background(), "FD-001")
		if err != nil {
			t.Fatal(err)
		}
		if !f.VerifiedAt.IsZero() {
			t.Errorf("verify reply %q stamped VerifiedAt %s", reply, f.VerifiedAt)
		}
	}
}

// TestVerifiedStampKeepsTheFirstPassTime mirrors engine.Advance's own
// idempotency: re-reaching a gate that is already stamped must not slide
// the timestamp forward, or "when did this become ready to land" answers
// "just now" for a branch that has been ready for a week.
func TestVerifiedStampKeepsTheFirstPassTime(t *testing.T) {
	m := runVerify(t, "All checks green.\nVERDICT: pass")
	ctx := context.Background()

	first := time.Date(2020, 3, 4, 5, 6, 7, 0, time.UTC)
	if err := m.store.SetVerifiedAt(ctx, "FD-001", first); err != nil {
		t.Fatal(err)
	}
	m.now = func() time.Time { return time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC) }
	m = pump(t, m, m.markVerified("FD-001"))

	f, err := m.store.GetFeature(ctx, "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	if !f.VerifiedAt.Equal(first) {
		t.Errorf("VerifiedAt = %s, want the first pass's %s kept", f.VerifiedAt, first)
	}
}

// TestResearchCardIsNotStampedVerified: a research card reaches the same
// clean-pass arm but carries no branch, and engine.Advance stamps only
// where a branch exists and is ahead of main — so headless never marks
// one verified. Stamping here would close the feature/bug divergence and
// open the identical one for research.
func TestResearchCardIsNotStampedVerified(t *testing.T) {
	m, _ := newWorkspace(t)
	m.now = func() time.Time { return fixedTime }
	if cmd := m.markVerified("RS-007"); cmd != nil {
		t.Error("markVerified returned a store write for a research card")
	}
	if cmd := m.markVerified("BG-007"); cmd == nil {
		t.Error("markVerified skipped a bug card, which does carry a branch")
	}
}
