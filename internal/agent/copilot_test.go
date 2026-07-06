package agent

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

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
