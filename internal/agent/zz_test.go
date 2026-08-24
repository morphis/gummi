package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestZZMapLifecycleAndUsage(t *testing.T) {
	s := &zzSession{model: "m"}
	if _, _, err := s.mapLine([]byte(`{"type":"session","id":"sess_1"}`)); err != nil {
		t.Fatal(err)
	}
	if got := s.SessionID(); got != "sess_1" {
		t.Fatalf("SessionID = %q", got)
	}
	evs, terminal, err := s.mapLine([]byte(`{"type":"text","delta":"done"}`))
	if err != nil || terminal || len(evs) != 1 || evs[0].Kind != EventTextDelta || evs[0].Text != "done" {
		t.Fatalf("text = %#v, %v, %v", evs, terminal, err)
	}
	evs, terminal, err = s.mapLine([]byte(`{"type":"turn_end","usage":{"prompt_tokens":100,"completion_tokens":25,"prompt_tokens_details":{"cached_tokens":40}}}`))
	if err != nil || terminal || len(evs) != 2 {
		t.Fatalf("turn_end = %#v, %v, %v", evs, terminal, err)
	}
	u := evs[0].Usage
	if u.InputTokens != 60 || u.CachedTokens != 40 || u.OutputTokens != 25 || u.Model != "m" {
		t.Fatalf("usage = %#v", u)
	}
	if evs[1].Kind != EventMessage || evs[1].Text != "done" {
		t.Fatalf("message = %#v", evs[1])
	}
	evs, terminal, err = s.mapLine([]byte(`{"type":"done","stop_reason":"end_turn"}`))
	if err != nil || !terminal || len(evs) != 1 || evs[0].Kind != EventIdle {
		t.Fatalf("done = %#v, %v, %v", evs, terminal, err)
	}
}

func TestZZEventMappingTable(t *testing.T) {
	s := &zzSession{model: "m"}
	evs, _, err := s.mapLine([]byte(`{"type":"reasoning","delta":"thinking"}`))
	if err != nil || len(evs) != 1 || evs[0].Kind != EventReasoningDelta || evs[0].Text != "thinking" {
		t.Fatalf("reasoning = %#v, %v", evs, err)
	}
	evs, _, err = s.mapLine([]byte(`{"type":"tool_call","id":"c1","name":"bash","args":{"command":"ls -la"}}`))
	if err != nil || len(evs) != 1 || evs[0].Kind != EventToolCall || evs[0].CallID != "c1" || evs[0].Tool != "bash" || evs[0].Detail != "ls -la" {
		t.Fatalf("tool_call = %#v, %v", evs, err)
	}
	evs, _, err = s.mapLine([]byte(`{"type":"tool_result","id":"c1","name":"bash","ok":true,"content":"output"}`))
	if err != nil || len(evs) != 1 || evs[0].Kind != EventToolResult || evs[0].CallID != "c1" || evs[0].Result == nil || !evs[0].Result.OK {
		t.Fatalf("tool_result = %#v, %v", evs, err)
	}
	evs, _, err = s.mapLine([]byte(`{"type":"context_warning","est_tokens":1000,"budget":8000}`))
	if err != nil || len(evs) != 1 || evs[0].Kind != EventContext || evs[0].Context.Tokens != 1000 || evs[0].Context.Limit != 8000 {
		t.Fatalf("context_warning = %#v, %v", evs, err)
	}
	evs, _, err = s.mapLine([]byte(`{"type":"error","message":"boom"}`))
	if err != nil || len(evs) != 1 || evs[0].Kind != EventError || !strings.Contains(evs[0].Err.Error(), "boom") {
		t.Fatalf("error = %#v, %v", evs, err)
	}
	for _, typ := range []string{"thinking", "turn_start", "user"} {
		evs, terminal, err := s.mapLine([]byte(fmt.Sprintf(`{"type":%q}`, typ)))
		if err != nil || terminal || len(evs) != 0 {
			t.Fatalf("dropped type %q = %#v, %v, %v", typ, evs, terminal, err)
		}
	}
}

