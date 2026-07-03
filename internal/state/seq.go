package state

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	seqLockRetries = 50
	seqLockDelay   = 10 * time.Millisecond
)

// NextSeq increments the monotonic FD counter stored in seqFile and
// returns the new value. Concurrency is handled with a lock file and
// bounded retry (DESIGN §10 decision 2: retry-on-conflict); if the
// lock cannot be acquired the caller gets a deterministic error naming
// the lock path — gummi never guesses a free number.
func NextSeq(seqFile string) (int, error) {
	lock := seqFile + ".lock"
	acquired := false
	for range seqLockRetries {
		f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_ = f.Close()
			acquired = true
			break
		}
		if !errors.Is(err, fs.ErrExist) {
			return 0, fmt.Errorf("acquiring seq lock: %w", err)
		}
		time.Sleep(seqLockDelay)
	}
	if !acquired {
		return 0, fmt.Errorf("seq counter is locked by %s; if no other gummi is running, remove the stale lock file", lock)
	}
	defer os.Remove(lock)

	raw, err := os.ReadFile(seqFile)
	if err != nil {
		return 0, fmt.Errorf("reading seq counter: %w", err)
	}
	cur, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || cur < 0 {
		return 0, fmt.Errorf("seq counter %s is corrupt (%q); fix it by writing the highest FD number in use", seqFile, strings.TrimSpace(string(raw)))
	}
	next := cur + 1
	tmp := seqFile + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(next)+"\n"), 0o600); err != nil {
		return 0, fmt.Errorf("writing seq counter: %w", err)
	}
	if err := os.Rename(tmp, seqFile); err != nil {
		return 0, fmt.Errorf("committing seq counter: %w", err)
	}
	return next, nil
}
