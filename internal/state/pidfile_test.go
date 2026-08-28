package state

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
)

// Write, then Read the same value back — the round-trip a status probe
// makes to answer "is a run still governing this workspace?".
func TestWriteAndReadPIDFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".gummi", "state", "gummi.pid")

	if err := WritePIDFile(path, 12345); err != nil {
		t.Fatalf("WritePIDFile: %v", err)
	}
	if got := ReadPIDFile(path); got != 12345 {
		t.Fatalf("ReadPIDFile = %d, want 12345", got)
	}
}

// Clear removes the file so a subsequent read returns the "no run recorded"
// zero — the clean-exit signal a caller relies on to detect the run is
// really gone (rather than merely orphaned past its wrapper).
func TestClearPIDFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gummi.pid")

	if err := WritePIDFile(path, 42); err != nil {
		t.Fatalf("WritePIDFile: %v", err)
	}
	if err := ClearPIDFile(path, 42); err != nil {
		t.Fatalf("ClearPIDFile: %v", err)
	}
	if got := ReadPIDFile(path); got != 0 {
		t.Fatalf("ReadPIDFile after clear = %d, want 0", got)
	}
	// A second clear on the missing file is a no-op, not an error, so a
	// double-defer or a crash-then-clean-exit sequence doesn't spew errors.
	if err := ClearPIDFile(path, 42); err != nil {
		t.Fatalf("ClearPIDFile on absent file: %v", err)
	}
}

// Compare-and-clear: ClearPIDFile only removes an entry that still names the
// caller's own pid. This is the defense-in-depth half of BG-006 — even
// though each card now gets its own path, a stale clear (e.g. an old run
// that crashed and was restarted under the same card id) must not delete a
// fresher run's entry recorded at the same path.
func TestClearPIDFileLeavesMismatchedPidAlone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gummi.pid")

	if err := WritePIDFile(path, 42); err != nil {
		t.Fatalf("WritePIDFile: %v", err)
	}
	// A newer run recorded its own pid at the same path (e.g. after a
	// crash-and-restart under the same card id).
	if err := WritePIDFile(path, 99); err != nil {
		t.Fatalf("WritePIDFile: %v", err)
	}
	// The older run's deferred clear fires with its own (stale) pid — it
	// must not remove the newer run's entry.
	if err := ClearPIDFile(path, 42); err != nil {
		t.Fatalf("ClearPIDFile: %v", err)
	}
	if got := ReadPIDFile(path); got != 99 {
		t.Fatalf("ReadPIDFile after mismatched clear = %d, want 99 (newer entry preserved)", got)
	}
}

// A file with garbage in it reads as 0 (no run) rather than erroring —
// status probes are best-effort by design, so an unreadable pid is
// indistinguishable from a missing one for the caller's purposes.
func TestReadPIDFileGarbage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gummi.pid")
	if err := os.WriteFile(path, []byte("not-a-number\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ReadPIDFile(path); got != 0 {
		t.Fatalf("ReadPIDFile(garbage) = %d, want 0", got)
	}
}

// The current process is always alive, so it's the safest positive-case
// probe. A pid we know can never exist (a very large number) reads as
// dead.
func TestProcessAlive(t *testing.T) {
	if !ProcessAlive(os.Getpid()) {
		t.Fatalf("ProcessAlive(self) = false, want true")
	}
	if ProcessAlive(0) {
		t.Fatalf("ProcessAlive(0) = true, want false (sentinel for no-pid)")
	}
	// A pid past the kernel's max is guaranteed non-existent.
	if ProcessAlive(1 << 30) {
		t.Fatalf("ProcessAlive(1<<30) = true, want false")
	}
}

// PIDFile / EventsFile live inside the state dir, so the gitignore rules
// on state/ keep them out of commits (transcripts share the same treatment).
// PIDFile is scoped per card, mirroring CardLockFile, so two cards never
// share a path.
func TestPIDAndEventsFilePaths(t *testing.T) {
	w := Workspace{Root: "/repo"}
	id, err := domain.NewFeatureID(7)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := w.PIDFile(id), "/repo/.gummi/state/locks/FD-007.pid"; got != want {
		t.Fatalf("PIDFile = %q, want %q", got, want)
	}
	if got, want := w.EventsFile(), "/repo/.gummi/state/events.jsonl"; got != want {
		t.Fatalf("EventsFile = %q, want %q", got, want)
	}
}

