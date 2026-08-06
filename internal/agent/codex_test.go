package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodexMapLifecycleAndUsage(t *testing.T) {
	s := &codexSession{model: "gpt-test"}
	if _, _, err := s.mapLine([]byte(`{"type":"thread.started","thread_id":"thr_42"}`)); err != nil {
		t.Fatal(err)
	}
	if got := s.SessionID(); got != "thr_42" {
		t.Fatalf("SessionID = %q", got)
	}
	evs, terminal, err := s.mapLine([]byte(`{"type":"item.completed","item":{"id":"m1","type":"agent_message","text":"done"}}`))
	if err != nil || terminal || len(evs) != 1 || evs[0].Kind != EventMessage || evs[0].Text != "done" {
		t.Fatalf("message = %#v, %v, %v", evs, terminal, err)
	}
	evs, terminal, err = s.mapLine([]byte(`{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":25,"reasoning_output_tokens":12}}`))
	if err != nil || !terminal || len(evs) != 2 {
		t.Fatalf("completed = %#v, %v, %v", evs, terminal, err)
	}
	u := evs[0].Usage
	if u.InputTokens != 60 || u.CachedTokens != 40 || u.OutputTokens != 25 {
		t.Fatalf("usage = %#v", u)
	}
}

func TestCodexMalformedAndFailure(t *testing.T) {
	s := &codexSession{}
	if _, _, err := s.mapLine([]byte(`not-json`)); err == nil {
		t.Fatal("malformed JSON accepted")
	}
	if _, terminal, err := s.mapLine([]byte(`{"type":"turn.failed","error":{"message":"boom"}}`)); err == nil || !terminal || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("failure = %v, %v", terminal, err)
	}
}

func TestCodexGuardedRejected(t *testing.T) {
	c := &Codex{bin: "codex"}
	_, err := c.NewSession(context.Background(), SessionOpts{Model: "gpt", Permission: PermissionGuarded})
	if err == nil || !strings.Contains(err.Error(), "allow-all") {
		t.Fatalf("error = %v", err)
	}
}

func TestCodexIdentityCapabilitiesAndMissingBinary(t *testing.T) {
	c := &Codex{bin: "codex"}
	if c.Name() != "codex" || c.CreditRate("gpt") != 0 {
		t.Fatalf("identity/rate = %q/%v", c.Name(), c.CreditRate("gpt"))
	}
	caps := c.Capabilities()
	if !caps.Resume || !caps.UsageEvents || !caps.Interrupt || caps.ClientTools {
		t.Fatalf("capabilities = %#v", caps)
	}
	if _, err := NewCodex(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing binary accepted")
	}
}

func TestCodexFirstAndResumeInvocation(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	bin := filepath.Join(dir, "codex")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> \"$CODEX_TEST_LOG\"\n" +
		"IFS= read -r prompt; printf 'prompt=%s\\n' \"$prompt\" >> \"$CODEX_TEST_LOG\"\n" +
		"printf '%s\\n' '{\"type\":\"thread.started\",\"thread_id\":\"thr_test\"}' '{\"type\":\"turn.completed\",\"usage\":{}}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_TEST_LOG", log)
	c, err := NewCodex(bin)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	sess, err := c.NewSession(context.Background(), SessionOpts{WorkDir: dir, Model: "gpt-x", SystemHints: []string{"hint"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range []string{"first", "second"} {
		if err := sess.Send(context.Background(), msg); err != nil {
			t.Fatal(err)
		}
		for {
			select {
			case ev := <-sess.Events():
				if ev.Kind == EventIdle {
					goto idle
				}
				if ev.Kind == EventError {
					t.Fatal(ev.Err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("timeout")
			}
		}
	idle:
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	if !strings.Contains(got, "exec --json --color never -m gpt-x --dangerously-bypass-approvals-and-sandbox -") {
		t.Fatalf("first argv missing:\n%s", got)
	}
	if !strings.Contains(got, "resume thr_test -") {
		t.Fatalf("resume argv missing:\n%s", got)
	}
	if strings.Count(got, "prompt=hint") != 1 || !strings.Contains(got, "prompt=second") {
		t.Fatalf("prompts wrong:\n%s", got)
	}
}

func TestCodexLiveRoundTrip(t *testing.T) {
	if os.Getenv("GUMMI_CODEX_TEST") != "1" {
		t.Skip("set GUMMI_CODEX_TEST=1 for authenticated real CLI test")
	}
	c, err := NewCodex(os.Getenv("GUMMI_CODEX_BIN"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	sess, err := c.NewSession(context.Background(), SessionOpts{WorkDir: t.TempDir(), Model: "gpt-5", Permission: PermissionAllowAll})
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Send(context.Background(), "Reply with exactly PONG"); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case ev := <-sess.Events():
			if ev.Kind == EventError {
				t.Fatal(ev.Err)
			}
			if ev.Kind == EventIdle {
				return
			}
		case <-time.After(2 * time.Minute):
			t.Fatal("timeout")
		}
	}
}
