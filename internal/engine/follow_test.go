package engine

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/livelog"
)

// newLiveSession returns a session bound to a live file at path, with the
// same header bindLiveLog stamps.
func newLiveSession(t *testing.T, path string, f domain.Feature) *Session {
	t.Helper()
	w, err := livelog.Create(path, livelog.Record{
		Feature: string(f.ID), Stage: string(f.Stage), Role: string(agent.RoleImplementer),
		Agent: "fake", Model: "test-model",
	})
	if err != nil {
		t.Fatalf("create live file: %v", err)
	}
	s := &Session{Feature: f, Role: agent.RoleImplementer, done: make(chan struct{})}
	s.bindLive(w)
	return s
}

// collect follows path until the session's terminal record, folding every
// record into fl.
func collect(t *testing.T, path string, fl *Follower) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for r := range livelog.Follow(ctx, path, 10*time.Millisecond) {
		fl.Apply(r)
		if r.Kind == livelog.KindStopped {
			return
		}
	}
	t.Fatal("the stream ended without a terminal record")
}

// The whole point of the live file: a follower in another process
// reconstructs the transcript the owning session holds in memory — the
// streamed prose, the tool calls and their outcomes, in order.
func TestFollowerReconstructsTranscript(t *testing.T) {
	f := domain.Feature{ID: "FD-100", Stage: domain.StageImplement, Title: "live view"}
	path := filepath.Join(t.TempDir(), "FD-100.jsonl")
	s := newLiveSession(t, path, f)

	s.appendSystem("kickoff: implement the thing")
	s.appendDelta("I'll start by ")
	s.appendDelta("reading the spec.")
	s.finishAssistant("I'll start by reading the spec.")
	s.appendToolCall("call-1", "read  spec.md")
	s.resolveToolResult("call-1", true, "42 lines")
	s.appendToolCall("call-2", "bash  go test ./...")
	s.resolveToolResult("call-2", false, "FAIL\ninternal/foo")
	s.appendActivity("checkpoint: 3 files")
	s.finishAssistant("Tests fail; fixing.")
	s.stop()

	fl := NewFollower(f)
	collect(t, path, fl)

	got := fl.Snapshot().Transcript
	want := s.Snapshot().Transcript
	if len(got) != len(want) {
		t.Fatalf("followed transcript has %d messages, session has %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Author != want[i].Author || got[i].Content != want[i].Content {
			t.Errorf("message %d = %s/%q, want %s/%q", i,
				got[i].Author, got[i].Content, want[i].Author, want[i].Content)
		}
		if got[i].ToolStatus != want[i].ToolStatus || got[i].ToolOutput != want[i].ToolOutput {
			t.Errorf("message %d outcome = %q/%q, want %q/%q", i,
				got[i].ToolStatus, got[i].ToolOutput, want[i].ToolStatus, want[i].ToolOutput)
		}
	}
	if !reflect.DeepEqual(fl.Snapshot().Activity, s.Snapshot().Activity) {
		t.Errorf("activity = %v, want %v", fl.Snapshot().Activity, s.Snapshot().Activity)
	}
	if !fl.Stopped() {
		t.Error("follower did not see the session end")
	}
	if fl.PID() == 0 {
		t.Error("follower has no owning pid; the header never landed")
	}
}

// The header's stage and backend identity reach the follower, which has
// only the store's copy of the card to start from.
func TestFollowerTakesIdentityFromHeader(t *testing.T) {
	f := domain.Feature{ID: "FD-101", Stage: domain.StageSpec, Title: "identity"}
	path := filepath.Join(t.TempDir(), "FD-101.jsonl")
	// the live session is further along than the store's copy the
	// follower was seeded with.
	s := newLiveSession(t, path, domain.Feature{ID: "FD-101", Stage: domain.StageImplement})
	s.setState(StateRunning)
	s.setBusy(true)
	s.stop()

	fl := NewFollower(f)
	collect(t, path, fl)

	snap := fl.Snapshot()
	if snap.Feature.Title != "identity" {
		t.Errorf("title = %q, want the caller's card metadata preserved", snap.Feature.Title)
	}
	if snap.Feature.Stage != domain.StageImplement {
		t.Errorf("stage = %q, want the live header's %q", snap.Feature.Stage, domain.StageImplement)
	}
	if snap.AgentName != "fake" || snap.Model != "test-model" {
		t.Errorf("backend = %s/%s, want fake/test-model", snap.AgentName, snap.Model)
	}
	if snap.Busy {
		t.Error("a stopped session still reads as busy")
	}
}

