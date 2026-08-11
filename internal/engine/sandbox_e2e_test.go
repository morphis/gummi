package engine

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/sandbox"
)

// newSandboxEng returns an engine whose `bad` fake is scripted to write the
// given path into the main checkout on its turn, so the tripwire (warn /
// enforce) or its absence (off) is observable through a real Run.
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

// TestSandboxE2E drives the full session-start + tripwire behavior through
// the autonomous Run path for each mode.
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

	t.Run("warn with gap starts and arms the tripwire", func(t *testing.T) {
		e := newSandboxEng(t, "warn", "cmd/gummi/main.go")
		f := implFeature(3)
		withWorktree(t, e.cfg.Worktrees, f)
		if err := e.Run(f); err != nil {
			t.Fatalf("warn must not refuse, got error: %v", err)
		}
		ev := waitFor(t, e, EventTripwire)
		if want := []string{"cmd/gummi/main.go"}; !reflect.DeepEqual(ev.DirtyPaths, want) {
			t.Errorf("DirtyPaths = %v, want %v", ev.DirtyPaths, want)
		}
	})

	t.Run("off starts and disarms the tripwire", func(t *testing.T) {
		e := newSandboxEng(t, "off", "cmd/gummi/main.go")
		f := implFeature(4)
		withWorktree(t, e.cfg.Worktrees, f)
		if err := e.Run(f); err != nil {
			t.Fatalf("off must not refuse, got error: %v", err)
		}
		waitFor(t, e, EventIdle) // a trip would replace this with EventTripwire and hang the wait
	})
}
