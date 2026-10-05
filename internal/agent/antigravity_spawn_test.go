package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAntigravityFirstTurnNamesTheMCPServer: a session wired to a gummi
// endpoint is told, on its first turn, which server serves its tools and
// how to call them — after the engine's own hints, never instead of them.
func TestAntigravityFirstTurnNamesTheMCPServer(t *testing.T) {
	_ = antigravityTokenFixture(t, "tok")
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	bin := filepath.Join(dir, "agy")
	if err := os.WriteFile(bin, []byte("#!/usr/bin/env python3\n"+fakeAgyArgvEcho), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGY_LOG", log)
	prev := antigravityExecPath
	antigravityExecPath = func() (string, error) { return "/opt/gummi-stub", nil }
	t.Cleanup(func() { antigravityExecPath = prev })
	ag, err := NewAntigravity(bin)
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	sess, err := ag.NewSession(context.Background(), SessionOpts{
		WorkDir: dir, Permission: PermissionAllowAll, AgentHomeDir: t.TempDir(),
		FeatureID: "FD-1", MCPSockPath: "/tmp/mcp/FD-1.sock", SystemHints: []string{"HINT"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if err := sess.Send(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	waitAgyIdle(t, sess)
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	frames := antigravityStdinFrames(t, string(raw))
	if len(frames) != 1 {
		t.Fatalf("frames = %q, want one", frames)
	}
	f := frames[0]
	hint, server := strings.Index(f, "HINT"), strings.Index(f, "`gummi-FD-1`")
	if hint < 0 || server < 0 || hint > server || !strings.Contains(f, "call_mcp_tool") {
		t.Errorf("first frame %q: want the engine's hint, then the server gummi-FD-1 and call_mcp_tool", f)
	}
}

// TestAntigravityFailedStartReleasesItsEntry: a child that cannot start
// leaves no MCP entry behind in the card home, so the next spawn's config
// does not list an endpoint nothing serves.
func TestAntigravityFailedStartReleasesItsEntry(t *testing.T) {
	_ = antigravityTokenFixture(t, "tok")
	dir := t.TempDir()
	bin := filepath.Join(dir, "agy")
	if err := os.WriteFile(bin, []byte("#!/usr/bin/env python3\n"+fakeAgyArgvEcho), 0o700); err != nil {
		t.Fatal(err)
	}
	prev := antigravityExecPath
	antigravityExecPath = func() (string, error) { return "/opt/gummi-stub", nil }
	t.Cleanup(func() { antigravityExecPath = prev })
	ag, err := NewAntigravity(bin)
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	// resolved at construction; no longer executable at spawn
	if err := os.Chmod(bin, 0o600); err != nil {
		t.Fatal(err)
	}
	scratch := t.TempDir()
	if _, err := ag.NewSession(context.Background(), SessionOpts{
		WorkDir: dir, Permission: PermissionAllowAll, AgentHomeDir: scratch,
		FeatureID: "FD-1", MCPSockPath: "/tmp/mcp/FD-1.sock",
	}); err == nil {
		t.Fatal("NewSession succeeded with an unexecutable binary")
	}
	home := ag.homes[filepath.Join(scratch, "agy")]
	if home == nil {
		t.Fatal("card home not created")
	}
	if len(home.socks) != 0 {
		t.Errorf("failed spawn left its MCP entry registered: %v", home.socks)
	}
}
