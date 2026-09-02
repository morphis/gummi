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

// consultMCPSockPath is the per-open unix socket address a ConsultSession's
// MCPTools backend dials — distinct from mcpSockPath's own per-feature path
// (mcpsock.go) because a card can have both a live stage session and a live
// consult session bound to the same feature id at once (asking a question
// about a card whose stage is still running), and the two must never share
// a socket. The nonce half (workspaceMCPNonce, reused as-is) covers the
// same case it covers there: this card's consult backend respawning after
// an idle timeout must never collide with the endpoint it is replacing.
func consultMCPSockPath(w state.Workspace, id domain.FeatureID, nonce string) string {
	path := filepath.Join(w.StateDir(), "mcp", fmt.Sprintf("consult-%s-%s.sock", id, nonce))
	if len(path) <= unixPathMax {
		return path
	}
	sum := sha256.Sum256([]byte(path))
	return filepath.Join(os.TempDir(), fmt.Sprintf("gummi-mcp-consult-%x.sock", sum[:6]))
}

// consultEndpoint is one ConsultSession spawn's inbound tool-call listener
// for an MCPTools backend — the card-scoped sibling of workspaceEndpoint,
// serving only the read-only three (consultTools) with id forced to the
// one card this session is bound to, never the model's own argument.
// Structurally mcpEndpoint's own shape (mcpsock.go), minus the
// flavor/readOnly fields a stage session needs and this never does, and
// answering through dispatchConsultTool instead of DispatchClientTool.
type consultEndpoint struct {
	engine *Engine
	id     domain.FeatureID
	ln     net.Listener

	ctx    context.Context
	cancel context.CancelFunc

	wg     sync.WaitGroup
	connMu sync.Mutex
	conns  map[net.Conn]struct{}
	closed bool
}

// startConsultMCPEndpoint binds and serves one ConsultSession spawn's
// inbound tool-call socket. Mirrors startMCPEndpoint's contract: the
// listener is accepting before this returns, and the returned teardown is
// single-shot and idempotent — stashed on the session via setMCPTeardown so
// Session.stop releases it exactly once, the same path a respawn (idle
// timeout) or Engine.Close already drives for a stage session's own
// endpoint.
func (e *Engine) startConsultMCPEndpoint(ctx context.Context, id domain.FeatureID) (string, func(), error) {
	path := consultMCPSockPath(e.cfg.Workspace, id, workspaceMCPNonce())
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, fmt.Errorf("consult mcp socket dir: %w", err)
	}
	_ = os.Remove(path)
	ln, err := (&net.ListenConfig{}).Listen(ctx, "unix", path)
	if err != nil {
		return "", nil, fmt.Errorf("consult mcp listen %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return "", nil, fmt.Errorf("consult mcp chmod %s: %w", path, err)
	}
	epCtx, epCancel := context.WithCancel(context.Background())
	ep := &consultEndpoint{
		engine: e,
		id:     id,
		ln:     ln,
		ctx:    epCtx,
		cancel: epCancel,
		conns:  map[net.Conn]struct{}{},
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
			ep.closed = true
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
func (ep *consultEndpoint) acceptLoop() {
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

// serveConn speaks the same session-socket JSON-RPC 2.0 codec mcpEndpoint
// does, checking the same {"feature":"<id>"} hello shape (helloParams,
// mcpsock.go) against this consult session's own bound card.
func (ep *consultEndpoint) serveConn(conn net.Conn) {
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
			if req.Method != "hello" || h.Feature != string(ep.id) {
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
			// each request in its own goroutine so one slow call (card_diff
			// on a large worktree) never blocks a peer call on this
			// connection.
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

// writeFrame serializes one response onto conn under wmu.
func (ep *consultEndpoint) writeFrame(conn net.Conn, wmu *sync.Mutex, v any) {
	wmu.Lock()
	defer wmu.Unlock()
	_ = mcp.Encode(conn, v)
}

// dispatch answers one list_tools / call_tool request against this
// session's read-only three.
func (ep *consultEndpoint) dispatch(conn net.Conn, wmu *sync.Mutex, req *mcp.Request) {
	var result json.RawMessage
	var err error
	switch req.Method {
	case "list_tools":
		result, err = ep.listTools()
	case "call_tool":
		result, err = ep.callTool(req)
	}
	if err != nil {
		ep.writeFrame(conn, wmu, mcp.ErrorObject{
			JSONRPC: mcp.JSONRPC, ID: req.ID,
			Error: mcp.ErrorData{Code: mcp.ToolError, Message: err.Error()},
		})
		return
	}
	ep.writeFrame(conn, wmu, mcp.Response{JSONRPC: mcp.JSONRPC, ID: req.ID, Result: result})
}

// listTools advertises consultTools() — the fixed, zero-argument
// read-only three, never the full seven a workspaceEndpoint offers.
func (ep *consultEndpoint) listTools() (json.RawMessage, error) {
	return mcp.MarshalTools(consultTools())
}

// callTool unmarshals one call_tool request and forwards it to
// dispatchConsultTool, which forces this endpoint's own bound card in as
// the argument regardless of what (if anything) the model's request
// carried.
func (ep *consultEndpoint) callTool(req *mcp.Request) (json.RawMessage, error) {
	var p callToolParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return nil, fmt.Errorf("call_tool: %w", err)
	}
	result, err := ep.engine.dispatchConsultTool(ep.ctx, ep.id, p.Name, p.Args)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"result": result})
}
