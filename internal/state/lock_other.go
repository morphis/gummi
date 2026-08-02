//go:build !unix

package state

import "errors"

// AcquireLock fails loud on platforms without flock: gummi never runs
// without the exclusive-instance guard (deterministic failure over silent
// degradation). A unix build (the supported target) uses the real lock in
// lock_unix.go.
func AcquireLock(path string) (func(), error) {
	return nil, errors.New("workspace locking is not supported on this platform")
}
