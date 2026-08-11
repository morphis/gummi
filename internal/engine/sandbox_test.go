package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/sandbox"
)

// sandboxProfiles wires two named fakes into a single profile set: mixed
// routes implementer at "bad" (no tool coverage), mcponly at "mcp"
// (MCP-only coverage).
func sandboxProfiles() config.Profiles {
	return config.Profiles{
		Default: "mixed",
		Profiles: map[string]config.Profile{
			"mixed":   {"implementer": {Backend: "bad", Model: "m"}},
			"mcponly": {"implementer": {Backend: "mcp", Model: "m"}},
		},
		Sandboxes: map[string]string{},
	}
}

// newSandboxEngine builds a fleet engine keyed by backend name, with the
// given workspace sandbox mode and profile set.
func newSandboxEngine(t *testing.T, sandboxMode string, profiles config.Profiles) *Engine {
	t.Helper()
	ws, store, wt := newRepo(t)
	good := agent.NewFake("ok")
	good.Caps = agent.Capabilities{ClientTools: true, MCPTools: false}
	mcp := agent.NewFake("ok")
	mcp.Caps = agent.Capabilities{ClientTools: false, MCPTools: true}
	bad := agent.NewFake("ok")
	bad.Caps = agent.Capabilities{}
	fleet := map[string]agent.Agent{
		"good": good,
		"mcp":  mcp,
		"bad":  bad,
		"":     good,
	}
	e := New(Config{
		Agents: fleet, Store: store, Worktrees: wt, Workspace: ws,
		Model: "m", MaxActive: 1, Sandbox: sandboxMode, Profiles: profiles,
	})
	t.Cleanup(func() { e.Close() })
	return e
}

func implFeature(num int) domain.Feature {
	f := feature(num, "Sandbox test", domain.StageImplement)
	f.Profile = "mixed"
	return f
}

// TestResolveSandboxMerges: the engine's resolver merges workspace mode,
// profile mode, and live adapter capabilities the way the verdict requires.
func TestResolveSandboxMerges(t *testing.T) {
	t.Run("enforce with no coverage gaps", func(t *testing.T) {
		e := newSandboxEngine(t, "enforce", sandboxProfiles())
		res := e.resolveSandbox(implFeature(1))
		if res.Mode != sandbox.ModeEnforce {
			t.Errorf("Mode = %q, want enforce", res.Mode)
		}
		if len(res.Gaps) == 0 {
			t.Error("expected a coverage gap for the no-flags backend")
		}
	})
	t.Run("warn with no coverage reports gaps but keeps mode", func(t *testing.T) {
		e := newSandboxEngine(t, "warn", sandboxProfiles())
		res := e.resolveSandbox(implFeature(1))
		if res.Mode != sandbox.ModeWarn {
			t.Errorf("Mode = %q, want warn", res.Mode)
		}
		if len(res.Gaps) == 0 {
			t.Error("expected a coverage gap under warn")
		}
	})
	t.Run("off resolves to off regardless of gaps", func(t *testing.T) {
		e := newSandboxEngine(t, "off", sandboxProfiles())
		res := e.resolveSandbox(implFeature(1))
		if res.Mode != sandbox.ModeOff {
			t.Errorf("Mode = %q, want off", res.Mode)
		}
	})
	t.Run("enforce with MCP-only coverage has no gaps", func(t *testing.T) {
		e := newSandboxEngine(t, "enforce", sandboxProfiles())
		f := implFeature(2)
		f.Profile = "mcponly"
		res := e.resolveSandbox(f)
		if res.Mode != sandbox.ModeEnforce {
			t.Errorf("Mode = %q, want enforce", res.Mode)
		}
		if len(res.Gaps) != 0 {
			t.Errorf("Gaps = %+v, want none (MCP-only coverage suffices)", res.Gaps)
		}
	})
}

func TestRunRefusesEnforceWithGap(t *testing.T) {
	e := newSandboxEngine(t, "enforce", sandboxProfiles())
	f := implFeature(1)
	withWorktree(t, e.cfg.Worktrees, f)
	err := e.Run(f)
	var ref *sandbox.RefusalError
	if !errors.As(err, &ref) {
		t.Fatalf("Run error = %v, want a sandbox.RefusalError", err)
	}
	if !strings.Contains(err.Error(), "bad/implementer") {
		t.Errorf("refusal should name bad/implementer, got: %v", err)
	}
	if s := e.Get(f.ID); s != nil {
		t.Error("refusal must install no session")
	}
}

func TestRunPermitsEnforceWithMCPOnly(t *testing.T) {
	e := newSandboxEngine(t, "enforce", sandboxProfiles())
	f := implFeature(2)
	f.Profile = "mcponly"
	withWorktree(t, e.cfg.Worktrees, f)
	if err := e.Run(f); err != nil {
		t.Fatalf("MCP-only coverage should satisfy enforce, got error: %v", err)
	}
	if e.Get(f.ID) == nil {
		t.Error("permitted run should install a session")
	}
}

func TestRunPermitsWarnWithGap(t *testing.T) {
	e := newSandboxEngine(t, "warn", sandboxProfiles())
	f := implFeature(3)
	withWorktree(t, e.cfg.Worktrees, f)
	if err := e.Run(f); err != nil {
		t.Fatalf("warn mode must not refuse despite the gap, got error: %v", err)
	}
}

func TestAttachRefusesEnforceWithGap(t *testing.T) {
	e := newSandboxEngine(t, "enforce", sandboxProfiles())
	f := implFeature(4)
	f.Stage = domain.StageBrainstorm
	_, err := e.Attach(context.Background(), f)
	var ref *sandbox.RefusalError
	if !errors.As(err, &ref) {
		t.Fatalf("Attach error = %v, want a sandbox.RefusalError", err)
	}
	if !strings.Contains(err.Error(), "bad/implementer") {
		t.Errorf("refusal should name bad/implementer, got: %v", err)
	}
}
