package main

import (
	"os"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// BG-006: one .gummi/state/gummi.pid per workspace, shared by every headless
// drive, meant a second card's drive overwrote the first's pid entry, and
// whichever card's drive exited first unconditionally deleted the file —
// even if a different, still-running card's drive had last written it.
// buildStatus (and the driver's exhaustion/timeout resume-precondition
// probe) read that same file for every card's liveness, so a still-running
// card could read running=false the moment an unrelated concurrent card
// exited.
//
// The fix scopes the pid file per card (Workspace.PIDFile(id), mirroring
// CardLockFile), and layers a compare-and-clear guard onto ClearPIDFile as
// defense in depth. This test exercises the exact call sites the bug
// report identified — WritePIDFile/ClearPIDFile against buildStatus — with
// two independent cards racing the way concurrent per-card drives do.
func TestBG006ConcurrentDrivesDoNotClobberEachOthersLiveness(t *testing.T) {
	f := newReadFixture(t)
	cardA := f.mkFeature(t, "")
	cardB, err := domain.NewFeatureID(2)
	if err != nil {
		t.Fatal(err)
	}

	// cardA's drive starts and records its pid at its own path.
	if err := state.WritePIDFile(f.ws.PIDFile(cardA.ID), os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if v := buildStatus(f.ctx, f.store, f.wt, f.ws, &cardA); !v.Running {
		t.Fatal("sanity: cardA should read running right after it records its pid")
	}

	// cardB's drive starts and exits concurrently — its own pidfile, not
	// cardA's, so writing and clearing it must not touch cardA's entry.
	if err := state.WritePIDFile(f.ws.PIDFile(cardB), os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if err := state.ClearPIDFile(f.ws.PIDFile(cardB), os.Getpid()); err != nil {
		t.Fatal(err)
	}

	v := buildStatus(f.ctx, f.store, f.wt, f.ws, &cardA)
	if !v.Running {
		t.Fatal("BG-006: cardA reads running=false after cardB exited, though cardA's drive never exited")
	}
}