// TestZZToolSourceNamespacing proves tool_call/tool_result apply the same
// source-prefix rule (mirroring codex's mcp_tool_call convention) and that
// call/result correlation by CallID survives it.
func TestZZToolSourceNamespacing(t *testing.T) {
	cases := []struct {
		name   string
		source string
		want   string
	}{
		{"builtin", "builtin", "bash"},
		{"mcp-labeled", "mcp:gummi", "gummi.ask_user"},
		{"absent", "", "bash"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &zzSession{model: "m"}
			toolName := "bash"
			if tc.source == "mcp:gummi" {
				toolName = "ask_user"
			}
			call := fmt.Sprintf(`{"type":"tool_call","id":"c1","name":%q,"source":%q,"args":{}}`, toolName, tc.source)
			evs, _, err := s.mapLine([]byte(call))
			if err != nil || len(evs) != 1 || evs[0].Tool != tc.want {
				t.Fatalf("tool_call = %#v, %v, want Tool %q", evs, err, tc.want)
			}
			result := fmt.Sprintf(`{"type":"tool_result","id":"c1","name":%q,"source":%q,"ok":true,"content":""}`, toolName, tc.source)
			evs, _, err = s.mapLine([]byte(result))
			if err != nil || len(evs) != 1 || evs[0].Tool != tc.want || evs[0].CallID != "c1" {
				t.Fatalf("tool_result = %#v, %v, want Tool %q CallID c1", evs, err, tc.want)
			}
		})
	}
}

