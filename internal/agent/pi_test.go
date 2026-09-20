package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newPiSession() *piSession {
	return &piSession{
		model: "m", workdir: "/w",
		raw: make(chan Event, 8), events: make(chan Event), stop: make(chan struct{}),
	}
}

// piLineOf builds one RPC line for mapLine tests.
func piLineOf(t *testing.T, s string) []byte {
	t.Helper()
	if !json.Valid([]byte(s)) {
		t.Fatalf("test line is not valid JSON: %s", s)
	}
	return []byte(s)
}

// resultOutput reads a ToolResult event's Output.
func resultOutput(t *testing.T, ev Event) string {
	t.Helper()
	if ev.Result == nil {
		t.Fatal("event carries no Result")
	}
	return ev.Result.Output
}

// writeFakePi drops an executable fake `pi` at dir/pi: it dumps its argv
// (one per line) to dir/args, then reads stdin line by line. get_state is
// answered with the fixed canned state; a prompt runs promptArm (shell
// statements, with $line set to the prompt frame); abort runs abortArm and
// unblocks a prompt arm that is blocking on `read next`. Non-matching
// lines are consumed silently.
func writeFakePi(t *testing.T, dir, promptArm string) string {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	path := filepath.Join(dir, "pi")
	body := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > " + filepath.Join(dir, "args") + "\n" +
		"while IFS= read -r line; do\n" +
		`  case "$line" in` + "\n" +
		`    *'"type":"get_state"'*)` +
		` echo '{"type":"response","command":"get_state","success":true,"data":{"sessionId":"ses_test","model":{"contextWindow":1000000}}}' ;;` + "\n" +
		`    *'"type":"prompt"'*)` + "\n" +
		promptArm + "\n" +
		`      ;;` + "\n" +
		`    *'"type":"abort"'*)` + "\n" +
		`      echo '{"type":"response","command":"abort","success":true}'` + "\n" +
		`      echo '{"type":"agent_settled"}'` + "\n" +
		`      ;;` + "\n" +
		"  esac\n" +
		"done\n"
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

const piHappyArm = `      echo '{"type":"response","command":"prompt","success":true}'
      echo '{"type":"agent_start"}'
      echo '{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"ok"}}'
      echo '{"type":"message_end","message":{"role":"assistant","stopReason":"stop","usage":{"input":10,"output":2,"cost":{"total":0.01}}}}'
      echo '{"type":"agent_settled"}'`

func TestPiMapLineTextDeltasAndFlushes(t *testing.T) {
	s := newPiSession()
	if evs := s.mapLine(piLineOf(t, `{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"Working on "}}`)); len(evs) != 1 || evs[0].Kind != EventTextDelta || evs[0].Text != "Working on " {
		t.Fatalf("text delta = %+v, want one delta", evs)
	}
	if s.msg.String() != "Working on " {
		t.Fatalf("accumulator = %q, want the delta accumulated", s.msg.String())
	}
	// a tool call starting flushes the prose as its own bubble
	evs := s.mapLine(piLineOf(t, `{"type":"message_update","assistantMessageEvent":{"type":"toolcall_start","id":"c1","toolName":"bash"}}`))
	if len(evs) != 1 || evs[0].Kind != EventMessage || evs[0].Text != "Working on" {
		t.Fatalf("toolcall_start = %+v, want EventMessage 'Working on'", evs)
	}
	if s.msg.String() != "" {
		t.Fatalf("accumulator = %q, want reset after flush", s.msg.String())
	}
	// message_end flushes the trailing segment (the accumulated text, not
	// the message snapshot — deltas and snapshot are equal, and re-reading
	// the snapshot would risk double-emitting)
	if evs := s.mapLine(piLineOf(t, `{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"Found it."}}`)); len(evs) != 1 {
		t.Fatalf("second delta = %+v", evs)
	}
	evs = s.mapLine(piLineOf(t, `{"type":"message_end","message":{"role":"assistant","model":"m1","stopReason":"stop","content":[{"type":"text","text":"Working on Found it."}]}}`))
	if len(evs) != 1 || evs[0].Kind != EventMessage || evs[0].Text != "Found it." {
		t.Fatalf("message_end = %+v, want the trailing segment flushed", evs)
	}
}

func TestPiMapMessageEndMetersUsage(t *testing.T) {
	s := newPiSession()
	s.ctxLimit = 1000000
	// input excludes the cache sides: gummi re-splits input+cacheWrite and
	// cacheRead, exactly the claude adapter's convention. USD cost → credits.
	evs := s.mapLine(piLineOf(t, `{"type":"message_end","message":{"role":"assistant","model":"m1","stopReason":"stop","usage":{"input":100,"output":20,"cacheRead":30,"cacheWrite":5,"cost":{"total":0.01}}}}`))
	var usage *Usage
	var ctx *Context
	for _, ev := range evs {
		switch ev.Kind {
		case EventUsage:
			usage = &ev.Usage
		case EventContext:
			ctx = &ev.Context
		}
	}
	if usage == nil {
		t.Fatalf("no usage event in %+v", evs)
	}
	if usage.InputTokens != 105 || usage.OutputTokens != 20 || usage.CachedTokens != 30 {
		t.Errorf("usage tokens = %+v, want in105/cache30/out20 (input excludes the cache sides)", usage)
	}
	if usage.Credits < 0.99 || usage.Credits > 1.01 || !usage.Metered {
		t.Errorf("usage credits = %v metered=%v, want 1.0 metered", usage.Credits, usage.Metered)
	}
	if usage.Model != "m1" {
		t.Errorf("usage model = %q, want the message's model", usage.Model)
	}
	if ctx == nil || ctx.Tokens != 135 || ctx.Limit != 1000000 {
		t.Errorf("context = %+v, want tokens 135 against limit 1000000", ctx)
	}
	// a message without usage (impossible for pi today, tolerated for drift)
	evs = s.mapLine(piLineOf(t, `{"type":"message_end","message":{"role":"assistant","stopReason":"stop"}}`))
	for _, ev := range evs {
		if ev.Kind == EventUsage || ev.Kind == EventContext {
			t.Errorf("usage-less message surfaced %+v", ev)
		}
	}
}

func TestPiMapToolEnd(t *testing.T) {
	s := newPiSession()
	evs := s.mapLine(piLineOf(t, `{"type":"tool_execution_end","toolCallId":"c1","toolName":"bash","isError":true,"result":{"content":[{"type":"text","text":"permission denied: /etc/passwd"}]}}`))
	if len(evs) != 1 || evs[0].Kind != EventToolResult || evs[0].CallID != "c1" {
		t.Fatalf("tool end = %+v, want EventToolResult for c1", evs)
	}
	if evs[0].Result.OK {
		t.Error("isError=true must read as a failed result")
	}
	if !strings.Contains(resultOutput(t, evs[0]), "permission denied") {
		t.Errorf("result output = %q, want the tool's message", resultOutput(t, evs[0]))
	}
}

func TestPiMapLineSettledEndsTurn(t *testing.T) {
	s := newPiSession()
	// a settled with no turn in flight is a stray: no idle, no events
	if evs := s.mapLine(piLineOf(t, `{"type":"agent_settled"}`)); len(evs) != 0 {
		t.Fatalf("stray settled = %+v, want nothing", evs)
	}
	s.mu.Lock()
	s.inTurn = true
	s.mu.Unlock()
	evs := s.mapLine(piLineOf(t, `{"type":"agent_settled"}`))
	if len(evs) != 1 || evs[0].Kind != EventIdle {
		t.Fatalf("settled = %+v, want EventIdle", evs)
	}
	s.mu.Lock()
	in := s.inTurn
	s.mu.Unlock()
	if in {
		t.Error("settled must clear the in-turn flag")
	}
}

func TestPiMapPromptRejectedFailsTurn(t *testing.T) {
	s := newPiSession()
	s.mu.Lock()
	s.inTurn = true
	s.mu.Unlock()
	evs := s.mapLine(piLineOf(t, `{"type":"response","id":"p","command":"prompt","success":false,"error":"No API key found for the selected model."}`))
	if len(evs) != 1 || evs[0].Kind != EventError {
		t.Fatalf("rejected prompt = %+v, want EventError", evs)
	}
	var rf *RunFailure
	if !errors.As(evs[0].Err, &rf) || rf.Backend != "pi" || !rf.FirstTurn {
		t.Fatalf("Err = %v, want a first-turn pi RunFailure", evs[0].Err)
	}
	if !strings.Contains(rf.Diagnostic, "No API key") {
		t.Errorf("Diagnostic = %q, want pi's own error text", rf.Diagnostic)
	}
	// the turn is over: there is nothing left to wait a settled for
	if s.inTurnLocked() {
		t.Error("a rejected prompt must clear the in-turn flag")
	}
}

// A transient error is pi's to retry: a failed assistant message must NOT
// fail the turn on its own (a transparent retry may still land), and a
// settled only after a retry restart (message_start clears the diagnosis)
// ends idle.
func TestPiMapTransientErrorRetriedThenIdle(t *testing.T) {
	s := newPiSession()
	s.mu.Lock()
	s.inTurn = true
	s.mu.Unlock()
	s.mapLine(piLineOf(t, `{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"partial"}}`))
	s.mapLine(piLineOf(t, `{"type":"message_end","message":{"role":"assistant","stopReason":"error","errorMessage":"529 overloaded"}}`))
	if s.errDiag == "" {
		t.Fatal("an errored message must record a pending diagnosis")
	}
	// the retry begins; its message_start clears the stale diagnosis
	s.mapLine(piLineOf(t, `{"type":"auto_retry_start","attempt":1}`))
	s.mapLine(piLineOf(t, `{"type":"message_start"}`))
	if s.errDiag != "" {
		t.Fatalf("message_start after a retry must clear the diagnosis, got %q", s.errDiag)
	}
	s.mapLine(piLineOf(t, `{"type":"message_end","message":{"role":"assistant","stopReason":"stop"}}`))
	evs := s.mapLine(piLineOf(t, `{"type":"agent_settled"}`))
	if len(evs) != 1 || evs[0].Kind != EventIdle {
		t.Fatalf("settled = %+v, want EventIdle after a successful retry", evs)
	}
}

// Without a successful retry — a message that errored and a settled right
// behind it, or a retry loop that exhausted — the turn fails with pi's own
// diagnosis, not a clean idle.
func TestPiMapFailedTurnSurfacesAtSettled(t *testing.T) {
	t.Run("unrecovered error", func(t *testing.T) {
		s := newPiSession()
		s.mu.Lock()
		s.inTurn = true
		s.mu.Unlock()
		s.mapLine(piLineOf(t, `{"type":"message_end","message":{"role":"assistant","stopReason":"error","errorMessage":"quota exceeded"}}`))
		evs := s.mapLine(piLineOf(t, `{"type":"agent_settled"}`))
		if len(evs) != 1 || evs[0].Kind != EventError {
			t.Fatalf("settled = %+v, want EventError", evs)
		}
		if !strings.Contains(evs[0].Err.Error(), "quota exceeded") {
			t.Errorf("err = %q, want the backend's message", evs[0].Err.Error())
		}
		if s.inTurnLocked() {
			t.Error("the failed turn must have ended")
		}
	})
	t.Run("retry exhausted", func(t *testing.T) {
		s := newPiSession()
		s.mu.Lock()
		s.inTurn = true
		s.mu.Unlock()
		s.mapLine(piLineOf(t, `{"type":"auto_retry_end","success":false,"attempt":3,"finalError":"529 overloaded"}`))
		evs := s.mapLine(piLineOf(t, `{"type":"agent_settled"}`))
		if len(evs) != 1 || evs[0].Kind != EventError {
			t.Fatalf("settled = %+v, want EventError", evs)
		}
		if !strings.Contains(evs[0].Err.Error(), "529 overloaded") {
			t.Errorf("err = %q, want the final retry error", evs[0].Err.Error())
		}
	})
	t.Run("compaction failed", func(t *testing.T) {
		s := newPiSession()
		s.mu.Lock()
		s.inTurn = true
		s.mu.Unlock()
		s.mapLine(piLineOf(t, `{"type":"compaction_end","reason":"overflow","aborted":false,"errorMessage":"provider down"}`))
		evs := s.mapLine(piLineOf(t, `{"type":"agent_settled"}`))
		if len(evs) != 1 || evs[0].Kind != EventError {
			t.Fatalf("settled = %+v, want EventError", evs)
		}
	})
}

// An interrupted turn ends idle even with a pending diagnosis: the engine
// itself interrupts sessions on budget stops, and an EventError would
// downgrade that clean stop to a failed run.
func TestPiMapInterruptedSettledIsIdle(t *testing.T) {
	s := newPiSession()
	s.mu.Lock()
	s.inTurn = true
	s.interrupted = true
	s.mu.Unlock()
	s.errDiag = "quota exceeded"
	evs := s.mapLine(piLineOf(t, `{"type":"agent_settled"}`))
	if len(evs) != 1 || evs[0].Kind != EventIdle {
		t.Fatalf("interrupted settled = %+v, want EventIdle", evs)
	}
}

// An extension UI request is declined on the wire: RPC mode has no human
// at the keyboard, and an unanswered dialog blocks the extension forever.
func TestPiMapExtensionUIDeclined(t *testing.T) {
	s := newPiSession()
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	s.stdin = stdinW
	t.Cleanup(func() { _ = stdinW.Close() })
	if evs := s.mapLine(piLineOf(t, `{"type":"extension_ui_request","id":"u1","method":"confirm","title":"Allow?"}`)); len(evs) != 0 {
		t.Fatalf("extension_ui_request produced events: %+v", evs)
	}
	buf := make([]byte, 256)
	n, err := stdinR.Read(buf)
	if err != nil {
		t.Fatalf("no response written: %v", err)
	}
	var resp map[string]any
	if err := json.Unmarshal(buf[:n], &resp); err != nil {
		t.Fatalf("response not JSON: %q", buf[:n])
	}
	if resp["type"] != "extension_ui_response" || resp["id"] != "u1" || resp["cancelled"] != true {
		t.Fatalf("response = %v, want a cancelled extension_ui_response for u1", resp)
	}
}

func TestPiSendBusyAndFlags(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	// the prompt arm acks, streams a delta, then blocks on `read next`
	// until the abort line arrives — an interruptible turn, deterministic
	// without sleeps.
	path := writeFakePi(t, dir, `      echo '{"type":"response","command":"prompt","success":true}'
      echo '{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"ok"}}'
      read next
      echo '{"type":"agent_settled"}'`)
	ag, err := NewPi(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	ctx := context.Background()
	sess, err := ag.NewSession(ctx, SessionOpts{WorkDir: t.TempDir(), Model: "test-model"})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if err := sess.Send(ctx, "go"); err != nil {
		t.Fatal(err)
	}
	if err := sess.Send(ctx, "two"); !errors.Is(err, ErrBusy) {
		t.Errorf("a second Send during an in-flight turn = %v, want ErrBusy", err)
	}
	if err := sess.Interrupt(ctx); err != nil {
		t.Fatal(err)
	}
	// the interrupted turn ends idle, never error — and argv carries the
	// spawn flags while we are at it
	deadline := time.After(5 * time.Second)
	for {
		select {
		case e := <-sess.Events():
			if e.Kind == EventError {
				t.Fatalf("interrupt surfaced as error: %v", e.Err)
			}
			if e.Kind != EventIdle {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, "args"))
			if err != nil {
				t.Fatal(err)
			}
			argv := strings.Split(strings.TrimSpace(string(data)), "\n")
			for _, want := range []string{"--mode", "rpc", "--no-extensions", "--model", "test-model"} {
				if !sliceContains(argv, want) {
					t.Errorf("pi args %v missing %q", argv, want)
				}
			}
			return
		case <-deadline:
			t.Fatal("interrupted turn never went idle")
		}
	}
}

func sliceContains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// SessionID comes from the pipelined get_state; the engine persists it and
// hands it back as ResumeID, so the session must publish it.
func TestPiCapturesSessionID(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	path := writeFakePi(t, dir, piHappyArm)
	ag, err := NewPi(path)
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
	id, ok := sess.(Identified)
	if !ok {
		t.Fatal("pi session does not implement Identified; the engine cannot persist its id")
	}
	deadline := time.After(5 * time.Second)
	for id.SessionID() != "ses_test" {
		select {
		case e := <-sess.Events():
			if e.Kind == EventError {
				t.Fatalf("session errored: %v", e.Err)
			}
		case <-time.After(10 * time.Millisecond):
		case <-deadline:
			t.Fatalf("SessionID() = %q, want ses_test from the pipelined get_state", id.SessionID())
		}
	}
	if err := sess.Send(ctx, "go"); err != nil {
		t.Fatal(err)
	}
	waitPiIdle(t, sess)
}

func TestPiResumesAHandedInSession(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	// pi's session store, pointed at the test: the handed-in id resolves
	// only when a transcript named *_<id>.jsonl exists under it.
	if err := os.MkdirAll(filepath.Join(dir, "sessions", "slug"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sessions", "slug", "2026-01-01T00-00-00Z_ses_prior.jsonl"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PI_CODING_AGENT_DIR", dir)

	path := writeFakePi(t, dir, piHappyArm)
	ag, err := NewPi(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	sess, err := ag.NewSession(context.Background(), SessionOpts{WorkDir: t.TempDir(), Model: "x", ResumeID: "ses_prior"})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	deadline := time.After(5 * time.Second)
	for {
		data, err := os.ReadFile(filepath.Join(dir, "args"))
		if err == nil && strings.Contains(string(data), "--session") && strings.Contains(string(data), "ses_prior") {
			return // the child was spawned resuming the handed-in session
		}
		select {
		case <-deadline:
			t.Fatalf("argv does not resume the handed-in session:\n%s", data)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// A session id pi cannot confirm must not reach the command line: a resume
// is an optimization and must never be the reason a session fails to start.
func TestPiDropsUnconfirmableResume(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PI_CODING_AGENT_DIR", dir) // exists, but holds no session
	// the fake never answers: the test reads argv directly
	path := dir + "/pi"
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > "+dir+"/args\nsleep 5\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	ag, err := NewPi(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	sess, err := ag.NewSession(context.Background(), SessionOpts{WorkDir: t.TempDir(), Model: "x", ResumeID: "ses_gone"})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	data, err := waitArgs(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "--session") {
		t.Errorf("the child was spawned resuming a session pi cannot confirm:\n%s", data)
	}
}

func TestPiReadOnlyTools(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	path := dir + "/pi"
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > "+dir+"/args\nsleep 5\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	ag, err := NewPi(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	sess, err := ag.NewSession(context.Background(), SessionOpts{WorkDir: t.TempDir(), Model: "x", ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	data, err := waitArgs(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Split(strings.TrimSpace(string(data)), "\n")
	var allowlist string
	for i, a := range argv {
		if a == "--tools" && i+1 < len(argv) {
			allowlist = argv[i+1]
		}
	}
	if allowlist != "read,grep,find,ls" {
		t.Errorf("readOnly --tools = %q, want read,grep,find,ls (bash/edit/write structurally absent)", allowlist)
	}
}

func TestPiGuardedAccepted(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	p := &Pi{bin: writeFakePi(t, t.TempDir(), piHappyArm)}
	sess, err := p.NewSession(context.Background(), SessionOpts{WorkDir: t.TempDir(), Model: "x", Permission: PermissionGuarded})
	if err != nil {
		t.Fatalf("guarded NewSession should be accepted (like opencode's): %v", err)
	}
	_ = sess.Close()
}

// The MCP extension is materialized only for a bound session (socket plus
// a feature id, or the Workspace flag), written as a real file, handed to
// pi with --extension, and carries the child's scope in its baked config.
func TestPiMaterializesMCPExtension(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	path := writeFakePi(t, dir, piHappyArm)
	ag, err := NewPi(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	sess, err := ag.NewSession(context.Background(), SessionOpts{
		WorkDir: t.TempDir(), Model: "x", MCPSockPath: "/tmp/mcp/FD-011.sock", FeatureID: "FD-011",
	})
	if err != nil {
		t.Fatal(err)
	}
	ps := sess.(*piSession)
	if ps.extPath == "" {
		t.Fatal("a bound session must materialize the tool extension")
	}
	defer ps.Close()
	ext, err := os.ReadFile(ps.extPath)
	if err != nil {
		t.Fatalf("extension file not readable: %v", err)
	}
	rawArgs, err := waitArgs(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Split(strings.TrimSpace(string(rawArgs)), "\n")
	if !sliceContains(argv, "--extension") || !sliceContains(argv, ps.extPath) {
		t.Fatalf("pi argv missing --extension with the materialized path:\n%s", argv)
	}
	cfg := parsePiExtConfig(t, ext)
	env, _ := cfg["env"].(map[string]any)
	if env["GUMMI_MCP_SOCK"] != "/tmp/mcp/FD-011.sock" {
		t.Errorf("baked config env = %v, want the session socket", env)
	}
	args, _ := cfg["args"].([]any)
	var argvParts []string
	for _, a := range args {
		if s, ok := a.(string); ok {
			argvParts = append(argvParts, s)
		}
	}
	if !sliceContains(argvParts, "--feature") || !sliceContains(argvParts, "FD-011") {
		t.Errorf("extension args = %v, want a --feature FD-011 child", args)
	}
	if cmd, ok := cfg["cmd"].(string); !ok || cmd == "" {
		t.Errorf("baked config carries no cmd: %v", cfg)
	}
}

// An unbound session (no socket, no feature id, no Workspace) must get no
// extension file and no --extension flag: the one case that stays toolless.
func TestPiOmitsMCPWhenUnbound(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	path := dir + "/pi"
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > "+dir+"/args\nsleep 5\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	ag, err := NewPi(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	sess, err := ag.NewSession(context.Background(), SessionOpts{WorkDir: t.TempDir(), Model: "x"})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	data, err := waitArgs(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "--extension") {
		t.Errorf("unbound session was handed an extension:\n%s", data)
	}
}

// A board-level session (Workspace set, no FeatureID) binds its extension
// to the workspace endpoint, not to a feature id.
func TestPiWorkspaceExtension(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	saved := piExecPath
	piExecPath = func() (string, error) { return "/opt/gummi/bin/gummi", nil }
	t.Cleanup(func() { piExecPath = saved })
	dir := t.TempDir()
	path := dir + "/pi"
	if err := os.WriteFile(path, []byte("#!/bin/sh\nsleep 5\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	ag, err := NewPi(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	sess, err := ag.NewSession(context.Background(), SessionOpts{
		WorkDir: t.TempDir(), Model: "x", MCPSockPath: "/tmp/mcp/ws.sock", Workspace: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	ps := sess.(*piSession)
	ext, err := os.ReadFile(ps.extPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ext), `"__mcp","--workspace"`) {
		t.Errorf("extension does not bind the workspace endpoint:\n%s", ext)
	}
	cfg := parsePiExtConfig(t, ext)
	if cfg["cmd"] != "/opt/gummi/bin/gummi" {
		t.Errorf("baked cmd = %v, want the rebound piExecPath", cfg["cmd"])
	}
}

// parsePiExtConfig decodes the generated extension's baked config line.
func parsePiExtConfig(t *testing.T, ext []byte) map[string]any {
	t.Helper()
	i := strings.Index(string(ext), "const CFG = ")
	if i < 0 {
		t.Fatal("generated extension carries no config line")
	}
	dec := json.NewDecoder(strings.NewReader(string(ext[i+len("const CFG = "):])))
	var cfg map[string]any
	if err := dec.Decode(&cfg); err != nil {
		t.Fatalf("config not valid JSON: %v", err)
	}
	return cfg
}

// The stage's tool descriptors are baked into the extension so they
// register synchronously at load — an async tools/list would race pi's
// first turn, and a first prompt that reaches the model before the
// registration lands sees no gummi tools.
func TestPiBakesToolDescriptors(t *testing.T) {
	ext, err := buildPiExtension("/opt/gummi", "FD-011", "/tmp/mcp/x.sock", false, piMCPToolFromDefs([]ToolDef{
		{Name: "ask_user", Description: "ask the orchestrator", Parameters: map[string]any{"type": "object", "properties": map[string]any{"question": map[string]any{"type": "string"}}}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	cfg := parsePiExtConfig(t, ext)
	tools, _ := cfg["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("baked tools = %v, want the session's tool set", tools)
	}
	tool, _ := tools[0].(map[string]any)
	if tool["name"] != "ask_user" {
		t.Errorf("baked tool name = %v, want ask_user", tool["name"])
	}
	params, _ := tool["parameters"].(map[string]any)
	if params["type"] != "object" {
		t.Errorf("baked parameters = %v, want the tool's JSON schema", params)
	}
	// toolless sessions (board/hosted) fall back to a live tools/list
	ext, err = buildPiExtension("/opt/gummi", "", "/tmp/mcp/ws.sock", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ext), "tools/list") {
		t.Error("toolless extension carries no tools/list fallback")
	}
	cfg = parsePiExtConfig(t, ext)
	if _, hasTools := cfg["tools"]; hasTools {
		t.Errorf("toolless extension baked a tools array: %v", cfg["tools"])
	}
}

func TestPiRequiresModel(t *testing.T) {
	p := &Pi{bin: "pi"}
	if _, err := p.NewSession(context.Background(), SessionOpts{WorkDir: t.TempDir()}); err == nil {
		t.Error("NewSession without a model should error")
	}
}

// A child that dies mid-turn with its own words on stderr must surface a
// RunFailure carrying the diagnostic (first turn), not a bare exit code.
func TestPiRunFailureCarriesDiagnostic(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	path := writeFakePi(t, dir, `      echo 'boom: provider not authenticated' >&2
      exit 1`)
	ag, err := NewPi(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	sess, err := ag.NewSession(context.Background(), SessionOpts{WorkDir: t.TempDir(), Model: "x"})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if err := sess.Send(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case e := <-sess.Events():
			if e.Kind != EventError {
				continue
			}
			var rf *RunFailure
			if !errors.As(e.Err, &rf) {
				t.Fatalf("EventError.Err = %v (%T), want a *RunFailure", e.Err, e.Err)
			}
			if rf.Backend != "pi" {
				t.Errorf("RunFailure.Backend = %q, want pi", rf.Backend)
			}
			if !rf.FirstTurn {
				t.Error("RunFailure.FirstTurn = false on the session's first turn")
			}
			if !strings.Contains(rf.Diagnostic, "provider not authenticated") {
				t.Errorf("RunFailure.Diagnostic = %q, missing the backend's own stderr", rf.Diagnostic)
			}
			return
		case <-deadline:
			t.Fatal("no EventError before deadline")
		}
	}
}

// FirstTurn must read false once a prior turn on the same session has
// already reached idle — otherwise every mid-task crash would misreport
// as a fresh session's first turn and wrongly point a reader at `gummi
// doctor` for a backend that was, until a moment ago, working fine.
func TestPiRunFailureFirstTurnFalseAfterASuccess(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	marker := dir + "/turn-two"
	arm := `      if [ -f ` + marker + ` ]; then echo boom >&2; exit 1; fi
      touch ` + marker + `
      echo '{"type":"agent_settled"}'`
	path := writeFakePi(t, dir, arm)
	ag, err := NewPi(path)
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
	waitPiIdle(t, sess)
	// waitPiIdle ends the first turn cleanly; the second Send then hits
	// the marker-armed crash above.
	if err := sess.Send(ctx, "go again"); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case e := <-sess.Events():
			if e.Kind != EventError {
				continue
			}
			var rf *RunFailure
			if !errors.As(e.Err, &rf) {
				t.Fatalf("EventError.Err = %v, want a *RunFailure", e.Err)
			}
			if rf.FirstTurn {
				t.Error("RunFailure.FirstTurn = true on the session's second turn")
			}
			return
		case <-deadline:
			t.Fatal("no EventError before deadline")
		}
	}
}

// TestPiLiveRoundTrip drives the real pi binary against a cheap hosted
// model. It skips when pi isn't installed, and treats an error/timeout
// (network or auth trouble) as a skip — it verifies the adapter's mapping,
// not pi's uptime. Modeled on TestOpencodeLiveRoundTrip.
func TestPiLiveRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("pi"); err != nil {
		t.Skip("pi not installed")
	}
	ag, err := NewPi("pi")
	if err != nil {
		t.Skip(err)
	}
	defer ag.Close()

	ctx := context.Background()
	sess, err := ag.NewSession(ctx, SessionOpts{
		WorkDir: t.TempDir(),
		Model:   "openrouter/z-ai/glm-flash-latest",
	})
	if err != nil {
		t.Skip(err)
	}
	defer sess.Close()
	if err := sess.Send(ctx, "Reply with exactly one word: PONG"); err != nil {
		t.Skipf("pi could not start: %v", err)
	}

	var text string
	var sawUsage, sawIdle bool
	deadline := time.After(120 * time.Second)
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
				t.Skipf("pi/network unavailable: %v", e.Err)
			}
		case <-deadline:
			t.Skip("pi did not respond in time (network?)")
		}
	}
	if !strings.Contains(strings.ToUpper(text), "PONG") {
		t.Errorf("reply %q did not contain PONG", text)
	}
	if !sawUsage {
		t.Error("no usage event from the turn")
	}
}

// waitArgs waits for the fake's argv dump (the child execs after NewSession
// returns) and returns its contents.
func waitArgs(t *testing.T, dir string) ([]byte, error) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		data, err := os.ReadFile(filepath.Join(dir, "args"))
		if err == nil {
			return data, nil
		}
		select {
		case <-time.After(10 * time.Millisecond):
		case <-deadline:
			return nil, err
		}
	}
}

// waitPiIdle drains events until the turn ends, failing on an error event.
func waitPiIdle(t *testing.T, sess Session) {
	t.Helper()
	for {
		select {
		case e := <-sess.Events():
			if e.Kind == EventError {
				t.Fatalf("turn failed: %v", e.Err)
			}
			if e.Kind == EventIdle {
				return
			}
		case <-time.After(10 * time.Second):
			t.Fatal("timeout waiting for idle")
		}
	}
}
