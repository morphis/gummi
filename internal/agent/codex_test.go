package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
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

// writeCodexEchoBin writes a fake `codex` shell script that appends its argv
// to the file named by $CODEX_LOG and then emits a thread.started + empty
// turn.completed pair, letting tests observe the exact argv codex receives.
func writeCodexEchoBin(t *testing.T, bin, log string) {
	t.Helper()
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> \"$CODEX_LOG\"\n" +
		"IFS= read -r prompt; printf 'prompt=%s\\n' \"$prompt\" >> \"$CODEX_LOG\"\n" +
		"printf '%s\\n' '{\"type\":\"thread.started\",\"thread_id\":\"thr_test\"}' '{\"type\":\"turn.completed\",\"usage\":{}}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_LOG", log)
}

func waitCodexIdle(t *testing.T, sess Session) {
	t.Helper()
	for {
		select {
		case ev := <-sess.Events():
			if ev.Kind == EventError {
				t.Fatal(ev.Err)
			}
			if ev.Kind == EventIdle {
				return
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for idle")
		}
	}
}

func TestCodexGuardedNowAccepted(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	bin := filepath.Join(dir, "codex")
	writeCodexEchoBin(t, bin, log)
	c, err := NewCodex(bin)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var argvLines []string
	for _, perm := range []Permission{PermissionGuarded, PermissionAllowAll} {
		if err := os.Remove(log); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		sess, err := c.NewSession(context.Background(), SessionOpts{WorkDir: t.TempDir(), Model: "gpt-x", Permission: perm})
		if err != nil {
			t.Fatalf("NewSession(%v): %v", perm, err)
		}
		if err := sess.Send(context.Background(), "ping"); err != nil {
			t.Fatalf("Send(%v): %v", perm, err)
		}
		waitCodexIdle(t, sess)
		raw, err := os.ReadFile(log)
		if err != nil {
			t.Fatal(err)
		}
		argvLines = append(argvLines, strings.TrimSpace(string(raw)))
	}
	if argvLines[0] != argvLines[1] {
		t.Fatalf("guarded and allow-all argv differ:\nGUARDED:\n%s\nALLOW-ALL:\n%s", argvLines[0], argvLines[1])
	}
	if strings.Contains(argvLines[0], "--dangerously-bypass-approvals-and-sandbox") {
		t.Fatalf("bypass flag still emitted under guarded:\n%s", argvLines[0])
	}
}

func TestCodexIdentityCapabilitiesAndMissingBinary(t *testing.T) {
	c := &Codex{bin: "codex"}
	if c.Name() != "codex" || c.CreditRate("gpt") != 0 {
		t.Fatalf("identity/rate = %q/%v", c.Name(), c.CreditRate("gpt"))
	}
	caps := c.Capabilities()
	if !caps.Resume || !caps.UsageEvents || !caps.Interrupt || caps.ClientTools || !caps.MCPTools {
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
	writeCodexEchoBin(t, bin, log)
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
		waitCodexIdle(t, sess)
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	if !strings.Contains(got, "exec --json --color never -m gpt-x -s workspace-write -c approval_policy=\"never\" --skip-git-repo-check --ignore-user-config -") {
		t.Fatalf("first argv missing:\n%s", got)
	}
	if !strings.Contains(got, "resume thr_test -") {
		t.Fatalf("resume argv missing:\n%s", got)
	}
	if strings.Contains(got, "--dangerously-bypass-approvals-and-sandbox") {
		t.Fatalf("bypass flag still emitted:\n%s", got)
	}
	if strings.Count(got, "prompt=hint") != 1 || !strings.Contains(got, "prompt=second") {
		t.Fatalf("prompts wrong:\n%s", got)
	}
}

func TestCodexArgvShapes(t *testing.T) {
	prev := codexExecPath
	codexExecPath = func() (string, error) { return "/opt/gummi-stub", nil }
	t.Cleanup(func() { codexExecPath = prev })

	cases := []struct {
		name      string
		feature   string
		sock      string
		workspace bool
		wantMCP   bool
	}{
		{name: "on", feature: "FD-013", sock: "/tmp/mcp/FD-013.sock", wantMCP: true},
		{name: "off-nofeature", feature: "", sock: "/tmp/mcp/x.sock", wantMCP: false},
		{name: "off-nosock", feature: "FD-013", sock: "", wantMCP: false},
		{name: "workspace-on", sock: "/tmp/mcp/ws.sock", workspace: true, wantMCP: true},
		{name: "workspace-off-nosock", sock: "", workspace: true, wantMCP: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			log := filepath.Join(dir, "calls")
			bin := filepath.Join(dir, "codex")
			writeCodexEchoBin(t, bin, log)
			c, err := NewCodex(bin)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			sess, err := c.NewSession(context.Background(), SessionOpts{
				WorkDir: dir, Model: "gpt-x", FeatureID: tc.feature, MCPSockPath: tc.sock, Workspace: tc.workspace,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := sess.Send(context.Background(), "ping"); err != nil {
				t.Fatal(err)
			}
			waitCodexIdle(t, sess)
			raw, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			got := string(raw)
			for _, tok := range []string{"exec --json", "-s workspace-write", "approval_policy=\"never\"", "--skip-git-repo-check", "--ignore-user-config"} {
				if !strings.Contains(got, tok) {
					t.Errorf("argv missing %q:\n%s", tok, got)
				}
			}
			if strings.Contains(got, "--dangerously-bypass-approvals-and-sandbox") {
				t.Errorf("bypass flag present:\n%s", got)
			}
			switch tc.wantMCP {
			case true:
				if !strings.Contains(got, "-c mcp_servers.gummi=") {
					t.Errorf("MCP-on argv missing -c override:\n%s", got)
				}
				if tc.workspace {
					if !strings.Contains(got, `args=["__mcp","--workspace"]`) {
						t.Errorf("workspace override missing --workspace args:\n%s", got)
					}
					if strings.Contains(got, `"--feature"`) {
						t.Errorf("workspace override unexpectedly names --feature:\n%s", got)
					}
				} else {
					if !strings.Contains(got, `args=["__mcp","--feature","`+tc.feature+`"]`) {
						t.Errorf("feature override missing --feature args:\n%s", got)
					}
					if strings.Contains(got, "--workspace") {
						t.Errorf("feature override unexpectedly names --workspace:\n%s", got)
					}
				}
			case false:
				if strings.Contains(got, "-c mcp_servers.gummi=") {
					t.Errorf("MCP-off argv has unexpected -c override:\n%s", got)
				}
			}
		})
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
	workdir := t.TempDir()
	sess, err := c.NewSession(context.Background(), SessionOpts{WorkDir: workdir, Model: "gpt-5", Permission: PermissionAllowAll})
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Send(context.Background(), "Reply with exactly PONG"); err != nil {
		t.Fatal(err)
	}
	waitCodexIdle(t, sess)

	// cwd cage: a write outside the worktree must be refused.
	outside := filepath.Join(os.TempDir(), fmt.Sprintf("gummi-outside-cage-%d", os.Getpid()))
	_ = os.Remove(outside)
	if err := sess.Send(context.Background(),
		fmt.Sprintf("Use the write tool to write the text CANARY to the file %s. The sandbox should refuse.", outside)); err != nil {
		t.Fatal(err)
	}
	waitCodexIdle(t, sess)
	if _, err := os.Stat(outside); err == nil {
		t.Errorf("codex wrote outside the cage: %s exists", outside)
	}

	// a write inside the worktree succeeds.
	inside := filepath.Join(workdir, "inside.txt")
	if err := sess.Send(context.Background(),
		fmt.Sprintf("Use the write tool to write the text CANARY to the file %s. This must succeed.", inside)); err != nil {
		t.Fatal(err)
	}
	waitCodexIdle(t, sess)
	if b, err := os.ReadFile(inside); err != nil || string(b) != "CANARY" {
		t.Errorf("codex inside-write failed: %q, %v", b, err)
	}
}

// TestCodexRealArgvParses spawns the real codex binary with the adapter's
// exact argv and asserts it does not fail at argument parsing. It exists
// because the emit-side tests only capture argv verbatim through a stub and
// cannot detect an argv the real CLI rejects. It needs no credentials: an
// auth or provider error downstream is a pass — it proves the argv parsed.
//
// It runs twice: the base argv shape, and the MCP-on shape whose inline
// `-c mcp_servers.gummi=…` value is a TOML table, so a future codex version
// rejecting either is caught here the same way the `-a` regression was.
func TestCodexRealArgvParses(t *testing.T) {
	bin, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("codex binary not present; skipping real-argv parse check")
	}
	prev := codexExecPath
	codexExecPath = func() (string, error) { return "/opt/gummi-stub", nil }
	t.Cleanup(func() { codexExecPath = prev })
	cases := []struct {
		name    string
		feature string
		sock    string
	}{
		{name: "base"},
		{name: "mcp-on", feature: "FD-013", sock: "/tmp/mcp/x.sock"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &codexSession{model: "gpt-test", featureID: tc.feature, mcpSock: tc.sock}
			args, err := s.buildArgs()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, bin, args...)
			cmd.Dir = t.TempDir()
			cmd.Stdin = strings.NewReader("Reply with PONG\n")
			var stdout, stderr strings.Builder
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			_ = cmd.Run()
			out := strings.ToLower(stderr.String()) + strings.ToLower(stdout.String())
			if strings.Contains(out, "unexpected argument") || strings.Contains(out, "error: unexpected") {
				t.Fatalf("codex rejected adapter argv at parse time:\nargv: %s\nstderr: %s", strings.Join(args, " "), stderr.String())
			}
		})
	}
}

// A ReadOnly research session must never run on a backend that cannot
// structurally strip its write tools: codex advertises
// ReadOnlyEnforce:false, so NewSession refuses instead of silently running
// read-write over the main checkout.
func TestCodexRejectsReadOnly(t *testing.T) {
	c := &Codex{bin: "codex"}
	defer c.Close()
	if _, err := c.NewSession(context.Background(), SessionOpts{WorkDir: t.TempDir(), Model: "gpt-x", ReadOnly: true}); err == nil ||
		!strings.Contains(err.Error(), "read-only") {
		t.Errorf("ReadOnly session error = %v, want a clear read-only rejection", err)
	}
}
