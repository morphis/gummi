package state_test

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/livelog"
	"github.com/morphis/gummi/internal/state"
)

// TestForeignDriver_StageAdvanceGap pins BG-028: a stage advance stops the
// old session — writing the live file's terminal record — before the
// successor's bindLiveLog truncates the file and writes its own header.
// A probe landing in that gap must still read the card as foreign-driven,
// since the owning pid is alive and mid-advance, not gone.
func TestForeignDriver_StageAdvanceGap(t *testing.T) {
	dir := t.TempDir()
	ws := state.Workspace{Root: dir, RepoRoot: dir}

	owner := exec.CommandContext(context.Background(), "sleep", "30") // stand-in for the real owning process: alive, not us
	if err := owner.Start(); err != nil {
		t.Fatalf("start stand-in owner: %v", err)
	}
	defer owner.Process.Kill()

	w, err := livelog.Create(ws.LiveFile("FD-028"), livelog.Record{
		Feature: "FD-028", Stage: "plan", PID: owner.Process.Pid,
	})
	if err != nil {
		t.Fatalf("create live file: %v", err)
	}
	// Exactly what Session.stop() writes (engine/session.go), which every
	// stage advance does to the old session before bindLiveLog creates the
	// successor's file (engine.go).
	w.Emit(livelog.Record{Kind: livelog.KindStopped})
	w.Close()

	drive, ok := state.ForeignDriver(ws, domain.FeatureID("FD-028"))
	if !ok {
		t.Fatalf("ForeignDriver = (%+v, false), want ok=true: the owning pid %d is alive and mid stage-advance, not gone", drive, owner.Process.Pid)
	}
	if drive.PID != owner.Process.Pid {
		t.Errorf("drive.PID = %d, want %d", drive.PID, owner.Process.Pid)
	}
}

// TestForeignDriver_StoppedPastGrace confirms a stopped file well past the
// stage-advance window reads as free even though its pid is still alive —
// the pid just belongs to a process doing something else now.
func TestForeignDriver_StoppedPastGrace(t *testing.T) {
	dir := t.TempDir()
	ws := state.Workspace{Root: dir, RepoRoot: dir}

	owner := exec.CommandContext(context.Background(), "sleep", "30")
	if err := owner.Start(); err != nil {
		t.Fatalf("start stand-in owner: %v", err)
	}
	defer owner.Process.Kill()

	path := ws.LiveFile("FD-028")
	w, err := livelog.Create(path, livelog.Record{
		Feature: "FD-028", Stage: "plan", PID: owner.Process.Pid,
	})
	if err != nil {
		t.Fatalf("create live file: %v", err)
	}
	w.Emit(livelog.Record{Kind: livelog.KindStopped})
	w.Close()

	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("backdate live file: %v", err)
	}

	if drive, ok := state.ForeignDriver(ws, domain.FeatureID("FD-028")); ok {
		t.Fatalf("ForeignDriver = (%+v, true), want ok=false: the session ended an hour ago, well past the stage-advance window", drive)
	}
}

// TestForeignDriver_Busy pins FD-031: the tail scan's busy signal rides
// straight through ForeignDriver, and a stopped drive never reports one
// — a foreign row's busy marker must never fire past the point the drive
// itself says it is done.
func TestForeignDriver_Busy(t *testing.T) {
	dir := t.TempDir()
	ws := state.Workspace{Root: dir, RepoRoot: dir}

	owner := exec.CommandContext(context.Background(), "sleep", "30")
	if err := owner.Start(); err != nil {
		t.Fatalf("start stand-in owner: %v", err)
	}
	defer owner.Process.Kill()

	path := ws.LiveFile("FD-031")
	w, err := livelog.Create(path, livelog.Record{
		Feature: "FD-031", Stage: "implement", PID: owner.Process.Pid,
	})
	if err != nil {
		t.Fatalf("create live file: %v", err)
	}
	w.Emit(livelog.Record{Kind: livelog.KindBusy, Busy: true})
	w.Close()

	drive, ok := state.ForeignDriver(ws, domain.FeatureID("FD-031"))
	if !ok {
		t.Fatalf("ForeignDriver = (%+v, false), want ok=true", drive)
	}
	if !drive.Busy {
		t.Errorf("drive.Busy = false, want true: the live file's last record is a busy one")
	}

	w2, err := livelog.Create(path, livelog.Record{
		Feature: "FD-031", Stage: "implement", PID: owner.Process.Pid,
	})
	if err != nil {
		t.Fatalf("re-create live file: %v", err)
	}
	w2.Emit(livelog.Record{Kind: livelog.KindBusy, Busy: true})
	w2.Emit(livelog.Record{Kind: livelog.KindStopped})
	w2.Close()

	// Still inside the stage-advance grace window: ForeignDriver reports
	// the drive as live (the pid may be about to keep driving the card),
	// but the terminal record must dominate the busy one that preceded it.
	if drive, ok := state.ForeignDriver(ws, domain.FeatureID("FD-031")); !ok || drive.Busy {
		t.Fatalf("ForeignDriver = (%+v, %v), want ok=true, Busy=false: the stopped record must dominate the busy one before it", drive, ok)
	}

	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("backdate live file: %v", err)
	}
	if drive, ok := state.ForeignDriver(ws, domain.FeatureID("FD-031")); ok {
		t.Fatalf("ForeignDriver = (%+v, true), want ok=false: the session stopped well past the stage-advance window", drive)
	}
}
