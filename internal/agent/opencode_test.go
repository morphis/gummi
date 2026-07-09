package agent

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func newOCSession() *opencodeSession {
	return &opencodeSession{model: "opencode/x", partLen: map[string]int{}, raw: make(chan Event, 8), stop: make(chan struct{})}
}

func TestOpencodeMapEventText(t *testing.T) {
	s := newOCSession()
	var msg strings.Builder
	// first text part
	evs := s.mapEvent([]byte(`{"type":"text","sessionID":"ses_1","part":{"id":"p1","type":"text","text":"Hello"}}`), &msg)
	if len(evs) != 1 || evs[0].Kind != EventTextDelta || evs[0].Text != "Hello" {
		t.Fatalf("text delta = %+v, want one EventTextDelta 'Hello'", evs)
	}
	// same part streamed further: only the new suffix is emitted
	evs = s.mapEvent([]byte(`{"type":"text","part":{"id":"p1","type":"text","text":"Hello, world"}}`), &msg)
	if len(evs) != 1 || evs[0].Text != ", world" {
		t.Fatalf("cumulative delta = %+v, want ', world'", evs)
	}
	if msg.String() != "Hello, world" {
		t.Errorf("accumulated message = %q, want 'Hello, world'", msg.String())
	}
	// session id captured from the first event
	if s.sessionID != "ses_1" {
		t.Errorf("sessionID = %q, want ses_1", s.sessionID)
	}
}

func TestOpencodeMapEventToolAndUsage(t *testing.T) {
	s := newOCSession()
	var msg strings.Builder
	evs := s.mapEvent([]byte(`{"type":"tool_use","part":{"type":"tool","tool":"read","callID":"c1","state":{"input":{"filePath":"internal/ui/chat.go"}}}}`), &msg)
	if len(evs) != 1 || evs[0].Kind != EventToolCall || evs[0].Tool != "read" || evs[0].Detail != "internal/ui/chat.go" {
		t.Fatalf("tool = %+v, want EventToolCall read internal/ui/chat.go", evs)
	}
	// args with no displayable value fall back to opencode's rendered title
	evs = s.mapEvent([]byte(`{"type":"tool_use","part":{"type":"tool","tool":"todo","state":{"title":"3 todos","input":{"todos":[]}}}}`), &msg)
	if len(evs) != 1 || evs[0].Detail != "3 todos" {
		t.Fatalf("tool = %+v, want title fallback '3 todos'", evs)
	}
	evs = s.mapEvent([]byte(`{"type":"step_finish","part":{"type":"step-finish","tokens":{"input":100,"output":20},"cost":0.05}}`), &msg)
	// step_finish yields a usage event plus a context event (input≈context)
	if len(evs) != 2 || evs[0].Kind != EventUsage || evs[1].Kind != EventContext {
		t.Fatalf("step_finish = %+v, want [usage, context]", evs)
	}
	u := evs[0].Usage
	// cost 0.05 USD → 5 credits ($0.01 units); tokens carried through
	if u.InputTokens != 100 || u.OutputTokens != 20 || u.Credits < 4.99 || u.Credits > 5.01 {
		t.Errorf("usage = %+v, want in100/out20/credits~5", u)
	}
	if evs[1].Context.Tokens != 100 {
		t.Errorf("context tokens = %d, want 100 (step input)", evs[1].Context.Tokens)
	}
}

func TestOpencodeMapEventFlushesSegmentBeforeTool(t *testing.T) {
	s := newOCSession()
	var msg strings.Builder
	// prose, then a tool call, then more prose: the tool call must flush the
	// first segment as its own EventMessage (before the tool) and reset the
	// accumulator, so the final message carries only the trailing segment —
	// otherwise the whole turn's text is emitted once and duplicates the
	// pre-tool prose in the transcript.
	if evs := s.mapEvent([]byte(`{"type":"text","part":{"id":"p1","text":"Looking at the failure."}}`), &msg); len(evs) != 1 || evs[0].Kind != EventTextDelta {
		t.Fatalf("text = %+v, want one EventTextDelta", evs)
	}
	evs := s.mapEvent([]byte(`{"type":"tool_use","part":{"type":"tool","tool":"read"}}`), &msg)
	if len(evs) != 2 || evs[0].Kind != EventMessage || evs[0].Text != "Looking at the failure." || evs[1].Kind != EventToolCall {
		t.Fatalf("tool = %+v, want [EventMessage(segment), EventToolCall]", evs)
	}
	if msg.String() != "" {
		t.Errorf("accumulator = %q, want reset after flush", msg.String())
	}
	if evs := s.mapEvent([]byte(`{"type":"text","part":{"id":"p2","text":"Found it."}}`), &msg); len(evs) != 1 || evs[0].Text != "Found it." {
		t.Fatalf("second text = %+v, want one delta 'Found it.'", evs)
	}
	if msg.String() != "Found it." {
		t.Errorf("final accumulator = %q, want only the trailing segment", msg.String())
	}
}

