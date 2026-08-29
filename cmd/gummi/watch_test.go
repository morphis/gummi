package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/livelog"
)

// The rendered stream reads as a transcript: streamed deltas print as
// they arrive, and the finalizing message does not print the same prose a
// second time.
func TestWatchRenderStreamsOnce(t *testing.T) {
	var r watchRender
	var b strings.Builder
	for _, rec := range []livelog.Record{
		{Kind: livelog.KindSession, Feature: "FD-300", Stage: "implement", Role: "implementer", PID: 77},
		{Kind: livelog.KindDelta, Text: "reading the "},
		{Kind: livelog.KindDelta, Text: "spec"},
		{Kind: livelog.KindMessage, Text: "reading the spec"},
		{Kind: livelog.KindTool, Text: "read  spec.md", Call: "c1"},
		{Kind: livelog.KindResult, Call: "c1", OK: true},
	} {
		b.WriteString(r.line(rec))
	}
	out := b.String()
	if strings.Count(out, "reading the spec") != 1 {
		t.Errorf("prose appears %d times, want once:\n%s", strings.Count(out, "reading the spec"), out)
	}
	if !strings.Contains(out, "FD-300") || !strings.Contains(out, "pid 77") {
		t.Errorf("header missing the session's identity:\n%s", out)
	}
	if !strings.Contains(out, "read  spec.md") {
		t.Errorf("tool line missing:\n%s", out)
	}
}

// A message that arrives whole (no deltas — the common case for adapters
// that don't stream) still prints.
func TestWatchRenderWholeMessage(t *testing.T) {
	var r watchRender
	out := r.line(livelog.Record{Kind: livelog.KindMessage, Text: "done"})
	if !strings.Contains(out, "done") {
		t.Errorf("a non-streamed message printed %q", out)
	}
}

// A failing tool call is called out; a passing one stays quiet, so the
// stream reads as the run's own narration rather than a log.
func TestWatchRenderToolOutcome(t *testing.T) {
	var r watchRender
	if got := r.line(livelog.Record{Kind: livelog.KindResult, Call: "c1", OK: true}); got != "" {
		t.Errorf("a passing tool result printed %q, want nothing", got)
	}
	got := r.line(livelog.Record{Kind: livelog.KindResult, Call: "c2", Output: "FAIL\ndetails"})
	if !strings.Contains(got, "FAIL") {
		t.Errorf("a failing tool result printed %q, want its first output line", got)
	}
	if strings.Contains(got, "details") {
		t.Errorf("a failing tool result printed the whole output: %q", got)
	}
}

// A watcher cannot answer: the question is shown, with where to answer it.
func TestWatchRenderAskIsReadOnly(t *testing.T) {
	var r watchRender
	got := r.line(livelog.Record{Kind: livelog.KindAsk, Text: "Postgres or SQLite?"})
	if !strings.Contains(got, "Postgres or SQLite?") {
		t.Errorf("the question is not shown: %q", got)
	}
	if !strings.Contains(got, "cannot") {
		t.Errorf("nothing says a watcher can't answer: %q", got)
	}
}

// A takeover mid-watch is announced rather than spliced onto the previous
// session's transcript.
func TestWatchRenderReset(t *testing.T) {
	var r watchRender
	got := r.line(livelog.Record{Kind: livelog.KindReset})
	if !strings.Contains(got, "took this card over") {
		t.Errorf("reset rendered as %q", got)
	}
}

// The header names the owner and admits when that owner is gone, so an
// abandoned file never reads as a live run.
func TestRenderWatchHeader(t *testing.T) {
	var b bytes.Buffer
	renderWatchHeader(&b, "FD-301", "a card", livelog.Status{
		PID: 1 << 30, Stage: "verify", Role: "reviewer", Agent: "copilot", Model: "gpt-5",
	}, true)
	out := b.String()
	if !strings.Contains(out, "gone") {
		t.Errorf("header does not admit the owner has exited:\n%s", out)
	}

	b.Reset()
	renderWatchHeader(&b, "FD-301", "a card", livelog.Status{PID: os.Getpid(), Stage: "verify", Stopped: true}, true)
	if !strings.Contains(b.String(), "session ended") {
		t.Errorf("header does not report a finished session:\n%s", b.String())
	}

	b.Reset()
	renderWatchHeader(&b, "FD-301", "a card", livelog.Status{}, false)
	if !strings.Contains(b.String(), "waiting") {
		t.Errorf("header for a card with no stream yet:\n%s", b.String())
	}
}

// followLive renders records as they land and returns when its context
// ends — the Ctrl-C path.
func TestFollowLiveStreams(t *testing.T) {
	path := t.TempDir() + "/FD-302.jsonl"
	w, err := livelog.Create(path, livelog.Record{Feature: "FD-302", Stage: "implement", PID: os.Getpid()})
	if err != nil {
		t.Fatal(err)
	}
	w.Emit(livelog.Record{Kind: livelog.KindMessage, Text: "hello from the other process"})
	w.Emit(livelog.Record{Kind: livelog.KindStopped})
	w.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var b bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- followLive(ctx, &b, path, false, false) }()

	deadline := time.After(3 * time.Second)
	for {
		if strings.Contains(b.String(), "hello from the other process") {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("the message never rendered:\n%s", b.String())
		case <-time.After(20 * time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("followLive: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("followLive did not return after its context ended")
	}
}

// --json emits the raw records, one JSON object per line, for a caller
// that would rather parse than read.
func TestFollowLiveJSON(t *testing.T) {
	path := t.TempDir() + "/FD-303.jsonl"
	w, err := livelog.Create(path, livelog.Record{Feature: "FD-303", PID: os.Getpid()})
	if err != nil {
		t.Fatal(err)
	}
	w.Emit(livelog.Record{Kind: livelog.KindStopped})
	w.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var b bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- followLive(ctx, &b, path, true, false) }()
	deadline := time.After(3 * time.Second)
	for !strings.Contains(b.String(), `"kind":"stopped"`) {
		select {
		case <-deadline:
			t.Fatalf("no NDJSON reached the writer:\n%s", b.String())
		case <-time.After(20 * time.Millisecond):
		}
	}
	cancel()
	<-done
	if !strings.Contains(b.String(), `"id":"FD-303"`) {
		t.Errorf("the header record is not in the JSON stream:\n%s", b.String())
	}
}