// TestZZSessionRequiresMCPRegistration covers the MCP-registration assertion
// added to the session arm of mapLine.
func TestZZSessionRequiresMCPRegistration(t *testing.T) {
	t.Run("builtin+mcp-labeled passes", func(t *testing.T) {
		s := &zzSession{model: "m", featureID: "F", mcpSock: "/tmp/s", mcpLabel: "gummi"}
		_, _, err := s.mapLine([]byte(`{"type":"session","id":"s1","tools":[{"name":"bash","source":"builtin"},{"name":"ask_user","source":"mcp:gummi"}]}`))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("builtin-only under MCP-configured session errors", func(t *testing.T) {
		s := &zzSession{model: "m", featureID: "F", mcpSock: "/tmp/s", mcpLabel: "gummi"}
		_, _, err := s.mapLine([]byte(`{"type":"session","id":"s1","tools":[{"name":"bash","source":"builtin"}]}`))
		if err == nil || !strings.Contains(err.Error(), "MCP") || !strings.Contains(err.Error(), "gummi") {
			t.Fatalf("err = %v, want a message naming MCP and the label", err)
		}
	})
	t.Run("builtin-only under non-MCP session is fine", func(t *testing.T) {
		s := &zzSession{model: "m"}
		_, _, err := s.mapLine([]byte(`{"type":"session","id":"s1","tools":[{"name":"bash","source":"builtin"}]}`))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("label follows the resolved executable basename", func(t *testing.T) {
		renamed := &zzSession{model: "m", featureID: "F", mcpSock: "/tmp/s", mcpLabel: "renamed"}
		if _, _, err := renamed.mapLine([]byte(`{"type":"session","id":"s1","tools":[{"name":"ask_user","source":"mcp:renamed"}]}`)); err != nil {
			t.Fatalf("mcp:renamed should be accepted for a renamed executable: %v", err)
		}
		if _, _, err := renamed.mapLine([]byte(`{"type":"session","id":"s1","tools":[{"name":"ask_user","source":"mcp:gummi"}]}`)); err == nil {
			t.Fatal("mcp:gummi should be rejected when the resolved label is \"renamed\"")
		}
	})
}

// TestZZWaitingSurfacesAsPair proves a waiting frame lifts to one bounded
// call+result pair rather than being silently dropped.
func TestZZWaitingSurfacesAsPair(t *testing.T) {
	s := &zzSession{model: "m"}
	evs, terminal, err := s.mapLine([]byte(`{"type":"waiting","turn":1,"status":429,"attempt":2,"delay_ms":4000}`))
	if err != nil || terminal {
		t.Fatalf("waiting = %#v, %v, %v", evs, terminal, err)
	}
	if len(evs) != 2 || evs[0].Kind != EventToolCall || evs[1].Kind != EventToolResult {
		t.Fatalf("waiting events = %#v", evs)
	}
	if evs[0].CallID == "" || evs[0].CallID != evs[1].CallID {
		t.Fatalf("waiting CallID pairing = %#v", evs)
	}
	if evs[1].Result == nil || !evs[1].Result.OK {
		t.Fatalf("waiting result = %#v", evs[1])
	}
	if evs[0].Detail != "http 429 · attempt 2 · retry in 4.0s" {
		t.Fatalf("waiting detail = %q", evs[0].Detail)
	}
}

// TestZZCompactionSurfacesAndUpdatesContext proves a compaction frame lifts
// to a bounded call+result pair plus a context-meter update, and that the
// meter's Limit tracks the most recent context_warning budget.
func TestZZCompactionSurfacesAndUpdatesContext(t *testing.T) {
	t.Run("no prior context_warning uses Limit 0", func(t *testing.T) {
		s := &zzSession{model: "m"}
		evs, _, err := s.mapLine([]byte(`{"type":"compaction","turn":2,"trigger":"auto","messages_folded":12,"bytes_before":1,"bytes_after":1,"est_tokens_before":48000,"est_tokens_after":12000,"content":"…"}`))
		if err != nil || len(evs) != 3 {
			t.Fatalf("compaction = %#v, %v", evs, err)
		}
		if evs[0].Kind != EventToolCall || evs[0].Tool != "compaction" || evs[0].Detail != "folded 12 messages · ~48000 -> ~12000 tokens" {
			t.Fatalf("compaction call = %#v", evs[0])
		}
		if evs[1].Kind != EventToolResult || evs[1].CallID != evs[0].CallID || evs[1].Result == nil || !evs[1].Result.OK {
			t.Fatalf("compaction result = %#v", evs[1])
		}
		if evs[2].Kind != EventContext || evs[2].Context.Tokens != 12000 || evs[2].Context.Limit != 0 {
			t.Fatalf("compaction context = %#v", evs[2])
		}
	})
	t.Run("prior context_warning seeds Limit", func(t *testing.T) {
		s := &zzSession{model: "m"}
		if _, _, err := s.mapLine([]byte(`{"type":"context_warning","est_tokens":1000,"budget":8000}`)); err != nil {
			t.Fatal(err)
		}
		evs, _, err := s.mapLine([]byte(`{"type":"compaction","turn":2,"trigger":"auto","messages_folded":12,"bytes_before":1,"bytes_after":1,"est_tokens_before":48000,"est_tokens_after":12000,"content":"…"}`))
		if err != nil || len(evs) != 3 {
			t.Fatalf("compaction = %#v, %v", evs, err)
		}
		if evs[2].Kind != EventContext || evs[2].Context.Tokens != 12000 || evs[2].Context.Limit != 8000 {
			t.Fatalf("compaction context = %#v", evs[2])
		}
	})
	t.Run("long content does not unbound the detail", func(t *testing.T) {
		s := &zzSession{model: "m"}
		big := strings.Repeat("x", 10*1024)
		line := fmt.Sprintf(`{"type":"compaction","turn":2,"trigger":"auto","messages_folded":12,"bytes_before":1,"bytes_after":1,"est_tokens_before":48000,"est_tokens_after":12000,"content":%q}`, big)
		evs, _, err := s.mapLine([]byte(line))
		if err != nil || len(evs) != 3 {
			t.Fatalf("compaction = %#v, %v", evs, err)
		}
		if r := []rune(evs[0].Detail); len(r) > detailCap+len("…") {
			t.Fatalf("detail length = %d, want <= %d", len(r), detailCap+len("…"))
		}
		if strings.Contains(evs[0].Detail, "xxxx") {
			t.Fatalf("detail leaked compaction content: %q", evs[0].Detail)
		}
	})
}

func TestZZToolCallDetail(t *testing.T) {
	s := &zzSession{model: "m"}
	evs, _, err := s.mapLine([]byte(`{"type":"tool_call","id":"c2","name":"read","args":{"path":"internal/agent/zz.go"}}`))
	if err != nil || len(evs) != 1 || evs[0].Detail != "internal/agent/zz.go" {
		t.Fatalf("path arg = %#v, %v", evs, err)
	}

	evs, _, err = s.mapLine([]byte(`{"type":"tool_call","id":"c3","name":"bash","args":"echo hi"}`))
	if err != nil || len(evs) != 1 || evs[0].Detail != "echo hi" {
		t.Fatalf("string args = %#v, %v", evs, err)
	}

	ws := &zzSession{model: "m", workdir: "/wt"}
	evs, _, err = ws.mapLine([]byte(`{"type":"tool_call","id":"c4","name":"read","args":{"path":"/wt/internal/agent/zz.go"}}`))
	if err != nil || len(evs) != 1 || evs[0].Detail != "internal/agent/zz.go" {
		t.Fatalf("absolute path under workdir should be repo-relative = %#v, %v", evs, err)
	}

	evs, _, err = s.mapLine([]byte(`{"type":"tool_call","id":"c5","name":"weird","args":{"unknown":1}}`))
	if err != nil || len(evs) != 1 || evs[0].Kind != EventToolCall || evs[0].Tool != "weird" || evs[0].CallID != "c5" || evs[0].Detail != "" {
		t.Fatalf("no displayable args = %#v, %v", evs, err)
	}
}

func TestZZBuildArgs(t *testing.T) {
	prev := zzExecPath
	zzExecPath = func() (string, error) { return "/opt/gummi-stub", nil }
	t.Cleanup(func() { zzExecPath = prev })

	s := &zzSession{model: "m", sessionPath: "/tmp/sess.json", workdir: "/work"}
	args, err := s.buildArgs()
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(args, " ")
	for _, tok := range []string{"-p", "--model m", "--session /tmp/sess.json", "--cwd /work", "ask"} {
		if !strings.Contains(got, tok) {
			t.Errorf("argv missing %q: %s", tok, got)
		}
	}
	if strings.Contains(got, "--continue") {
		t.Errorf("turn 1 argv has --continue: %s", got)
	}
	if strings.Contains(got, "--mcp") {
		t.Errorf("argv has unexpected --mcp: %s", got)
	}
	if strings.Contains(got, "--provider") {
		t.Errorf("argv has unexpected --provider (Provider unset): %s", got)
	}
	if args[len(args)-1] != "ask" {
		t.Errorf("ask must be the last token (prompt appended by Send): %v", args)
	}

	s.primed = true
	args, err = s.buildArgs()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(args, " "), "--continue") {
		t.Errorf("turn 2 missing --continue: %v", args)
	}

	mcp := &zzSession{model: "m", sessionPath: "/tmp/sess.json", workdir: "/work", featureID: "FD-100", mcpSock: "/tmp/mcp.sock"}
	args, err = mcp.buildArgs()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(args, " "), "--mcp /opt/gummi-stub __mcp --feature FD-100") {
		t.Errorf("mcp argv wrong: %v", args)
	}

	noFeature := &zzSession{model: "m", sessionPath: "/tmp/sess.json", workdir: "/work", mcpSock: "/tmp/mcp.sock"}
	args, err = noFeature.buildArgs()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(args, " "), "--mcp") {
		t.Errorf("mcp emitted without a feature id: %v", args)
	}

	suppressed := &zzSession{model: "m", sessionPath: "/tmp/sess.json", workdir: "/work", cwdSuppressed: true}
	args, err = suppressed.buildArgs()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(args, " "), "--cwd") {
		t.Errorf("cwd should be suppressed: %v", args)
	}
}

