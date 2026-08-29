package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/mcp"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
)

// helloWorkspace completes the workspace endpoint's own handshake, the
// counterpart to sockConn.hello (mcpsock_test.go) for the per-feature one.
func (c *sockConn) helloWorkspace() map[string]any {
	c.t.Helper()
	id := c.nextID()
	c.send(mcp.Request{
		JSONRPC: mcp.JSONRPC, ID: jsonRaw(id), Method: "hello",
		Params: jsonRaw(`{"mode":"workspace"}`),
	})
	return c.read(id)
}

// StartWorkspaceMCPEndpoint returns only once the listener is accepting,
// same bind-ordering guarantee startMCPEndpoint gives the per-feature
// endpoint, and its path is workspaceMCPSockPath's pid-suffixed one.
func TestWorkspaceMCPBindOrderingAndPath(t *testing.T) {
	e := newEngine(t, &fakeNoTools{agent.NewFake("")})
	path, teardown, err := e.StartWorkspaceMCPEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	defer teardown()
	base := filepath.Base(path)
	if want := fmt.Sprintf("ws-%d-", os.Getpid()); !strings.HasPrefix(base, want) {
		t.Fatalf("path %s does not have the pid-nonce prefix %q", path, want)
	}
	if !strings.HasPrefix(path, filepath.Join(e.cfg.Workspace.StateDir(), "mcp")) {
		t.Fatalf("path %s is not under the workspace's mcp dir", path)
	}
	conn, err := (&net.Dialer{}).DialContext(context.Background(), "unix", path)
	if err != nil {
		t.Fatalf("dial immediately after StartWorkspaceMCPEndpoint: %v", err)
	}
	_ = conn.Close()
}

