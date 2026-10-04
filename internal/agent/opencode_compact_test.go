package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOpencodeServerSummarize(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/session/ses_1/summarize" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		if got["modelID"] == "bad" {
			http.Error(w, "no such model", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte("true"))
	}))
	defer srv.Close()
	o := opencodeServer{base: srv.URL}
	if err := o.summarize(context.Background(), "ses_1", "opencode", "mimo"); err != nil {
		t.Fatal(err)
	}
	if got["providerID"] != "opencode" || got["modelID"] != "mimo" {
		t.Errorf("body = %v", got)
	}
	err := o.summarize(context.Background(), "ses_1", "opencode", "bad")
	if err == nil || !strings.Contains(err.Error(), "no such model") {
		t.Errorf("err = %v, want the server's reason", err)
	}
}

// With no turn behind it there is no opencode session to compact: Compact
// says so and ends idle, without touching the server.
func TestOpencodeCompactBeforeAnyTurn(t *testing.T) {
	_, _ = stubServeOpencode(t)
	sess := ocSession(t, SessionOpts{WorkDir: t.TempDir(), Model: "p/m"})
	defer func() { _ = sess.Close() }()
	if err := sess.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	var kinds []EventKind
	deadline := time.After(5 * time.Second)
	for {
		select {
		case e := <-sess.Events():
			kinds = append(kinds, e.Kind)
			if e.Kind == EventIdle {
				if len(kinds) != 2 || kinds[0] != EventMessage {
					t.Errorf("events = %v, want a message then idle", kinds)
				}
				return
			}
			if e.Kind == EventError {
				t.Fatalf("compacting with no conversation errored: %v", e.Err)
			}
		case <-deadline:
			t.Fatal("no idle before deadline")
		}
	}
}

// The compaction rides the session's own server: one summarize call
// against it, and the report when it lands.
func TestOpencodeCompactRunsOnTheSessionServer(t *testing.T) {
	f, _ := stubServeOpencode(t)
	sess := ocSession(t, SessionOpts{WorkDir: t.TempDir(), Model: "opencode/mimo"})
	defer func() { _ = sess.Close() }()
	// a turn first, so the session exists
	runCleanTurn(t, f, sess)
	id := sess.SessionID()

	if err := sess.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	var sawMessage bool
	deadline := time.After(10 * time.Second)
	for {
		select {
		case e := <-sess.Events():
			switch e.Kind {
			case EventMessage:
				if !strings.Contains(e.Text, "Compacted the conversation") {
					t.Errorf("compaction message = %q", e.Text)
				}
				sawMessage = true
			case EventError:
				t.Fatalf("compaction failed: %v", e.Err)
			case EventIdle:
				f.mu.Lock()
				summaries := append([]string(nil), f.summaries...)
				spawns := len(f.spawns)
				f.mu.Unlock()
				if !sawMessage {
					t.Error("compaction reported no message")
				}
				if len(summaries) != 1 || summaries[0] != id {
					t.Errorf("summaries = %v, want one against %s", summaries, id)
				}
				if spawns != 1 {
					t.Errorf("spawns = %d, want only the session's own (no per-compaction serve)", spawns)
				}
				return
			}
		case <-deadline:
			t.Fatal("compaction never ended")
		}
	}
}

// Interrupting a running compaction cancels the summarize call and keeps
// the "stopped" report: the conversation is as it was.
func TestOpencodeCompactInterruptStopsTheSummarize(t *testing.T) {
	f, _ := stubServeOpencode(t)
	sess := ocSession(t, SessionOpts{WorkDir: t.TempDir(), Model: "opencode/mimo"})
	defer func() { _ = sess.Close() }()
	runCleanTurn(t, f, sess)

	f.holdSummarize()
	if err := sess.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-time.After(50 * time.Millisecond):
			f.mu.Lock()
			n := len(f.summaries)
			f.mu.Unlock()
			if n > 0 {
				goto compacting
			}
		case <-deadline:
			t.Fatal("compaction never started")
		}
	}
compacting:
	if err := sess.Interrupt(context.Background()); err != nil {
		t.Fatal(err)
	}
	sawStopped := false
	deadline = time.After(10 * time.Second)
	for {
		select {
		case e := <-sess.Events():
			switch e.Kind {
			case EventMessage:
				if !strings.Contains(e.Text, "Compaction stopped") {
					t.Errorf("report = %q, want the compaction-stopped wording", e.Text)
				}
				sawStopped = true
			case EventError:
				t.Fatalf("an interrupted compaction surfaced as error: %v", e.Err)
			case EventIdle:
				if !sawStopped {
					t.Error("the stop was not reported")
				}
				f.releaseSummarize()
				return
			}
		case <-deadline:
			t.Fatal("the interrupted compaction never ended")
		}
	}
}

// A turn while a compaction runs is refused, the same way a turn during a
// turn is.
func TestOpencodeCompactBusy(t *testing.T) {
	f, _ := stubServeOpencode(t)
	sess := ocSession(t, SessionOpts{WorkDir: t.TempDir(), Model: "opencode/mimo"})
	defer func() { _ = sess.Close() }()
	f.holdTurns()
	if err := sess.Send(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	f.waitPosted(t)
	f.push(f.partEvent("text", "p", "ok"))
	if err := sess.Compact(context.Background()); !errors.Is(err, ErrBusy) {
		t.Errorf("Compact during a turn = %v, want ErrBusy", err)
	}
	f.releaseTurn()
	waitTurnEnd(t, sess)
}
