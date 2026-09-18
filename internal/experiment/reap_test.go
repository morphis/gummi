package experiment

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/substrate"
)

func running(pid int) bool { return syscall.Kill(pid, 0) == nil }

// TestARunWhoseRunnerDiedTakesItsPhaseWithIt: the parent-death signal
// reaches a phase's shell and nothing under it, so a runner killed outright
// leaves a process tree still working the substrate the next holder is
// about to take. Reading the run back is what notices the runner is gone,
// and it is where what the run left behind is killed.
func TestARunWhoseRunnerDiedTakesItsPhaseWithIt(t *testing.T) {
	if _, ok := substrate.ProcessStart(os.Getpid()); !ok {
		t.Skip("this platform cannot identify a process beyond its pid")
	}
	dir := t.TempDir()

	// A phase's tree: the shell the parent-death signal can reach, and
	// under it the process that is actually working the substrate — which
	// it cannot. The grandchild is what this is about.
	phase := exec.Command("sh", "-c", "sleep 60 & echo $! && sleep 60")
	phase.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := phase.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout: %v", err)
	}
	if err := phase.Start(); err != nil {
		t.Fatalf("starting the phase: %v", err)
	}
	go func() { _ = phase.Wait() }()
	pgid := phase.Process.Pid
	var grandchild int
	if _, err := fmt.Fscanln(out, &grandchild); err != nil {
		t.Fatalf("reading the grandchild's pid: %v", err)
	}
	start, ok := substrate.ProcessStart(pgid)
	if !ok {
		t.Fatalf("no start time for the phase's group leader")
	}
	defer func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) }()
	if !running(grandchild) {
		t.Fatalf("the grandchild %d was not running to begin with", grandchild)
	}

	// a runner that is gone
	dead := exec.Command("sh", "-c", "exit 0")
	if err := dead.Run(); err != nil {
		t.Fatalf("running the stand-in runner: %v", err)
	}
	deadPID := dead.Process.Pid
	if running(deadPID) {
		t.Skip("the stand-in runner's pid was reused")
	}

	res := Result{
		ID: "R1", Experiment: "x", State: StateRunning, PID: deadPID,
		Started: time.Now().UTC(), Heartbeat: time.Now().UTC(),
		PhaseGroup: pgid, PhaseGroupStart: start,
	}
	raw, _ := json.MarshalIndent(res, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, resultFile), raw, 0o600); err != nil {
		t.Fatalf("writing the run record: %v", err)
	}

	got, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Outcome != Inconclusive {
		t.Errorf("a run nobody finished judged nothing, got %s", got.Outcome)
	}
	for i := 0; i < 50 && running(grandchild); i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if running(grandchild) {
		t.Fatalf("the phase's command %d is still working the substrate after its run was read back as over", grandchild)
	}
	if got.PhaseGroup != 0 {
		t.Errorf("a group that has been killed is not still the run's: %d", got.PhaseGroup)
	}
}

// TestAPhaseGroupIsNotKilledAfterItsPidIsReused: the record names a group
// by its leader's start time as well as its pid, so a pid the kernel has
// handed to somebody else is left alone.
func TestAPhaseGroupIsNotKilledAfterItsPidIsReused(t *testing.T) {
	if _, ok := substrate.ProcessStart(os.Getpid()); !ok {
		t.Skip("this platform cannot identify a process beyond its pid")
	}
	other := exec.Command("sh", "-c", "sleep 60")
	other.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := other.Start(); err != nil {
		t.Fatalf("starting the innocent process: %v", err)
	}
	defer func() { _ = syscall.Kill(-other.Process.Pid, syscall.SIGKILL) }()
	start, _ := substrate.ProcessStart(other.Process.Pid)

	if substrate.KillGroup(other.Process.Pid, start+1) {
		t.Fatalf("a group whose leader started at another moment was killed")
	}
	if !running(other.Process.Pid) {
		t.Fatalf("the innocent process was killed")
	}
}
