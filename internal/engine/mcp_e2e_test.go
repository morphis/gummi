package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
)

// gummiBin is the real binary built once by TestMain; the e2e tests exec
// it as `gummi __mcp --feature <id>` against a live in-test engine.
var gummiBin string

// TestMain builds the gummi binary so the e2e suite can exec it. There is
// deliberately no `t.Skip` guard on `go` availability: this is a test
// binary invoked by `go test`, so `go` is definitionally on PATH, and a
// masked build error would silently disable the only end-to-end coverage
// of the MCP transport.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "gummi-mcp-e2e")
	if err != nil {
		fmt.Fprintln(os.Stderr, "gummi-mcp-e2e tmpdir:", err)
		os.Exit(1)
	}
	gummiBin = filepath.Join(dir, "gummi")
	build := exec.CommandContext(context.Background(), "go", "build", "-o", gummiBin, "github.com/morphis/gummi/cmd/gummi")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building gummi for __mcp e2e: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// mcpChild execs the built binary as `__mcp` against the given socket,
// with stdin/stdout as full-duplex MCP pipes. done closes exactly once
// when the child reaps, so both the test body and the cleanup can await
// it without racing one shared value.
type mcpChild struct {
	t     *testing.T
	stdin io.WriteCloser
	sc    *bufio.Scanner
	done  chan struct{}
	seq   int
}

func startChild(t *testing.T, socket string) *mcpChild {
	return startChildFor(t, socket, "FD-001")
}

func startChildFor(t *testing.T, socket, featureID string) *mcpChild {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), gummiBin, "__mcp", "--feature", featureID)
	cmd.Env = append(os.Environ(), "GUMMI_MCP_SOCK="+socket)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start __mcp: %v\n%s", err, stderr.String())
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	c := &mcpChild{t: t, stdin: stdin, sc: sc, done: done}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	})
	return c
}

// send writes one MCP frame to the child's stdin.
func (c *mcpChild) send(body string) {
	c.t.Helper()
	if _, err := io.WriteString(c.stdin, body+"\n"); err != nil {
		c.t.Fatalf("write child stdin: %v", err)
	}
}

// nextID allocates a fresh numeric MCP id for a request.
func (c *mcpChild) nextID() string {
	c.seq++
	return fmt.Sprintf("%d", c.seq)
}

// receive scans the child's stdout for the MCP response with the given id.
func (c *mcpChild) receive(id string) map[string]any {
	c.t.Helper()
	deadline := time.After(testWaitTimeout)
	for {
		select {
		case <-deadline:
			c.t.Fatalf("timed out awaiting child response id %s", id)
		default:
		}
		if !c.sc.Scan() {
			c.t.Fatalf("child stdout ended awaiting id %s", id)
		}
		var got map[string]any
		if err := json.Unmarshal(c.sc.Bytes(), &got); err != nil {
			c.t.Fatalf("child frame unmarshal: %v", err)
		}
		b, _ := json.Marshal(got["id"])
		if string(b) == id {
			return got
		}
	}
}

// call writes a tools/call and returns its resolved CallToolResult text
// and isError flag.
func (c *mcpChild) call(section string, args string) (text string, isErr bool) {
	c.t.Helper()
	id := c.nextID()
	c.send(`{"jsonrpc":"2.0","id":` + id + `,"method":"tools/call","params":{"name":"` +
		section + `","arguments":` + args + `}}`)
	resp := c.receive(id)
	res := resp["result"].(map[string]any)
	text = res["content"].([]any)[0].(map[string]any)["text"].(string)
	isErr, _ = res["isError"].(bool)
	return text, isErr
}

