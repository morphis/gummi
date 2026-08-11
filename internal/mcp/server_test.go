package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
)

// stubLister returns a fixed tool set.
type stubLister struct {
	defs []agent.ToolDef
	err  error
}

func (l stubLister) ListTools(context.Context) ([]agent.ToolDef, error) { return l.defs, l.err }

// stubEngine records calls and returns a scripted result, optionally
// blocking until released (to exercise concurrency).
type stubEngine struct {
	mu     sync.Mutex
	calls  []string
	block  chan struct{}
	result string
	err    error
}

func (s *stubEngine) CallTool(_ context.Context, name string, _ json.RawMessage) (string, error) {
	s.mu.Lock()
	s.calls = append(s.calls, name)
	block := s.block
	s.mu.Unlock()
	if block != nil && name == "slow" {
		<-block
	}
	return s.result, s.err
}

type errBoom struct{}

func (errBoom) Error() string { return "boom" }

// client drives a Server over full-duplex pipes. Requests are issued via
// call/callAsync; a background router correlates every response to its
// waiting caller by id, so concurrent calls interleave correctly.
type client struct {
	mu      sync.Mutex
	pending map[string]chan map[string]any
	in      io.WriteCloser
}

// serve spins up the Server over pipes and returns the client side.
func serve(t *testing.T, srv *Server) *client {
	t.Helper()
	srvOut, respW := io.Pipe()
	reqR, reqW := io.Pipe()
	sc := bufio.NewScanner(srvOut)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	c := &client{pending: map[string]chan map[string]any{}, in: reqW}
	go func() {
		for sc.Scan() {
			var got map[string]any
			if err := json.Unmarshal(sc.Bytes(), &got); err != nil {
				continue
			}
			b, _ := json.Marshal(got["id"])
			c.mu.Lock()
			ch := c.pending[string(b)]
			c.mu.Unlock()
			if ch != nil {
				select {
				case ch <- got:
				default:
				}
			}
		}
	}()
	go func() { _ = srv.Serve(context.Background(), reqR, respW) }()
	t.Cleanup(func() { _ = reqW.Close(); _ = respW.Close() })
	return c
}

// register wires to so the response router delivers that id's frame to it.
func (c *client) register(id string, to chan map[string]any) {
	c.mu.Lock()
	c.pending[id] = to
	c.mu.Unlock()
}

// do writes one request and synchronously returns its response by id.
func (c *client) do(t *testing.T, id, body string, _ *map[string]any) map[string]any {
	t.Helper()
	ch := make(chan map[string]any, 1)
	c.dispatch(body, id, ch)
	select {
	case r := <-ch:
		return r
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out awaiting response id %s; body=%s", id, body)
		return nil
	}
}

// dispatch fires one request and routes its response to to by id.
func (c *client) dispatch(body, id string, to chan map[string]any) {
	c.register(id, to)
	if _, err := io.WriteString(c.in, body+"\n"); err != nil {
		close(to)
	}
}

// fire returns a channel that receives the response to one request by id.
func (c *client) fire(body, id string) <-chan map[string]any {
	ch := make(chan map[string]any, 1)
	c.dispatch(body, id, ch)
	return ch
}

func newServer() (*Server, *stubEngine) {
	eng := &stubEngine{result: "ok"}
	defs := []agent.ToolDef{
		{Name: "spec_view", Description: "read a section", Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"section": map[string]any{"type": "string"}},
		}},
		{Name: "ask_user", Description: "ask the user"},
	}
	srv := NewServer(stubLister{defs: defs}, eng)
	srv.version = "test"
	return srv, eng
}

