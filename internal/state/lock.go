package state

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/morphis/gummi/internal/domain"
)

// ErrLocked is returned by AcquireLock when another process already holds
// the lock — for a per-card lock, another headless drive of the same card
// (a concurrent resume/merge/clean for it).
var ErrLocked = errors.New("another gummi process is already driving this card (close the other run or the TUI first)")

// LockFile is the workspace's exclusive-instance lock path, taken by the
// TUI for its whole lifetime so a second interactive board refuses to open
// while one is already up. It is deliberately NOT taken by headless
// run/resume/verify/merge/clean — those hold a per-card lock instead
// (CardLockFile), so independent cards drive concurrently.
// It lives under the gitignored state/ dir, so it is never committed and
// never leaves the machine.
func (w Workspace) LockFile() string { return filepath.Join(w.StateDir(), "instance.lock") }

// CardLockFile is the per-card exclusive lock path for one work item. Two
// disjoint cards take disjoint locks and drive concurrently; two headless
// drives of the same card at once (e.g. two resumes, or a resume and a
// merge for it) are refused with ErrLocked. It lives under the gitignored
// state/ dir, so it
// is never committed and never leaves the machine.
func (w Workspace) CardLockFile(id domain.FeatureID) string {
	return filepath.Join(w.StateDir(), "locks", fmt.Sprintf("%s.lock", id))
}