// mcpE2ESetup builds a live in-test engine with a non-ClientTools fake,
// attaches a Spec-stage session (binding the MCP socket), and returns the
// engine, the socket path, and the session.
func mcpE2ESetup(t *testing.T) (*Engine, string, *Session) {
	t.Helper()
	e := newEngine(t, &fakeNoTools{agent.NewFake("ack")})
	f := feature(1, "Dark mode", domain.StageSpec)
	seedDraft(t, e, f)
	s, err := e.Attach(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	socket := mcpSockPath(e.cfg.Workspace, f.ID)
	t.Cleanup(func() { s.stop() })
	return e, socket, s
}

// initialize over MCP stdio succeeds; tools/list mirrors stageTools for
// the live Spec-stage feature in order.
func TestMCPEndToEndListAndSpecView(t *testing.T) {
	_, socket, _ := mcpE2ESetup(t)
	child := startChild(t, socket)

	id := child.nextID()
	child.send(`{"jsonrpc":"2.0","id":` + id + `,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	resp := child.receive(id)
	si := resp["result"].(map[string]any)["serverInfo"].(map[string]any)
	if si["name"] != "gummi" {
		t.Fatalf("serverInfo.name = %v, want gummi", si["name"])
	}

	id = child.nextID()
	child.send(`{"jsonrpc":"2.0","id":` + id + `,"method":"tools/list"}`)
	resp = child.receive(id)
	tools := resp["result"].(map[string]any)["tools"].([]any)
	want := []string{"ask_user", "spec_annotate", "spec_view", "spec_replace_section"}
	if len(tools) != len(want) {
		t.Fatalf("tools length = %d, want %d", len(tools), len(want))
	}
	for i, w := range want {
		if tools[i].(map[string]any)["name"] != w {
			t.Fatalf("tool[%d] = %v, want %s", i, tools[i].(map[string]any)["name"], w)
		}
	}
	if tools[0].(map[string]any)["inputSchema"] == nil {
		t.Fatalf("tool[0] missing inputSchema")
	}

	// spec_view round-trips the section bytes, byte-identical to the
	// engine's in-process handler.
	text, isErr := child.call("spec_view", `{"section":"Problem"}`)
	if isErr {
		t.Fatalf("spec_view isError: %s", text)
	}
	wantBody, _ := spec.ViewSection(engineSpecFixture, "Problem")
	if text != wantBody {
		t.Fatalf("spec_view text = %q, want %q", text, wantBody)
	}
}

// ask_user blocks until the engine's operator-answer path resolves it,
// then that answer returns as the CallToolResult — proving reuse of the
// engine's handleClientTool / resolveNow path.
func TestMCPEndToEndAskUserRoundTrip(t *testing.T) {
	e, socket, s := mcpE2ESetup(t)
	child := startChild(t, socket)
	defer func() { _ = child.stdin.Close() }()

	done := make(chan struct{})
	var text string
	go func() {
		defer close(done)
		text, _ = child.call("ask_user", `{"question":"theme?","options":[{"label":"dark"},{"label":"light"}]}`)
	}()

	deadline := time.After(testWaitTimeout)
	for s.Snapshot().PendingAsk == nil {
		select {
		case <-deadline:
			t.Fatal("ask_user never surfaced")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if err := e.Answer(context.Background(), "FD-001", "dark"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("ask_user call never returned")
	}
	if text != "dark" {
		t.Fatalf("ask_user result = %q, want dark", text)
	}
}

// two concurrent tools/call with distinct ids interleave: the fast
// spec_view response arrives before the blocked ask_user resolves.
func TestMCPEndToEndConcurrency(t *testing.T) {
	e, socket, s := mcpE2ESetup(t)
	child := startChild(t, socket)

	slow := child.nextID()
	fast := child.nextID()
	child.send(`{"jsonrpc":"2.0","id":` + slow + `,"method":"tools/call","params":{"name":"ask_user","arguments":{"question":"pick","options":[{"label":"a"}]}}}`)
	child.send(`{"jsonrpc":"2.0","id":` + fast + `,"method":"tools/call","params":{"name":"spec_view","arguments":{"section":"Problem"}}}`)

	// the fast response must arrive first, before the ask is answered.
	deadline := time.After(testWaitTimeout)
	first := make(chan string, 1)
	go func() {
		for child.sc.Scan() {
			var got map[string]any
			_ = json.Unmarshal(child.sc.Bytes(), &got)
			b, _ := json.Marshal(got["id"])
			first <- string(b)
			return
		}
	}()
	select {
	case id := <-first:
		if id != fast {
			t.Fatalf("first response id = %s, want %s (fast spec_view first)", id, fast)
		}
	case <-deadline:
		t.Fatal("no response before ask resolved")
	}

	ddl := time.After(testWaitTimeout)
	for s.Snapshot().PendingAsk == nil {
		select {
		case <-ddl:
			t.Fatal("ask_user never surfaced")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if err := e.Answer(context.Background(), "FD-001", "picked"); err != nil {
		t.Fatal(err)
	}
	if resp := child.receive(slow); resp["result"] == nil {
		t.Fatalf("ask_user response errored: %v", resp["error"])
	}
}

// lifecycle: when the engine session ends, the child exits 0 within 2s
// and the engine has no orphaned resolver entry (in-flight calls are
// torn down with the session).
func TestMCPEndToEndLifecycle(t *testing.T) {
	_, socket, s := mcpE2ESetup(t)
	child := startChild(t, socket)

	// put an ask_user in flight so teardown must release it.
	sendID := child.nextID()
	child.send(`{"jsonrpc":"2.0","id":` + sendID + `,"method":"tools/call","params":{"name":"ask_user","arguments":{"question":"q","options":[{"label":"a"}]}}}`)
	deadline := time.After(testWaitTimeout)
	for s.Snapshot().PendingAsk == nil {
		select {
		case <-deadline:
			t.Fatal("ask_user never surfaced")
		case <-time.After(5 * time.Millisecond):
		}
	}

	// end the session (interactive). The socket endpoint closes, tearing
	// down the child's in-flight call.
	s.stop()
	_ = child.stdin.Close()

	select {
	case <-child.done:
	case <-time.After(2 * time.Second):
		t.Fatal("child did not exit within 2s of session end")
	}
	if got := s.resolverCount(); got != 0 {
		t.Fatalf("session leaked %d resolver entries after teardown", got)
	}
}

// tools/list for an unimplemented method surfaces -32601.
func TestMCPEndToEndUnknownMethod(t *testing.T) {
	_, socket, _ := mcpE2ESetup(t)
	child := startChild(t, socket)
	id := child.nextID()
	child.send(`{"jsonrpc":"2.0","id":` + id + `,"method":"resources/list"}`)
	resp := child.receive(id)
	er := resp["error"].(map[string]any)
	if int(er["code"].(float64)) != -32601 {
		t.Fatalf("error code = %v, want -32601", er["code"])
	}
}

// mcpE2EReadonlySetup builds a live in-test engine with a read-only
// autonomous research investigate session (fake advertises
// ReadOnlyEnforce so the engine admits it), binding the MCP socket, and
// returns the engine, the socket path, and the session.
func mcpE2EReadonlySetup(t *testing.T) (*Engine, string, *Session) {
	t.Helper()
	fk := agent.NewFake("ack")
	fk.Caps.ReadOnlyEnforce = true
	e := newEngine(t, &fakeNoTools{fk})
	f := feature(1, "rs investigate", domain.StageInvestigate)
	f.ID = domain.FeatureID("RS-001")
	f.Kind = domain.KindResearch
	seedDraft(t, e, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	s := e.Get("RS-001")
	if s == nil {
		t.Fatal("no session for research investigate")
	}
	socket := mcpSockPath(e.cfg.Workspace, f.ID)
	t.Cleanup(func() { s.stop() })
	return e, socket, s
}

// TestMCPEndToEndReadonlySurface: a read-only research session's MCP
// tool surface is the stripped set — spec_replace_section and
// spec_annotate are absent (tools/list), and a hand-crafted
// call_tool spec_replace_section is refused without touching the
// artifact, so the MCP shim cannot rewrite the main checkout.
func TestMCPEndToEndReadonlySurface(t *testing.T) {
	_, socket, s := mcpE2EReadonlySetup(t)
	child := startChildFor(t, socket, "RS-001")

	id := child.nextID()
	child.send(`{"jsonrpc":"2.0","id":` + id + `,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	child.receive(id)

	id = child.nextID()
	child.send(`{"jsonrpc":"2.0","id":` + id + `,"method":"tools/list"}`)
	resp := child.receive(id)
	tools := resp["result"].(map[string]any)["tools"].([]any)
	names := make([]string, 0, len(tools))
	for _, tl := range tools {
		names = append(names, tl.(map[string]any)["name"].(string))
	}
	for _, gone := range []string{"spec_replace_section", "spec_annotate"} {
		for _, n := range names {
			if n == gone {
				t.Fatalf("read-only tools/list contains %s: %v", gone, names)
			}
		}
	}
	if !slices.Contains(names, "spec_view") {
		t.Fatalf("read-only tools/list lost spec_view: %v", names)
	}

	before, err := os.ReadFile(s.SpecPath())
	if err != nil {
		t.Fatal(err)
	}
	text, _ := child.call("spec_replace_section", `{"section":"Problem","body":"pwned"}`)
	if !strings.Contains(text, "not available") {
		t.Fatalf("spec_replace_section on a read-only session resolved to %q, want a not-available refusal", text)
	}
	after, err := os.ReadFile(s.SpecPath())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("read-only session's spec_replace_section mutated the artifact")
	}
}
