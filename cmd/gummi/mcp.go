package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/mcp"
	"github.com/spf13/cobra"
)

// mcpCmd is the hidden `gummi __mcp` subcommand that serves gummi's live
// tool set over MCP stdio to whichever backend spawned it. It is not in
// the user-facing surface (Hidden), carries no tool logic of its own, and
// only works as a child of a running engine: the session socket address
// always arrives on GUMMI_MCP_SOCK. Two scopes select what it bridges to:
// --feature <id> dials the per-feature endpoint for that stage session
// (the original mode); --workspace dials the process-lifetime board-level
// endpoint a TUI-hosted agent uses to drive the gummi it lives inside,
// instead of shelling out to a second `gummi` process that would contend
// for a card's per-card lock. Exactly one of the two must be given — there
// is no sentinel feature id that means "workspace", because the two
// endpoints validate different handshakes and mixing them into one flag
// would just move the ambiguity from here into that handshake. Every MCP
// request is bridged to the engine's live socket and answered by its
// existing tool-dispatch machinery (handleClientTool for a feature,
// workspaceEndpoint's board-level tools for the workspace).
var mcpCmd = &cobra.Command{
	Use:    "__mcp",
	Hidden: true,
	Short:  "Serve a live feature's or the workspace's tools over MCP stdio (internal)",
	RunE:   runMCP,
}

func init() {
	mcpCmd.Flags().String("feature", "", "feature id whose stage tools to serve")
	mcpCmd.Flags().Bool("workspace", false, "serve the process-lifetime board-level tools instead of one feature's stage tools")
}

func runMCP(cmd *cobra.Command, _ []string) error {
	featureID, _ := cmd.Flags().GetString("feature")
	workspace, _ := cmd.Flags().GetBool("workspace")
	path := os.Getenv("GUMMI_MCP_SOCK")
	if path == "" {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"gummi __mcp: GUMMI_MCP_SOCK is not set; this command only runs as a "+
				"child spawned by gummi (there is no other way to reach a session)\n")
		return &exitError{code: 2}
	}
	switch {
	case workspace && featureID != "":
		fmt.Fprintf(cmd.ErrOrStderr(), "gummi __mcp: --feature and --workspace are mutually exclusive\n")
		return &exitError{code: 2}
	case !workspace && featureID == "":
		fmt.Fprintf(cmd.ErrOrStderr(), "gummi __mcp: one of --feature or --workspace is required\n")
		return &exitError{code: 2}
	}
	conn, err := (&net.Dialer{}).DialContext(context.Background(), "unix", path)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "gummi __mcp: engine socket %s unreachable: %v\n", path, err)
		return &exitError{code: 2}
	}
	client := newSocketClient(conn)
	if workspace {
		if err := client.helloWorkspace(); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "gummi __mcp: engine refused the workspace handshake: %v\n", err)
			return &exitError{code: 2}
		}
	} else if err := client.hello(featureID); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "gummi __mcp: engine refused feature %s: %v\n", featureID, err)
		return &exitError{code: 2}
	}

	instructions := mcp.FeatureInstructions(featureID)
	if workspace {
		instructions = mcp.WorkspaceInstructions()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := mcp.NewServer(client, client, instructions).Serve(ctx, os.Stdin, os.Stdout); err != nil {
		return err
	}
	return nil
}

// socketClient is the __mcp side's view of the session socket. It
// implements both mcp.ToolLister (list_tools) and mcp.EngineTransport
// (call_tool) by issuing JSON-RPC frames and correlating responses by id
// across a background reader, so concurrent tool calls interleave.
type socketClient struct {
	conn    net.Conn
	sc      *bufio.Scanner
	mu      sync.Mutex // serializes frame writes
	pending struct {
		sync.Mutex
		m map[string]chan rawFrame
	}
	seq atomic.Uint64
}

type rawFrame struct {
	result  json.RawMessage
	errData *mcp.ErrorData
}

func newSocketClient(conn net.Conn) *socketClient {
	c := &socketClient{conn: conn}
	c.sc = bufio.NewScanner(conn)
	c.sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	c.pending.m = map[string]chan rawFrame{}
	go c.route()
	return c
}

// route reads response frames and hands each to the caller awaiting its id.
func (c *socketClient) route() {
	for c.sc.Scan() {
		var frame struct {
			Result json.RawMessage `json:"result"`
			Error  *mcp.ErrorData  `json:"error"`
			ID     json.RawMessage `json:"id"`
		}
		if len(c.sc.Bytes()) == 0 || json.Unmarshal(c.sc.Bytes(), &frame) != nil {
			continue
		}
		id := string(frame.ID)
		c.pending.Lock()
		ch, ok := c.pending.m[id]
		if ok {
			delete(c.pending.m, id)
		}
		c.pending.Unlock()
		if ok {
			select {
			case ch <- rawFrame{result: frame.Result, errData: frame.Error}:
			default:
			}
		}
	}
}

// request issues one JSON-RPC request and waits for its id, or ctx cancel.
func (c *socketClient) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := fmt.Sprintf("%d", c.seq.Add(1))
	bp, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	ch := make(chan rawFrame, 1)
	c.pending.Lock()
	c.pending.m[id] = ch
	c.pending.Unlock()

	c.mu.Lock()
	err = mcp.Encode(c.conn, map[string]any{
		"jsonrpc": mcp.JSONRPC, "id": json.RawMessage(id), "method": method, "params": json.RawMessage(bp),
	})
	c.mu.Unlock()
	if err != nil {
		c.pending.Lock()
		delete(c.pending.m, id)
		c.pending.Unlock()
		return nil, fmt.Errorf("mcp %s write: %w", method, err)
	}

	select {
	case f := <-ch:
		if f.errData != nil {
			return nil, fmt.Errorf("mcp %s: %s", method, f.errData.Message)
		}
		return f.result, nil
	case <-ctx.Done():
		c.pending.Lock()
		delete(c.pending.m, id)
		c.pending.Unlock()
		return nil, ctx.Err()
	}
}

// hello completes the handshake against the engine's endpoint, which
// rejects a feature it is not serving.
func (c *socketClient) hello(feature string) error {
	_, err := c.request(context.Background(), "hello", map[string]any{"feature": feature})
	return err
}

// helloWorkspace completes the workspace endpoint's own handshake
// ({"mode":"workspace"}, not {"feature":"<id>"}) — the engine refuses a
// connection that isn't bound to the endpoint it's actually dialing.
func (c *socketClient) helloWorkspace() error {
	_, err := c.request(context.Background(), "hello", map[string]any{"mode": "workspace"})
	return err
}

// ListTools implements mcp.ToolLister.
func (c *socketClient) ListTools(ctx context.Context) ([]agent.ToolDef, error) {
	res, err := c.request(ctx, "list_tools", map[string]any{})
	if err != nil {
		return nil, err
	}
	var w struct {
		Tools []struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(res, &w); err != nil {
		return nil, fmt.Errorf("mcp list_tools: %w", err)
	}
	defs := make([]agent.ToolDef, 0, len(w.Tools))
	for _, t := range w.Tools {
		defs = append(defs, agent.ToolDef{Name: t.Name, Description: t.Description, Parameters: t.InputSchema})
	}
	return defs, nil
}

// CallTool implements mcp.EngineTransport.
func (c *socketClient) CallTool(ctx context.Context, name string, args json.RawMessage) (string, error) {
	res, err := c.request(ctx, "call_tool", map[string]any{"name": name, "args": args})
	if err != nil {
		return "", err
	}
	var v struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(res, &v); err != nil {
		return "", fmt.Errorf("mcp call_tool: %w", err)
	}
	return v.Result, nil
}