// A second session on the same card truncates the file; the follower
// discards the first session's transcript rather than concatenating them.
func TestFollowerResetsOnNewSession(t *testing.T) {
	f := domain.Feature{ID: "FD-102", Stage: domain.StagePlan}
	path := filepath.Join(t.TempDir(), "FD-102.jsonl")

	first := newLiveSession(t, path, f)
	first.finishAssistant("the previous session's work")
	first.stop()

	fl := NewFollower(f)
	collect(t, path, fl)
	if len(fl.Snapshot().Transcript) != 1 {
		t.Fatalf("first pass transcript = %d messages, want 1", len(fl.Snapshot().Transcript))
	}

	second := newLiveSession(t, path, f)
	second.finishAssistant("the new session's work")
	second.stop()
	collect(t, path, fl)

	tr := fl.Snapshot().Transcript
	if len(tr) != 1 || tr[0].Content != "the new session's work" {
		t.Fatalf("transcript after the takeover = %+v, want only the new session's message", tr)
	}
	if fl.Snapshot().Err != nil {
		t.Error("the reset carried the previous session's error forward")
	}
}

// A watcher sees the agent's open question, but only as a question: it
// carries no options, because a follower can never answer one.
func TestFollowerShowsAskReadOnly(t *testing.T) {
	f := domain.Feature{ID: "FD-103", Stage: domain.StageSpec}
	path := filepath.Join(t.TempDir(), "FD-103.jsonl")
	s := newLiveSession(t, path, f)
	s.setPendingAsk(&Ask{CallID: "ask-1", Question: "Postgres or SQLite?", Options: []AskOption{{Label: "Postgres"}}})
	s.stop()

	fl := NewFollower(f)
	collect(t, path, fl)
	// the terminal record clears the question: nothing will answer it on
	// this stream, and a picker left on screen would lie about that.
	if ask := fl.Snapshot().PendingAsk; ask != nil {
		t.Fatalf("PendingAsk = %+v after the session ended, want nil", ask)
	}

	fl2 := NewFollower(f)
	fl2.Apply(livelog.Record{Kind: livelog.KindAsk, Call: "ask-1", Text: "Postgres or SQLite?"})
	ask := fl2.Snapshot().PendingAsk
	if ask == nil || ask.Question != "Postgres or SQLite?" {
		t.Fatalf("PendingAsk = %+v, want the question", ask)
	}
	if len(ask.Options) != 0 {
		t.Error("a followed ask carries options, implying a watcher could pick one")
	}
}

// The transcript a session already carries (a restart-reattach) is
// replayed onto the live file, so a follower joining later sees the whole
// conversation rather than only what arrives next.
func TestBindLiveReplaysCarriedTranscript(t *testing.T) {
	f := domain.Feature{ID: "FD-104", Stage: domain.StageSpec}
	path := filepath.Join(t.TempDir(), "FD-104.jsonl")

	s := &Session{Feature: f, Role: agent.RoleArchitect, done: make(chan struct{})}
	s.transcript = []Message{
		{Author: AuthorSystem, Content: "kickoff"},
		{Author: AuthorUser, Content: "use sqlite"},
		{Author: AuthorAssistant, Content: "understood"},
	}
	w, err := livelog.Create(path, livelog.Record{Feature: string(f.ID), Stage: string(f.Stage)})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	s.bindLive(w)
	s.stop()

	fl := NewFollower(f)
	collect(t, path, fl)
	got := fl.Snapshot().Transcript
	if len(got) != 3 {
		t.Fatalf("replayed transcript = %d messages, want the 3 carried over", len(got))
	}
	if got[1].Author != AuthorUser || got[1].Content != "use sqlite" {
		t.Errorf("message 1 = %s/%q, want user/\"use sqlite\"", got[1].Author, got[1].Content)
	}
}

// A session with no live writer works exactly as before: the nil Writer
// swallows every emit, so an engine with no workspace (tests, transient
// board helpers) is unaffected.
func TestSessionWithoutLiveWriter(t *testing.T) {
	s := &Session{Feature: domain.Feature{ID: "FD-105"}, done: make(chan struct{})}
	s.appendSystem("no writer bound")
	s.appendDelta("still fine")
	s.finishAssistant("still fine")
	s.stop()
	if got := len(s.Snapshot().Transcript); got != 2 {
		t.Fatalf("transcript = %d messages, want 2", got)
	}
}
