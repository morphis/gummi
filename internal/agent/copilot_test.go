package agent

import (
	"context"
	"os"
	"os/exec"
	"strings"
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
	s.onEvent(copilot.SessionEvent{Data: &copilot.ToolExecutionStartData{
		ToolName:  "read",
		Arguments: map[string]any{"path": "internal/ui/chat.go"},
	}})
	s.onEvent(copilot.SessionEvent{Data: &copilot.AssistantReasoningDeltaData{DeltaContent: "hmm, "}})
	s.onEvent(copilot.SessionEvent{Data: &copilot.AssistantMessageDeltaData{DeltaContent: "hello"}})

	want := []Event{
		{Kind: EventToolCall, Tool: "read", Detail: "internal/ui/chat.go"},
		{Kind: EventReasoningDelta, Text: "hmm, "},
		{Kind: EventTextDelta, Text: "hello"},
	}
	for i, w := range want {
		select {
		case got := <-s.raw:
			if got.Kind != w.Kind || got.Text != w.Text || got.Tool != w.Tool || got.Detail != w.Detail {
				t.Errorf("event %d = %+v, want %+v", i, got, w)
			}
		default:
			t.Fatalf("event %d (%+v) was dropped", i, w)
		}
	}
}

// TestCopilotToolResultRoundTrip drives the real CLI end to end through
// a tool execution: the scripted BYOK model requests a shell command,
// the CLI runs it, and the adapter must surface both the tool call and
// its captured result (output + call-id pairing) — the events the
// transcript's forensic view is built on.
func TestCopilotToolResultRoundTrip(t *testing.T) {
	cli := findCopilot(t)

	srv := fakeopenai.New(
		fakeopenai.WithReply("command ran"),
		fakeopenai.WithToolCall("bash", `{"command":"echo gummi-tool-e2e"}`))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	ag, err := NewCopilot(ctx, CopilotOptions{CLIPath: cli, LogLevel: "error"})
	if err != nil {
		t.Skipf("cannot start copilot CLI (no session/network?): %v", err)
	}
	defer ag.Close()

	wd := t.TempDir()
	sess, err := ag.NewSession(ctx, SessionOpts{
		WorkDir:    wd,
		Role:       RoleImplementer,
		Model:      "fake-model",
		Permission: PermissionAllowAll,
		Provider:   Provider{Type: "openai", BaseURL: srv.BaseURL()},
	})
	if err != nil {
		t.Fatalf("create BYOK session: %v", err)
	}
	defer sess.Close()

	if id, ok := sess.(Identified); !ok || id.SessionID() == "" {
		t.Error("copilot session does not expose its session id")
	}

	if err := sess.Send(ctx, "Run the echo command."); err != nil {
		t.Fatalf("send: %v", err)
	}

	var call, result *Event
	deadline := time.After(60 * time.Second)
loop:
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				break loop
			}
			switch ev.Kind {
			case EventToolCall:
				e := ev
				call = &e
			case EventToolResult:
				e := ev
				result = &e
			case EventError:
				t.Fatalf("session error: %v", ev.Err)
			case EventIdle:
				break loop
			}
		case <-deadline:
			t.Fatal("timed out waiting for the agent")
		}
	}

	if call == nil || call.CallID == "" {
		t.Fatalf("no tool call observed (call=%+v)", call)
	}
	if result == nil {
		t.Fatal("no tool result observed — completions are being dropped")
	}
	if result.CallID != call.CallID {
		t.Errorf("result call id %q != call id %q", result.CallID, call.CallID)
	}
	if result.Result == nil || !result.Result.OK {
		t.Errorf("result = %+v, want OK", result.Result)
	}
	if !strings.Contains(result.Result.Output, "gummi-tool-e2e") {
		t.Errorf("captured output %q missing the command's stdout", result.Result.Output)
	}
}

// TestCopilotOnEventMapsToolResults verifies tool completions reach the
// stream as EventToolResult — the failure message leading the output and
// the call id carried through, so the engine can attach the outcome to
// the tool line it already displayed.
func TestCopilotOnEventMapsToolResults(t *testing.T) {
	s := &copilotSession{raw: make(chan Event, 8), stop: make(chan struct{})}
	detailed := "full build log"
	s.onEvent(copilot.SessionEvent{Data: &copilot.ToolExecutionCompleteData{
		ToolCallID: "call-ok", Success: true,
		Result: &copilot.ToolExecutionCompleteResult{Content: "concise", DetailedContent: &detailed},
	}})
	s.onEvent(copilot.SessionEvent{Data: &copilot.ToolExecutionCompleteData{
		ToolCallID: "call-fail", Success: false,
		Error:  &copilot.ToolExecutionCompleteError{Message: "device already exists"},
		Result: &copilot.ToolExecutionCompleteResult{Content: "lxc launch output"},
	}})

	ok := <-s.raw
	if ok.Kind != EventToolResult || ok.CallID != "call-ok" ||
		ok.Result == nil || !ok.Result.OK || ok.Result.Output != "full build log" {
		t.Errorf("success event = %+v (result %+v), want detailed content preferred", ok, ok.Result)
	}
	fail := <-s.raw
	if fail.Kind != EventToolResult || fail.CallID != "call-fail" ||
		fail.Result == nil || fail.Result.OK {
		t.Fatalf("failure event = %+v (result %+v)", fail, fail.Result)
	}
	if fail.Result.Output != "device already exists\nlxc launch output" {
		t.Errorf("failure output = %q, want the error message leading the output", fail.Result.Output)
	}
}

