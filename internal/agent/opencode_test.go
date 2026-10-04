package agent

import (
	"context"
	"encoding/json"
	"os"
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
}

func TestOpencodeMapEventReasoning(t *testing.T) {
	s := newOCSession()
	var msg strings.Builder
	evs := s.mapEvent([]byte(`{"type":"reasoning","part":{"id":"r1","type":"reasoning","text":"Look at"}}`), &msg)
	if len(evs) != 1 || evs[0].Kind != EventReasoningDelta || evs[0].Text != "Look at" {
		t.Fatalf("reasoning = %+v, want one EventReasoningDelta 'Look at'", evs)
	}
	evs = s.mapEvent([]byte(`{"type":"reasoning","part":{"id":"r1","type":"reasoning","text":"Look at main.go"}}`), &msg)
	if len(evs) != 1 || evs[0].Text != " main.go" {
		t.Fatalf("reasoning suffix = %+v, want ' main.go'", evs)
	}
	if msg.Len() != 0 {
		t.Errorf("reasoning leaked into the message: %q", msg.String())
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

// A step-finish carries opencode's own price and the cache split: the
// price is metered even at $0 (a free model must not be token-priced into
// spend), cache reads go to CachedTokens and cache writes count as input,
// and the context size is the step's whole prompt — the shape is opencode
// 1.2.16's, where a warm turn's fresh input is a sliver of its context.
func TestOpencodeMapEventStepFinishCacheAndMetering(t *testing.T) {
	s := newOCSession()
	var msg strings.Builder
	evs := s.mapEvent([]byte(`{"type":"step_finish","part":{"type":"step-finish","reason":"stop","cost":0,`+
		`"tokens":{"total":13741,"input":45,"output":125,"reasoning":0,"cache":{"write":30,"read":13571}}}}`), &msg)
	if len(evs) != 2 || evs[0].Kind != EventUsage || evs[1].Kind != EventContext {
		t.Fatalf("step_finish = %+v, want [usage, context]", evs)
	}
	u := evs[0].Usage
	if !u.Metered || u.Credits != 0 || u.Estimate {
		t.Errorf("usage = %+v, want a metered zero-credit sample", u)
	}
	if u.InputTokens != 75 || u.CachedTokens != 13571 || u.OutputTokens != 125 {
		t.Errorf("usage tokens = in%d/cached%d/out%d, want in75/cached13571/out125", u.InputTokens, u.CachedTokens, u.OutputTokens)
	}
	if got := evs[1].Context.Tokens; got != 45+13571+30 {
		t.Errorf("context tokens = %d, want the whole prompt %d", got, 45+13571+30)
	}
}

// opencode reports a tool part once the call has finished, and the part
// carries its outcome. A permission denial is an "error" part; it must
// reach the engine as a failed result, or a model retrying the denial
// loops with nothing ever seeing it fail. The part shape is opencode
// 1.18's, the error text the one its permission layer writes.
func TestOpencodeMapEventToolOutcome(t *testing.T) {
	s := newOCSession()
	var msg strings.Builder
	denied := `{"type":"tool_use","part":{"type":"tool","tool":"bash","callID":"c9","state":{"status":"error",` +
		`"input":{"command":"HOME=/tmp/opencode/home make check"},` +
		`"error":"The user has specified a rule which prevents you from using this specific tool call. Here are some of the relevant rules [...]"}}}`
	evs := s.mapEvent([]byte(denied), &msg)
	if len(evs) != 2 || evs[0].Kind != EventToolCall || evs[1].Kind != EventToolResult {
		t.Fatalf("denied tool_use = %+v, want [tool-call, tool-result]", evs)
	}
	if evs[0].CallID != "c9" || evs[1].CallID != "c9" {
		t.Errorf("call ids = %q/%q, want c9 on both so the result pairs with its call", evs[0].CallID, evs[1].CallID)
	}
	if r := evs[1].Result; r == nil || r.OK || !strings.HasPrefix(r.Output, "The user has specified a rule") {
		t.Errorf("result = %+v, want a failure carrying opencode's reason", r)
	}

	done := `{"type":"tool_use","part":{"type":"tool","tool":"read","callID":"c10","state":{"status":"completed",` +
		`"input":{"filePath":"go.mod"},"output":"module x"}}}`
	evs = s.mapEvent([]byte(done), &msg)
	if len(evs) != 2 || evs[1].Kind != EventToolResult || evs[1].Result == nil || !evs[1].Result.OK || evs[1].Result.Output != "module x" {
		t.Fatalf("completed tool_use = %+v, want a successful result with its output", evs)
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

// Guarded is accepted and stamps the session's config with the ask-on-
// request catch-all; the adapter answers what the server asks for.
func TestOpencodeGuardedAccepted(t *testing.T) {
	_, spawns := stubServeOpencode(t)
	o := &Opencode{bin: "opencode"}
	sess, err := o.NewSession(context.Background(), SessionOpts{WorkDir: t.TempDir(), Model: "opencode/x", Permission: PermissionGuarded})
	if err != nil {
		t.Fatalf("guarded NewSession should succeed: %v", err)
	}
	if sess == nil {
		t.Fatal("guarded NewSession returned a nil session")
	}
	env := strings.Join((*spawns)[0].env, "\n")
	cfgPath := sess.(*opencodeSession).configPath
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("session config not readable: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	perm := m["permission"].(map[string]any)
	if perm["*"] != "ask" {
		t.Errorf("guarded config's catch-all = %v, want ask", perm["*"])
	}
	_ = env
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
	_, _ = stubServeOpencode(t)
	o := &Opencode{bin: "opencode"}
	wt := t.TempDir()
	sess, err := o.NewSession(context.Background(), SessionOpts{
		WorkDir: wt, Model: "opencode/x", MCPSockPath: "/tmp/mcp/FD-011.sock", FeatureID: "FD-011",
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

// An unbound session (no MCPSockPath/FeatureID) must still materialize the
// config file, but emit no top-level mcp key — so no __mcp child spawns.
func TestOpencodeNewSessionOmitsMCPWhenUnbound(t *testing.T) {
	_, _ = stubServeOpencode(t)
	o := &Opencode{bin: "opencode"}
	sess, err := o.NewSession(context.Background(), SessionOpts{WorkDir: t.TempDir(), Model: "opencode/x"})
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

// The session's server runs with the role's output-token cap exported,
// and with nothing exported when the role sets none — it is opencode's
// sole lever above its hardcoded 32000 per-step output cap.
func TestOpencodeSpawnInjectsOutputTokenMax(t *testing.T) {
	if old, ok := os.LookupEnv("OPENCODE_EXPERIMENTAL_OUTPUT_TOKEN_MAX"); ok {
		os.Unsetenv("OPENCODE_EXPERIMENTAL_OUTPUT_TOKEN_MAX")
		t.Cleanup(func() { os.Setenv("OPENCODE_EXPERIMENTAL_OUTPUT_TOKEN_MAX", old) })
	}
	for _, tc := range []struct {
		name    string
		otm     int
		want    string
		present bool
	}{
		{"set", 128000, "OPENCODE_EXPERIMENTAL_OUTPUT_TOKEN_MAX=128000", true},
		{"unset", 0, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, spawns := stubServeOpencode(t)
			sess, err := (&Opencode{bin: "opencode"}).NewSession(context.Background(),
				SessionOpts{WorkDir: t.TempDir(), Model: "opencode/x", OutputTokenMax: tc.otm})
			if err != nil {
				t.Fatal(err)
			}
			defer sess.Close()
			env := strings.Join((*spawns)[0].env, "\n")
			if tc.present && !strings.Contains(env, tc.want) {
				t.Errorf("spawn env missing %q:\n%s", tc.want, env)
			}
			if !tc.present && strings.Contains(env, "OPENCODE_EXPERIMENTAL_OUTPUT_TOKEN_MAX") {
				t.Errorf("otm=0 must not set OPENCODE_EXPERIMENTAL_OUTPUT_TOKEN_MAX:\n%s", env)
			}
		})
	}
}

// Close must remove the session's config file.
func TestOpencodeCloseRemovesConfig(t *testing.T) {
	_, _ = stubServeOpencode(t)
	o := &Opencode{bin: "opencode"}
	sess, err := o.NewSession(context.Background(), SessionOpts{WorkDir: t.TempDir(), Model: "opencode/x"})
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

// A session id handed in by the engine is what the turn's message POST
// addresses: without it a session opened after a restart begins a blank
// conversation and re-reads what the last one had open. The adapter also
// has to publish the id, or the engine can never persist one to hand back.
func TestOpencodeResumesAHandedInSession(t *testing.T) {
	f, _ := stubServeOpencode(t)
	sess := ocSession(t, SessionOpts{WorkDir: t.TempDir(), Model: "opencode/x", ResumeID: "ses_prior"})
	f.mu.Lock()
	f.cur = "ses_prior" // the conversation the session continues
	f.mu.Unlock()
	id, ok := Session(sess).(Identified)
	if !ok {
		t.Fatal("opencode session does not implement Identified; the engine cannot persist its id")
	}
	if id.SessionID() != "ses_prior" {
		t.Errorf("SessionID() = %q, want the session it was told to continue", id.SessionID())
	}
	runCleanTurn(t, f, sess)
	f.mu.Lock()
	msgs := append([]ocFakeMsg(nil), f.msgs...)
	created := append([]string(nil), f.sids...)
	f.mu.Unlock()
	if len(created) != 0 {
		t.Errorf("a resumed turn created server sessions: %v", created)
	}
	if len(msgs) != 1 || msgs[0].SessionID != "ses_prior" {
		t.Errorf("turn addressed %v, want the handed-in session", msgs)
	}
}

// A session whose backend failed mid-task reports FirstTurn false — the
// signal a caller needs to tell a misconfigured backend from a mid-task
// crash.
func TestOpencodeRunFailureFirstTurnFalseAfterASuccess(t *testing.T) {
	f, _ := stubServeOpencode(t)
	sess := ocSession(t, SessionOpts{WorkDir: t.TempDir(), Model: "opencode/x"})
	runCleanTurn(t, f, sess)

	f.failNext.Store(true)
	if err := sess.Send(context.Background(), "go again"); err != nil {
		t.Fatal(err)
	}
	rf := waitTurnFailure(t, sess)
	if rf.FirstTurn {
		t.Error("RunFailure.FirstTurn = true on a session's second turn")
	}
}

// The zero-spend sanity: usage carries the model the turn ran.
func TestOpencodeUsageCarriesModel(t *testing.T) {
	f, _ := stubServeOpencode(t)
	sess := ocSession(t, SessionOpts{WorkDir: t.TempDir(), Model: "opencode/gpt-5"})
	f.holdTurns()
	if err := sess.Send(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	f.waitPosted(t)
	f.push(f.partEvent("step-finish", "s1", ""))
	f.releaseTurn()
	var u *Usage
	for _, e := range waitTurnEnd(t, sess) {
		if e.Kind == EventUsage {
			u = &e.Usage
		}
	}
	if u == nil || u.Model != "opencode/gpt-5" {
		t.Errorf("usage = %+v, want the turn's model", u)
	}
}

// The context window is the catalog's: once the server is up the session
// asks /config/providers for its model's limit, and the context events it
// reports from then on carry it.
func TestOpencodeContextLimitFromCatalog(t *testing.T) {
	f, _ := stubServeOpencode(t)
	sess := ocSession(t, SessionOpts{WorkDir: t.TempDir(), Model: "opencode/qwen3-coder"})
	deadline := time.Now().Add(5 * time.Second)
	for {
		runCleanTurn(t, f, sess)
		if sess.contextLimitValue() == 262144 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("context limit = %d, want the catalog's 262144", sess.contextLimitValue())
		}
	}
	f.mu.Lock()
	posted := len(f.msgs)
	f.mu.Unlock()
	f.holdTurns()
	if err := sess.Send(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	f.waitPostedN(t, posted+1)
	f.push(f.partEvent("step-finish", "s1", ""))
	f.releaseTurn()
	var c *Context
	for _, e := range waitTurnEnd(t, sess) {
		if e.Kind == EventContext {
			c = &e.Context
		}
	}
	if c == nil || c.Limit != 262144 {
		t.Errorf("context = %+v, want the catalog's limit", c)
	}
}
