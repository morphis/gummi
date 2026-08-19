package engine

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/mcp"
	"github.com/morphis/gummi/internal/state"
)

// unixPathMax is the longest accepted filesystem unix socket path (the
// platform sun_path length, 108 bytes, with room for the terminating NUL).
// Paths longer than this make bind() fail with EINVAL.
const unixPathMax = 107

// mcpSockPath is the per-feature, per-workspace unix socket address: the
// endpoint the engine binds a live feature session's tools to, and the
// path a `gummi __mcp --feature <id>` child reads from GUMMI_MCP_SOCK.
// Derived purely from the workspace + feature id so the listener (bound
// before the child spawns) and any future dial site agree without
// threading state between them. Every stage session binds an endpoint, so
// a workspace living deep on disk must not push the socket path past the
// unix path limit: when the workspace-scoped path would be too long, it
// falls back to a short, deterministic path under the system temp dir
// (bind and teardown both use this same value, so they agree).
func mcpSockPath(w state.Workspace, id domain.FeatureID) string {
	path := filepath.Join(w.StateDir(), "mcp", string(id)+".sock")
	if len(path) <= unixPathMax {
		return path
	}
	sum := sha256.Sum256([]byte(path))
	return filepath.Join(os.TempDir(), fmt.Sprintf("gummi-mcp-%x.sock", sum[:6]))
}

// mcpEndpoint is one live feature session's inbound tool-call listener.
// Each accepted connection must first complete a `hello{feature}`
// handshake (validated against the endpoint's captured feature); every
// subsequent `list_tools` / `call_tool` request runs in its own
// subgoroutine with responses serialized by a per-connection mutex, so a
// blocking `call_tool ask_user` can't park the reader or starve a peer
// `call_tool spec_view` on the same connection. It owns its lifetime:
// teardown cancels the endpoint context (releasing any in-flight blocking
// dispatch), closes the listener, joins the goroutines, and removes the
// socket file.
type mcpEndpoint struct {
	engine   *Engine
	feature  domain.Feature
	flavor   runFlavor
	readOnly bool
	ln       net.Listener

	ctx    context.Context
	cancel context.CancelFunc

	wg      sync.WaitGroup
	connMu  sync.Mutex
	conns   map[net.Conn]struct{}
	started bool
	closed  bool
}

// startMCPEndpoint binds and serves the inbound tool-call socket for a
// live feature. The listener is accepting before it returns, so a child
// inheriting GUMMI_MCP_SOCK (spawned after this) can dial immediately
// without racing the bind. The returned teardown is single-shot and
// idempotent: it cancels in-flight dispatches, closes the listener, waits
// for the accept and per-connection goroutines, and removes the socket
// file. It must be stashed on the Session (setMCPTeardown) so Session.stop
// invokes it exactly once; an error here leaves nothing behind to release.
func (e *Engine) startMCPEndpoint(ctx context.Context, f domain.Feature, flavor runFlavor, readOnly bool) (string, func(), error) {
	path := mcpSockPath(e.cfg.Workspace, f.ID)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, fmt.Errorf("mcp socket dir: %w", err)
	}
	// a stale socket from a crashed run would clash with net.Listen; drop
	// it. The 0o600 chmod below restores owner-only access.
	_ = os.Remove(path)
	ln, err := (&net.ListenConfig{}).Listen(ctx, "unix", path)
	if err != nil {
		return "", nil, fmt.Errorf("mcp listen %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return "", nil, fmt.Errorf("mcp chmod %s: %w", path, err)
	}
	epCtx, epCancel := context.WithCancel(context.Background())
	ep := &mcpEndpoint{
		engine:   e,
		feature:  f,
		flavor:   flavor,
		readOnly: readOnly,
		ln:       ln,
		ctx:      epCtx,
		cancel:   epCancel,
		conns:    map[net.Conn]struct{}{},
	}
	ep.wg.Add(1)
	go func() {
		defer ep.wg.Done()
		ep.acceptLoop()
	}()
	var once sync.Once
	teardown := func() {
		once.Do(func() {
			epCancel()
			_ = ln.Close()
			ep.connMu.Lock()
			for conn := range ep.conns {
				_ = conn.Close()
			}
			ep.connMu.Unlock()
			ep.wg.Wait()
			_ = os.Remove(path)
		})
	}
	return path, teardown, nil
}

// acceptLoop accepts connections until the listener closes (teardown).
func (ep *mcpEndpoint) acceptLoop() {
	for {
		conn, err := ep.ln.Accept()
		if err != nil {
			return
		}
		ep.connMu.Lock()
		if ep.closed {
			ep.connMu.Unlock()
			_ = conn.Close()
			return
		}
		ep.conns[conn] = struct{}{}
		ep.started = true
		ep.connMu.Unlock()
		ep.wg.Add(1)
		go func() {
			defer ep.wg.Done()
			defer func() {
				ep.connMu.Lock()
				delete(ep.conns, conn)
				ep.connMu.Unlock()
			}()
			ep.serveConn(conn)
		}()
	}
}