// initialize returns the handshake result.
func TestInitialize(t *testing.T) {
	srv, _ := newServer()
	c := serve(t, srv)
	var got map[string]any
	r := c.do(t, "1", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`, &got)
	res := r["result"].(map[string]any)
	if res["protocolVersion"] != "2025-06-18" {
		t.Fatalf("protocolVersion = %v, want 2025-06-18", res["protocolVersion"])
	}
	si := res["serverInfo"].(map[string]any)
	if si["name"] != "gummi" || si["version"] != "test" {
		t.Fatalf("serverInfo = %v, want name gummi version test", si)
	}
	if res["capabilities"].(map[string]any)["tools"] == nil {
		t.Fatalf("capabilities missing tools: %v", res["capabilities"])
	}
}

// tools/list returns ToolDefs mapped 1:1 to MCP Tool descriptors.
func TestToolsList(t *testing.T) {
	srv, _ := newServer()
	c := serve(t, srv)
	var got map[string]any
	resp := c.do(t, "5", `{"jsonrpc":"2.0","id":5,"method":"tools/list"}`, &got)
	tools := resp["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("tools length = %d, want 2", len(tools))
	}
	first := tools[0].(map[string]any)
	if first["name"] != "spec_view" || first["description"] != "read a section" {
		t.Fatalf("first tool = %v, want spec_view/read a section", first)
	}
	props := first["inputSchema"].(map[string]any)["properties"].(map[string]any)
	if props["section"].(map[string]any)["type"] != "string" {
		t.Fatalf("inputSchema.properties.section.type = %v, want string", props["section"])
	}
}

// tools/call forwards to the EngineTransport and wraps the result.
func TestToolsCall(t *testing.T) {
	srv, eng := newServer()
	c := serve(t, srv)
	var got map[string]any
	resp := c.do(t, "2", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"spec_view","arguments":{"section":"Problem"}}}`, &got)
	res := resp["result"].(map[string]any)
	if res["isError"] != false {
		t.Fatalf("isError = %v, want false", res["isError"])
	}
	content := res["content"].([]any)[0].(map[string]any)
	if content["text"] != "ok" {
		t.Fatalf("text = %v, want ok", content["text"])
	}
	eng.mu.Lock()
	defer eng.mu.Unlock()
	if len(eng.calls) != 1 || eng.calls[0] != "spec_view" {
		t.Fatalf("engine calls = %v, want [spec_view]", eng.calls)
	}
}

// a failing EngineTransport surfaces as a CallToolResult with isError:true.
func TestToolsCallErrorResult(t *testing.T) {
	srv, eng := newServer()
	eng.err = errBoom{}
	c := serve(t, srv)
	var got map[string]any
	resp := c.do(t, "3", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"ask_user","arguments":{}}}`, &got)
	res := resp["result"].(map[string]any)
	if res["isError"] != true {
		t.Fatalf("isError = %v, want true", res["isError"])
	}
	if text := res["content"].([]any)[0].(map[string]any)["text"]; text != "boom" {
		t.Fatalf("text = %v, want boom", text)
	}
}

// an unknown method answers the JSON-RPC -32601 error object.
func TestUnknownMethod(t *testing.T) {
	srv, _ := newServer()
	c := serve(t, srv)
	var got map[string]any
	resp := c.do(t, "4", `{"jsonrpc":"2.0","id":4,"method":"resources/list"}`, &got)
	er := resp["error"].(map[string]any)
	if int(er["code"].(float64)) != MethodNotFound {
		t.Fatalf("error code = %v, want %d", er["code"], MethodNotFound)
	}
}

// Serve returns nil on EOF.
func TestServeReturnsOnEOF(t *testing.T) {
	srv, _ := newServer()
	if err := srv.Serve(context.Background(), strings.NewReader(""), io.Discard); err != nil {
		t.Fatalf("Serve on EOF = %v, want nil", err)
	}
}

// Two concurrent tool-calls interleave: the fast response arrives while
// the slow one is still in flight, correlated by id (not by arrival order).
func TestServeConcurrentCalls(t *testing.T) {
	srv, eng := newServer()
	eng.block = make(chan struct{})
	c := serve(t, srv)

	slow := c.fire(`{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"slow","arguments":{}}}`, "10")
	fast := c.fire(`{"jsonrpc":"2.0","id":11,"method":"tools/call","params":{"name":"fast","arguments":{}}}`, "11")

	// id 11 must arrive first; the engine is still blocked on id 10.
	select {
	case r := <-fast:
		if ident(r) != "11" {
			t.Fatalf("first response id = %s, want 11 (fast)", ident(r))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("fast response did not arrive while slow was in flight")
	}
	select {
	case <-slow:
		t.Fatal("slow response arrived before slow call was released")
	case <-time.After(100 * time.Millisecond):
		// expected: slow still blocked
	}
	close(eng.block)
	select {
	case r := <-slow:
		if ident(r) != "10" {
			t.Fatalf("second response id = %s, want 10 (slow)", ident(r))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("slow response did not arrive after release")
	}
}

func ident(m map[string]any) string {
	b, _ := json.Marshal(m["id"])
	return string(b)
}
