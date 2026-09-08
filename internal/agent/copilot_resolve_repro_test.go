package agent

import (
	"context"
	"testing"
	"time"

	copilot "github.com/github/copilot-sdk/go"
)

// TestCopilotResolveAfterTeardownMustNotClaimSuccess is the copilot twin
// of the engine's TestAnswerAbandonedResolverMustNotReturnNilSilently.
//
// toolHandler parks on `select { case <-ans; case <-s.stop }`. When the
// session is torn down, the s.stop arm wins: the model is handed
// "cancelled — proceed with your best judgment" and the turn moves on.
// But the handler does NOT delete its entry from s.pending, and Close
// never drains the map either — so the callID lingers.
//
// A later Resolve then finds that stale entry, drops the answer into a
// cap-1 buffer that nobody will ever read, and returns nil. engine's
// AnswerAs treats that nil as delivery: it calls resumeAfterAnswer and
// returns success, so the user's answer is recorded and silently lost
// while the agent has already been told to proceed without it.
//
// Resolve must report that the call is no longer waiting.
func TestCopilotResolveAfterTeardownMustNotClaimSuccess(t *testing.T) {
	s := &copilotSession{
		raw:     make(chan Event, 8),
		events:  make(chan Event, 8),
		stop:    make(chan struct{}),
		pending: map[string]chan string{},
	}

	done := make(chan copilot.ToolResult, 1)
	go func() {
		res, _ := s.toolHandler("ask_user")(copilot.ToolInvocation{ToolCallID: "call-1"})
		done <- res
	}()

	// let the handler register itself and park
	waitFor := time.After(2 * time.Second)
	for {
		s.mu.Lock()
		registered := s.pending["call-1"] != nil
		s.mu.Unlock()
		if registered {
			break
		}
		select {
		case <-waitFor:
			t.Fatal("tool handler never registered its pending call")
		default:
			time.Sleep(time.Millisecond)
		}
	}

	// teardown: the handler's s.stop arm fires and the model is told to
	// proceed without an answer.
	close(s.stop)
	res := <-done
	t.Logf("model was handed: %q", res.TextResultForLLM)

	// the operator now answers the question they were still being shown.
	err := s.Resolve(context.Background(), "call-1", "CLI flag")
	if err == nil {
		t.Error("Resolve returned nil after the handler gave up: the answer " +
			"went into a buffer nobody reads, and AnswerAs will report success")
	} else {
		t.Logf("Resolve correctly reported: %v", err)
	}
}
