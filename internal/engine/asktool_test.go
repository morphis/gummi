package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morphia/gummi/internal/agent"
	"github.com/morphia/gummi/internal/domain"
	"github.com/morphia/gummi/internal/spec"
)

// fixedNow is a deterministic clock for spec-capture marker dates.
func fixedNow() time.Time { return time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC) }

// askArgs builds an ask_user tool-call argument blob.
func askArgs(t *testing.T, a Ask) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// clientToolFake advertises ClientTools and, on its first turn, emits an
// ask_user client-tool call instead of finishing.
func clientToolFake(args json.RawMessage) *agent.Fake {
	f := agent.NewFake("")
	f.Caps = agent.Capabilities{ClientTools: true, Interrupt: true, UsageEvents: true}
	first := true
	f.Responder = func(_ agent.SessionOpts, msg string) []agent.Event {
		if first {
			first = false
			return []agent.Event{
				{Kind: agent.EventClientToolCall, ToolCall: &agent.ToolCall{ID: "call-1", Name: "ask_user", Args: args}},
			}
		}
		return []agent.Event{{Kind: agent.EventMessage, Text: "done: " + msg}, {Kind: agent.EventIdle}}
	}
	return f
}

func TestAskUserSurfacesAndResolves(t *testing.T) {
	args := askArgs(t, Ask{
		Question: "Persist where?",
		Options:  []AskOption{{Label: "per-device"}, {Label: "synced"}},
	})
	ag := clientToolFake(args)
	e := newEngine(t, ag)
	ctx := context.Background()

	s, err := e.Attach(ctx, feature(1, "Dark mode", domain.StageBrainstorm))
	if err != nil {
		t.Fatal(err)
	}
	// the kickoff turn triggers the ask
	waitFor(t, e, EventQuestion)

	snap := s.Snapshot()
	if snap.PendingAsk == nil || snap.PendingAsk.Question != "Persist where?" {
		t.Fatalf("pending ask not surfaced: %+v", snap.PendingAsk)
	}
	if snap.Busy {
		t.Error("session should not be busy while waiting on the user")
	}

	// answering resolves the blocked tool call and records the choice.
	// (A real backend resumes the same turn to idle; the Fake models the
	// resolve synchronously, so no idle follows here.)
	if err := e.Answer(ctx, "FD-001", "per-device"); err != nil {
		t.Fatal(err)
	}

	if s.Snapshot().PendingAsk != nil {
		t.Error("pending ask not cleared after answer")
	}
	// the fake session recorded what gummi resolved the call with
	type resolver interface {
		Resolved(string) (string, bool)
	}
	if r, ok := s.agent().(resolver); ok {
		if got, _ := r.Resolved("call-1"); got != "per-device" {
			t.Errorf("resolved with %q, want per-device", got)
		}
	} else {
		t.Fatal("fake session is not a resolver")
	}
	// the answer is in the transcript as a user turn
	found := false
	for _, m := range s.Snapshot().Transcript {
		if m.Author == AuthorUser && m.Content == "per-device" {
			found = true
		}
	}
	if !found {
		t.Errorf("answer not recorded in transcript: %+v", s.Snapshot().Transcript)
	}
}

func TestAskUserCapturesToSpec(t *testing.T) {
	args := askArgs(t, Ask{
		Question:   "Persist where?",
		Options:    []AskOption{{Label: "per-device"}, {Label: "synced"}},
		SpecAnchor: "## Chosen approach",
	})
	ag := clientToolFake(args)
	e := newEngine(t, ag)
	e.now = fixedNow // deterministic marker date
	ctx := context.Background()

	f := feature(1, "Dark mode", domain.StageBrainstorm)
	s, err := e.Attach(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventQuestion)
	if err := e.Answer(ctx, "FD-001", "per-device"); err != nil {
		t.Fatal(err)
	}

	draft := filepath.Join(e.cfg.Workspace.DraftsDir(), spec.DraftFilename(&f))
	raw, err := os.ReadFile(draft)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "%% @user(2026-07-04): resolved — per-device") {
		t.Errorf("answer not captured into spec:\n%s", raw)
	}
	_ = s
}

func TestAskUserBadAnchorStillAnswers(t *testing.T) {
	args := askArgs(t, Ask{
		Question:   "Persist where?",
		Options:    []AskOption{{Label: "per-device"}},
		SpecAnchor: "no such line anywhere",
	})
	e := newEngine(t, clientToolFake(args))
	ctx := context.Background()
	if _, err := e.Attach(ctx, feature(1, "Dark mode", domain.StageBrainstorm)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventQuestion)
	// a bad anchor must not block the answer
	if err := e.Answer(ctx, "FD-001", "per-device"); err != nil {
		t.Fatalf("answer with bad anchor failed: %v", err)
	}
	// the skip is noted in activity
	var noted bool
	for _, a := range e.Get("FD-001").Snapshot().Activity {
		if strings.Contains(a, "spec capture skipped") {
			noted = true
		}
	}
	if !noted {
		t.Error("bad-anchor skip not noted in activity")
	}
}

func TestConventionAskPath(t *testing.T) {
	// a backend WITHOUT client tools emits a gummi-ask fenced block
	block := "Here's my question.\n```gummi-ask\n" +
		`{"question":"Persist where?","options":[{"label":"per-device"},{"label":"synced"}]}` +
		"\n```"
	ag := agent.NewFake(block)
	ag.Caps = agent.Capabilities{} // no ClientTools
	e := newEngine(t, ag)
	ctx := context.Background()

	s, err := e.Attach(ctx, feature(1, "Dark mode", domain.StageBrainstorm))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventQuestion)

	snap := s.Snapshot()
	if snap.PendingAsk == nil || snap.PendingAsk.Question != "Persist where?" {
		t.Fatalf("convention ask not parsed: %+v", snap.PendingAsk)
	}
	// the block is stripped from the visible message
	last, _ := s.lastAssistant()
	if strings.Contains(last, "gummi-ask") {
		t.Errorf("ask block not stripped from transcript: %q", last)
	}
	// answering a convention ask delivers it as the next turn
	if err := e.Answer(ctx, "FD-001", "synced"); err != nil {
		t.Fatal(err)
	}
}

func TestUnknownClientToolAutoResolves(t *testing.T) {
	ag := agent.NewFake("")
	ag.Caps = agent.Capabilities{ClientTools: true, Interrupt: true}
	ag.Responder = func(_ agent.SessionOpts, _ string) []agent.Event {
		return []agent.Event{
			{Kind: agent.EventClientToolCall, ToolCall: &agent.ToolCall{ID: "c1", Name: "mystery", Args: json.RawMessage(`{}`)}},
			{Kind: agent.EventIdle},
		}
	}
	e := newEngine(t, ag)
	ctx := context.Background()
	s, err := e.Attach(ctx, feature(1, "x", domain.StageBrainstorm))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventIdle)
	// an unknown tool never becomes a pending question
	if s.Snapshot().PendingAsk != nil {
		t.Error("unknown tool surfaced as a question")
	}
	type resolver interface {
		Resolved(string) (string, bool)
	}
	if r, ok := s.agent().(resolver); ok {
		if got, done := r.Resolved("c1"); !done || !strings.Contains(got, "unknown tool") {
			t.Errorf("unknown tool not auto-resolved: %q done=%v", got, done)
		}
	}
}
