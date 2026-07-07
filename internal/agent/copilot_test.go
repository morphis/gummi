package agent

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	copilot "github.com/github/copilot-sdk/go"

	"github.com/morphis/gummi/internal/agent/fakeopenai"
)

// findCopilot locates the CLI so the test can drive it; it skips the
// test when the binary is absent (CI without Copilot installed).
func findCopilot(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("COPILOT_CLI_PATH"); p != "" {
		return p
	}
	p, err := exec.LookPath("copilot")
	if err != nil {
		// the installer drops it under ~/.local/bin, which may be off PATH
		if home, herr := os.UserHomeDir(); herr == nil {
			cand := home + "/.local/bin/copilot"
			if _, serr := os.Stat(cand); serr == nil {
				return cand
			}
		}
		t.Skip("copilot CLI not found; skipping real round-trip")
	}
	return p
}

// TestCopilotBYOKRoundTrip is the phase-7 spike as a test: start the
// CLI via the SDK, open a BYOK session against a local fake OpenAI
// server, send one message, and assert the streamed reply + usage.
// This needs no GitHub authentication — BYOK routes the model call to
// the fake server, and the CLI's server mode does not gate BYOK on a
// GitHub session.
func TestCopilotBYOKRoundTrip(t *testing.T) {
	cli := findCopilot(t)

	srv := fakeopenai.New(fakeopenai.WithReply("hello from byok"))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	ag, err := NewCopilot(ctx, CopilotOptions{CLIPath: cli, LogLevel: "error"})
	if err != nil {
		t.Skipf("cannot start copilot CLI (no session/network?): %v", err)
	}
	defer ag.Close()

	caps := ag.Capabilities()
	if !caps.BYOK || !caps.Interrupt || !caps.UsageEvents || !caps.Resume {
		t.Errorf("unexpected capabilities: %+v", caps)
	}

	wd, _ := os.Getwd()
	sess, err := ag.NewSession(ctx, SessionOpts{
		WorkDir:    wd,
		Role:       RoleScribe,
		Model:      "fake-model",
		Permission: PermissionAllowAll,
		Provider:   Provider{Type: "openai", BaseURL: srv.BaseURL()},
	})
	if err != nil {
		t.Fatalf("create BYOK session: %v", err)
	}
	defer sess.Close()

	if err := sess.Send(ctx, "Say hello."); err != nil {
		t.Fatalf("send: %v", err)
	}

	var gotMessage string
	var sawUsage bool
	deadline := time.After(60 * time.Second)
loop:
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				break loop
			}
			switch ev.Kind {
			case EventMessage:
				gotMessage = ev.Text
			case EventUsage:
				sawUsage = true
			case EventError:
				t.Fatalf("session error: %v", ev.Err)
			case EventIdle:
				break loop
			}
		case <-deadline:
			t.Fatal("timed out waiting for the agent")
		}
	}

	if gotMessage != "hello from byok" {
		t.Errorf("assistant message = %q, want the fake reply", gotMessage)
	}
	if !sawUsage {
		t.Error("no usage event observed")
	}
	// the fake provider must have actually been called (BYOK routing)
	reqs := srv.Requests()
	if len(reqs) == 0 {
		t.Fatal("fake provider received no requests — BYOK not routed")
	}
	if reqs[0].Model != "fake-model" {
		t.Errorf("provider called with model %q", reqs[0].Model)
	}
}

// TestCopilotOnEventMapsStreamingEvents drives onEvent with synthetic
// SDK events (no CLI needed) and checks the mid-turn events a live view
// depends on — tool starts, reasoning deltas, text deltas — reach the
// session stream instead of being dropped.
func TestCopilotOnEventMapsStreamingEvents(t *testing.T) {
	s := &copilotSession{raw: make(chan Event, 8), stop: make(chan struct{})}
	s.onEvent(copilot.SessionEvent{Data: &copilot.ToolExecutionStartData{ToolName: "read"}})
	s.onEvent(copilot.SessionEvent{Data: &copilot.AssistantReasoningDeltaData{DeltaContent: "hmm, "}})
	s.onEvent(copilot.SessionEvent{Data: &copilot.AssistantMessageDeltaData{DeltaContent: "hello"}})

	want := []Event{
		{Kind: EventToolCall, Tool: "read"},
		{Kind: EventReasoningDelta, Text: "hmm, "},
		{Kind: EventTextDelta, Text: "hello"},
	}
	for i, w := range want {
		select {
		case got := <-s.raw:
			if got.Kind != w.Kind || got.Text != w.Text || got.Tool != w.Tool {
				t.Errorf("event %d = %+v, want %+v", i, got, w)
			}
		default:
			t.Fatalf("event %d (%+v) was dropped", i, w)
		}
	}
}

