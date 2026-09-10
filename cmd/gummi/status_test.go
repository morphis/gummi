package main

import (
	"testing"

	"github.com/morphis/gummi/internal/state"
)

// TestBuildStatusRunningReflectsCardLock is the TUI-board half of the
// FD-001 report: a card driven by an open board never writes the pid file
// (only headless run/resume does — see trackPID in run.go), so before this
// fix status always read running=false for it, even mid-turn or paused on
// an open ask. The board (and every other drive) holds the card's own
// exclusive lock for as long as it drives the card — state.CardLocks — so a
// held lock with no pid file is exactly that situation, and an
// uncontended lock is the honest "no" this process can actually stand
// behind.
func TestBuildStatusRunningReflectsCardLock(t *testing.T) {
	f := newReadFixture(t)
	feat := f.mkFeature(t, "")

	// no pid file, lock free: nothing is driving this card.
	if v := buildStatus(f.ctx, f.store, f.wt, f.ws, &feat); v.Running {
		t.Fatal("running=true with no pid file and an uncontended card lock")
	}

	// simulate another gummi process (a TUI board, or a headless drive
	// whose pid file write raced this read) holding the card's lock: a
	// second, independent open of the same lock file contends exactly as
	// a foreign process would (flock is per open-file-description, not
	// per-process).
	release, err := state.AcquireLock(f.ws.CardLockFile(feat.ID))
	if err != nil {
		t.Fatalf("acquiring the card lock to simulate a foreign holder: %v", err)
	}
	defer release()

	if v := buildStatus(f.ctx, f.store, f.wt, f.ws, &feat); !v.Running {
		t.Fatal("FD-001: running=false while another process holds the card's lock")
	}

	release()
	if v := buildStatus(f.ctx, f.store, f.wt, f.ws, &feat); v.Running {
		t.Fatal("running=true after the foreign lock holder released")
	}
}
