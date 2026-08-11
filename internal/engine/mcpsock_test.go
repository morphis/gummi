package engine

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/mcp"
	"github.com/morphis/gummi/internal/spec"
)

// fakeNoTools is an agent that cannot call client tools, so the engine
// bridges it over MCP.
type fakeNoTools struct{ *agent.Fake }

// sockConn is a raw framing client over one accepted session socket.
type sockConn struct {
	t    *testing.T
	conn net.Conn
	sc   *bufio.Scanner
	seq  int
}

func dialSock(t *testing.T, path string) *sockConn {
	t.Helper()
	conn, err := (&net.Dialer{}).DialContext(context.Background(), "unix", path)
	if err != nil {
		t.Fatalf("dial %s: %v", path, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	return &sockConn{t: t, conn: conn, sc: sc}
}

func (c *sockConn) send(v any) {
	c.t.Helper()
	if err := mcp.Encode(c.conn, v); err != nil {
		c.t.Fatal(err)
	}
}

// read scans for the next frame with the given json id (skipping any
// interleaved others — for single-flight tests there are none).
func (c *sockConn) read(id string) map[string]any {
	c.t.Helper()
	if !c.sc.Scan() {
		c.t.Fatalf("socket closed awaiting id %s", id)
	}
	var got map[string]any
	if err := jsonUnmarshal(c.t, c.sc.Bytes(), &got); err != nil {
		c.t.Fatal(err)
	}
	b, _ := jsonMarshal(got["id"])
	if string(b) != id {
		c.t.Fatalf("socket response id = %s, want %s", b, id)
	}
	return got
}

func (c *sockConn) hello(feature string) map[string]any {
	c.t.Helper()
	id := c.nextID()
	c.send(mcp.Request{
		JSONRPC: mcp.JSONRPC, ID: jsonRaw(id), Method: "hello",
		Params: jsonRaw(`{"feature":"` + feature + `"}`),
	})
	return c.read(id)
}

func (c *sockConn) nextID() string {
	c.seq++
	return i2s(c.seq)
}

// startMCPEndpoint returns only once the listener is accepting: dialing
// the returned path succeeds synchronously before any child spawns.
func TestMCPSockBindOrderingDial(t *testing.T) {
	e := newEngine(t, &fakeNoTools{agent.NewFake("")})
	f := domain.Feature{ID: "FD-001", Stage: domain.StageBrainstorm, Profile: "default"}
	path, teardown, err := e.startMCPEndpoint(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	defer teardown()
	if got, want := path, mcpSockPath(e.cfg.Workspace, f.ID); got != want {
		t.Fatalf("path = %s, want %s", got, want)
	}
	// the bind-ordering guarantee: dial succeeds without racing the accept
	conn, err := (&net.Dialer{}).DialContext(context.Background(), "unix", path)
	if err != nil {
		t.Fatalf("dial immediately after startMCPEndpoint: %v", err)
	}
	_ = conn.Close()
}

// a hello for the wrong feature is answered -32000 and the endpoint
// closes the connection rather than serving it.
func TestMCPSockHandshakeFeatureMismatch(t *testing.T) {
	e := newEngine(t, &fakeNoTools{agent.NewFake("")})
	f := domain.Feature{ID: "FD-001", Stage: domain.StageBrainstorm, Profile: "default"}
	path, teardown, err := e.startMCPEndpoint(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	defer teardown()
	resp := dialSock(t, path).hello("FD-999")
	er := resp["error"].(map[string]any)
	if int(er["code"].(float64)) != mcp.FeatureMismatch {
		t.Fatalf("error code = %v, want %d", er["code"], mcp.FeatureMismatch)
	}
}

// a correct hello is acknowledged and list_tools returns stageTools for
// the feature's stage, in order.
func TestMCPSockListTools(t *testing.T) {
	e := newEngine(t, &fakeNoTools{agent.NewFake("")})
	f := domain.Feature{ID: "FD-001", Stage: domain.StageBrainstorm, Profile: "default"}
	path, teardown, err := e.startMCPEndpoint(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	defer teardown()
	c := dialSock(t, path)
	if r := c.hello("FD-001"); r["error"] != nil {
		t.Fatalf("hello error: %v", r["error"])
	}
	id := c.nextID()
	c.send(mcp.Request{JSONRPC: mcp.JSONRPC, ID: jsonRaw(id), Method: "list_tools"})
	resp := c.read(id)
	tools := resp["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 4 {
		t.Fatalf("tools length = %d, want 4", len(tools))
	}
	for i, want := range []string{"ask_user", "spec_annotate", "spec_view", "spec_replace_section"} {
		if tools[i].(map[string]any)["name"] != want {
			t.Fatalf("tool[%d] = %v, want %s", i, tools[i].(map[string]any)["name"], want)
		}
	}
}

// call_tool with no live session for the feature answers an error.
func TestMCPSockCallToolNoSession(t *testing.T) {
	e := newEngine(t, &fakeNoTools{agent.NewFake("")})
	f := domain.Feature{ID: "FD-001", Stage: domain.StageBrainstorm, Profile: "default"}
	path, teardown, err := e.startMCPEndpoint(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	defer teardown()
	c := dialSock(t, path)
	c.hello("FD-001")
	id := c.nextID()
	c.send(mcp.Request{
		JSONRPC: mcp.JSONRPC, ID: jsonRaw(id), Method: "call_tool",
		Params: jsonRaw(`{"name":"spec_view","args":{}}`),
	})
	if resp := c.read(id); resp["error"] == nil {
		t.Fatalf("call_tool without a session succeeded: %v", resp)
	}
}

// two in-flight call_tools on one connection interleave: spec_view (fast,
// resolves immediately) returns while ask_user is still blocked on the
// human. The session is registered directly with a real spec file.
func TestMCPSockCallToolInterleave(t *testing.T) {
	e := newEngine(t, &fakeNoTools{agent.NewFake("")})
	f := domain.Feature{ID: "FD-001", Stage: domain.StageBrainstorm, Profile: "default"}

	specPath := filepath.Join(t.TempDir(), "spec.md")
	specBody := "# FD-001\n\n## Problem\n\nsome problem\n"
	if err := os.WriteFile(specPath, []byte(specBody), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Session{
		Feature: f, Role: agent.RoleArchitect, Interactive: true, specPath: specPath,
		state: StateInteractive, done: make(chan struct{}),
	}
	if !e.replace(f.ID, s) {
		t.Fatal("replace")
	}
	defer s.stop()

	path, teardown, err := e.startMCPEndpoint(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	defer teardown()
	c := dialSock(t, path)
	if r := c.hello("FD-001"); r["error"] != nil {
		t.Fatalf("hello error: %v", r["error"])
	}

	// slow ask_user blocks; fast spec_view must resolve first.
	slowID, fastID := c.nextID(), c.nextID()
	c.send(mcp.Request{
		JSONRPC: mcp.JSONRPC, ID: jsonRaw(slowID), Method: "call_tool",
		Params: jsonRaw(`{"name":"ask_user","args":{"question":"pick","options":[{"label":"a"}]}}`),
	})
	c.send(mcp.Request{
		JSONRPC: mcp.JSONRPC, ID: jsonRaw(fastID), Method: "call_tool",
		Params: jsonRaw(`{"name":"spec_view","args":{"section":"Problem"}}`),
	})

	// spec_view arrives while ask_user is still blocked.
	if !c.sc.Scan() {
		t.Fatal("socket closed before spec_view response")
	}
	var fast map[string]any
	if err := jsonUnmarshal(t, c.sc.Bytes(), &fast); err != nil {
		t.Fatal(err)
	}
	gotID, _ := jsonMarshal(fast["id"])
	if string(gotID) != fastID {
		t.Fatalf("first response id = %s, want %s (fast spec_view first)", gotID, fastID)
	}
	text := fast["result"].(map[string]any)["result"].(string)
	wantBody, _ := spec.ViewSection(specBody, "Problem")
	if text != wantBody {
		t.Fatalf("spec_view text = %q, want %q", text, wantBody)
	}

	// only now resolve the ask; its response is the chosen answer. Wait
	// for the ask_user dispatch to have registered the pending question
	// (it runs in its own socket goroutine) before answering.
	askDeadline := time.After(3 * time.Second)
	for s.Snapshot().PendingAsk == nil {
		select {
		case <-askDeadline:
			t.Fatal("ask_user pending question never registered")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if err := e.Answer(context.Background(), f.ID, "picked-option"); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if !c.sc.Scan() {
		t.Fatal("socket closed before ask_user response")
	}
	var slow map[string]any
	if err := jsonUnmarshal(t, c.sc.Bytes(), &slow); err != nil {
		t.Fatal(err)
	}
	gotID, _ = jsonMarshal(slow["id"])
	if string(gotID) != slowID {
		t.Fatalf("second response id = %s, want %s", gotID, slowID)
	}
	if text := slow["result"].(map[string]any)["result"].(string); text != "picked-option" {
		t.Fatalf("ask_user text = %q, want picked-option", text)
	}
}

// teardown removes the socket file and leaves the accept loop done.
func TestMCPSockTeardown(t *testing.T) {
	e := newEngine(t, &fakeNoTools{agent.NewFake("")})
	f := domain.Feature{ID: "FD-001", Stage: domain.StageBrainstorm, Profile: "default"}
	path, teardown, err := e.startMCPEndpoint(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	teardown()
	teardown() // idempotent
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("socket file still present after teardown: %v", err)
	}
	select {
	case <-time.After(2 * time.Second):
		t.Fatal("accept loop not joined within 2s")
	default:
	}
}

func jsonUnmarshal(t *testing.T, b []byte, v any) error {
	t.Helper()
	return json.Unmarshal(b, v)
}

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

func jsonRaw(s string) json.RawMessage { return json.RawMessage(s) }

func i2s(n int) string { return strconv.Itoa(n) }