// Two engines over the same workspace get two distinct sockets (the
// socket-steal hazard the pid suffix exists to close) and both endpoints
// stay independently reachable.
func TestWorkspaceMCPTwoEnginesDontCollide(t *testing.T) {
	ws, store, wt := newRepo(t)
	e1 := New(Config{Agents: singleAgent(agent.NewFake("")), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
	t.Cleanup(func() { e1.Close() })
	e2 := New(Config{Agents: singleAgent(agent.NewFake("")), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
	t.Cleanup(func() { e2.Close() })

	p1, td1, err := e1.StartWorkspaceMCPEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	defer td1()
	p2, td2, err := e2.StartWorkspaceMCPEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	defer td2()
	if p1 == p2 {
		t.Fatalf("two engines on one workspace bound the same socket: %s", p1)
	}
	// both still answer: tearing down neither stole the other's listener.
	for _, p := range []string{p1, p2} {
		c := dialSock(t, p)
		if r := c.helloWorkspace(); r["error"] != nil {
			t.Fatalf("hello on %s: %v", p, r["error"])
		}
	}
}

// a hello that isn't {"mode":"workspace"} — including a well-formed
// per-feature hello — is answered ModeMismatch and the connection closes
// rather than being served.
func TestWorkspaceMCPHandshakeModeMismatch(t *testing.T) {
	e := newEngine(t, &fakeNoTools{agent.NewFake("")})
	path, teardown, err := e.StartWorkspaceMCPEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	defer teardown()
	resp := dialSock(t, path).hello("FD-001") // the *feature* hello shape
	er := resp["error"].(map[string]any)
	if int(er["code"].(float64)) != mcp.ModeMismatch {
		t.Fatalf("error code = %v, want %d", er["code"], mcp.ModeMismatch)
	}
}

// list_tools advertises the fixed board-level set, in order, regardless
// of any card's stage (there is none to depend on).
func TestWorkspaceMCPListTools(t *testing.T) {
	e := newEngine(t, &fakeNoTools{agent.NewFake("")})
	path, teardown, err := e.StartWorkspaceMCPEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	defer teardown()
	c := dialSock(t, path)
	if r := c.helloWorkspace(); r["error"] != nil {
		t.Fatalf("hello error: %v", r["error"])
	}
	id := c.nextID()
	c.send(mcp.Request{JSONRPC: mcp.JSONRPC, ID: jsonRaw(id), Method: "list_tools"})
	resp := c.read(id)
	tools := resp["result"].(map[string]any)["tools"].([]any)
	want := []string{"board_list", "card_status", "card_spec", "card_diff", "card_run", "card_resume"}
	if len(tools) != len(want) {
		t.Fatalf("tools length = %d, want %d", len(tools), len(want))
	}
	for i, w := range want {
		if tools[i].(map[string]any)["name"] != w {
			t.Fatalf("tool[%d] = %v, want %s", i, tools[i].(map[string]any)["name"], w)
		}
	}
}

// callWorkspaceTool is the test helper every board-level call_tool test
// below drives: hello, then one call_tool, decoded into (raw text
// result, error message or "").
func callWorkspaceTool(t *testing.T, path, name string, args map[string]any) (string, string) {
	t.Helper()
	c := dialSock(t, path)
	if r := c.helloWorkspace(); r["error"] != nil {
		t.Fatalf("hello error: %v", r["error"])
	}
	b, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	id := c.nextID()
	c.send(mcp.Request{
		JSONRPC: mcp.JSONRPC, ID: jsonRaw(id), Method: "call_tool",
		Params: jsonRaw(`{"name":"` + name + `","args":` + string(b) + `}`),
	})
	resp := c.read(id)
	if e, ok := resp["error"].(map[string]any); ok {
		return "", e["message"].(string)
	}
	return resp["result"].(map[string]any)["result"].(string), ""
}

// board_list reflects every stored feature, and reads a card's kind
// default (empty column reads as "feature") the same way the store does.
func TestWorkspaceMCPBoardList(t *testing.T) {
	e := newEngine(t, &fakeNoTools{agent.NewFake("")})
	ctx := context.Background()
	f1 := feature(1, "one", domain.StageImplement)
	f1.Budget.Envelope = 100
	if err := e.cfg.Store.CreateFeature(ctx, &f1); err != nil {
		t.Fatal(err)
	}
	f2 := bugFeature("two")
	if err := e.cfg.Store.CreateFeature(ctx, &f2); err != nil {
		t.Fatal(err)
	}

	path, teardown, err := e.StartWorkspaceMCPEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	defer teardown()

	result, errMsg := callWorkspaceTool(t, path, "board_list", map[string]any{})
	if errMsg != "" {
		t.Fatalf("board_list error: %s", errMsg)
	}
	var items []boardListItem
	if err := json.Unmarshal([]byte(result), &items); err != nil {
		t.Fatalf("board_list result not JSON: %v (%s)", err, result)
	}
	if len(items) != 2 {
		t.Fatalf("board_list items = %d, want 2", len(items))
	}
	if items[0].ID != "FD-001" || items[0].Kind != "feature" || items[0].Envelope != 100 {
		t.Errorf("items[0] = %+v", items[0])
	}
	if items[1].ID != "BG-002" || items[1].Kind != "bug" {
		t.Errorf("items[1] = %+v", items[1])
	}
}

// card_status reports stage, spend/envelope, and blockers for a resolved
// card, and refuses an unknown id.
func TestWorkspaceMCPCardStatus(t *testing.T) {
	e := newEngine(t, &fakeNoTools{agent.NewFake("")})
	ctx := context.Background()
	f := feature(1, "one", domain.StageImplement)
	f.Budget.Envelope = 50
	if err := e.cfg.Store.CreateFeature(ctx, &f); err != nil {
		t.Fatal(err)
	}

	path, teardown, err := e.StartWorkspaceMCPEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	defer teardown()

	result, errMsg := callWorkspaceTool(t, path, "card_status", map[string]any{"id": "FD-001"})
	if errMsg != "" {
		t.Fatalf("card_status error: %s", errMsg)
	}
	var item cardStatusItem
	if err := json.Unmarshal([]byte(result), &item); err != nil {
		t.Fatalf("card_status result not JSON: %v (%s)", err, result)
	}
	// CreateFeature never writes spend (it accumulates via metering, not
	// creation), so only stage/envelope round-trip here.
	if item.ID != "FD-001" || item.Stage != string(domain.StageImplement) || item.Envelope != 50 {
		t.Errorf("card_status = %+v", item)
	}
	if item.BranchState != "none" {
		t.Errorf("branch state = %q, want none (no worktree created)", item.BranchState)
	}

	if _, errMsg := callWorkspaceTool(t, path, "card_status", map[string]any{"id": "FD-999"}); errMsg == "" {
		t.Fatal("card_status on an unknown id should error")
	}
}

// card_spec returns the draft's raw markdown before any artifact is
// promoted, and errors clearly when neither exists.
func TestWorkspaceMCPCardSpec(t *testing.T) {
	e := newEngine(t, &fakeNoTools{agent.NewFake("")})
	ctx := context.Background()
	f := feature(1, "one", domain.StageSpec)
	if err := e.cfg.Store.CreateFeature(ctx, &f); err != nil {
		t.Fatal(err)
	}

	path, teardown, err := e.StartWorkspaceMCPEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	defer teardown()

	if _, errMsg := callWorkspaceTool(t, path, "card_spec", map[string]any{"id": "FD-001"}); errMsg == "" {
		t.Fatal("card_spec before any draft/artifact exists should error")
	}

	draft := filepath.Join(e.cfg.Workspace.DraftsDir(), spec.DraftFilename(&f))
	if err := os.MkdirAll(filepath.Dir(draft), 0o750); err != nil {
		t.Fatal(err)
	}
	body := "# FD-001\n\n## Problem\n\nsome problem\n"
	if err := os.WriteFile(draft, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	result, errMsg := callWorkspaceTool(t, path, "card_spec", map[string]any{"id": "FD-001"})
	if errMsg != "" {
		t.Fatalf("card_spec error: %s", errMsg)
	}
	if result != body {
		t.Fatalf("card_spec = %q, want %q", result, body)
	}
}

// card_diff before a worktree exists errors clearly instead of panicking
// or reporting an empty diff as success.
func TestWorkspaceMCPCardDiffNoWorktree(t *testing.T) {
	e := newEngine(t, &fakeNoTools{agent.NewFake("")})
	ctx := context.Background()
	f := feature(1, "one", domain.StageImplement)
	if err := e.cfg.Store.CreateFeature(ctx, &f); err != nil {
		t.Fatal(err)
	}

	path, teardown, err := e.StartWorkspaceMCPEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	defer teardown()

	if _, errMsg := callWorkspaceTool(t, path, "card_diff", map[string]any{"id": "FD-001"}); errMsg == "" {
		t.Fatal("card_diff with no worktree should error")
	}
}

// card_run drives an autonomous stage in this same engine — the property
// the whole endpoint exists for — and refuses an interactive stage
// (there is no chat surface to attach one to over MCP).
func TestWorkspaceMCPCardRun(t *testing.T) {
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{{Kind: agent.EventMessage, Text: "done"}, {Kind: agent.EventIdle}}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "impl", domain.StageImplement)
	withWorktree(t, wt, f)
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}

	path, teardown, err := e.StartWorkspaceMCPEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	defer teardown()

	if _, errMsg := callWorkspaceTool(t, path, "card_run", map[string]any{"id": "FD-001"}); errMsg != "" {
		t.Fatalf("card_run error: %s", errMsg)
	}
	waitState(t, e, "FD-001", StateDone)

	// an interactive stage has no agent-driven run to kick off over MCP.
	f2 := feature(2, "spec", domain.StageBrainstorm)
	if err := store.CreateFeature(context.Background(), &f2); err != nil {
		t.Fatal(err)
	}
	if _, errMsg := callWorkspaceTool(t, path, "card_run", map[string]any{"id": "FD-002"}); errMsg == "" {
		t.Fatal("card_run on an interactive stage should error")
	}
}

// card_resume takes the same RunWith(note) path the UI's own
// "request changes" flow uses, and the note reaches the kickoff turn.
func TestWorkspaceMCPCardResumeAppendsNote(t *testing.T) {
	var mu sync.Mutex
	var got string
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		mu.Lock()
		got = msg
		mu.Unlock()
		return []agent.Event{{Kind: agent.EventIdle}}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "impl", domain.StageImplement)
	withWorktree(t, wt, f)
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}

	path, teardown, err := e.StartWorkspaceMCPEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	defer teardown()

	if _, errMsg := callWorkspaceTool(t, path, "card_resume", map[string]any{"id": "FD-001", "note": "please fix the lint error"}); errMsg != "" {
		t.Fatalf("card_resume error: %s", errMsg)
	}
	waitState(t, e, "FD-001", StateDone)
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(got, "please fix the lint error") {
		t.Fatalf("kickoff turn = %q, missing the resume note", got)
	}
}

// teardown removes the socket file and leaves the accept loop joined,
// idempotently — same contract as the per-feature endpoint.
func TestWorkspaceMCPTeardown(t *testing.T) {
	e := newEngine(t, &fakeNoTools{agent.NewFake("")})
	path, teardown, err := e.StartWorkspaceMCPEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	teardown()
	teardown() // idempotent
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("socket file still present after teardown: %v", err)
	}
}

