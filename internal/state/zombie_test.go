package state

import (
	"os/exec"
	"testing"
	"time"
)

// TestProcessAliveRejectsZombie pins the liveness probe against the shape
// that made a dead gummi look like a running one: a child that has exited
// but has not been reaped. kill -0 succeeds on it, so the bare probe said
// "alive" and ForeignDriver reported a card as driven by a corpse.
func TestProcessAliveRejectsZombie(t *testing.T) {
	cmd := exec.Command("/bin/true")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	// deliberately no cmd.Wait(): the child stays <defunct> while this
	// test process remains its unreaping parent.
	t.Cleanup(func() { _ = cmd.Wait() })

	deadline := time.Now().Add(5 * time.Second)
	for !processIsZombie(pid) {
		if time.Now().After(deadline) {
			t.Skip("child never became a zombie (no procfs?); nothing to assert")
		}
		time.Sleep(5 * time.Millisecond)
	}

	if ProcessAlive(pid) {
		t.Errorf("ProcessAlive(%d) = true for a <defunct> process; "+
			"a reaped-pending corpse is not a live driver", pid)
	}
}

// TestProcessAliveAcceptsSelf guards the narrowing: a genuinely running
// process must still read as alive.
func TestProcessAliveAcceptsSelf(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	if !ProcessAlive(cmd.Process.Pid) {
		t.Errorf("ProcessAlive(%d) = false for a running process", cmd.Process.Pid)
	}
}
