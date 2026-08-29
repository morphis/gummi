package livelog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"time"
)

// DefaultPoll is how often Follow checks a live file for growth. The
// writer coalesces at flushInterval, so polling faster only burns
// syscalls; polling much slower is what a reader would notice as lag.
const DefaultPoll = 100 * time.Millisecond

// readChunk bounds one read from the tailed file.
const readChunk = 32 << 10

// ErrNoLiveFile reports that a card has no live file — nothing has ever
// driven it in a process that writes one, or the workspace was cleaned.
// Callers that must fail loud (rather than wait for a run that may never
// start) check for it before following.
var ErrNoLiveFile = errors.New("no live file for this card")

// Exists reports whether path names a readable live file.
func Exists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}

// Follow tails the live file at path and delivers its records on the
// returned channel until ctx is canceled, at which point the channel
// closes. It handles the three things a naive tail gets wrong:
//
//   - the file may not exist yet (a watcher started before the run), so
//     Follow waits for it instead of failing;
//   - the last line may be half-written when the reader catches up, so a
//     partial line is buffered until its newline arrives;
//   - a new session truncates the file, so a shrink is reported as
//     KindReset and reading restarts from the top.
//
// The channel is unbuffered by design: a slow consumer applies
// backpressure to the tailer, never to the process being watched.
func Follow(ctx context.Context, path string, poll time.Duration) <-chan Record {
	if poll <= 0 {
		poll = DefaultPoll
	}
	out := make(chan Record)
	go func() {
		defer close(out)
		t := &tailer{path: path, poll: poll, out: out}
		t.run(ctx)
	}()
	return out
}

// tailer holds one Follow's position in the file it watches.
type tailer struct {
	path string
	poll time.Duration
	out  chan<- Record

	f       *os.File
	offset  int64
	partial []byte
}

func (t *tailer) run(ctx context.Context) {
	defer func() {
		if t.f != nil {
			_ = t.f.Close()
		}
	}()
	for {
		t.drain(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(t.poll):
		}
	}
}

// drain reads everything available, opening or reopening the file as
// needed. Every failure mode here is transient by nature (the file has
// not appeared yet, or was just replaced), so it returns quietly and the
// next tick retries rather than ending the follow.
func (t *tailer) drain(ctx context.Context) {
	if t.f == nil {
		f, err := os.Open(t.path)
		if err != nil {
			return // not there yet (or gone); retry next tick
		}
		t.f = f
		t.offset = 0
		t.partial = nil
	}
	// the path may now name a different file than our handle — the
	// workspace was cleaned and re-created underneath us. A stale handle
	// would then read EOF forever, so rebind to the path.
	if cur, err := os.Stat(t.path); err == nil {
		if held, herr := t.f.Stat(); herr == nil && !os.SameFile(cur, held) {
			t.reopen()
			return
		}
	}
	// a shrink means a new session truncated the file under us: what we
	// accumulated describes the previous session, so say so and restart.
	if fi, err := t.f.Stat(); err == nil && fi.Size() < t.offset {
		t.offset = 0
		t.partial = nil
		if _, err := t.f.Seek(0, io.SeekStart); err != nil {
			t.reopen()
			return
		}
		if !t.send(ctx, Record{Kind: KindReset, Time: time.Now()}) {
			return
		}
	}
	buf := make([]byte, readChunk)
	for {
		n, err := t.f.Read(buf)
		if n > 0 {
			t.offset += int64(n)
			t.partial = append(t.partial, buf[:n]...)
			if !t.emitLines(ctx) {
				return
			}
		}
		if err != nil {
			if errors.Is(err, fs.ErrClosed) || errors.Is(err, os.ErrInvalid) {
				t.reopen()
			}
			return // io.EOF is the normal exit: caught up, wait for more
		}
	}
}

// emitLines delivers every complete line buffered so far, keeping any
// trailing partial line for the next read. It reports false when ctx
// ended mid-delivery.
func (t *tailer) emitLines(ctx context.Context) bool {
	for {
		i := bytes.IndexByte(t.partial, '\n')
		if i < 0 {
			return true
		}
		line := bytes.TrimSpace(t.partial[:i])
		t.partial = t.partial[i+1:]
		if len(line) == 0 {
			continue
		}
		var r Record
		if err := json.Unmarshal(line, &r); err != nil {
			continue // a torn or foreign line is skipped, never fatal
		}
		if !t.send(ctx, r) {
			return false
		}
	}
}

// send delivers one record, honoring cancellation while a slow consumer
// is still rendering the last one.
func (t *tailer) send(ctx context.Context, r Record) bool {
	select {
	case t.out <- r:
		return true
	case <-ctx.Done():
		return false
	}
}

// reopen drops the current handle so the next drain opens the path
// afresh — the recovery for a file replaced rather than truncated.
func (t *tailer) reopen() {
	if t.f != nil {
		_ = t.f.Close()
	}
	t.f = nil
	t.offset = 0
	t.partial = nil
}
