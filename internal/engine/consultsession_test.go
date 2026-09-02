package engine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/mcp"
)

// waitConsultIdle polls a consult session until its current backend is no
// longer busy — waitBoardIdle's counterpart.
func waitConsultIdle(t *testing.T, c *ConsultSession) {
	t.Helper()
	deadline := time.After(testWaitTimeout)
	for {
		if !c.Snapshot().Busy {
			return
		}
		select {
		case <-deadline:
			t.Fatal("consult session never went idle")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// TestOpenConsultIdempotent: a second OpenConsult call for the same card
// returns the identical *ConsultSession rather than spawning a second
// backend — the "one per card ever asked" contract.
func TestOpenConsultIdempotent(t *testing.T) {
	r := recordingAgent()
	e := newEngine(t, r)
	f := feature(1, "dark mode", domain.StageImplement)

	c1, err := e.OpenConsult(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	c2, err := e.OpenConsult(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if c1 != c2 {
		t.Fatalf("OpenConsult returned distinct sessions on the second call")
	}
	if r.count() != 1 {
		t.Errorf("backend spawned %d times, want 1", r.count())
	}
	if got := e.Consult(f.ID); got != c1 {
		t.Errorf("Consult(id) = %p, want the same session %p", got, c1)
	}
}

// TestConsultToolScopedToBoundCard: a card_status client-tool call from
// the consult session's own model — which the zero-parameter schema
// never lets name a different card — answers only for the card this
// session is bound to.
func TestConsultToolScopedToBoundCard(t *testing.T) {
	first := true
	ag := &agent.Fake{Responder: func(_ agent.SessionOpts, msg string) []agent.Event {
		if first {
			first = false
			return []agent.Event{
				{Kind: agent.EventClientToolCall, ToolCall: &agent.ToolCall{
					ID: "call-1", Name: cardStatusToolName, Args: json.RawMessage(`{}`),
				}},
			}
		}
		return []agent.Event{{Kind: agent.EventMessage, Text: "done"}, {Kind: agent.EventIdle}}
	}}
	ag.Caps = agent.Capabilities{ClientTools: true, UsageEvents: true, Interrupt: true}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
	t.Cleanup(func() { e.Close() })
	ctx := context.Background()

	f := feature(7, "consult me", domain.StageImplement)
	createFeature(t, store, f)

	c, err := e.OpenConsult(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Send(ctx, "how's it going?"); err != nil {
		t.Fatal(err)
	}

	type resolver interface {
		Resolved(string) (string, bool)
	}
	rs, ok := c.sess.agent().(resolver)
	if !ok {
		t.Fatal("fake session does not implement Resolved")
	}
	deadline := time.After(testWaitTimeout)
	var result string
	for {
		if got, done := rs.Resolved("call-1"); done {
			result = got
			break
		}
		select {
		case <-deadline:
			t.Fatal("card_status call never resolved")
		case <-time.After(5 * time.Millisecond):
		}
	}
	var item cardStatusItem
	if err := json.Unmarshal([]byte(result), &item); err != nil {
		t.Fatalf("card_status result not the expected JSON: %v (%q)", err, result)
	}
	if item.ID != string(f.ID) {
		t.Errorf("card_status answered for %q, want the bound card %q", item.ID, f.ID)
	}
}

// TestConsultMCPToolsWiring: an MCPTools backend (claude/codex/opencode/zz
// shape — MCPTools true, ClientTools false) gets a card-scoped inbound MCP
// endpoint (MCPSockPath set, Workspace left false since this dials in
// --feature <id> mode, not --workspace) and no opts.Tools — mirrors
// TestBoardMCPToolsWiring for the consult-session equivalent of that same
// capability branch.
func TestConsultMCPToolsWiring(t *testing.T) {
	r := &recorder{Fake: agent.NewFake("ok")}
	r.Fake.Caps = agent.Capabilities{MCPTools: true, UsageEvents: true, Interrupt: true}
	e := newEngine(t, r)
	f := feature(3, "mcp consult", domain.StageImplement)

	c, err := e.OpenConsult(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	opts := r.opts()
	if len(opts.Tools) != 0 {
		t.Errorf("an MCPTools backend must not get opts.Tools: %+v", opts.Tools)
	}
	if opts.MCPSockPath == "" {
		t.Fatalf("an MCPTools backend must get a card-scoped MCP endpoint: %+v", opts)
	}
	if opts.Workspace {
		t.Errorf("a consult session's MCP endpoint dials in --feature mode, not --workspace: %+v", opts)
	}
	if opts.FeatureID != string(f.ID) {
		t.Errorf("FeatureID = %q, want %q", opts.FeatureID, f.ID)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestConsultMCPEndpointServesReadOnlyThree: a real dial against the
// per-open consult MCP endpoint (the same socket an MCPTools backend's
// `gummi __mcp --feature <id>` child would dial) lists exactly the three
// read-only tools and answers card_status for the bound card even though
// the zero-parameter schema gives the caller no id to pass.
func TestConsultMCPEndpointServesReadOnlyThree(t *testing.T) {
	e := newEngine(t, &fakeNoTools{agent.NewFake("")})
	ctx := context.Background()
	f := feature(9, "endpoint check", domain.StageImplement)
	createFeature(t, e.cfg.Store, f)

	path, teardown, err := e.startConsultMCPEndpoint(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer teardown()

	c := dialSock(t, path)
	if r := c.hello(string(f.ID)); r["error"] != nil {
		t.Fatalf("hello error: %v", r["error"])
	}
	id := c.nextID()
	c.send(mcp.Request{JSONRPC: mcp.JSONRPC, ID: jsonRaw(id), Method: "list_tools"})
	resp := c.read(id)
	tools := resp["result"].(map[string]any)["tools"].([]any)
	want := []string{"card_status", "card_spec", "card_diff"}
	if len(tools) != len(want) {
		t.Fatalf("tools length = %d, want %d", len(tools), len(want))
	}
	for i, w := range want {
		if tools[i].(map[string]any)["name"] != w {
			t.Fatalf("tool[%d] = %v, want %s", i, tools[i].(map[string]any)["name"], w)
		}
	}

	callID := c.nextID()
	c.send(mcp.Request{
		JSONRPC: mcp.JSONRPC, ID: jsonRaw(callID), Method: "call_tool",
		Params: jsonRaw(`{"name":"card_status","args":{}}`),
	})
	callResp := c.read(callID)
	if callResp["error"] != nil {
		t.Fatalf("card_status call error: %v", callResp["error"])
	}
	result := callResp["result"].(map[string]any)["result"].(string)
	var item cardStatusItem
	if err := json.Unmarshal([]byte(result), &item); err != nil {
		t.Fatalf("card_status result not JSON: %v (%s)", err, result)
	}
	if item.ID != string(f.ID) {
		t.Errorf("card_status answered for %q, want the bound card %q (schema carries no id argument)", item.ID, f.ID)
	}

	// a wrong-feature hello on a fresh connection is refused, the same
	// FeatureMismatch mcpEndpoint's own per-stage socket gives.
	other := dialSock(t, path)
	r := other.hello("FD-999")
	er, ok := r["error"].(map[string]any)
	if !ok {
		t.Fatalf("hello with wrong feature should error, got %v", r)
	}
	if int(er["code"].(float64)) != mcp.FeatureMismatch {
		t.Fatalf("error code = %v, want %d", er["code"], mcp.FeatureMismatch)
	}
}

// TestConsultSpendPersistedCapExempt: a large usage event lands in the
// feature's persisted spend (recordUsage, shared with the stage pump),
// but never trips exhaustion — a ConsultSession has no budget
// (SessionOpts.MaxCredits is always 0), so overBudget/exhaust are never
// even reachable from handleConsult.
func TestConsultSpendPersistedCapExempt(t *testing.T) {
	ag := &agent.Fake{Responder: func(_ agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{
			{Kind: agent.EventMessage, Text: "big answer"},
			{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 10_000, OutputTokens: 500}},
			{Kind: agent.EventIdle},
		}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", Persist: true})
	t.Cleanup(func() { e.Close() })
	ctx := context.Background()

	f := feature(3, "envelope test", domain.StageImplement)
	f.Budget.Envelope = 5 // a tiny envelope the same spend would blow through as a stage
	createFeature(t, store, f)

	c, err := e.OpenConsult(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Send(ctx, "spend a lot, please"); err != nil {
		t.Fatal(err)
	}
	waitConsultIdle(t, c)

	got, err := store.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Spend.Credits != 10_000 {
		t.Errorf("persisted spend = %v, want 10000 (recordUsage must fire for a consult turn)", got.Spend.Credits)
	}
	if c.Snapshot().Err != nil {
		t.Errorf("consult session errored: %v", c.Snapshot().Err)
	}
	// nothing about this ever raised a budget gate: no session exists in
	// e.live for this card (a consult session is never installed there),
	// so there is nothing for the envelope gate to have fired against.
	if e.Get(f.ID) != nil {
		t.Errorf("a consult turn installed a session into e.live: %+v", e.Get(f.ID).Snapshot())
	}
}

// TestConsultSeedsFromFinishedStageSession: the very first OpenConsult
// call for a card carries over a finished autonomous stage's transcript
// — the "best conversation available" the Chosen approach names — into
// the fresh consult backend, in a genuinely separate session (the stage
// session's own agent is untouched).
func TestConsultSeedsFromFinishedStageSession(t *testing.T) {
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{
			{Kind: agent.EventMessage, Text: "implemented the thing"},
			{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 1}},
			{Kind: agent.EventIdle},
		}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })
	ctx := context.Background()

	f := feature(9, "seeded", domain.StageImplement)
	createFeature(t, store, f)
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, f.ID, StateDone)

	stageTranscript := e.Get(f.ID).Snapshot().Transcript
	if len(stageTranscript) == 0 {
		t.Fatal("finished stage session carries no transcript to seed from")
	}

	c, err := e.OpenConsult(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	seeded := c.Snapshot().Transcript
	if len(seeded) < len(stageTranscript) {
		t.Fatalf("consult transcript = %+v, want at least the stage's own %+v", seeded, stageTranscript)
	}
	if seeded[0].Content != stageTranscript[0].Content {
		t.Errorf("consult did not carry the stage session's transcript over: %+v", seeded[:1])
	}
	// the stage session itself is untouched — a genuinely separate
	// backend, not a reused handle.
	if e.Get(f.ID).Snapshot().Role == agent.RoleConsult {
		t.Error("OpenConsult mutated the stage session's own role")
	}
}

// TestConsultIdleTimeoutRespawnsWithCarryOver: once a consult session's
// backend idles out, it reports Live()==false and the next Send
// transparently respawns one whose opening context still carries every
// prior turn.
func TestConsultIdleTimeoutRespawnsWithCarryOver(t *testing.T) {
	ag := agent.NewFake("first reply")
	e := newEngine(t, ag)
	e.consultIdleTimeout = 10 * time.Millisecond
	f := feature(4, "idle out", domain.StageImplement)
	ctx := context.Background()

	c, err := e.OpenConsult(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Send(ctx, "first question"); err != nil {
		t.Fatal(err)
	}
	waitConsultIdle(t, c)
	firstSess := c.sess

	deadline := time.After(testWaitTimeout)
	for firstSess.Live() {
		select {
		case <-deadline:
			t.Fatal("consult backend never idled out")
		case <-time.After(5 * time.Millisecond):
		}
	}

	ag.Reply = "second reply"
	if err := c.Send(ctx, "second question"); err != nil {
		t.Fatal(err)
	}
	waitConsultIdle(t, c)

	if c.sess == firstSess {
		t.Fatal("Send did not respawn a fresh backend after the idle timeout")
	}
	snap := c.Snapshot()
	var sawFirst, sawSecond bool
	for _, m := range snap.Transcript {
		if m.Content == "first question" {
			sawFirst = true
		}
		if m.Content == "second question" {
			sawSecond = true
		}
	}
	if !sawFirst || !sawSecond {
		t.Fatalf("respawned session lost prior turns: %+v", snap.Transcript)
	}
}

// TestEngineCloseStopsConsultSessions: Close tears down every open
// consult session alongside e.live/e.board, and e.wg.Wait() (inside
// Close) returns — no goroutine leak.
func TestEngineCloseStopsConsultSessions(t *testing.T) {
	ag := agent.NewFake("hi")
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
	f := feature(5, "closing time", domain.StageImplement)

	c, err := e.OpenConsult(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() { defer close(done); _ = e.Close() }()
	select {
	case <-done:
	case <-time.After(testWaitTimeout):
		t.Fatal("Close did not return — a consult session's goroutine leaked")
	}

	if c.Snapshot().Busy {
		t.Error("consult session still busy after Close")
	}
	if e.Consult(f.ID) != nil {
		t.Error("Consult(id) still resolves a session after Close")
	}
}

// TestSessionLivePausedReportsFalse: Pause stops the backend but leaves
// agentSess non-nil (stop() closes it, never nils the field) — Live()
// has to read State(), not just the handle, to report false here.
func TestSessionLivePausedReportsFalse(t *testing.T) {
	ag := &agent.Fake{Responder: func(_ agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{{Kind: agent.EventToolCall, Tool: "noop"}}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })
	f := feature(11, "pause me", domain.StageImplement)
	withWorktree(t, wt, f)

	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, f.ID, StateRunning)
	if !e.Get(f.ID).Live() {
		t.Fatal("a running, attached session must report Live() == true")
	}
	if err := e.Pause(context.Background(), f.ID); err != nil {
		t.Fatal(err)
	}
	if e.Get(f.ID).Live() {
		t.Error("a paused session reports Live() == true")
	}
}

// TestSessionLiveNilAndZeroValueReportFalse covers the nil-receiver
// case (sessionFor returning nil) and a freshly zero-valued Session
// (never attached — the "restored after a restart" shape, minus the
// store round-trip session_test.go's restore tests already cover).
func TestSessionLiveNilAndZeroValueReportFalse(t *testing.T) {
	var nilSess *Session
	if nilSess.Live() {
		t.Error("a nil *Session reports Live() == true")
	}
	restored := &Session{state: StateInteractive} // no agentSess: exactly Attach's "restored, no agent" shape
	if restored.Live() {
		t.Error("a restored session with no agent handle reports Live() == true")
	}
}
