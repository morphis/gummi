package engine

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
)

// recordingSession opens a freeform session on a fake that cannot compact,
// recording every line it was sent and the hints each backend was spawned
// with.
func recordingSession(t *testing.T) (ff *FreeformSession, sent func() []string, hints func() []string) {
	t.Helper()
	var mu sync.Mutex
	var msgs, hs []string
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		mu.Lock()
		msgs = append(msgs, msg)
		hs = append(hs, strings.Join(opts.SystemHints, "\n"))
		mu.Unlock()
		return []agent.Event{{Kind: agent.EventMessage, Text: "did " + msg}, {Kind: agent.EventIdle}}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
	t.Cleanup(func() { e.Close() })
	f := freeformCard(1, "tidy the parser")
	createFeature(t, store, f)
	ff, err := e.OpenFreeform(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	snap := func(p *[]string) func() []string {
		return func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), *p...) }
	}
	return ff, snap(&msgs), snap(&hs)
}

func say(t *testing.T, ff *FreeformSession, line string) {
	t.Helper()
	if err := ff.Send(context.Background(), line); err != nil {
		t.Fatalf("send %q: %v", line, err)
	}
	waitFreeformIdle(t, ff)
}

func transcriptOf(ff *FreeformSession) string {
	var got []string
	for _, m := range ff.Snapshot().Transcript {
		got = append(got, string(m.Author)+":"+m.Content)
	}
	return strings.Join(got, "|")
}

// TestEverySessionOffersItsOwnCommands: the session's own commands are
// offered whatever the backend can do.
func TestEverySessionOffersItsOwnCommands(t *testing.T) {
	ff, _, _ := recordingSession(t)
	for _, name := range []string{"compact", "clear", "retry", "context", "cost", "help"} {
		if _, _, ok := FindProjectCommand(ff.Commands(), "/"+name); !ok {
			t.Errorf("/%s is not offered", name)
		}
	}
}

// TestCompactWithoutBackendCompactionSummarizes: a backend with no
// compaction of its own is asked for a summary, and the conversation is
// then replaced by it — the next backend is replayed the summary, not the
// turns it stands for.
func TestCompactWithoutBackendCompactionSummarizes(t *testing.T) {
	ff, sent, hints := recordingSession(t)
	say(t, ff, "split the lexer")
	say(t, ff, "/compact keep the lexer notes")
	msgs := sent()
	if last := msgs[len(msgs)-1]; !strings.HasPrefix(last, "Summarize this conversation") || !strings.Contains(last, "keep the lexer notes") {
		t.Fatalf("the agent was sent %q, want the summary prompt with the focus", last)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(transcriptOf(ff), "Compacted the conversation") {
		if time.Now().After(deadline) {
			t.Fatalf("transcript never compacted: %s", transcriptOf(ff))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if tr := transcriptOf(ff); strings.Contains(tr, "user:split the lexer") {
		t.Fatalf("the compacted transcript still holds the turns: %s", tr)
	}
	say(t, ff, "carry on")
	h := hints()
	last := h[len(h)-1]
	if !strings.Contains(last, "gummi: Compacted the conversation") || !strings.Contains(last, "did Summarize") ||
		strings.Contains(last, "them: split the lexer") {
		t.Errorf("the respawned backend's replay:\n%s", last)
	}
}

// TestClearStartsAfresh: /clear is never sent to the agent; the next
// backend is told only that the conversation was cleared.
func TestClearStartsAfresh(t *testing.T) {
	ff, sent, hints := recordingSession(t)
	say(t, ff, "split the lexer")
	if err := ff.Send(context.Background(), "/clear"); err != nil {
		t.Fatal(err)
	}
	if n := len(sent()); n != 1 {
		t.Fatalf("/clear reached the agent: %q", sent())
	}
	if tr := transcriptOf(ff); strings.Contains(tr, "split the lexer") || !strings.Contains(tr, "Cleared the conversation") {
		t.Fatalf("transcript after /clear = %s", tr)
	}
	if err := ff.Send(context.Background(), "/clear"); err == nil {
		t.Error("a second /clear on an empty conversation was not refused")
	}
	say(t, ff, "start over")
	h := hints()
	if last := h[len(h)-1]; !strings.Contains(last, "gummi: Cleared the conversation") || strings.Contains(last, "split the lexer") {
		t.Errorf("the respawned backend's replay:\n%s", last)
	}
}

// TestInstantCommandsSendNothing: /context, /cost and /help are answered
// on the thread and never reach the agent.
func TestInstantCommandsSendNothing(t *testing.T) {
	ff, sent, _ := recordingSession(t)
	say(t, ff, "split the lexer")
	for _, c := range []string{"/context", "/cost", "/help"} {
		if err := ff.Send(context.Background(), c); err != nil {
			t.Fatalf("%s: %v", c, err)
		}
	}
	if n := len(sent()); n != 1 {
		t.Fatalf("an instant command reached the agent: %q", sent())
	}
	tr := transcriptOf(ff)
	for _, want := range []string{"tool:gummi /context", "tool:gummi /cost", "tool:gummi /help"} {
		if !strings.Contains(tr, want) {
			t.Errorf("transcript lacks %q: %s", want, tr)
		}
	}
	if strings.Contains(tr, "user:/cost") {
		t.Errorf("an instant command was recorded as a turn: %s", tr)
	}
}

// TestRetrySendsTheLastMessageAgain: /retry drops the last exchange and
// sends the same line once more.
func TestRetrySendsTheLastMessageAgain(t *testing.T) {
	ff, sent, _ := recordingSession(t)
	say(t, ff, "split the lexer")
	say(t, ff, "/retry")
	if got := sent(); len(got) != 2 || got[1] != "split the lexer" {
		t.Fatalf("sent %q, want the last line twice", got)
	}
	if tr := transcriptOf(ff); strings.Count(tr, "user:split the lexer") != 1 || strings.Contains(tr, "/retry") {
		t.Errorf("transcript after /retry = %s", tr)
	}
}
