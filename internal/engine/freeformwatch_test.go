package engine

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
)

// TestAFreeformWatchReportsBackAsATurn: a backend with no Monitor of its
// own is offered gummi's watch; what the command prints and how it ended
// reach the agent later, as a turn gummi starts, shown in the transcript
// as an activity line rather than as something the person said.
func TestAFreeformWatchReportsBackAsATurn(t *testing.T) {
	var mu sync.Mutex
	var heard []string
	var offered []string
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		mu.Lock()
		defer mu.Unlock()
		heard = append(heard, msg)
		if len(heard) == 1 {
			for _, td := range opts.Tools {
				offered = append(offered, td.Name)
			}
			return []agent.Event{{Kind: agent.EventClientToolCall, ToolCall: &agent.ToolCall{
				ID: "c1", Name: watchToolName, Args: json.RawMessage(`{"command":"echo building; echo FAIL: TestX; exit 3"}`),
			}}, {Kind: agent.EventMessage, Text: "watching the build"}, {Kind: agent.EventIdle}}
		}
		return []agent.Event{{Kind: agent.EventMessage, Text: "TestX failed; looking"}, {Kind: agent.EventIdle}}
	}}
	ag.Caps = agent.Capabilities{ClientTools: true, UsageEvents: true, Interrupt: true}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
	t.Cleanup(func() { e.Close() })
	ctx := context.Background()
	f := freeformCard(1, "fix the build")
	createFeature(t, store, f)
	ff, err := e.OpenFreeform(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := ff.Send(ctx, "run the build and tell me"); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(testWaitTimeout)
	for {
		mu.Lock()
		n := len(heard)
		mu.Unlock()
		if n >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the watch never reported back")
		case <-time.After(20 * time.Millisecond):
		}
	}
	waitFreeformIdle(t, ff)

	mu.Lock()
	report := heard[1]
	tools := strings.Join(offered, ",")
	mu.Unlock()
	if !strings.Contains(tools, watchToolName) || !strings.Contains(tools, unwatchToolName) {
		t.Errorf("tools offered = %s, want watch and unwatch", tools)
	}
	for _, want := range []string{"[gummi watch w1", "FAIL: TestX", "exited 3"} {
		if !strings.Contains(report, want) {
			t.Errorf("the report lacks %q:\n%s", want, report)
		}
	}
	var users, notes []string
	for _, m := range ff.Session().Snapshot().Transcript {
		switch m.Author {
		case AuthorUser:
			users = append(users, m.Content)
		case AuthorTool:
			notes = append(notes, m.Content)
		}
	}
	if len(users) != 1 {
		t.Errorf("user turns = %q, want only the person's", users)
	}
	if !strings.Contains(strings.Join(notes, "\n"), "watch w1 · 2 line(s) · exited 3") {
		t.Errorf("activity = %q, want the watch's note", notes)
	}
	if len(ff.Watches()) != 0 {
		t.Errorf("watches = %v after the command exited", ff.Watches())
	}
}

// TestANativeWatchBackendIsNotOfferedGummis: Claude Code has Monitor, and
// two tools for one job only invites the wrong one.
func TestANativeWatchBackendIsNotOfferedGummis(t *testing.T) {
	var offered []string
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, _ string) []agent.Event {
		for _, td := range opts.Tools {
			offered = append(offered, td.Name)
		}
		return []agent.Event{{Kind: agent.EventMessage, Text: "ok"}, {Kind: agent.EventIdle}}
	}}
	ag.Caps = agent.Capabilities{ClientTools: true, NativeWatch: true}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
	t.Cleanup(func() { e.Close() })
	f := freeformCard(1, "fix the build")
	createFeature(t, store, f)
	ff, err := e.OpenFreeform(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if err := ff.Send(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	waitFreeformIdle(t, ff)
	if strings.Contains(strings.Join(offered, ","), watchToolName) {
		t.Errorf("tools offered = %v, want no gummi watch", offered)
	}
}

// TestUnwatchStopsAWatch: a stopped watch reports "stopped", and closing
// the session ends every watch it had.
func TestUnwatchStopsAWatch(t *testing.T) {
	ag := &agent.Fake{}
	ag.Caps = agent.Capabilities{ClientTools: true}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
	t.Cleanup(func() { e.Close() })
	f := freeformCard(1, "tail it")
	createFeature(t, store, f)
	ff, err := e.OpenFreeform(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	id, err := ff.startWatch("sleep 60 | cat", 0)
	if err != nil {
		t.Fatal(err)
	}
	other, err := ff.startWatch("sleep 60", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !ff.stopWatch(id) || ff.stopWatch("w99") {
		t.Fatal("stopWatch answered wrong")
	}
	deadline := time.After(testWaitTimeout)
	for len(ff.Watches()) != 1 {
		select {
		case <-deadline:
			t.Fatalf("watches = %v, want only %s", ff.Watches(), other)
		case <-time.After(10 * time.Millisecond):
		}
	}
	done := make(chan struct{})
	go func() { _ = ff.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(testWaitTimeout):
		t.Fatal("closing the session hung on a running watch")
	}
	if len(ff.Watches()) != 0 {
		t.Errorf("watches = %v after close", ff.Watches())
	}
}