// serveConn speaks the session-socket JSON-RPC 2.0 codec on one accepted
// connection: a `hello{feature}` handshake first, then any number of
// list_tools / call_tool requests, each in its own subgoroutine with a
// shared write mutex.
func (ep *mcpEndpoint) serveConn(conn net.Conn) {
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var wmu sync.Mutex
	first := true
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		req, err := mcp.Decode(line)
		if err != nil {
			continue // unparseable frame: no id to answer
		}
		if first {
			var h helloParams
			_ = json.Unmarshal(req.Params, &h)
			first = false
			if req.Method != "hello" || h.Feature != string(ep.feature.ID) {
				if len(req.ID) > 0 {
					ep.writeFrame(conn, &wmu, mcp.ErrorObject{
						JSONRPC: mcp.JSONRPC, ID: req.ID,
						Error: mcp.ErrorData{Code: mcp.FeatureMismatch, Message: "feature mismatch"},
					})
				}
				return
			}
			if len(req.ID) > 0 {
				ep.writeFrame(conn, &wmu, mcp.Response{
					JSONRPC: mcp.JSONRPC, ID: req.ID,
					Result: json.RawMessage(fmt.Sprintf(`{"feature":%q}`, h.Feature)),
				})
			}
			continue
		}
		switch req.Method {
		case "list_tools", "call_tool":
			// each request in its own goroutine so a blocking call_tool
			// (ask_user) never blocks a peer call on this connection.
			ep.wg.Add(1)
			go func(req *mcp.Request) {
				defer ep.wg.Done()
				ep.dispatch(conn, &wmu, req)
			}(req)
		default:
			if len(req.ID) > 0 {
				ep.writeFrame(conn, &wmu, mcp.ErrorObject{
					JSONRPC: mcp.JSONRPC, ID: req.ID,
					Error: mcp.ErrorData{Code: mcp.MethodNotFound, Message: "method not found"},
				})
			}
		}
	}
}

type helloParams struct {
	Feature string `json:"feature"`
}

// writeFrame serializes one response onto conn under wmu.
func (ep *mcpEndpoint) writeFrame(conn net.Conn, wmu *sync.Mutex, v any) {
	wmu.Lock()
	defer wmu.Unlock()
	_ = mcp.Encode(conn, v)
}

// dispatch answers one list_tools / call_tool request by calling into the
// engine's live session machinery.
func (ep *mcpEndpoint) dispatch(conn net.Conn, wmu *sync.Mutex, req *mcp.Request) {
	var result json.RawMessage
	var err error
	switch req.Method {
	case "list_tools":
		result, err = ep.listTools()
	case "call_tool":
		result, err = ep.callTool(req)
	}
	if err != nil {
		code, msg := mcp.FeatureMismatch, err.Error()
		ep.writeFrame(conn, wmu, mcp.ErrorObject{
			JSONRPC: mcp.JSONRPC, ID: req.ID,
			Error: mcp.ErrorData{Code: code, Message: msg},
		})
		return
	}
	ep.writeFrame(conn, wmu, mcp.Response{JSONRPC: mcp.JSONRPC, ID: req.ID, Result: result})
}

// listTools mirrors stageTools for the endpoint's feature and the pass
// flavor the session was created for. The endpoint is bound per-run from
// newAgentSession, which holds the same flavor it used to build the
// session's stageHints/toolHint, so the tool list it advertises matches
// what that pass's prompt told the model existed.
func (ep *mcpEndpoint) listTools() (json.RawMessage, error) {
	defs := filterReadOnlyTools(stageTools(ep.feature.Stage, ep.flavor), ep.readOnly)
	return mcp.MarshalTools(defs)
}

type callToolParams struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// callTool forwards one tool call to the live session via
// DispatchClientTool — the same handleClientTool path a native backend
// uses — and returns its result. A missing session is an error: there is
// nothing live to execute against.
func (ep *mcpEndpoint) callTool(req *mcp.Request) (json.RawMessage, error) {
	var p callToolParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return nil, fmt.Errorf("call_tool: %w", err)
	}
	s := ep.engine.Get(ep.feature.ID)
	if s == nil {
		return nil, fmt.Errorf("feature %s has no live session", ep.feature.ID)
	}
	result, err := ep.engine.DispatchClientTool(ep.ctx, s, p.Name, p.Args)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"result": result})
}
