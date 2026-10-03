package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/mcp"
	"github.com/morphis/gummi/internal/state"
)

// memoryToolResolver is what a fake session's client-tool results are
// read back through (agent.Fake implements ToolResolver).
type memoryToolResolver interface {
	Resolved(string) (string, bool)
}

// driveMemoryTool routes one client-tool call through the same
// handleClientTool path a live backend exercises and returns what gummi
// resolved it with.
func driveMemoryTool(t *testing.T, e *Engine, s *Session, name, args string) string {
	t.Helper()
	callID := "c-" + name + "-" + args
	e.handleClientTool(s, &agent.ToolCall{ID: callID, Name: name, Args: json.RawMessage(args)})
	r, ok := s.agent().(memoryToolResolver)
	if !ok {
		t.Fatal("fake session is not a resolver")
	}
	got, done := r.Resolved(callID)
	if !done {
		t.Fatalf("%s(%s) was never resolved", name, args)
	}
	return got
}

// fRead reads a file whole as a string.
func fRead(path string) (string, error) {
	b, err := os.ReadFile(path)
	return string(b), err
}

// writeMemoryFile seeds one session-memory file directly, the way a
// previous run of the card would have left it.
func writeMemoryFile(t *testing.T, ws state.Workspace, id domain.FeatureID, name, content string) error {
	t.Helper()
	dir := ws.SessionMemoryDir(id)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600)
}

// TestAFreeformSessionIsOfferedMemoryTools: the freeform session's tool
// surface carries the memory pair — on a plain client-tools backend and,
// unlike the watch pair, also on one with a Monitor of its own, because
// memory has no native counterpart to duplicate. A backend with no tool
// route gets neither the tools nor their hint.
func TestAFreeformSessionIsOfferedMemoryTools(t *testing.T) {
	for _, tc := range []struct {
		name         string
		caps         agent.Capabilities
		wantMemory   bool
		wantWatchRef bool
	}{
		{name: "client tools", caps: agent.Capabilities{ClientTools: true, UsageEvents: true, Interrupt: true}, wantMemory: true},
		{name: "native watch", caps: agent.Capabilities{ClientTools: true, NativeWatch: true}, wantMemory: true, wantWatchRef: true},
		{name: "no tool route", caps: agent.Capabilities{UsageEvents: true, Interrupt: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var offered []string
			var hints []string
			ag := &agent.Fake{Responder: func(opts agent.SessionOpts, _ string) []agent.Event {
				mu.Lock()
				for _, td := range opts.Tools {
					offered = append(offered, td.Name)
				}
				hints = opts.SystemHints
				mu.Unlock()
				return []agent.Event{{Kind: agent.EventMessage, Text: "ok"}, {Kind: agent.EventIdle}}
			}}
			ag.Caps = tc.caps
			ws, store, wt := newRepo(t)
			e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
			t.Cleanup(func() { e.Close() })
			f := freeformCard(1, "remember it")
			createFeature(t, store, f)
			ff, err := e.OpenFreeform(context.Background(), f)
			if err != nil {
				t.Fatal(err)
			}
			if err := ff.Send(context.Background(), "hi"); err != nil {
				t.Fatal(err)
			}
			waitFreeformIdle(t, ff)
			mu.Lock()
			tools := strings.Join(offered, ",")
			joined := strings.Join(hints, "\n")
			mu.Unlock()
			if tc.wantMemory && (!strings.Contains(tools, memoryReadToolName) || !strings.Contains(tools, memoryWriteToolName)) {
				t.Errorf("tools offered = %s, want the memory pair", tools)
			}
			if !tc.wantMemory && strings.Contains(tools, memoryReadToolName) {
				t.Errorf("tools offered = %s, want no memory tools", tools)
			}
			if tc.wantMemory && !strings.Contains(joined, "memory_read and memory_write") {
				t.Errorf("hints lack the memory tools' hint:\n%s", joined)
			}
			if !tc.wantMemory && strings.Contains(joined, "memory_read") {
				t.Errorf("hints offer memory tools the backend was never handed:\n%s", joined)
			}
			if tc.wantWatchRef && strings.Contains(joined, freeformWatchHint) {
				t.Errorf("a native-watch backend was also handed gummi's watch hint:\n%s", joined)
			}
		})
	}
}

