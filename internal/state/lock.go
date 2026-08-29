package state

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/morphis/gummi/internal/domain"
)

// ErrLocked is returned by AcquireLock when another process already holds
// the lock — for a per-card lock, another drive of the same card: a
// concurrent run/resume/merge/clean, or an open board driving it (the
// board takes each card's lock too, via CardLocks).
//
// The wording stays symmetric on purpose: either side can be the one
// refused, so it names the situation rather than assuming the reader is
// the headless caller.
var ErrLocked = errors.New("another gummi process is already driving this card — wait for it to finish, or stop it")

// LockFile is the workspace's exclusive-instance lock path, taken by the
// TUI for its whole lifetime so a second interactive board refuses to open
// while one is already up. It is deliberately NOT taken by headless
// run/resume/verify/merge/clean — those hold a per-card lock instead
// (CardLockFile), so independent cards drive concurrently.
// It lives under the gitignored state/ dir, so it is never committed and
// never leaves the machine.
func (w Workspace) LockFile() string { return filepath.Join(w.StateDir(), "instance.lock") }

// CardLockFile is the per-card exclusive lock path for one work item. Two
// disjoint cards take disjoint locks and drive concurrently; two drives of
// the same card at once are refused with ErrLocked — two resumes, a resume
// and a merge, or a headless drive racing an open board, which holds this
// same lock for every card it drives (state.CardLocks). It lives under the gitignored
// state/ dir, so it
// is never committed and never leaves the machine.
func (w Workspace) CardLockFile(id domain.FeatureID) string {
	return filepath.Join(w.StateDir(), "locks", fmt.Sprintf("%s.lock", id))
}
