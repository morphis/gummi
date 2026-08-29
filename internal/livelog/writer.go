package livelog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// queueDepth bounds the writer's in-flight queue. It is deep enough to
// absorb a fast streaming turn between disk writes and shallow enough
// that a wedged disk can't grow it without bound; past it, Emit drops
// and the loss is reported as a KindDropped record rather than blocking
// the engine goroutine that produced it.
const queueDepth = 512

// flushInterval paces coalesced assistant deltas. A streaming turn emits
// chunks far faster than any reader can render, so they accumulate and
// land as one record per interval — ~10 writes a second instead of
// hundreds, with a latency a watcher reads as live.
const flushInterval = 100 * time.Millisecond

// maxPendingDelta flushes a long pending run early, so one very fast
// stream can't hold an unbounded string between ticks.
const maxPendingDelta = 8 << 10

// fileMode is the live file's permission. Transcripts are workspace
// machinery under the gitignored state dir; keep them owner-only, as the
// events mirror and pid files are.
const fileMode = 0o600

// Writer appends Records to one card's live file.
//
// Emit never blocks and never fails: the live file is a view for other
// processes, and the run it mirrors must not stall (or die) because a
// watcher's convenience hit a full disk. Writes happen on the Writer's
// own goroutine, so the engine goroutines calling Emit — some of which
// hold a session lock the UI also takes — never touch the filesystem.
//
// A nil *Writer is a valid no-op Writer, so callers with live logging
// disabled need no branch at every call site.
type Writer struct {
	ch      chan Record
	done    chan struct{}
	dropped atomic.Int64
	// closeMu guards the queue against a late Emit. A session's teardown
	// closes the writer while goroutines that were mid-turn may still be
	// appending to the transcript, and a send on a closed channel would
	// take the whole process down — so Emit holds the read side and a
	// post-Close record is discarded rather than fatal.
	closeMu sync.RWMutex
	closed  bool
	now     func() time.Time
}

// Create opens path for a new session, truncating whatever the previous
// session left there, and writes hdr as the file's first record.
//
// Truncation is the point: the live file answers "what is this card doing
// now", so it holds one session, and a follower that sees the file shrink
// knows to discard what it accumulated. History belongs to the state
// store, which is untouched by any of this.
func Create(path string, hdr Record) (*Writer, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, fileMode)
	if err != nil {
		return nil, err
	}
	w := &Writer{
		ch:   make(chan Record, queueDepth),
		done: make(chan struct{}),
		now:  time.Now,
	}
	hdr.Kind = KindSession
	if hdr.PID == 0 {
		hdr.PID = os.Getpid()
	}
	go w.run(f, hdr)
	return w, nil
}

// Emit queues one record. It is safe on a nil Writer, safe from any
// goroutine, and never blocks: a full queue drops the record and counts
// it, so the next successful write can report the gap.
func (w *Writer) Emit(r Record) {
	if w == nil {
		return
	}
	w.closeMu.RLock()
	defer w.closeMu.RUnlock()
	if w.closed {
		return // the session ended; its stream is complete without this
	}
	select {
	case w.ch <- r:
	default:
		w.dropped.Add(1)
	}
}

// Delta queues a chunk of streaming assistant text. It is the one record
// kind the writer coalesces, so a per-token backend costs ~10 writes a
// second rather than one per token.
func (w *Writer) Delta(text string) {
	if text == "" {
		return
	}
	w.Emit(Record{Kind: KindDelta, Text: text})
}

// Close flushes what is pending, writes no further records, and waits for
// the writer goroutine to finish. Safe on a nil Writer and idempotent, so
// a session's teardown path can call it unconditionally.
func (w *Writer) Close() {
	if w == nil {
		return
	}
	w.closeMu.Lock()
	first := !w.closed
	w.closed = true
	if first {
		close(w.ch)
	}
	w.closeMu.Unlock()
	<-w.done
}

// run owns the file for the writer's lifetime: it is the only goroutine
// that touches it, so records serialize with no cross-line interleave and
// no lock is held across a syscall.
func (w *Writer) run(f *os.File, hdr Record) {
	defer close(w.done)
	defer f.Close()

	var pending strings.Builder
	// flushDelta lands the coalesced run as one delta record. Deltas are
	// additive, so merging them changes nothing a follower reconstructs.
	flushDelta := func() {
		if pending.Len() == 0 {
			return
		}
		w.write(f, Record{Kind: KindDelta, Text: pending.String()})
		pending.Reset()
	}

	w.write(f, hdr)

	tick := time.NewTicker(flushInterval)
	defer tick.Stop()
	for {
		select {
		case r, ok := <-w.ch:
			if !ok {
				flushDelta()
				w.reportDropped(f)
				return
			}
			if r.Kind == KindDelta {
				pending.WriteString(r.Text)
				if pending.Len() >= maxPendingDelta {
					flushDelta()
				}
				continue
			}
			// a non-delta record ends the streaming run it followed, so
			// the pending text lands first and order is preserved.
			flushDelta()
			w.reportDropped(f)
			w.write(f, r)
		case <-tick.C:
			flushDelta()
		}
	}
}

// reportDropped writes the accumulated drop count, if any, so a follower
// sees the gap instead of reading a lossy stream as complete.
func (w *Writer) reportDropped(f *os.File) {
	n := w.dropped.Swap(0)
	if n == 0 {
		return
	}
	w.write(f, Record{Kind: KindDropped, Count: int(n)})
}

// write serializes one record as a line. A marshal or write failure is
// dropped: the live file is advisory, and the run it mirrors is
// authoritative (the same reasoning as the driver's NDJSON emitter).
func (w *Writer) write(f *os.File, r Record) {
	if r.Time.IsZero() {
		r.Time = w.now()
	}
	line, err := json.Marshal(r)
	if err != nil {
		return
	}
	_, _ = f.Write(append(line, '\n'))
}
