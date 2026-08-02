package state

import (
	"errors"
	"path/filepath"
)

// ErrLocked is returned by AcquireLock when another process already holds
// the workspace's exclusive lock — the TUI or another headless run
// (DESIGN §8.2 D13: one process touches a .gummi workspace at a time).
var ErrLocked = errors.New("another gummi process holds this workspace (close the TUI or the other run first)")

// LockFile is the workspace's exclusive-instance lock path. It lives
// under the gitignored state/ dir, so it is never committed and never
// leaves the machine.
func (w Workspace) LockFile() string { return filepath.Join(w.StateDir(), "instance.lock") }
