package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"runtime/debug"
	"sync"

	"github.com/morphis/gummi/internal/agent"
)

// ToolLister supplies the MCP tools/list surface. gummi's __mcp shim
// implements it against the engine's live stageTools; a test supplies a
// fake.
type ToolLister interface {
	ListTools(ctx context.Context) ([]agent.ToolDef, error)
}

// EngineTransport executes an MCP tools/call. gummi's __mcp shim
// implements it by forwarding the call to the engine over the session
// socket; a test supplies a fake.
type EngineTransport interface {
	CallTool(ctx context.Context, name string, args json.RawMessage) (string, error)
}

// protocolVersion is the MCP protocol version this server speaks.
const protocolVersion = "2025-06-18"

// Server is an MCP stdio server. It speaks JSON-RPC 2.0 line-delimited
// over any io.Reader/io.Writer pair and dispatches initialize,
// tools/list, and tools/call; every other method is answered with
// -32601. Tools are not implemented here — the EngineTransport and
// ToolLister supplied at construction own all tool semantics.
type Server struct {
	lister       ToolLister
	engine       EngineTransport
	version      string
	instructions string

	wmu sync.Mutex // serializes writes across the per-request goroutines
}

// NewServer returns a Server serving lister's tools through engine.
// instructions is served verbatim in the initialize response's
// "instructions" field (see handleInitialize); an empty string omits the
// key entirely rather than sending an empty one.
func NewServer(l ToolLister, t EngineTransport, instructions string) *Server {
	return &Server{lister: l, engine: t, version: versionString(), instructions: instructions}
}

// Serve runs the server loop over r (requests) and w (responses). Each
// request is dispatched in its own goroutine — a blocking tools/call
// (ask_user) must not stall a concurrent tools/call on the same
// connection — and responses are serialized onto w by one mutex. It
// returns nil when r reaches EOF or ctx is done; in-flight calls are
// canceled first so a client that hung up mid-ask cannot strand the loop.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var g sync.WaitGroup
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		req, err := Decode(line)
		if err != nil {
			continue // unparseable frame: it has no id to answer, drop it
		}
		g.Add(1)
		go func(req *Request) {
			defer g.Done()
			s.dispatch(sctx, w, req)
		}(req)
	}
	cancel() // release any in-flight blocking call so g.Wait can finish
	g.Wait()
	if err := sc.Err(); err != nil {
		return err
	}
	return nil
}

// dispatch answers one request, or processes a notification silently.
func (s *Server) dispatch(ctx context.Context, w io.Writer, req *Request) {
	result, err := s.handle(ctx, req)
	if len(req.ID) == 0 {
		return // a notification: no response expected
	}
	v := any(Response{JSONRPC: JSONRPC, ID: req.ID, Result: result})
	if err != nil {
		re, ok := err.(*rpcError)
		if !ok {
			re = &rpcError{code: -32000, message: err.Error()}
		}
		v = ErrorObject{JSONRPC: JSONRPC, ID: req.ID, Error: ErrorData{Code: re.code, Message: re.message}}
	}
	_ = s.write(w, v)
}

func (s *Server) handle(ctx context.Context, req *Request) (json.RawMessage, error) {
	switch req.Method {
	case "initialize":
		return s.handleInitialize(req)
	case "tools/list":
		return s.handleToolsList(ctx)
	case "tools/call":
		return s.handleToolsCall(ctx, req)
	default:
		return nil, &rpcError{code: MethodNotFound, message: "method not found"}
	}
}

type initializeParams struct {
	ProtocolVersion string `json:"protocolVersion"`
}

// handleInitialize honors the MCP initialize handshake with the server
// identity and the capabilities this feature implements (tools only).
func (s *Server) handleInitialize(req *Request) (json.RawMessage, error) {
	var p initializeParams
	if len(req.Params) > 0 {
		_ = json.Unmarshal(req.Params, &p)
	}
	ver := p.ProtocolVersion
	if ver == "" {
		ver = protocolVersion
	}
	result := map[string]any{
		"protocolVersion": ver,
		"serverInfo":      map[string]any{"name": "gummi", "version": s.version},
		"capabilities":    map[string]any{"tools": map[string]any{}},
	}
	if s.instructions != "" {
		result["instructions"] = s.instructions
	}
	return json.Marshal(result)
}

// handleToolsList maps the lister's ToolDefs 1:1 into MCP Tool
// descriptors (Parameters → inputSchema).
func (s *Server) handleToolsList(ctx context.Context) (json.RawMessage, error) {
	defs, err := s.lister.ListTools(ctx)
	if err != nil {
		return nil, err
	}
	return MarshalTools(defs)
}

type callParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// handleToolsCall forwards one tools/call to the EngineTransport and
// wraps its result as a CallToolResult (isError set on failure).
func (s *Server) handleToolsCall(ctx context.Context, req *Request) (json.RawMessage, error) {
	var p callParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return nil, &rpcError{code: -32602, message: "invalid params"}
	}
	if s.engine == nil {
		return nil, &rpcError{code: -32000, message: "no engine transport"}
	}
	result, err := s.engine.CallTool(ctx, p.Name, p.Arguments)
	isErr := err != nil
	text := result
	if isErr {
		text = err.Error()
	}
	return json.Marshal(map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isErr,
	})
}

func (s *Server) write(w io.Writer, v any) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return Encode(w, v)
}

// MarshalTools renders ToolDefs as an MCP tools/list result. Exported so
// the engine session socket speaks the same wire shape as the MCP client
// surface (one marshal for both).
func MarshalTools(defs []agent.ToolDef) (json.RawMessage, error) {
	tools := make([]map[string]any, 0, len(defs))
	for _, d := range defs {
		schema := d.Parameters
		if schema == nil {
			schema = map[string]any{"type": "object"}
		}
		tools = append(tools, map[string]any{
			"name":        d.Name,
			"description": d.Description,
			"inputSchema": schema,
		})
	}
	return json.Marshal(map[string]any{"tools": tools})
}

// versionString is the server's advertised version: the module version
// from build info, or "devel" under go run/go test.
func versionString() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "devel"
}