// TestZZBuildArgsProvider proves --provider is emitted immediately after
// --model when SessionOpts.Provider is set (and NewSession copies it onto
// the session), so a role can pick its zz endpoint per-profile.
func TestZZBuildArgsProvider(t *testing.T) {
	s := &zzSession{model: "m", provider: "mab", sessionPath: "/tmp/sess.json", workdir: "/work"}
	args, err := s.buildArgs()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-p", "--model", "m", "--provider", "mab", "--session", "/tmp/sess.json", "--cwd", "/work", "ask"}
	if strings.Join(args, " ") != strings.Join(want, " ") {
		t.Errorf("argv = %v, want %v", args, want)
	}
}

func TestZZRefusesReadOnly(t *testing.T) {
	z := &ZZ{bin: "zz"}
	defer z.Close()
	if _, err := z.NewSession(context.Background(), SessionOpts{WorkDir: t.TempDir(), Model: "m", ReadOnly: true}); err == nil ||
		!strings.Contains(err.Error(), "read-only") {
		t.Errorf("ReadOnly session error = %v, want a clear read-only rejection", err)
	}
}

func TestZZRefusesGuarded(t *testing.T) {
	z := &ZZ{bin: "zz"}
	defer z.Close()
	if _, err := z.NewSession(context.Background(), SessionOpts{WorkDir: t.TempDir(), Model: "m", Permission: PermissionGuarded}); err == nil ||
		!strings.Contains(err.Error(), "guarded") {
		t.Errorf("Guarded session error = %v, want a clear guarded rejection", err)
	}
}

