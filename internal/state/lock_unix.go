//go:build unix

package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// AcquireLock takes an exclusive, non-blocking flock on path so at most
// one gummi process holds the given lock at a time. It backs both the
// TUI's whole-workspace lock and the per-card locks that headless
// run/resume/verify/merge/clean take for a drive, so disjoint cards lock
// concurrently while two headless drives of the same card are mutually
// excluded. It returns ErrLocked when the lock is already held, and a
// release func
// that unlocks and closes the file.
// The advisory lock is tied to the open file, so a crash releases it too
// — a stale lock file never wedges the next run.
func AcquireLock(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("preparing lock dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("locking workspace: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