// TestWorkspaceMCPTeardownClosesLiveConnections covers the half of
// teardown that unlinking the socket file cannot demonstrate. Once the
// path is removed, a fresh dial fails with ENOENT whether or not the
// listener is still serving, so "the file is gone" proves nothing about
// a child that already connected. This holds a live connection across
// teardown and requires it to be dropped: an agent mid-tool-call must
// not be left talking to an engine that believes it has shut down.
func TestWorkspaceMCPTeardownClosesLiveConnections(t *testing.T) {
	e := newEngine(t, &fakeNoTools{agent.NewFake("")})
	path, teardown, err := e.StartWorkspaceMCPEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial before teardown: %v", err)
	}
	defer conn.Close()

	teardown()

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var buf [1]byte
	if _, err := conn.Read(buf[:]); err == nil {
		t.Fatal("live connection still readable after teardown; it should have been closed")
	} else if errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatal("live connection left open across teardown (read timed out rather than failing)")
	}
}

// A workspace deep on disk must not push the pid-suffixed socket path
// past the unix socket length limit either — same fallback mcpSockPath
// uses, exercised here for the workspace path builder.
func TestWorkspaceMCPSockPathStaysUnderUnixLimit(t *testing.T) {
	longRoot := filepath.Join(t.TempDir(), strings.Repeat("d", 130))
	ws := state.Workspace{Root: longRoot}
	path := workspaceMCPSockPath(ws, workspaceMCPNonce())
	if len(path) > unixPathMax {
		t.Fatalf("socket path %d bytes exceeds unix limit: %s", len(path), path)
	}
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", path)
	if err != nil {
		t.Fatalf("bind long-root socket %s: %v", path, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove long-root socket: %v", err)
	}
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
}
