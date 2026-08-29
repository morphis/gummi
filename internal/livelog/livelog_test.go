package livelog

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// readAll parses every record a live file holds.
func readAll(t *testing.T, path string) []Record {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	var recs []Record
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r Record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("line %q: %v", line, err)
		}
		recs = append(recs, r)
	}
	return recs
}

// kinds is the record sequence a file holds, for order assertions.
func kinds(recs []Record) []Kind {
	out := make([]Kind, len(recs))
	for i, r := range recs {
		out[i] = r.Kind
	}
	return out
}

// A writer's records land as one JSON object per line, headed by the
// session record, and every record carries a timestamp.
func TestWriterRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "live", "FD-001.jsonl")
	w, err := Create(path, Record{Feature: "FD-001", Stage: "implement", Role: "implementer"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	w.Emit(Record{Kind: KindTool, Text: "read  main.go", Call: "c1"})
	w.Emit(Record{Kind: KindResult, Call: "c1", OK: true})
	w.Emit(Record{Kind: KindStopped})
	w.Close()

	recs := readAll(t, path)
	want := []Kind{KindSession, KindTool, KindResult, KindStopped}
	if got := kinds(recs); len(got) != len(want) {
		t.Fatalf("kinds = %v, want %v", got, want)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("kinds = %v, want %v", got, want)
			}
		}
	}
	if recs[0].Feature != "FD-001" || recs[0].Stage != "implement" {
		t.Errorf("header = %+v, want the session's identity", recs[0])
	}
	if recs[0].PID != os.Getpid() {
		t.Errorf("header PID = %d, want this process %d", recs[0].PID, os.Getpid())
	}
	for _, r := range recs {
		if r.Time.IsZero() {
			t.Errorf("%s record has no timestamp", r.Kind)
		}
	}
}