// TestMemoryToolsRoundTrip: a freeform session's memory is filled through
// the tools and read back — plan replaced and appended, dead-ends
// appended — and each write lands where the next backend's spawn hint
// reads it from (memoryCard). Global is refused outright: not a
// session's to write — the person fills it by hand and a later
// distillation pass owns what goes into it.
func TestMemoryToolsRoundTrip(t *testing.T) {
	ag := agent.NewFake("")
	ag.Caps = agent.Capabilities{ClientTools: true, UsageEvents: true, Interrupt: true}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
	t.Cleanup(func() { e.Close() })
	ctx := context.Background()

	f := freeformCard(2, "carry the plan")
	createFeature(t, store, f)
	ff, err := e.OpenFreeform(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := ff.Send(ctx, "hi"); err != nil {
		t.Fatal(err)
	}
	waitFreeformIdle(t, ff)
	sess := ff.Session()

	// reads of files nothing wrote yet are normal, not errors
	if got := driveMemoryTool(t, e, sess, memoryReadToolName, `{"which":"plan"}`); !strings.Contains(got, "nothing written yet") {
		t.Errorf("read of empty plan = %q", got)
	}

	if got := driveMemoryTool(t, e, sess, memoryWriteToolName, `{"which":"plan","content":"# Plan\n- split the parser"}`); got != "wrote plan" {
		t.Errorf("plan replace resolved %q", got)
	}
	if got := driveMemoryTool(t, e, sess, memoryWriteToolName, `{"which":"plan","content":"- part two","append":true}`); got != "appended to plan" {
		t.Errorf("plan append resolved %q", got)
	}
	planPath := filepath.Join(ws.SessionMemoryDir(f.ID), "plan.md")
	if b, err := fRead(planPath); err != nil || !strings.Contains(b, "split the parser") || !strings.Contains(b, "part two") {
		t.Errorf("plan.md lost an entry (%v):\n%s", err, b)
	}

	// a replace starts over: what it said is the whole file
	if got := driveMemoryTool(t, e, sess, memoryWriteToolName, `{"which":"plan","content":"# Plan, second try"}`); got != "wrote plan" {
		t.Errorf("plan re-replace resolved %q", got)
	}
	if b, _ := fRead(planPath); strings.Contains(b, "split the parser") {
		t.Errorf("plan replace did not replace:\n%s", b)
	}

	// dead-ends accumulate
	for i := 1; i <= 2; i++ {
		args := `{"which":"dead-ends","content":"attempt ` + string(rune('0'+i)) + ` failed","append":true}`
		if got := driveMemoryTool(t, e, sess, memoryWriteToolName, args); got != "appended to dead-ends" {
			t.Errorf("dead-ends append %d resolved %q", i, got)
		}
	}
	deadPath := filepath.Join(ws.SessionMemoryDir(f.ID), "dead-ends.md")
	if b, _ := fRead(deadPath); !strings.Contains(b, "attempt 1 failed") || !strings.Contains(b, "attempt 2 failed") {
		t.Errorf("dead-ends.md lost an entry:\n%s", b)
	}

	// global is not a session's to write — no replace, no append
	for _, args := range []string{
		`{"which":"global","content":"overwrite the world"}`,
		`{"which":"global","content":"sneak in with append","append":true}`,
	} {
		if got := driveMemoryTool(t, e, sess, memoryWriteToolName, args); !strings.Contains(got, "not written by sessions") {
			t.Errorf("global write %s resolved %q, want the refusal", args, got)
		}
	}
	if _, err := fRead(ws.GlobalMemoryFile()); err == nil {
		t.Error("a refused global write created global.md anyway")
	}

	// the person fills global by hand; a session still reads it
	if err := os.MkdirAll(ws.MemoryDir(), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ws.GlobalMemoryFile(), []byte("the repo's checks are make ci\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := driveMemoryTool(t, e, sess, memoryReadToolName, `{"which":"global"}`); !strings.Contains(got, "make ci") {
		t.Errorf("read of global = %q", got)
	}

	// an unknown which is refused by name, listing the three
	if got := driveMemoryTool(t, e, sess, memoryWriteToolName, `{"which":"notebook","content":"x"}`); !strings.Contains(got, `"dead-ends"`) {
		t.Errorf("unknown which resolved %q, want the three listed", got)
	}

	// and the next spawn's hint carries what was written
	card := e.memoryCard(f.ID)
	for _, want := range []string{"Global memory", "Session memory, this card's own (" + string(f.ID) + ")", "# Plan, second try", "attempt 2 failed"} {
		if !strings.Contains(card, want) {
			t.Errorf("memory card lacks %q:\n%s", want, card)
		}
	}
}

// TestMemoryCardInlineIsCapped: what rides the system prompt is bounded,
// and a file too big for its budget says so and points at memory_read.
func TestMemoryCardInlineIsCapped(t *testing.T) {
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(agent.NewFake("")), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
	t.Cleanup(func() { e.Close() })
	f := freeformCard(3, "bounded")

	// nothing written yet: no card at all
	if got := e.memoryCard(f.ID); got != "" {
		t.Errorf("memory card with nothing written = %q, want none", got)
	}

	// a plan far past its inline budget is cut, with the tail gone
	plan := strings.Repeat("a", maxSessionMemoryInline+100) + "TAIL-MARKER"
	if err := writeMemoryFile(t, ws, f.ID, "plan.md", plan); err != nil {
		t.Fatal(err)
	}
	card := e.memoryCard(f.ID)
	if strings.Contains(card, "TAIL-MARKER") {
		t.Error("the inline card carried a plan past its budget")
	}
	if !strings.Contains(card, "(truncated — read the rest with memory_read)") {
		t.Errorf("the truncation is silent:\n%s", card)
	}

	// a plan inside the budget rides whole
	small := "the approach is one file"
	if err := writeMemoryFile(t, ws, f.ID, "dead-ends.md", small); err != nil {
		t.Fatal(err)
	}
	if card := e.memoryCard(f.ID); !strings.Contains(card, small) {
		t.Errorf("memory card lacks the dead-ends line:\n%s", card)
	}
}

// TestMemoryReadsAreCappedAndRuneSafe: memory_read of a file past its
// cap truncates with the note — never loading the whole file to do it —
// and the cut lands on a rune boundary, since a split UTF-8 rune reads
// worse than a slightly shorter cut.
func TestMemoryReadsAreCappedAndRuneSafe(t *testing.T) {
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(agent.NewFake("")), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
	t.Cleanup(func() { e.Close() })
	f := freeformCard(5, "rune safe")
	createFeature(t, store, f)

	// a direct read needs the session's id and the engine's workspace,
	// not a spawned backend
	ff := &FreeformSession{engine: e, id: f.ID}

	// the plan's last runes straddle the cap, so a byte cut splits one
	content := strings.Repeat("a", maxMemoryFile-2) + "日本語"
	if err := writeMemoryFile(t, ws, f.ID, "plan.md", content); err != nil {
		t.Fatal(err)
	}
	got, err := ff.readMemory("plan")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "(truncated at ") {
		t.Errorf("oversize read not truncated:\n%.120s", got)
	}
	if !utf8.ValidString(got) {
		t.Errorf("the cut split a rune:\n%.120s", got)
	}
	if strings.Contains(got, "\uFFFD") {
		t.Errorf("the cut left a replacement rune behind:\n%.120s", got)
	}
}

// TestMemoryRefusesAnOversizeWrite: the cap is what keeps a memory file
// from becoming the per-spawn tax it exists to avoid.
func TestMemoryRefusesAnOversizeWrite(t *testing.T) {
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(agent.NewFake("")), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
	t.Cleanup(func() { e.Close() })
	f := freeformCard(4, "too big")
	createFeature(t, store, f)
	ff, err := e.OpenFreeform(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if err := ff.Send(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	waitFreeformIdle(t, ff)
	sess := ff.Session()
	big := strings.Repeat("x", maxMemoryFile+1)
	if got := driveMemoryTool(t, e, sess, memoryWriteToolName, `{"which":"plan","content":"`+big+`"}`); !strings.Contains(got, "capped") {
		t.Errorf("oversize replace resolved %q, want the cap", got)
	}

	// the cap holds on the append form too — an append onto a file
	// nothing wrote yet would otherwise bypass appendMemory's own
	// size check, which sees only the growth of a file with content
	if got := driveMemoryTool(t, e, sess, memoryWriteToolName, `{"which":"plan","content":"`+big+`","append":true}`); !strings.Contains(got, "capped") {
		t.Errorf("oversize append onto an empty file resolved %q, want the cap", got)
	}
	if b, err := fRead(filepath.Join(ws.SessionMemoryDir(f.ID), "plan.md")); err == nil && b != "" {
		t.Errorf("a refused oversize append wrote plan.md anyway:\n%.80s", b)
	}

	// and a file already at the cap refuses further growth with the
	// way out named: replace it tighter
	if err := writeMemoryFile(t, ws, f.ID, "plan.md", strings.Repeat("y", maxMemoryFile-4)); err != nil {
		t.Fatal(err)
	}
	if got := driveMemoryTool(t, e, sess, memoryWriteToolName, `{"which":"plan","content":"abcd","append":true}`); !strings.Contains(got, "replace it") {
		t.Errorf("append at the cap resolved %q, want the way out", got)
	}
}

// TestMemoryToolsAreRefusedOutsideFreeform: a stage session's durable
// context carrier is the spec, and the memory tools belong to no stage —
// the dispatch refuses them the way it refuses watch on a stage.
func TestMemoryToolsAreRefusedOutsideFreeform(t *testing.T) {
	e := newEngine(t, agent.NewFake("ack"))
	ctx := context.Background()
	s, err := e.Attach(ctx, feature(1, "spec work", domain.StagePlan))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{memoryReadToolName, memoryWriteToolName} {
		if got := driveMemoryTool(t, e, s, name, `{"which":"plan","content":"x"}`); !strings.Contains(got, "only available in a freeform session") {
			t.Errorf("%s on a stage session resolved %q", name, got)
		}
	}
}

// TestAFreeformMCPEndpointAdvertisesMemory: the MCP route lists what the
// session's hints promise — the memory pair rides the endpoint's extras
// beside the stage pair, exactly as the watch pair does.
func TestAFreeformMCPEndpointAdvertisesMemory(t *testing.T) {
	e := newEngine(t, &fakeNoTools{agent.NewFake("")})
	f := domain.Feature{ID: "FF-001", Stage: domain.StageOpen, Profile: "default"}
	path, teardown, err := e.startMCPEndpoint(context.Background(), f, flavorStage, memoryReadTool(), memoryWriteTool())
	if err != nil {
		t.Fatal(err)
	}
	defer teardown()
	c := dialSock(t, path)
	if r := c.hello("FF-001"); r["error"] != nil {
		t.Fatalf("hello error: %v", r["error"])
	}
	id := c.nextID()
	c.send(mcp.Request{JSONRPC: mcp.JSONRPC, ID: jsonRaw(id), Method: "list_tools"})
	resp := c.read(id)
	tools := resp["result"].(map[string]any)["tools"].([]any)
	names := make([]string, 0, len(tools))
	for _, td := range tools {
		names = append(names, td.(map[string]any)["name"].(string))
	}
	joined := strings.Join(names, ",")
	for _, want := range []string{memoryReadToolName, memoryWriteToolName, "ask_user", "resolve_annotation"} {
		if !strings.Contains(joined, want) {
			t.Errorf("tools listed = %s, want %s too", joined, want)
		}
	}
}