func TestOpencodeMapEventIgnoresLifecycle(t *testing.T) {
	s := newOCSession()
	var msg strings.Builder
	if evs := s.mapEvent([]byte(`{"type":"step_start","part":{"type":"step-start"}}`), &msg); len(evs) != 0 {
		t.Errorf("step_start produced events: %+v", evs)
	}
	if evs := s.mapEvent([]byte(`not json at all`), &msg); len(evs) != 0 {
		t.Errorf("non-JSON produced events: %+v", evs)
	}
}

func TestOpencodeRequiresModel(t *testing.T) {
	o := &Opencode{bin: "opencode"}
	if _, err := o.NewSession(context.Background(), SessionOpts{WorkDir: t.TempDir()}); err == nil {
		t.Error("NewSession without a model should error")
	}
}

func TestOpencodeRejectsBYOKProvider(t *testing.T) {
	o := &Opencode{bin: "opencode"}
	_, err := o.NewSession(context.Background(), SessionOpts{
		WorkDir: t.TempDir(), Model: "openai/gpt-4",
		Provider: Provider{Type: "openai", BaseURL: "http://127.0.0.1:8080/v1"},
	})
	if err == nil || !strings.Contains(err.Error(), "opencode manages providers") {
		t.Errorf("BYOK provider should fail clearly, got %v", err)
	}
}

// fakeOC writes a fake `opencode` script that emits one text event then
// sleeps, so a turn can be interrupted mid-flight deterministically.
func fakeOC(t *testing.T) string {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	path := dir + "/opencode"
	body := "#!/bin/sh\n" +
		`echo '{"type":"text","sessionID":"ses_test","part":{"id":"p1","type":"text","text":"working"}}'` + "\n" +
		"sleep 10\n"
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	_ = sh
	return path
}

func TestOpencodeInterruptYieldsIdle(t *testing.T) {
	ag, err := NewOpencode(fakeOC(t))
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	ctx := context.Background()
	sess, err := ag.NewSession(ctx, SessionOpts{WorkDir: t.TempDir(), Model: "x"})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if err := sess.Send(ctx, "go"); err != nil {
		t.Fatal(err)
	}
	// wait for the first text event, then interrupt the (sleeping) turn
	deadline := time.After(5 * time.Second)
	got := false
	for !got {
		select {
		case e := <-sess.Events():
			if e.Kind == EventTextDelta {
				got = true
			}
		case <-deadline:
			t.Fatal("no text event before interrupt")
		}
	}
	if err := sess.Interrupt(ctx); err != nil {
		t.Fatal(err)
	}
	// the interrupted turn must end idle, never error
	for {
		select {
		case e := <-sess.Events():
			switch e.Kind {
			case EventIdle:
				return
			case EventError:
				t.Fatalf("interrupt surfaced as error: %v", e.Err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("interrupted turn never went idle")
		}
	}
}

func TestOpencodeRejectsConcurrentSend(t *testing.T) {
	ag, err := NewOpencode(fakeOC(t))
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	ctx := context.Background()
	sess, err := ag.NewSession(ctx, SessionOpts{WorkDir: t.TempDir(), Model: "x"})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if err := sess.Send(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	if err := sess.Send(ctx, "two"); err == nil {
		t.Error("a second Send during an in-flight turn should be rejected")
	}
}

// TestOpencodeLiveRoundTrip drives the real opencode binary against a free
// hosted model. It skips when opencode isn't installed, and treats an
// error/timeout (network or gateway trouble) as a skip — it verifies the
// adapter's mapping, not opencode's uptime.
func TestOpencodeLiveRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("opencode"); err != nil {
		t.Skip("opencode not installed")
	}
	ag, err := NewOpencode("opencode")
	if err != nil {
		t.Skip(err)
	}
	defer ag.Close()

	ctx := context.Background()
	sess, err := ag.NewSession(ctx, SessionOpts{
		WorkDir: t.TempDir(),
		Model:   "opencode/deepseek-v4-flash-free",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if err := sess.Send(ctx, "Reply with exactly one word: PONG"); err != nil {
		t.Fatal(err)
	}

	var text string
	var sawUsage, sawIdle bool
	deadline := time.After(90 * time.Second)
	for !sawIdle {
		select {
		case e := <-sess.Events():
			switch e.Kind {
			case EventTextDelta, EventMessage:
				if e.Kind == EventMessage {
					text = e.Text
				} else {
					text += e.Text
				}
			case EventUsage:
				sawUsage = true
			case EventIdle:
				sawIdle = true
			case EventError:
				t.Skipf("opencode/network unavailable: %v", e.Err)
			}
		case <-deadline:
			t.Skip("opencode did not respond in time (network?)")
		}
	}
	if !strings.Contains(strings.ToUpper(text), "PONG") {
		t.Errorf("reply %q did not contain PONG", text)
	}
	if !sawUsage {
		t.Error("no usage event from the turn")
	}
}