// Deltas coalesce: a burst of streamed chunks lands as one record whose
// text is their concatenation, so a per-token backend doesn't cost one
// write per token.
func TestWriterCoalescesDeltas(t *testing.T) {
	path := filepath.Join(t.TempDir(), "FD-002.jsonl")
	w, err := Create(path, Record{Feature: "FD-002"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, chunk := range []string{"the ", "quick ", "brown ", "fox"} {
		w.Delta(chunk)
	}
	w.Emit(Record{Kind: KindMessage, Text: "the quick brown fox"})
	w.Close()

	recs := readAll(t, path)
	var deltas []Record
	for _, r := range recs {
		if r.Kind == KindDelta {
			deltas = append(deltas, r)
		}
	}
	if len(deltas) != 1 {
		t.Fatalf("got %d delta records, want the 4 chunks coalesced into 1", len(deltas))
	}
	if deltas[0].Text != "the quick brown fox" {
		t.Errorf("coalesced delta = %q, want the chunks concatenated", deltas[0].Text)
	}
	// the pending run must land before the record that follows it.
	if kinds(recs)[len(recs)-1] != KindMessage {
		t.Errorf("kinds = %v, want the message last", kinds(recs))
	}
}

// An empty delta is not a record: it would open an empty bubble in every
// follower for nothing.
func TestWriterIgnoresEmptyDelta(t *testing.T) {
	path := filepath.Join(t.TempDir(), "FD-003.jsonl")
	w, err := Create(path, Record{Feature: "FD-003"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	w.Delta("")
	w.Close()
	if got := len(readAll(t, path)); got != 1 {
		t.Fatalf("file holds %d records, want only the header", got)
	}
}

// Emit after Close is a no-op, not a panic: a session's teardown races
// goroutines that are still appending to its transcript, and a send on a
// closed channel would take the whole run down with it.
func TestEmitAfterCloseIsSafe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "FD-004.jsonl")
	w, err := Create(path, Record{Feature: "FD-004"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	w.Close()
	w.Emit(Record{Kind: KindTool, Text: "late"})
	w.Delta("late")
	w.Close() // idempotent

	for _, r := range readAll(t, path) {
		if r.Text == "late" {
			t.Fatal("a post-Close record reached the file")
		}
	}
}

// A nil Writer is a working no-op, so a session with live logging
// disabled needs no branch at each call site.
func TestNilWriterIsNoOp(t *testing.T) {
	var w *Writer
	w.Emit(Record{Kind: KindTool})
	w.Delta("x")
	w.Close()
}

// Follow delivers records as they are appended, including ones written
// after the follower attached.
func TestFollowStreamsAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "FD-005.jsonl")
	w, err := Create(path, Record{Feature: "FD-005", Stage: "verify"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer w.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch := Follow(ctx, path, 10*time.Millisecond)

	if got := next(t, ch); got.Kind != KindSession {
		t.Fatalf("first record = %s, want the session header", got.Kind)
	}
	w.Emit(Record{Kind: KindTool, Text: "go test ./..."})
	got := next(t, ch)
	if got.Kind != KindTool || got.Text != "go test ./..." {
		t.Fatalf("record = %+v, want the tool line just written", got)
	}
}

// A follower that attaches before the run starts waits for the file
// instead of failing — the watcher may well be up first.
func TestFollowWaitsForFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "FD-006.jsonl")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch := Follow(ctx, path, 10*time.Millisecond)

	w, err := Create(path, Record{Feature: "FD-006"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer w.Close()
	if got := next(t, ch); got.Feature != "FD-006" {
		t.Fatalf("record = %+v, want the header of the file that appeared", got)
	}
}

// A new session truncates the live file; the follower reports the reset
// rather than splicing the new session onto the old one's transcript.
func TestFollowReportsTruncation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "FD-007.jsonl")
	first, err := Create(path, Record{Feature: "FD-007", Stage: "plan"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	first.Emit(Record{Kind: KindMessage, Text: strings.Repeat("x", 2048)})
	first.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch := Follow(ctx, path, 10*time.Millisecond)
	for r := next(t, ch); r.Kind != KindMessage; r = next(t, ch) {
	}

	second, err := Create(path, Record{Feature: "FD-007", Stage: "implement"})
	if err != nil {
		t.Fatalf("re-create: %v", err)
	}
	defer second.Close()

	var sawReset bool
	for i := 0; i < 4; i++ {
		r := next(t, ch)
		if r.Kind == KindReset {
			sawReset = true
			continue
		}
		if r.Kind == KindSession {
			if !sawReset {
				t.Fatal("the new session's header arrived without a reset before it")
			}
			if r.Stage != "implement" {
				t.Fatalf("header stage = %q, want the new session's", r.Stage)
			}
			return
		}
	}
	t.Fatal("never saw the new session's header")
}

// Follow stops when its context is canceled, closing the channel.
func TestFollowStopsOnCancel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "FD-008.jsonl")
	w, err := Create(path, Record{Feature: "FD-008"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer w.Close()

	ctx, cancel := context.WithCancel(context.Background())
	ch := Follow(ctx, path, 10*time.Millisecond)
	next(t, ch)
	cancel()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("channel still open after cancel")
		}
	}
}

// Stat summarizes a file without following it: identity from the header,
// and whether the session already ended from the tail.
func TestStat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "FD-009.jsonl")

	if _, err := Stat(path); err != ErrNoLiveFile {
		t.Fatalf("Stat of a missing file = %v, want ErrNoLiveFile", err)
	}

	w, err := Create(path, Record{Feature: "FD-009", Stage: "review", Role: "reviewer", Agent: "copilot", Model: "gpt-5"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	w.Emit(Record{Kind: KindTool, Text: "read  spec.md"})
	w.Close()

	st, err := Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if st.Feature != "FD-009" || st.Stage != "review" || st.Role != "reviewer" {
		t.Errorf("Stat identity = %+v, want the header's", st)
	}
	if st.Agent != "copilot" || st.Model != "gpt-5" {
		t.Errorf("Stat backend = %s/%s, want copilot/gpt-5", st.Agent, st.Model)
	}
	if st.PID != os.Getpid() {
		t.Errorf("Stat PID = %d, want %d", st.PID, os.Getpid())
	}
	if st.Stopped {
		t.Error("Stopped is true for a session that never wrote its terminal record")
	}

	w2, err := Create(path, Record{Feature: "FD-009", Stage: "verify"})
	if err != nil {
		t.Fatalf("re-create: %v", err)
	}
	w2.Emit(Record{Kind: KindStopped})
	w2.Close()
	st, err = Stat(path)
	if err != nil {
		t.Fatalf("Stat after stop: %v", err)
	}
	if !st.Stopped {
		t.Error("Stopped is false after the session wrote its terminal record")
	}
}

// A live file longer than the tail Stat reads still reports its final
// record — the tail is a read budget, not a limit on what it can see.
func TestStatLongFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "FD-010.jsonl")
	w, err := Create(path, Record{Feature: "FD-010", Stage: "implement"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for i := 0; i < 200; i++ {
		w.Emit(Record{Kind: KindTool, Text: strings.Repeat("y", 200)})
	}
	w.Emit(Record{Kind: KindStopped})
	w.Close()

	st, err := Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if st.Feature != "FD-010" {
		t.Errorf("Feature = %q, want the header's even past the tail window", st.Feature)
	}
	if !st.Stopped {
		t.Error("Stopped is false for a long file whose last record is the terminal one")
	}
}

// next takes the follower's next record or fails the test on timeout.
func next(t *testing.T, ch <-chan Record) Record {
	t.Helper()
	select {
	case r, ok := <-ch:
		if !ok {
			t.Fatal("follow channel closed early")
		}
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a record")
	}
	return Record{}
}
