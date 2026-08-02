package state

import (
	"errors"
	"path/filepath"
	"testing"
)

// A second AcquireLock on the same path fails with ErrLocked; releasing
// the first lets the next acquire succeed.
func TestAcquireLockExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instance.lock")

	release, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	if _, err := AcquireLock(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("second acquire err = %v, want ErrLocked", err)
	}

	release()

	release2, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	release2()
}

// LockFile lives under the gitignored state/ dir so it never gets
// committed.
func TestLockFileUnderState(t *testing.T) {
	w := Workspace{Root: "/repo"}
	if got, want := w.LockFile(), "/repo/.gummi/state/instance.lock"; got != want {
		t.Fatalf("LockFile = %q, want %q", got, want)
	}
}
