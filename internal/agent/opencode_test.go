package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"reflect"
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

// A step_finish with reason=length and output=0 means the model exhausted
// its max_tokens cap entirely on reasoning tokens and emitted no visible
// text. Without a specific signal the driver just sees a clean idle with
// empty output and escalates as "unclear verdict". The adapter must surface
// this as an EventError so the operator can raise limit.output.
func TestOpencodeMapEventLengthTruncationSurfaces(t *testing.T) {
	s := newOCSession()
	var msg strings.Builder
	evs := s.mapEvent([]byte(`{"type":"step_finish","part":{"type":"step-finish","reason":"length","tokens":{"input":1325,"output":0,"reasoning":32000},"cost":0}}`), &msg)
	// usage, context, then the length-cap error
	if len(evs) != 3 {
		t.Fatalf("events = %d, want 3 (usage, context, error): %+v", len(evs), evs)
	}
	if evs[2].Kind != EventError || evs[2].Err == nil {
		t.Fatalf("evs[2] = %+v, want EventError with a message", evs[2])
	}
	if !strings.Contains(evs[2].Err.Error(), "reason=length") || !strings.Contains(evs[2].Err.Error(), "limit.output") {
		t.Errorf("err = %q, want mention of reason=length and limit.output", evs[2].Err.Error())
	}
	// A step_finish with reason=length but some output still emitted must NOT
	// surface an error — the model got a partial turn through and the driver
	// can decide what to do with the partial text.
	s = newOCSession()
	msg.Reset()
	evs = s.mapEvent([]byte(`{"type":"step_finish","part":{"type":"step-finish","reason":"length","tokens":{"input":100,"output":50,"reasoning":200},"cost":0}}`), &msg)
	for _, e := range evs {
		if e.Kind == EventError {
			t.Errorf("length-with-output should not surface an error, got %+v", e)
		}
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

// The per-session config cages opencode's file tools to the worktree, and
// --auto only auto-approves what isn't explicitly denied, so guarded is as
// safe as allow-all for opencode until a per-tool approval bridge lands.
// Guarded must therefore be accepted, not rejected.
func TestOpencodeGuardedAccepted(t *testing.T) {
	o := &Opencode{bin: "opencode"}
	sess, err := o.NewSession(context.Background(), SessionOpts{WorkDir: t.TempDir(), Model: "x", Permission: PermissionGuarded})
	if err != nil {
		t.Fatalf("guarded NewSession should succeed: %v", err)
	}
	if sess == nil {
		t.Fatal("guarded NewSession returned a nil session")
	}
	_ = sess.Close()
}

func TestOpencodeCapabilitiesReportsMCPTools(t *testing.T) {
	c := (&Opencode{}).Capabilities()
	if !c.MCPTools {
		t.Errorf("MCPTools = false, want true (opencode reaches tools via MCP)")
	}
	if c.ClientTools {
		t.Errorf("ClientTools = true, want false (opencode ignores opts.Tools)")
	}
}

// NewSession must materialize the OPENCODE_CONFIG file, with the caller's
// worktree/socket/feature id reaching the emitted mcp.gummi command.
func TestOpencodeNewSessionMaterializesConfig(t *testing.T) {
	o := &Opencode{bin: "opencode"}
	wt := t.TempDir()
	sess, err := o.NewSession(context.Background(), SessionOpts{
		WorkDir: wt, Model: "x", MCPSockPath: "/tmp/mcp/FD-011.sock", FeatureID: "FD-011",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	oc := sess.(*opencodeSession)
	if oc.configPath == "" || oc.featureID != "FD-011" {
		t.Fatalf("session configPath=%q featureID=%q", oc.configPath, oc.featureID)
	}
	raw, err := os.ReadFile(oc.configPath)
	if err != nil {
		t.Fatalf("config file not readable: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("config not valid JSON: %v\n%s", err, raw)
	}
	gummi := m["mcp"].(map[string]any)["gummi"].(map[string]any)
	cmd := gummi["command"].([]any)
	exe, _ := os.Executable()
	if got, _ := cmd[0].(string); got != exe {
		t.Errorf("mcp.gummi.command[0] = %q, want %q", got, exe)
	}
	env := gummi["environment"].(map[string]any)
	if env["GUMMI_MCP_SOCK"] != "/tmp/mcp/FD-011.sock" {
		t.Errorf("GUMMI_MCP_SOCK = %v, want /tmp/mcp/FD-011.sock", env["GUMMI_MCP_SOCK"])
	}
}

// A board-level session (Workspace set, no FeatureID) must still get an
// mcp.gummi block, wired to --workspace rather than --feature — the one
// thing SessionOpts.Workspace changes, exercised here through NewSession
// (not just buildOpencodeConfig directly) so opts.Workspace threading
// through the adapter is pinned too.
func TestOpencodeNewSessionMaterializesConfigWorkspace(t *testing.T) {
	o := &Opencode{bin: "opencode"}
	wt := t.TempDir()
	sess, err := o.NewSession(context.Background(), SessionOpts{
		WorkDir: wt, Model: "x", MCPSockPath: "/tmp/mcp/ws.sock", Workspace: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	oc := sess.(*opencodeSession)
	raw, err := os.ReadFile(oc.configPath)
	if err != nil {
		t.Fatalf("config file not readable: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("config not valid JSON: %v\n%s", err, raw)
	}
	gummi := m["mcp"].(map[string]any)["gummi"].(map[string]any)
	cmd := gummi["command"].([]any)
	exe, _ := os.Executable()
	if !reflect.DeepEqual(cmd, []any{exe, "__mcp", "--workspace"}) {
		t.Errorf("mcp.gummi.command = %v, want [%s __mcp --workspace]", cmd, exe)
	}
	env := gummi["environment"].(map[string]any)
	if env["GUMMI_MCP_SOCK"] != "/tmp/mcp/ws.sock" {
		t.Errorf("GUMMI_MCP_SOCK = %v, want /tmp/mcp/ws.sock", env["GUMMI_MCP_SOCK"])
	}
}

// An unbound session (no MCPSockPath/FeatureID) must still materialize the
// config file, but emit no top-level mcp key — so no __mcp child spawns.
func TestOpencodeNewSessionOmitsMCPWhenUnbound(t *testing.T) {
	o := &Opencode{bin: "opencode"}
	sess, err := o.NewSession(context.Background(), SessionOpts{WorkDir: t.TempDir(), Model: "x"})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	oc := sess.(*opencodeSession)
	raw, err := os.ReadFile(oc.configPath)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if _, present := m["mcp"]; present {
		t.Errorf("mcp block present when unbound")
	}
	if _, present := m["permission"]; !present {
		t.Errorf("permission block missing when unbound")
	}
}

// Close must remove the session's config file.
func TestOpencodeCloseRemovesConfig(t *testing.T) {
	o := &Opencode{bin: "opencode"}
	sess, err := o.NewSession(context.Background(), SessionOpts{WorkDir: t.TempDir(), Model: "x"})
	if err != nil {
		t.Fatal(err)
	}
	oc := sess.(*opencodeSession)
	path := oc.configPath
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("config file still present after Close (stat=%v)", err)
	}
}

// Send must pass --auto to `opencode run` so tool calls touching paths outside
// the worktree (the spec at .gummi/specs/... lives in the main checkout) are
// not silently rejected.
func TestOpencodeSendPassesAutoFlag(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	argsFile := dir + "/args"
	path := dir + "/opencode"
	body := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > " + argsFile + "\n" +
		`echo '{"type":"text","sessionID":"ses_test","part":{"id":"p1","type":"text","text":"ok"}}'` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	ag, err := NewOpencode(path)
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
	deadline := time.After(5 * time.Second)
	for {
		select {
		case e := <-sess.Events():
			if e.Kind == EventIdle {
				data, err := os.ReadFile(argsFile)
				if err != nil {
					t.Fatal(err)
				}
				lines := strings.Split(strings.TrimSpace(string(data)), "\n")
				var seen bool
				for _, l := range lines {
					if l == "--auto" {
						seen = true
						break
					}
				}
				if !seen {
					t.Errorf("opencode args %q missing --auto flag", lines)
				}
				return
			}
			if e.Kind == EventError {
				t.Fatalf("send errored: %v", e.Err)
			}
		case <-deadline:
			t.Fatal("no idle before deadline")
		}
	}
}

// Send exports OPENCODE_EXPERIMENTAL_OUTPUT_TOKEN_MAX into opencode's
// environment when (and only when) the role sets output_token_max — it is
// opencode's sole lever above its hardcoded 32000 per-step output cap.
func TestOpencodeSendInjectsOutputTokenMax(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	// The harness may already export this var; scrub it for the test's
	// duration so the "unset" subtest's absence assertion isn't confounded
	// by an ambient value leaking into the fake opencode's env dump.
	if old, ok := os.LookupEnv("OPENCODE_EXPERIMENTAL_OUTPUT_TOKEN_MAX"); ok {
		os.Unsetenv("OPENCODE_EXPERIMENTAL_OUTPUT_TOKEN_MAX")
		t.Cleanup(func() { os.Setenv("OPENCODE_EXPERIMENTAL_OUTPUT_TOKEN_MAX", old) })
	}
	run := func(t *testing.T, otm int) string {
		dir := t.TempDir()
		envFile := dir + "/env"
		path := dir + "/opencode"
		body := "#!/bin/sh\n" +
			"env > " + envFile + "\n" +
			`echo '{"type":"text","sessionID":"ses_test","part":{"id":"p1","type":"text","text":"ok"}}'` + "\n"
		if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
		ag, err := NewOpencode(path)
		if err != nil {
			t.Fatal(err)
		}
		defer ag.Close()
		ctx := context.Background()
		sess, err := ag.NewSession(ctx, SessionOpts{WorkDir: t.TempDir(), Model: "x", OutputTokenMax: otm})
		if err != nil {
			t.Fatal(err)
		}
		defer sess.Close()
		if err := sess.Send(ctx, "go"); err != nil {
			t.Fatal(err)
		}
		deadline := time.After(5 * time.Second)
		for {
			select {
			case e := <-sess.Events():
				if e.Kind == EventIdle {
					data, err := os.ReadFile(envFile)
					if err != nil {
						t.Fatal(err)
					}
					return string(data)
				}
				if e.Kind == EventError {
					t.Fatalf("send errored: %v", e.Err)
				}
			case <-deadline:
				t.Fatal("no idle before deadline")
			}
		}
	}
	t.Run("set", func(t *testing.T) {
		if env := run(t, 128000); !strings.Contains(env, "OPENCODE_EXPERIMENTAL_OUTPUT_TOKEN_MAX=128000") {
			t.Errorf("env missing OPENCODE_EXPERIMENTAL_OUTPUT_TOKEN_MAX=128000:\n%s", env)
		}
	})
	t.Run("unset", func(t *testing.T) {
		if env := run(t, 0); strings.Contains(env, "OPENCODE_EXPERIMENTAL_OUTPUT_TOKEN_MAX") {
			t.Errorf("otm=0 must not set OPENCODE_EXPERIMENTAL_OUTPUT_TOKEN_MAX:\n%s", env)
		}
	})
}

func TestOpencodeSendPassesOpencodeConfigEnv(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	envFile := dir + "/env"
	path := dir + "/opencode"
	body := "#!/bin/sh\n" +
		"env > " + envFile + "\n" +
		`echo '{"type":"text","sessionID":"ses_test","part":{"id":"p1","type":"text","text":"ok"}}'` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	ag, err := NewOpencode(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	ctx := context.Background()
	sess, err := ag.NewSession(ctx, SessionOpts{WorkDir: t.TempDir(), Model: "x", FeatureID: "FD-011"})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if err := sess.Send(ctx, "go"); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case e := <-sess.Events():
			if e.Kind == EventIdle {
				data, err := os.ReadFile(envFile)
				if err != nil {
					t.Fatal(err)
				}
				want := "OPENCODE_CONFIG=" + sess.(*opencodeSession).configPath
				if !strings.Contains(string(data), want) {
					t.Errorf("env missing %q:\n%s", want, data)
				}
				return
			}
			if e.Kind == EventError {
				t.Fatalf("send errored: %v", e.Err)
			}
		case <-deadline:
			t.Fatal("no idle before deadline")
		}
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

// A backend that exits cleanly (status 0) with zero stdout — no text, no
// tool call, no usage, no error line — is indistinguishable from a real
// empty pass today: the engine marks the session done with empty spend and
// no error, and the verdict falls to "unclear". Surface it as a legible
// EventError so the operator can tell a silent backend outage from a model
// that genuinely failed to reach a verdict.
func TestOpencodeZeroEventSessionSurfacesError(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	binary := dir + "/opencode"
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	ag, err := NewOpencode(binary)
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess, err := ag.NewSession(ctx, SessionOpts{WorkDir: t.TempDir(), Model: "x"})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if err := sess.Send(ctx, "critique the plan"); err != nil {
		t.Fatal(err)
	}
	var kinds []EventKind
	var errMsg string
	deadline := time.After(5 * time.Second)
	for {
		select {
		case e := <-sess.Events():
			kinds = append(kinds, e.Kind)
			if e.Kind == EventError && e.Err != nil {
				errMsg = e.Err.Error()
			}
			if e.Kind == EventIdle {
				t.Fatalf("zero-event opencode session ended idle (kinds=%v); must surface EventError", kinds)
			}
			if e.Kind == EventError {
				goto gotError
			}
		case <-deadline:
			t.Fatal("timed out waiting for an event")
		}
	}
gotError:
	if !strings.Contains(strings.ToLower(errMsg), "no output") &&
		!strings.Contains(strings.ToLower(errMsg), "no events") &&
		!strings.Contains(strings.ToLower(errMsg), "empty") {
		t.Errorf("EventError present but wording %q does not name the empty-session failure mode", errMsg)
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
