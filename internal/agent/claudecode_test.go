package agent

import (
	"context"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeFakeClaude writes an executable python script standing in for the
// claude binary (the adapter execs a single path, so unlike writeFakeAgent
// the script itself must be the executable, via shebang).
func writeFakeClaude(t *testing.T, body string) string {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	path := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(path, []byte("#!/usr/bin/env python3\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// fakeClaudeScript replays recorded stream-json protocol lines (shapes from
// the P0 probes against CLI 2.1.204): two turns against one model, with
// cumulative modelUsage on the result lines — exercising the delta/estimate
// settlement math end to end.
const fakeClaudeScript = `import sys, json
def out(o):
    sys.stdout.write(json.dumps(o)+"\n"); sys.stdout.flush()
MODEL = "claude-test-1"
turn = 0
for line in sys.stdin:
    line = line.strip()
    if not line: continue
    m = json.loads(line)
    if m.get("type") != "user": continue
    turn += 1
    out({"type":"system","subtype":"init","session_id":"sess-1","model":MODEL,"permissionMode":"bypassPermissions"})
    out({"type":"stream_event","event":{"type":"message_start","message":{"model":MODEL}}})
    if turn == 1:
        out({"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"th"}}})
        out({"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"he"}}})
        out({"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"llo"}}})
        out({"type":"assistant","message":{"model":MODEL,"content":[{"type":"text","text":"hello"}]}})
        out({"type":"assistant","message":{"model":MODEL,"content":[{"type":"tool_use","name":"Bash","input":{"command":"true"}}]}})
        out({"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"ok"}]}})
        out({"type":"stream_event","event":{"type":"message_delta","delta":{},"usage":{"input_tokens":10,"output_tokens":20,"cache_read_input_tokens":70,"cache_creation_input_tokens":0}}})
        out({"type":"result","subtype":"success","is_error":False,"session_id":"sess-1",
             "modelUsage":{MODEL:{"inputTokens":10,"outputTokens":20,"cacheReadInputTokens":70,"cacheCreationInputTokens":0,"costUSD":0.01,"contextWindow":200000}}})
    else:
        out({"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"ok"}}})
        out({"type":"assistant","message":{"model":MODEL,"content":[{"type":"text","text":"ok"}]}})
        out({"type":"stream_event","event":{"type":"message_delta","delta":{},"usage":{"input_tokens":20,"output_tokens":30,"cache_read_input_tokens":50,"cache_creation_input_tokens":0}}})
        out({"type":"result","subtype":"success","is_error":False,"session_id":"sess-1",
             "modelUsage":{MODEL:{"inputTokens":30,"outputTokens":50,"cacheReadInputTokens":120,"cacheCreationInputTokens":0,"costUSD":0.025,"contextWindow":200000}}})
`

func TestClaudeCodeRoundTripAndSettlement(t *testing.T) {
	ag, err := NewClaudeCode(writeFakeClaude(t, fakeClaudeScript))
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	caps := ag.Capabilities()
	if caps.BYOK || caps.ClientTools || !caps.Resume || !caps.UsageEvents || !caps.Interrupt {
		t.Errorf("capabilities = %+v", caps)
	}

	ctx := context.Background()
	sess, err := ag.NewSession(ctx, SessionOpts{WorkDir: t.TempDir(), Permission: PermissionAllowAll})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	// --- turn 1: no realized rate yet → token-only mid-turn usage, full
	// settlement at result.
	if err := sess.Send(ctx, "hello"); err != nil {
		t.Fatal(err)
	}
	evs := collect(t, sess)
	var kinds []EventKind
	for _, e := range evs {
		kinds = append(kinds, e.Kind)
	}
	want := []EventKind{
		EventReasoningDelta, EventTextDelta, EventTextDelta,
		EventMessage, EventToolCall,
		EventUsage,   // mid-turn (message_delta), Credits 0 on turn one
		EventContext, // at result
		EventUsage,   // settlement
		EventIdle,
	}
	if strings.Join(kindStrings(kinds), ",") != strings.Join(kindStrings(want), ",") {
		t.Fatalf("turn 1 kinds = %v, want %v", kinds, want)
	}
	if evs[3].Text != "hello" || evs[4].Tool != "Bash" || evs[4].Detail != "true" {
		t.Errorf("message/tool = %q/%q(%q)", evs[3].Text, evs[4].Tool, evs[4].Detail)
	}
	mid, ctxEv, settle := evs[5].Usage, evs[6].Context, evs[7].Usage
	if mid.Credits != 0 || mid.InputTokens != 10 || mid.OutputTokens != 20 || mid.Model != "claude-test-1" {
		t.Errorf("turn 1 mid-turn usage = %+v (want token-only, no rate yet)", mid)
	}
	if ctxEv.Tokens != 80 || ctxEv.Limit != 200000 {
		t.Errorf("turn 1 context = %+v, want 80/200000", ctxEv)
	}
	// settlement: 0.01 USD × 100, nothing estimated to subtract
	if math.Abs(settle.Credits-1.0) > 1e-9 || settle.Model != "claude-test-1" {
		t.Errorf("turn 1 settlement = %+v, want ≈1.0 credits", settle)
	}

	// --- turn 2: the realized rate (0.01 USD / 100 tokens) prices the
	// mid-turn request, and settlement is the cumulative-cost delta minus
	// that estimate — total credits stay equal to the CLI's actual cost.
	if err := sess.Send(ctx, "again"); err != nil {
		t.Fatal(err)
	}
	evs = collect(t, sess)
	var usages []Usage
	var ctx2 Context
	for _, e := range evs {
		if e.Kind == EventUsage {
			usages = append(usages, e.Usage)
		}
		if e.Kind == EventContext {
			ctx2 = e.Context
		}
	}
	if len(usages) != 2 {
		t.Fatalf("turn 2 usage events = %+v, want mid-turn + settlement", usages)
	}
	if math.Abs(usages[0].Credits-1.0) > 1e-6 || usages[0].InputTokens != 20 || usages[0].OutputTokens != 30 {
		t.Errorf("turn 2 mid-turn estimate = %+v, want ≈1.0 credits (100 tokens at realized rate)", usages[0])
	}
	if math.Abs(usages[1].Credits-0.5) > 1e-6 {
		t.Errorf("turn 2 settlement = %+v, want ≈0.5 ((0.025-0.01)×100 − 1.0)", usages[1])
	}
	if math.Abs((usages[0].Credits+usages[1].Credits)-1.5) > 1e-6 {
		t.Errorf("turn 2 total = %v, want the CLI's actual turn cost 1.5", usages[0].Credits+usages[1].Credits)
	}
	if ctx2.Tokens != 70 || ctx2.Limit != 200000 {
		t.Errorf("turn 2 context = %+v, want 70/200000", ctx2)
	}
}

// interruptFakeScript starts a turn and never finishes it on its own: only
// an interrupt control_request produces the error_during_execution result
// (the shape the real CLI emitted in P0). A second user turn then succeeds,
// proving the session survives.
const interruptFakeScript = `import sys, json
def out(o):
    sys.stdout.write(json.dumps(o)+"\n"); sys.stdout.flush()
MODEL = "claude-test-1"
started = False
for line in sys.stdin:
    line = line.strip()
    if not line: continue
    m = json.loads(line)
    t = m.get("type")
    if t == "user":
        out({"type":"system","subtype":"init","session_id":"s","model":MODEL})
        if not started:
            started = True
            out({"type":"stream_event","event":{"type":"message_start","message":{"model":MODEL}}})
            out({"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"counting"}}})
        else:
            out({"type":"assistant","message":{"model":MODEL,"content":[{"type":"text","text":"alive"}]}})
            out({"type":"result","subtype":"success","is_error":False,
                 "modelUsage":{MODEL:{"inputTokens":2,"outputTokens":2,"cacheReadInputTokens":0,"cacheCreationInputTokens":0,"costUSD":0.002,"contextWindow":200000}}})
    elif t == "control_request" and m.get("request",{}).get("subtype") == "interrupt":
        out({"type":"control_response","response":{"subtype":"success","request_id":m.get("request_id")}})
        out({"type":"result","subtype":"error_during_execution","is_error":True,"result":None,
             "modelUsage":{MODEL:{"inputTokens":1,"outputTokens":1,"cacheReadInputTokens":0,"cacheCreationInputTokens":0,"costUSD":0.001,"contextWindow":200000}}})
`

func TestClaudeCodeInterruptEndsIdle(t *testing.T) {
	ag, err := NewClaudeCode(writeFakeClaude(t, interruptFakeScript))
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	ctx := context.Background()
	sess, err := ag.NewSession(ctx, SessionOpts{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	if err := sess.Send(ctx, "count forever"); err != nil {
		t.Fatal(err)
	}
	// wait until the turn is visibly streaming, then interrupt it
	deadline := time.After(5 * time.Second)
awaitDelta:
	for {
		select {
		case e := <-sess.Events():
			if e.Kind == EventTextDelta {
				break awaitDelta
			}
		case <-deadline:
			t.Fatal("no delta before interrupt")
		}
	}
	if err := sess.Interrupt(ctx); err != nil {
		t.Fatal(err)
	}
	evs := collect(t, sess)
	last := evs[len(evs)-1]
	if last.Kind != EventIdle {
		t.Fatalf("interrupted turn ended with %v (err=%v), want idle", last.Kind, last.Err)
	}
	for _, e := range evs {
		if e.Kind == EventError {
			t.Fatalf("interrupted turn surfaced an error: %v", e.Err)
		}
	}

	// the session must accept the next turn (the CLI keeps running)
	if err := sess.Send(ctx, "still there?"); err != nil {
		t.Fatal(err)
	}
	evs = collect(t, sess)
	var msg string
	for _, e := range evs {
		if e.Kind == EventMessage {
			msg = e.Text
		}
	}
	if msg != "alive" {
		t.Errorf("post-interrupt turn message = %q, want alive", msg)
	}
}

func TestClaudeCodeArgsPlumbing(t *testing.T) {
	// the fake echoes its argv, cwd, and the received turn text back as the
	// assistant message, proving the CLI flags and frames are built right.
	script := `import sys, json, os
def out(o):
    sys.stdout.write(json.dumps(o)+"\n"); sys.stdout.flush()
for line in sys.stdin:
    line = line.strip()
    if not line: continue
    m = json.loads(line)
    if m.get("type") != "user": continue
    text = "argv=" + " ".join(sys.argv[1:]) + " cwd=" + os.getcwd() + " msg=" + m["message"]["content"][0]["text"]
    out({"type":"assistant","message":{"model":"m","content":[{"type":"text","text":text}]}})
    out({"type":"result","subtype":"success","is_error":False,"modelUsage":{}})
`
	ag, err := NewClaudeCode(writeFakeClaude(t, script))
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	wd := t.TempDir()
	wd, _ = filepath.EvalSymlinks(wd)
	sess, err := ag.NewSession(context.Background(), SessionOpts{
		WorkDir:     wd,
		Model:       "test-model",
		SystemHints: []string{"hint one", "hint two"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if err := sess.Send(context.Background(), "ping"); err != nil {
		t.Fatal(err)
	}
	evs := collect(t, sess)
	var msg string
	for _, e := range evs {
		if e.Kind == EventMessage {
			msg = e.Text
		}
	}
	for _, wantPart := range []string{
		"--input-format stream-json", "--output-format stream-json",
		"--include-partial-messages", "--permission-mode bypassPermissions",
		"--model test-model", "--append-system-prompt hint one\n\nhint two",
		"cwd=" + wd, "msg=ping",
	} {
		if !strings.Contains(msg, wantPart) {
			t.Errorf("echoed invocation missing %q: %s", wantPart, msg)
		}
	}
}

// The parent's Claude Code session markers are stripped from the child's
// environment so a gummi driven from inside a Claude Code session spawns a
// top-level child, not a bridge child; auth and runtime vars pass through.
func TestScrubClaudeSessionEnv(t *testing.T) {
	in := []string{
		"PATH=/usr/bin",
		"CLAUDECODE=1",
		"CLAUDE_CODE_SESSION_ID=parent",
		"CLAUDE_CODE_CHILD_SESSION=1",
		"CLAUDE_CODE_BRIDGE_SESSION_ID=bridge",
		"AI_AGENT=claude",
		"CLAUDE_CONFIG_DIR=/home/x/.claude",
		"ANTHROPIC_API_KEY=sk-test",
		"HOME=/home/x",
	}
	set := map[string]bool{}
	for _, kv := range scrubClaudeSessionEnv(in) {
		k, _, _ := strings.Cut(kv, "=")
		set[k] = true
	}
	for _, dropped := range []string{
		"CLAUDECODE", "CLAUDE_CODE_SESSION_ID", "CLAUDE_CODE_CHILD_SESSION",
		"CLAUDE_CODE_BRIDGE_SESSION_ID", "AI_AGENT",
	} {
		if set[dropped] {
			t.Errorf("kept %q; want it scrubbed from the child env", dropped)
		}
	}
	// CLAUDE_CONFIG_DIR is auth (not a CLAUDE_CODE_ key) and must survive.
	for _, kept := range []string{"PATH", "CLAUDE_CONFIG_DIR", "ANTHROPIC_API_KEY", "HOME"} {
		if !set[kept] {
			t.Errorf("scrubbed %q; want it preserved (auth/runtime)", kept)
		}
	}
}

// End to end: the spawned child actually sees the scrubbed environment —
// proving the scrub is wired into cmd.Env, not just a standalone helper.
func TestClaudeCodeScrubsSessionEnvOnSpawn(t *testing.T) {
	script := `import sys, json, os
def out(o):
    sys.stdout.write(json.dumps(o)+"\n"); sys.stdout.flush()
for line in sys.stdin:
    line = line.strip()
    if not line: continue
    m = json.loads(line)
    if m.get("type") != "user": continue
    keys = ["CLAUDECODE","CLAUDE_CODE_SESSION_ID","CLAUDE_CODE_BRIDGE_SESSION_ID","AI_AGENT","CLAUDE_CONFIG_DIR"]
    text = " ".join(k+"="+os.environ.get(k,"<unset>") for k in keys)
    out({"type":"assistant","message":{"model":"m","content":[{"type":"text","text":text}]}})
    out({"type":"result","subtype":"success","is_error":False,"modelUsage":{}})
`
	// gummi is "inside" a Claude Code session: the markers are in its env.
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "parent-sess")
	t.Setenv("CLAUDE_CODE_BRIDGE_SESSION_ID", "bridge-sess")
	t.Setenv("AI_AGENT", "claude")
	t.Setenv("CLAUDE_CONFIG_DIR", "/home/x/.claude")

	ag, err := NewClaudeCode(writeFakeClaude(t, script))
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	sess, err := ag.NewSession(context.Background(), SessionOpts{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if err := sess.Send(context.Background(), "ping"); err != nil {
		t.Fatal(err)
	}
	var msg string
	for _, e := range collect(t, sess) {
		if e.Kind == EventMessage {
			msg = e.Text
		}
	}
	for _, want := range []string{
		"CLAUDECODE=<unset>", "CLAUDE_CODE_SESSION_ID=<unset>",
		"CLAUDE_CODE_BRIDGE_SESSION_ID=<unset>", "AI_AGENT=<unset>",
		"CLAUDE_CONFIG_DIR=/home/x/.claude", // auth preserved
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("child env missing %q; got: %s", want, msg)
		}
	}
}

func TestClaudeCodeCrashMidTurnIsError(t *testing.T) {
	script := `import sys
sys.stdin.readline()
sys.stderr.write("boom: api key invalid\n")
sys.exit(2)
`
	ag, err := NewClaudeCode(writeFakeClaude(t, script))
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	sess, err := ag.NewSession(context.Background(), SessionOpts{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if err := sess.Send(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	evs := collect(t, sess)
	last := evs[len(evs)-1]
	if last.Kind != EventError || !strings.Contains(last.Err.Error(), "boom: api key invalid") {
		t.Fatalf("crash surfaced as %v (err=%v), want error carrying the stderr tail", last.Kind, last.Err)
	}
}

func TestClaudeCodeRejectsGuardedAndBYOK(t *testing.T) {
	ag, err := NewClaudeCode(writeFakeClaude(t, fakeClaudeScript))
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	if _, err := ag.NewSession(context.Background(), SessionOpts{
		WorkDir: t.TempDir(), Permission: PermissionGuarded,
	}); err == nil || !strings.Contains(err.Error(), "guarded") {
		t.Errorf("guarded session error = %v, want a clear guarded rejection", err)
	}
	if _, err := ag.NewSession(context.Background(), SessionOpts{
		WorkDir: t.TempDir(), Model: "m",
		Provider: Provider{BaseURL: "http://127.0.0.1:8080/v1"},
	}); err == nil || !strings.Contains(err.Error(), "BYOK") {
		t.Errorf("BYOK session error = %v, want a clear provider rejection", err)
	}
}

// A foreign (non-Anthropic) model id is rejected at session start with a
// clear message naming the model, rather than forwarded as --model to fail
// opaquely mid-run. Anthropic ids and an empty id still start.
func TestClaudeCodeRejectsForeignModel(t *testing.T) {
	ag, err := NewClaudeCode(writeFakeClaude(t, fakeClaudeScript))
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	if _, err := ag.NewSession(context.Background(), SessionOpts{
		WorkDir: t.TempDir(), Model: "gpt-5-mini",
	}); err == nil || !strings.Contains(err.Error(), "gpt-5-mini") {
		t.Errorf("foreign-model session error = %v, want a rejection naming the model", err)
	}
	// an Anthropic model and an empty (CLI-default) model both start.
	for _, m := range []string{"claude-sonnet-5", ""} {
		sess, err := ag.NewSession(context.Background(), SessionOpts{WorkDir: t.TempDir(), Model: m})
		if err != nil {
			t.Errorf("Model %q should start, got %v", m, err)
			continue
		}
		sess.Close()
	}
}

func TestClaudeCodeMissingBinary(t *testing.T) {
	if _, err := NewClaudeCode("definitely-not-a-real-binary-xyz"); err == nil {
		t.Error("missing binary should fail fast")
	}
}

func TestClaudeCodeCloseClosesEvents(t *testing.T) {
	ag, err := NewClaudeCode(writeFakeClaude(t, fakeClaudeScript))
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	sess, err := ag.NewSession(context.Background(), SessionOpts{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	sess.Close()
	select {
	case _, ok := <-sess.Events():
		if ok {
			for range sess.Events() {
			}
		}
	case <-time.After(3 * time.Second):
		t.Fatal("events channel did not close after Close")
	}
}

// TestClaudeCodeRealCLI drives the actual claude binary — one cheap haiku
// turn. Explicitly opt-in: it spends real quota, so presence-gating alone
// (the findCopilot pattern) would bill every `go test ./...` on a machine
// with claude installed.
func TestClaudeCodeRealCLI(t *testing.T) {
	if os.Getenv("GUMMI_CLAUDE_TEST") != "1" {
		t.Skip("set GUMMI_CLAUDE_TEST=1 to test against the real claude CLI (spends quota)")
	}
	ag, err := NewClaudeCode("")
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	ctx := context.Background()
	sess, err := ag.NewSession(ctx, SessionOpts{
		WorkDir: t.TempDir(), Model: "haiku", Permission: PermissionAllowAll,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if err := sess.Send(ctx, "Reply with exactly: ok"); err != nil {
		t.Fatal(err)
	}
	var evs []Event
	deadline := time.After(120 * time.Second)
drain:
	for {
		select {
		case e, ok := <-sess.Events():
			if !ok {
				break drain
			}
			evs = append(evs, e)
			if e.Kind == EventIdle || e.Kind == EventError {
				break drain
			}
		case <-deadline:
			t.Fatal("timed out waiting for the real CLI")
		}
	}
	var sawText, sawIdle bool
	var credits float64
	var ctxEv Context
	for _, e := range evs {
		switch e.Kind {
		case EventError:
			t.Fatalf("real CLI turn errored: %v", e.Err)
		case EventTextDelta, EventMessage:
			sawText = true
		case EventUsage:
			credits += e.Usage.Credits
		case EventContext:
			ctxEv = e.Context
		case EventIdle:
			sawIdle = true
		}
	}
	if !sawIdle || !sawText {
		t.Errorf("real CLI turn: idle=%v text=%v (events: %d)", sawIdle, sawText, len(evs))
	}
	if credits <= 0 {
		t.Errorf("real CLI turn metered %v credits, want > 0", credits)
	}
	if ctxEv.Tokens <= 0 || ctxEv.Limit <= 0 {
		t.Errorf("real CLI context = %+v, want live occupancy", ctxEv)
	}
}