func TestZZRefusesEmptyModel(t *testing.T) {
	z := &ZZ{bin: "zz"}
	defer z.Close()
	if _, err := z.NewSession(context.Background(), SessionOpts{WorkDir: t.TempDir()}); err == nil {
		t.Fatal("empty model accepted")
	}
}

func TestZZRefusesWhitespaceExecPath(t *testing.T) {
	prev := zzExecPath
	zzExecPath = func() (string, error) { return "/opt/gummi stub/gummi", nil }
	t.Cleanup(func() { zzExecPath = prev })
	z := &ZZ{bin: "zz"}
	defer z.Close()
	_, err := z.NewSession(context.Background(), SessionOpts{
		WorkDir: t.TempDir(), Model: "m", FeatureID: "FD-100", MCPSockPath: "/tmp/mcp.sock",
	})
	if err == nil || !strings.Contains(err.Error(), "whitespace") {
		t.Errorf("whitespace exec path error = %v, want a clear rejection", err)
	}
}

// writeZZEchoBin writes a fake `zz` shell script that appends its argv,
// followed by a line recording $GUMMI_MCP_SOCK, to the file named by
// $ZZ_LOG, then emits a session + empty turn_end + end_turn done triple,
// letting tests observe the exact argv and env zz receives. The session
// line advertises an mcp-labeled tool matching the real (unstubbed)
// zzExecPath basename, so a caller that sets FeatureID+MCPSockPath (like
// TestZZSendSetsMCPSockEnv) does not trip the MCP-registration assertion.
func writeZZEchoBin(t *testing.T, bin, log string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	label := filepath.Base(exe)
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> \"$ZZ_LOG\"\n" +
		"printf 'MCP_SOCK=%s\\n' \"$GUMMI_MCP_SOCK\" >> \"$ZZ_LOG\"\n" +
		"printf '%s\\n' '{\"type\":\"session\",\"id\":\"zz_test\",\"tools\":[{\"name\":\"ask_user\",\"source\":\"mcp:" + label + "\"}]}' " +
		"'{\"type\":\"turn_end\",\"usage\":{}}' '{\"type\":\"done\",\"stop_reason\":\"end_turn\"}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ZZ_LOG", log)
}

