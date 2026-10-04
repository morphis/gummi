package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

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
		WorkDir: dir, Permission: PermissionAllowAll, ScratchDir: scratch,
		FeatureID: "FD-1", MCPSockPath: "/tmp/mcp/FD-1.sock",
	}); err == nil {
		t.Fatal("NewSession succeeded with an unexecutable binary")
	}
	home := ag.homes[filepath.Join(scratch, "agy-home")]
	if home == nil {
		t.Fatal("card home not created")
	}
	if len(home.socks) != 0 {
		t.Errorf("failed spawn left its MCP entry registered: %v", home.socks)
	}
}
