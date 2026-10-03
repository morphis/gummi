package engine

import (
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// the shape of the verify stage that looped for 45 minutes: one call,
// refused by the backend's permission policy, retried word for word
const deniedCall = "The user has specified a rule which prevents you from using this specific tool call."

func toolTurn(n int, ok func(i int) bool, detail func(i int) string) []agent.Event {
	var evs []agent.Event
	for i := range n {
		id := fmt.Sprintf("c%d", i)
		out := "ok"
		if !ok(i) {
			out = deniedCall
		}
		evs = append(evs,
			agent.Event{Kind: agent.EventToolCall, Tool: "bash", Detail: detail(i), CallID: id},
			agent.Event{Kind: agent.EventToolResult, CallID: id, Result: &agent.ToolResult{OK: ok(i), Output: out}})
	}
	return append(evs, agent.Event{Kind: agent.EventIdle})
}

// runUntilTerminal runs one implement card on ag and returns the first
// EventError, or nil when the turn went idle first.
func runUntilTerminal(t *testing.T, ag *agent.Fake) (*Engine, error) {
	t.Helper()
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
	t.Cleanup(func() { e.Close() })
	f := feature(1, "one", domain.StageImplement)
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(testWaitTimeout)
	for {
		select {
		case ev := <-e.Events():
			switch ev.Kind {
			case EventError:
				return e, ev.Err
			case EventIdle:
				return e, nil
			}
		case <-deadline:
			t.Fatal("timed out waiting for the turn to end")
		}
	}
}

// A model that retries a refused call is stopped after toolLoopRepeatCap
// tries, the backend interrupted, and the card paused with an error that
// says what was refused — not left spinning until a person notices.
func TestAStageRetryingARefusedCallIsStopped(t *testing.T) {
	var interrupts atomic.Int32
	ag := &agent.Fake{
		OnInterrupt: func() { interrupts.Add(1) },
		Responder: func(agent.SessionOpts, string) []agent.Event {
			return toolTurn(600, func(int) bool { return false },
				func(int) string { return "HOME=/tmp/opencode/home make check" })
		},
	}
	e, err := runUntilTerminal(t, ag)
	var loop *errToolLoop
	if !errors.As(err, &loop) {
		t.Fatalf("run ended with %v, want an errToolLoop", err)
	}
	if loop.repeats != toolLoopRepeatCap {
		t.Errorf("stopped after %d repeats, want %d", loop.repeats, toolLoopRepeatCap)
	}
	for _, want := range []string{"5 times", "HOME=/tmp/opencode/home make check", "specified a rule"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
	waitState(t, e, "FD-001", StatePaused)
	if interrupts.Load() == 0 {
		t.Error("the looping backend was never interrupted")
	}
}

// Failures separated by successes are a session working, not looping:
// every success resets the streak, however many failures there are.
func TestFailuresBetweenSuccessesNeverStopAStage(t *testing.T) {
	ag := &agent.Fake{Responder: func(agent.SessionOpts, string) []agent.Event {
		return toolTurn(200, func(i int) bool { return i%4 == 3 },
			func(int) string { return "go test ./..." })
	}}
	if _, err := runUntilTerminal(t, ag); err != nil {
		t.Fatalf("run failed with %v, want a clean idle", err)
	}
}

// A model that varies the refused call a little each time never repeats
// one five times; the streak cap is what catches it.
func TestAStreakOfDifferentFailuresIsStopped(t *testing.T) {
	var s Session
	var err error
	for i := 0; err == nil && i < 100; i++ {
		err = s.noteToolOutcome("bash", fmt.Sprintf("ls /tmp/try-%d", i), false, deniedCall)
	}
	var loop *errToolLoop
	if !errors.As(err, &loop) || loop.streak != toolLoopStreakCap {
		t.Fatalf("got %v, want a stop at a streak of %d", err, toolLoopStreakCap)
	}
	if !strings.Contains(err.Error(), "20 tool calls in a row failed") {
		t.Errorf("error %q does not say it was a streak", err)
	}
}
