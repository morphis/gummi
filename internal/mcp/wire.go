// Package mcp speaks the Model Context Protocol transport that a
// template spreadsheet — gummi's tool surface served over MCP — needs:
// JSON-RPC 2.0, line-delimited, over any io.Reader/io.Writer pair. It is
// deliberately a smaller surface than the MCP SDK: only the methods this
// feature ships (initialize, tools/list, tools/call) are dispatched, and
// the on-the-wire framing mirrors the line-delimited style of the
// headless agent protocol so both sides stay tiny and auditable.
package mcp

import (
	"encoding/json"
	"fmt"
	"io"
)

// JSONRPC is the wire protocol version this transport speaks.
const JSONRPC = "2.0"

// MCP error codes (JSON-RPC 2.0 reserved + MCP-specified).
const (
	// MethodNotFound is the standard JSON-RPC code for a method the
	// server does not implement. Every surface outside this feature's
	// scope (resources, prompts, sampling, …) is answered with it.
	MethodNotFound = -32601
	// FeatureMismatch is gummi's own code for a hello handshake whose
	// feature id does not match the endpoint's captured one.
	FeatureMismatch = -32000
	// ModeMismatch is the workspace-scoped endpoint's own handshake
	// failure code: a hello that isn't {"mode":"workspace"}. It is
	// distinct from FeatureMismatch because a workspace connection
	// carries no feature id to mismatch against — the two endpoints
	// validate different things at hello, so a caller can tell which
	// handshake it failed rather than both collapsing to one code.
	ModeMismatch = -32001
	// ToolError is a generic code for a tool call (list_tools or
	// call_tool) that failed for a reason that isn't a handshake problem
	// — an unknown card id, a card that refused to start, a missing
	// worktree. The workspace endpoint's board-level tools have no single
	// "session" to blame a failure on the way the per-feature endpoint's
	// FeatureMismatch code does double duty for, so they get their own
	// catch-all instead of borrowing that one's name for an unrelated
	// meaning.
	ToolError = -32002
)

// Request is a JSON-RPC 2.0 request or notification envelope. The ID is
// omitted (nil) for a notification, which receives no response.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is a JSON-RPC 2.0 success response.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
}

// ErrorObject is a JSON-RPC 2.0 error response.
type ErrorObject struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Error   ErrorData       `json:"error"`
}

// ErrorData is the error member of an ErrorObject.
type ErrorData struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// rpcError is an internal error carrying a JSON-RPC code, so dispatch can
// answer a bad frame with the correct error object.
type rpcError struct {
	code    int
	message string
}

func (e *rpcError) Error() string { return e.message }

// Encode writes v as one line-delimited JSON frame. Exported so the
// engine's session-socket endpoint writes the same wire shape.
func Encode(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = w.Write(b)
	return err
}

// Decode parses one JSON-RPC 2.0 request frame. Exported for the engine's
// session-socket endpoint.
func Decode(line []byte) (*Request, error) {
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		return nil, fmt.Errorf("mcp: malformed frame: %w", err)
	}
	if req.JSONRPC != JSONRPC {
		return nil, fmt.Errorf("mcp: unsupported jsonrpc version %q", req.JSONRPC)
	}
	return &req, nil
}
