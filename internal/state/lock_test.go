package state

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/morphis/gummi/internal/domain"
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

// CardLockFile is keyed to a work item: two disjoint cards resolve to
// disjoint lock paths, so they drive concurrently (each acquires its own
// lock), while re-acquiring the same card's lock is refused.
func TestCardLockFileIndependentAndExclusive(t *testing.T) {
	w := Workspace{Root: t.TempDir()}
	a, b := domain.FeatureID("FD-001"), domain.FeatureID("FD-002")
	lockA, lockB := w.CardLockFile(a), w.CardLockFile(b)
	if lockA == lockB {
		t.Fatalf("disjoint cards share a lock path %q", lockA)
	}

	releaseA, err := AcquireLock(lockA)
	if err != nil {
		t.Fatalf("acquire A: %v", err)
	}

	// B is independent: its lock is free even while A is held.
	releaseB, err := AcquireLock(lockB)
	if err != nil {
		t.Fatalf("acquire B while A held: %v (independent cards must drive concurrently)", err)
	}
	releaseB()

	// The same card is guarded: a second acquire of A is refused.
	if _, err := AcquireLock(lockA); !errors.Is(err, ErrLocked) {
		t.Fatalf("re-acquire A err = %v, want ErrLocked", err)
	}

	releaseA()

	releaseA2, err := AcquireLock(lockA)
	if err != nil {
		t.Fatalf("acquire A after release: %v", err)
	}
	releaseA2()
}