// BG-002: a `kill -9` of the driver runs no Go code, so nothing ever signals
// the group an agent session was spawned into — it (and any same-group
// child it spawned, e.g. claude's own `gummi __mcp` tool child) is simply
// reparented and keeps running. This reproduces that exact shape without
// gummi or claude: a stub process group standing in for the agent, recorded
// at AgentPGIDFile exactly as Engine.trackAgentPID would, and asserts
// ReapOrphanAgent — the check a subsequent run/resume/clean runs once it has
// (re-)acquired the card's lock — kills the whole group and clears the
// record.
func TestReapOrphanAgentKillsOrphanedGroup(t *testing.T) {
	ws := Workspace{Root: t.TempDir()}
	id, err := domain.NewFeatureID(2)
	if err != nil {
		t.Fatal(err)
	}

	pidDir := t.TempDir()
	// Mirrors internal/agent/claudecode.go's NewSession spawn exactly:
	// Setpgid true, and the child spawns its own same-group grandchild.
	script := fmt.Sprintf(`
		echo $$ > %[1]s/child.pid
		sh -c 'echo $$ > %[1]s/grandchild.pid; sleep 30' &
		sleep 30
	`, pidDir)
	cmd := exec.CommandContext(context.Background(), "sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting stub process group: %v", err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_, _ = cmd.Process.Wait()
	})

	childPath := filepath.Join(pidDir, "child.pid")
	grandchildPath := filepath.Join(pidDir, "grandchild.pid")
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, errChild := os.Stat(childPath)
		_, errGrandchild := os.Stat(grandchildPath)
		if errChild == nil && errGrandchild == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stub process group never wrote its pid files")
		}
		time.Sleep(10 * time.Millisecond)
	}
	grandchildPID := ReadPIDFile(grandchildPath)
	if grandchildPID == 0 {
		t.Fatalf("grandchild pid file did not contain a valid pid")
	}

	// This is what Engine.trackAgentPID records at session start: the
	// spawned agent's own pid, which — because it was started with
	// Setpgid: true and no explicit Pgid — is also its whole process
	// group's pgid (agent.OSProcess's contract).
	if err := WritePIDFile(ws.AgentPGIDFile(id), cmd.Process.Pid); err != nil {
		t.Fatalf("WritePIDFile: %v", err)
	}

	ReapOrphanAgent(ws, id)
	// The test is cmd's real parent, so a killed leader sits as a zombie —
	// still "alive" to a bare kill(pid, 0) probe — until reaped; production
	// callers are never the agent's parent (they're a later, unrelated
	// gummi invocation), so this Wait is purely to make the probe below
	// meaningful here.
	_, _ = cmd.Process.Wait()

	if ProcessAlive(cmd.Process.Pid) {
		t.Fatalf("BUG: agent %d survived ReapOrphanAgent", cmd.Process.Pid)
	}
	// SIGKILL delivery and teardown of an orphaned grandchild (reparented to
	// init/subreaper once the leader dies) isn't synchronous with the
	// signal call, so give it a moment before failing.
	deadline = time.Now().Add(2 * time.Second)
	for ProcessAlive(grandchildPID) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if ProcessAlive(grandchildPID) {
		t.Fatalf("BUG: agent's own child %d survived ReapOrphanAgent", grandchildPID)
	}
	if got := ReadPIDFile(ws.AgentPGIDFile(id)); got != 0 {
		t.Fatalf("AgentPGIDFile after reap = %d, want cleared", got)
	}
}

// A pid written by WritePIDFile is round-trippable by ReadPIDFile, and
// the recorded value is what actually ended up in the file (a manual read
// as extra insurance against a formatting regression).
func TestPIDFileContentsAreDecimal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gummi.pid")
	if err := WritePIDFile(path, 987); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(string(raw[:len(raw)-1])) // strip trailing newline
	if err != nil || n != 987 {
		t.Fatalf("pid file bytes = %q (parsed %d), want decimal 987", raw, n)
	}
}