// TestBoundTailKeepsFailureTail verifies output bounding keeps the end
// of the text (where errors land) and marks the cut.
func TestBoundTailKeepsFailureTail(t *testing.T) {
	long := strings.Repeat("x", toolOutputCapFail) + "the error"
	got := boundTail(long, false)
	if len(got) > toolOutputCapFail+len("…(truncated)\n") {
		t.Errorf("failure output len = %d, want ≤ cap", len(got))
	}
	if !strings.HasPrefix(got, "…(truncated)\n") || !strings.HasSuffix(got, "the error") {
		t.Errorf("bounded output lost the tail: %q…%q", got[:20], got[len(got)-20:])
	}
	if s := boundTail("short", true); s != "short" {
		t.Errorf("short output changed: %q", s)
	}
	if ok := boundTail(strings.Repeat("y", toolOutputCapOK+1), true); len(ok) > toolOutputCapOK+len("…(truncated)\n") {
		t.Errorf("success output len = %d, want the tighter cap", len(ok))
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

// TestCopilotUsageCostPrecedence checks the cost source precedence and the
// token folding in the AssistantUsageData mapping: the authoritative CAPI
// figure (nano-AIU → credits) wins over the Experimental Cost; Cost is used
// when CopilotUsage is absent; reasoning tokens fold into output; and cache
// read tokens are captured.
func TestCopilotUsageCostPrecedence(t *testing.T) {
	sess := func() *copilotSession {
		return &copilotSession{
			raw:          make(chan Event, 8),
			stop:         make(chan struct{}),
			pendingUsage: map[string]Usage{},
			meteredCalls: map[string]struct{}{},
		}
	}
	usageFrom := func(d *copilot.AssistantUsageData) Usage {
		s := sess()
		s.onEvent(copilot.SessionEvent{Data: d})
		select {
		case e := <-s.raw:
			if e.Kind != EventUsage {
				t.Fatalf("event kind = %v, want usage", e.Kind)
			}
			return e.Usage
		default:
			t.Fatal("no usage event emitted")
			return Usage{}
		}
	}
	f64 := func(v float64) *float64 { return &v }
	i64 := func(v int64) *int64 { return &v }

	// CopilotUsage.TotalNanoAiu wins over the Experimental Cost.
	u := usageFrom(&copilot.AssistantUsageData{
		Model:        "gpt-5-codex",
		Cost:         f64(9),
		CopilotUsage: &copilot.AssistantUsageCopilotUsage{TotalNanoAiu: 42e9},
	})
	if u.Credits != 42 {
		t.Errorf("credits = %v, want 42 (nano-AIU/1e9 over Cost)", u.Credits)
	}

	// no CopilotUsage → fall back to Cost.
	u = usageFrom(&copilot.AssistantUsageData{Model: "m", Cost: f64(7)})
	if u.Credits != 7 {
		t.Errorf("credits = %v, want 7 (Cost fallback)", u.Credits)
	}

	// a zero TotalNanoAiu is not authoritative — fall through to Cost.
	u = usageFrom(&copilot.AssistantUsageData{
		Model:        "m",
		Cost:         f64(3),
		CopilotUsage: &copilot.AssistantUsageCopilotUsage{TotalNanoAiu: 0},
	})
	if u.Credits != 3 {
		t.Errorf("credits = %v, want 3 (zero nano-AIU falls through to Cost)", u.Credits)
	}

	// neither cost source → 0 credits (token-priced downstream).
	u = usageFrom(&copilot.AssistantUsageData{Model: "m", InputTokens: i64(100)})
	if u.Credits != 0 {
		t.Errorf("credits = %v, want 0 (no cost source)", u.Credits)
	}

	// reasoning folds into output; cache read captured; input passthrough.
	u = usageFrom(&copilot.AssistantUsageData{
		Model:           "m",
		InputTokens:     i64(1000),
		OutputTokens:    i64(200),
		ReasoningTokens: i64(50),
		CacheReadTokens: i64(300),
	})
	if u.InputTokens != 1000 {
		t.Errorf("input = %d, want 1000", u.InputTokens)
	}
	if u.OutputTokens != 250 {
		t.Errorf("output = %d, want 250 (200 output + 50 reasoning)", u.OutputTokens)
	}
	if u.CachedTokens != 300 {
		t.Errorf("cached = %d, want 300 (cache read)", u.CachedTokens)
	}
}
