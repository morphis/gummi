package engine

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/sandbox"
)

// newSandboxEng returns an engine whose `bad` fake is scripted to write the
// given path into the main checkout on its turn, so a mode's session-start
// decision is observable through a real Run — and so is the fact that the
// write itself no longer ends anything.
func newSandboxEng(t *testing.T, mode, write string) *Engine {
	t.Helper()
	ws, store, wt := newRepo(t)
	good := agent.NewFake("ok")
	good.Caps = agent.Capabilities{ClientTools: true}
	mcp := agent.NewFake("ok")
	mcp.Caps = agent.Capabilities{ClientTools: false, MCPTools: true}
	bad := agent.NewFake("ok")
	bad.Caps = agent.Capabilities{}
	bad.Responder = func(opts agent.SessionOpts, msg string) []agent.Event {
		writeAt(t, wt.Root(), write)
		return []agent.Event{{Kind: agent.EventMessage, Text: "done"}, {Kind: agent.EventIdle}}
	}
	fleet := map[string]agent.Agent{"good": good, "mcp": mcp, "bad": bad, "": good}
	e := New(Config{
		Agents: fleet, Store: store, Worktrees: wt, Workspace: ws,
		Model: "m", MaxActive: 1, Sandbox: mode, Profiles: sandboxProfiles(),
	})
	t.Cleanup(func() { e.Close() })
	return e
}

// TestSandboxE2E drives the session-start decision through the autonomous
// Run path for each mode. Only enforce refuses; warn and off are the same
// permissive decision under two names, and neither watches the main
// checkout — a turn that dirties it still finishes as a normal turn.
func TestSandboxE2E(t *testing.T) {
	t.Run("enforce with gap refuses and emits no start", func(t *testing.T) {
		e := newSandboxEng(t, "enforce", "never-written.go")
		f := implFeature(1)
		withWorktree(t, e.cfg.Worktrees, f)
		err := e.Run(f)
		var ref *sandbox.RefusalError
		if !errors.As(err, &ref) {
			t.Fatalf("Run error = %v, want a sandbox.RefusalError", err)
		}
		if !strings.Contains(err.Error(), "bad/implementer") {
			t.Errorf("refusal should name bad/implementer: %v", err)
		}
		select {
		case ev := <-e.Events():
			t.Fatalf("no event expected after refusal, got %s", ev.Kind)
		case <-time.After(100 * time.Millisecond):
		}
	})

	t.Run("enforce with MCP-only coverage starts", func(t *testing.T) {
		e := newSandboxEng(t, "enforce", "never-written.go")
		f := implFeature(2)
		f.Profile = "mcponly"
		withWorktree(t, e.cfg.Worktrees, f)
		if err := e.Run(f); err != nil {
			t.Fatalf("MCP-only coverage must satisfy enforce, got error: %v", err)
		}
		waitFor(t, e, EventStarted)
	})

	// A turn that dirties main used to be a hard stop. It is now nothing at
	// all: the checkout is watched by no one, under any mode. Both of these
	// run the identical scenario to pin that warn and off no longer differ.
	for _, mode := range []string{"warn", "off"} {
		t.Run(mode+" with gap starts and lets a main-checkout write through", func(t *testing.T) {
			e := newSandboxEng(t, mode, "cmd/gummi/main.go")
			f := implFeature(3)
			withWorktree(t, e.cfg.Worktrees, f)
			if err := e.Run(f); err != nil {
				t.Fatalf("%s must not refuse, got error: %v", mode, err)
			}
			waitFor(t, e, EventIdle)
		})
	}
}
