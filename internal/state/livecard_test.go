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
