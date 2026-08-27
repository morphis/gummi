package state

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

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
