package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// startChildWorkspace execs the real built binary as `__mcp --workspace`
// against the given socket — the workspace-scoped counterpart to
// startChildFor (mcp_e2e_test.go) — proving the actual cmd/gummi binary,
// not just the in-package sockConn fakes mcpworkspace_test.go drives
// directly, can reach the board-level endpoint end to end.
func startChildWorkspace(t *testing.T, socket string) *mcpChild {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), gummiBin, "__mcp", "--workspace")
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
		t.Fatalf("start __mcp --workspace: %v\n%s", err, stderr.String())
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

// TestMCPWorkspaceEndToEndListAndBoardList proves the real binary, over
// stdio, reaches the workspace endpoint's board-level tools: initialize,
// tools/list, and one tools/call (board_list) round-trip a card created
// directly on the engine's store — the full path a hosted agent's own
// MCP client would exercise.
func TestMCPWorkspaceEndToEndListAndBoardList(t *testing.T) {
	e := newEngine(t, &fakeNoTools{agent.NewFake("")})
	f := feature(1, "dark mode", domain.StageImplement)
	if err := e.cfg.Store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	socket, teardown, err := e.StartWorkspaceMCPEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	defer teardown()

	child := startChildWorkspace(t, socket)

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
	want := []string{"board_list", "card_status", "card_spec", "card_diff", "card_run", "card_resume"}
	if len(tools) != len(want) {
		t.Fatalf("tools length = %d, want %d", len(tools), len(want))
	}
	for i, w := range want {
		if tools[i].(map[string]any)["name"] != w {
			t.Fatalf("tool[%d] = %v, want %s", i, tools[i].(map[string]any)["name"], w)
		}
	}

	text, isErr := child.call("board_list", `{}`)
	if isErr {
		t.Fatalf("board_list isError: %s", text)
	}
	var items []boardListItem
	if err := json.Unmarshal([]byte(text), &items); err != nil {
		t.Fatalf("board_list result not JSON: %v (%s)", err, text)
	}
	if len(items) != 1 || items[0].ID != "FD-001" {
		t.Fatalf("board_list = %+v, want one FD-001 row", items)
	}
}

// TestMCPWorkspaceEndToEndCardRun proves card_run, dispatched through the
// real binary, actually drives a session inside *this* engine process —
// the whole point of the workspace endpoint versus a shelled-out `gummi
// run` that would spawn a second, lock-contending process.
func TestMCPWorkspaceEndToEndCardRun(t *testing.T) {
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

	socket, teardown, err := e.StartWorkspaceMCPEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	defer teardown()
	child := startChildWorkspace(t, socket)

	text, isErr := child.call("card_run", `{"id":"FD-001"}`)
	if isErr {
		t.Fatalf("card_run isError: %s", text)
	}
	waitState(t, e, "FD-001", StateDone)
}