// writeZZBinWithLines writes a fake `zz` shell script that emits exactly
// the given JSONL lines, none of which may contain a single quote.
func writeZZBinWithLines(t *testing.T, bin string, lines []string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	for _, l := range lines {
		b.WriteString("printf '%s\\n' '" + l + "'\n")
	}
	if err := os.WriteFile(bin, []byte(b.String()), 0o700); err != nil {
		t.Fatal(err)
	}
}

func waitZZIdle(t *testing.T, sess Session) {
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

func TestZZRefusesOversizedPrompt(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	bin := filepath.Join(dir, "zz")
	writeZZEchoBin(t, bin, log)
	z, err := NewZZ(bin)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	sess, err := z.NewSession(context.Background(), SessionOpts{WorkDir: dir, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("x", zzArgvPromptMaxBytes+1)
	if err := sess.Send(context.Background(), big); err == nil || !strings.Contains(err.Error(), "argv limit") {
		t.Errorf("oversized prompt error = %v", err)
	}
}

func TestZZFirstAndResumeInvocation(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	bin := filepath.Join(dir, "zz")
	writeZZEchoBin(t, bin, log)
	z, err := NewZZ(bin)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	sess, err := z.NewSession(context.Background(), SessionOpts{WorkDir: dir, Model: "m", SystemHints: []string{"hint"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range []string{"first", "second"} {
		if err := sess.Send(context.Background(), msg); err != nil {
			t.Fatal(err)
		}
		waitZZIdle(t, sess)
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	if strings.Count(got, "--continue") != 1 {
		t.Fatalf("--continue should appear exactly once (turn 2 only):\n%s", got)
	}
	if !strings.Contains(got, "hint") || !strings.Contains(got, "second") {
		t.Fatalf("prompts wrong:\n%s", got)
	}
}

// TestZZSendSetsMCPSockEnv proves the spawned child's environment carries
// GUMMI_MCP_SOCK when --mcp is emitted (zz's --mcp has no env table of its
// own, so the socket path must reach it via cmd.Env).
func TestZZSendSetsMCPSockEnv(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	bin := filepath.Join(dir, "zz")
	writeZZEchoBin(t, bin, log)
	z, err := NewZZ(bin)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	sock := filepath.Join(dir, "mcp.sock")
	sess, err := z.NewSession(context.Background(), SessionOpts{
		WorkDir: dir, Model: "m", FeatureID: "FD-100", MCPSockPath: sock,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Send(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	waitZZIdle(t, sess)
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	if !strings.Contains(got, "MCP_SOCK="+sock) {
		t.Fatalf("child env missing GUMMI_MCP_SOCK=%s:\n%s", sock, got)
	}
}

func TestZZUsageSplit(t *testing.T) {
	t.Run("usage present", func(t *testing.T) {
		s := &zzSession{model: "m"}
		evs, _, err := s.mapLine([]byte(`{"type":"turn_end","usage":{"prompt_tokens":100,"completion_tokens":25,"prompt_tokens_details":{"cached_tokens":40}}}`))
		if err != nil {
			t.Fatal(err)
		}
		u := evs[0].Usage
		if u.InputTokens != 60 || u.CachedTokens != 40 || u.OutputTokens != 25 {
			t.Fatalf("usage = %#v", u)
		}
	})
	t.Run("derived fallback", func(t *testing.T) {
		s := &zzSession{model: "m"}
		evs, _, err := s.mapLine([]byte(`{"type":"turn_end","usage":null,"derived":{"actual_prompt_tokens":100,"cached_tokens":40,"completion_tokens":25}}`))
		if err != nil {
			t.Fatal(err)
		}
		u := evs[0].Usage
		if u.InputTokens != 60 || u.CachedTokens != 40 || u.OutputTokens != 25 {
			t.Fatalf("derived usage = %#v", u)
		}
	})
	t.Run("cached exceeds prompt clamps at zero", func(t *testing.T) {
		s := &zzSession{model: "m"}
		evs, _, err := s.mapLine([]byte(`{"type":"turn_end","usage":{"prompt_tokens":10,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":40}}}`))
		if err != nil {
			t.Fatal(err)
		}
		if got := evs[0].Usage.InputTokens; got != 0 {
			t.Fatalf("InputTokens = %d, want 0", got)
		}
	})
}

func TestZZStreamAbortsWithoutDone(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "zz")
	writeZZBinWithLines(t, bin, []string{`{"type":"session","id":"s1"}`})
	z, err := NewZZ(bin)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	sess, err := z.NewSession(context.Background(), SessionOpts{WorkDir: dir, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Send(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-sess.Events():
		if ev.Kind != EventError {
			t.Fatalf("kind = %v, want EventError", ev.Kind)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for error")
	}
}

func TestZZDoneNonEndTurn(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "zz")
	writeZZBinWithLines(t, bin, []string{
		`{"type":"session","id":"s1"}`,
		`{"type":"turn_end","usage":{}}`,
		`{"type":"done","stop_reason":"max_turns"}`,
	})
	z, err := NewZZ(bin)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	sess, err := z.NewSession(context.Background(), SessionOpts{WorkDir: dir, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Send(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-sess.Events():
			switch ev.Kind {
			case EventError:
				if !strings.Contains(ev.Err.Error(), "max_turns") {
					t.Fatalf("error = %v, want mention of max_turns", ev.Err)
				}
				return
			case EventIdle:
				t.Fatal("got EventIdle, want EventError for a non-end_turn stop reason")
			}
		case <-deadline:
			t.Fatal("timeout")
		}
	}
}

func TestZZInterruptEndsIdle(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "zz")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' '{\"type\":\"session\",\"id\":\"s1\"}'\n" +
		"sleep 30\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	z, err := NewZZ(bin)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	sess, err := z.NewSession(context.Background(), SessionOpts{WorkDir: dir, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Send(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	if err := sess.Interrupt(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-sess.Events():
		if ev.Kind != EventIdle {
			t.Fatalf("kind = %v, want EventIdle after interrupt", ev.Kind)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for idle after interrupt")
	}
}

func TestZZCreditRate(t *testing.T) {
	z := &ZZ{bin: "zz"}
	if got := z.CreditRate("m"); got != 0 {
		t.Errorf("unset = %v, want 0", got)
	}
	t.Setenv(zzCreditRateEnv, "1.5")
	if got := z.CreditRate("m"); got != 1.5 {
		t.Errorf("valid = %v, want 1.5", got)
	}
	t.Setenv(zzCreditRateEnv, "not-a-number")
	if got := z.CreditRate("m"); got != 0 {
		t.Errorf("malformed = %v, want 0", got)
	}
	t.Setenv(zzCreditRateEnv, "-1")
	if got := z.CreditRate("m"); got != 0 {
		t.Errorf("negative = %v, want 0", got)
	}
}

func TestZZIdentityAndCapabilities(t *testing.T) {
	z := &ZZ{bin: "zz"}
	if z.Name() != "zz" {
		t.Fatalf("Name = %q", z.Name())
	}
	caps := z.Capabilities()
	if !caps.Resume || !caps.UsageEvents || !caps.Interrupt || !caps.MCPTools || caps.ClientTools || caps.ReadOnlyEnforce {
		t.Fatalf("capabilities = %#v", caps)
	}
	if _, err := NewZZ(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing binary accepted")
	}
}