// TestCopilotUsageFallback verifies per-call metering when the CLI never
// sends the authoritative usage event (streamed BYOK calls): the
// completed message's token count is flushed as usage at idle — and NOT
// when the authoritative event did cover the call.
func TestCopilotUsageFallback(t *testing.T) {
	newSess := func() *copilotSession {
		return &copilotSession{
			raw:          make(chan Event, 16),
			stop:         make(chan struct{}),
			pendingUsage: map[string]Usage{},
			meteredCalls: map[string]struct{}{},
		}
	}
	drain := func(s *copilotSession) []Event {
		var out []Event
		for {
			select {
			case e := <-s.raw:
				out = append(out, e)
			default:
				return out
			}
		}
	}
	toks := int64(42)
	call := "chatcmpl-1"
	model := "m"

	// no authoritative usage → the message's count is metered at idle.
	s := newSess()
	s.onEvent(copilot.SessionEvent{Data: &copilot.AssistantMessageData{
		Content: "hi", APICallID: &call, OutputTokens: &toks, Model: &model,
	}})
	s.onEvent(copilot.SessionEvent{Data: &copilot.SessionIdleData{}})
	var usages []Usage
	var last EventKind
	for _, e := range drain(s) {
		if e.Kind == EventUsage {
			usages = append(usages, e.Usage)
		}
		last = e.Kind
	}
	if len(usages) != 1 || usages[0].OutputTokens != 42 || usages[0].Model != "m" {
		t.Errorf("fallback usage = %+v, want one with 42 output tokens", usages)
	}
	if last != EventIdle {
		t.Errorf("last event = %v, want idle after the usage flush", last)
	}

	// authoritative usage present → exactly one usage event, no fallback.
	s = newSess()
	s.onEvent(copilot.SessionEvent{Data: &copilot.AssistantMessageData{
		Content: "hi", APICallID: &call, OutputTokens: &toks, Model: &model,
	}})
	s.onEvent(copilot.SessionEvent{Data: &copilot.AssistantUsageData{
		APICallID: &call, OutputTokens: &toks, Model: model,
	}})
	s.onEvent(copilot.SessionEvent{Data: &copilot.SessionIdleData{}})
	usages = nil
	for _, e := range drain(s) {
		if e.Kind == EventUsage {
			usages = append(usages, e.Usage)
		}
	}
	if len(usages) != 1 {
		t.Errorf("got %d usage events, want exactly 1 (no double-count): %+v", len(usages), usages)
	}
}

// TestCopilotCloseClosesSessionChannels verifies Agent.Close() closes
// outstanding sessions' Events channels, so a `for range Events()`
// consumer does not leak when the agent is torn down without an
// explicit per-session Close.
func TestCopilotCloseClosesSessionChannels(t *testing.T) {
	cli := findCopilot(t)
	srv := fakeopenai.New(fakeopenai.WithReply("bye"))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	ag, err := NewCopilot(ctx, CopilotOptions{CLIPath: cli, LogLevel: "error"})
	if err != nil {
		t.Skipf("cannot start copilot CLI: %v", err)
	}
	wd, _ := os.Getwd()
	sess, err := ag.NewSession(ctx, SessionOpts{
		WorkDir:  wd,
		Model:    "fake-model",
		Provider: Provider{Type: "openai", BaseURL: srv.BaseURL()},
	})
	if err != nil {
		t.Fatal(err)
	}

	drained := make(chan struct{})
	go func() {
		for range sess.Events() { //nolint:revive // draining until close
		}
		close(drained)
	}()

	// close the agent WITHOUT closing the session; the consumer must
	// still observe the channel closing.
	if err := ag.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-drained:
	case <-time.After(10 * time.Second):
		t.Fatal("Events channel did not close after Agent.Close()")
	}
}
