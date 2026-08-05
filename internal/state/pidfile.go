package state

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/morphis/gummi/internal/atomicfile"
)

// WritePIDFile records pid at path atomically. The state dir must already
// exist (a live run has taken the workspace lock, which enforces that).
func WritePIDFile(path string, pid int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("preparing pid dir: %w", err)
	}
	data := []byte(strconv.Itoa(pid) + "\n")
	return atomicfile.Write(path, data, 0o600)
}

// ClearPIDFile removes path if present. A missing file is not an error —
// clean exit and crash both leave a caller free to notice the run is gone.
func ClearPIDFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// ReadPIDFile returns the pid recorded at path, or 0 if the file is
// missing/empty/unparseable — any read failure reads as "no live run
// recorded" so a caller can treat the absence as authoritative.
func ReadPIDFile(path string) int {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// ProcessAlive reports whether pid names a live process this uid can
// signal. kill -0 is the standard liveness probe: it delivers no signal
// but returns EPERM/ESRCH/OK based on process existence. A pid of 0 is
// treated as dead (matches ReadPIDFile's "no run recorded" sentinel).
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	// EPERM means the process exists but we can't signal it — still alive.
	return errors.Is(err, syscall.EPERM)
}
