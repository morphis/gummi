package state

import (
	"context"
	"errors"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

// A feature's fork-point SHA survives create → read, and the
// SetForkPoint side-channel stamps it in place once — the record the
// worktree manager uses to detect fork-point drift before diff-based
// stages.
func TestForkPointRoundtrip(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	f := feat(1, "Add a healthz endpoint")
	f.ForkPoint = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b"
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ForkPoint != f.ForkPoint {
		t.Fatalf("fork-point lost in roundtrip: %q, want %q", got.ForkPoint, f.ForkPoint)
	}

	// direct read via the side-channel.
	if got, err := s.ForkPoint(ctx, f.ID); err != nil || got != f.ForkPoint {
		t.Fatalf("ForkPoint = %q, %v; want %q", got, err, f.ForkPoint)
	}
}

// A feature created without a fork-point reads back "" — the sentinel that
// means "pre-existing worktree, needs lazy backfill on first access".
func TestForkPointDefaultsEmpty(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := feat(2, "Another feature")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	got, err := s.ForkPoint(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("default fork-point = %q, want empty", got)
	}
}

// SetForkPoint stamps an empty row, is a no-op-safe side-channel read, and
// refuses to overwrite a row that is already stamped — Create and the lazy
// backfill can never race a stored SHA into being replaced.
func TestSetForkPointStampedOnce(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := feat(3, "Drift guarded")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}

	const sha = "0123456789abcdef0123456789abcdef01234567"
	if err := s.SetForkPoint(ctx, f.ID, sha); err != nil {
		t.Fatalf("first stamp: %v", err)
	}
	if got, err := s.ForkPoint(ctx, f.ID); err != nil || got != sha {
		t.Fatalf("ForkPoint after stamp = %q, %v; want %q", got, err, sha)
	}

	// a second stamp with a different SHA is refused — the row stays put.
	other := domain.FeatureID("FD-003")
	const otherSHA = "ffffffffffffffffffffffffffffffffffffffff"
	if err := s.SetForkPoint(ctx, other, otherSHA); err == nil {
		t.Fatal("SetForkPoint stamped a wrong ID")
	}
	if err := s.SetForkPoint(ctx, f.ID, otherSHA); err == nil {
		t.Fatal("SetForkPoint overwrote a stamped value")
	} else if !errors.Is(err, ErrForkPointStamped) {
		t.Fatalf("refusal is not ErrForkPointStamped: %v", err)
	}
	if got, _ := s.ForkPoint(ctx, f.ID); got != sha {
		t.Fatalf("stamped value was clobbered: %q, want %q", got, sha)
	}
}

// ClearForkPoint resets the row to the empty sentinel — the step Remove and
// DeleteBranch take so a recreate re-anchors the fork — and is safe on a row
// that never had a fork recorded.
func TestClearForkPoint(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := feat(4, "Recreate")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}

	const sha = "0123456789abcdef0123456789abcdef01234567"
	if err := s.SetForkPoint(ctx, f.ID, sha); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearForkPoint(ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ForkPoint(ctx, f.ID); err != nil || got != "" {
		t.Fatalf("fork-point not cleared: %q, %v", got, err)
	}

	// re-stamping after a clear is allowed — the sentinel is back.
	if err := s.SetForkPoint(ctx, f.ID, sha); err != nil {
		t.Fatalf("re-stamp after clear: %v", err)
	}
}

// ReanchorForkPoint is the single explicit re-stamp: it overwrites a fork
// already stamped by SetForkPoint (so re-anchoring a drifted feature works),
// refuses an empty SHA, and leaves SetForkPoint's stamped-once rule intact.
func TestReanchorForkPoint(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := feat(5, "Reanchor")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}

	const first = "0123456789abcdef0123456789abcdef01234567"
	if err := s.SetForkPoint(ctx, f.ID, first); err != nil {
		t.Fatal(err)
	}

	// re-anchor overwrites unconditionally — the recovery write.
	const second = "9999999999999999999999999999999999999999"
	if err := s.ReanchorForkPoint(ctx, f.ID, second); err != nil {
		t.Fatalf("re-anchor: %v", err)
	}
	if got, err := s.ForkPoint(ctx, f.ID); err != nil || got != second {
		t.Fatalf("fork after re-anchor = %q, %v; want %q", got, err, second)
	}

	// an empty SHA is refused and leaves the recorded value untouched.
	if err := s.ReanchorForkPoint(ctx, f.ID, ""); err == nil {
		t.Fatal("re-anchor accepted an empty SHA")
	}
	if got, _ := s.ForkPoint(ctx, f.ID); got != second {
		t.Fatalf("empty re-anchor clobbered the fork: %q", got)
	}

	// SetForkPoint still refuses to overwrite — the stamped-once rule is
	// untouched by the new write.
	if err := s.SetForkPoint(ctx, f.ID, first); err == nil {
		t.Fatal("SetForkPoint overwrote a stamped value")
	} else if !errors.Is(err, ErrForkPointStamped) {
		t.Fatalf("SetForkPoint refusal is not ErrForkPointStamped: %v", err)
	}
	if got, _ := s.ForkPoint(ctx, f.ID); got != second {
		t.Fatalf("SetForkPoint clobbered the re-anchored fork: %q", got)
	}
}
