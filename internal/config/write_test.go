package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSetAgentPreservesCommentsAndOrder is the point of the writer: a
// naive yaml.Marshal(Config) round-trip would silently drop every
// comment and re-render the whole file. This proves the Node-based
// writer instead edits in place, leaving comments, key order, and
// unrelated keys byte-for-byte recognizable.
func TestSetAgentPreservesCommentsAndOrder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	src := `# gummi configuration. See docs/DESIGN.md.
#
# a human wrote this note and it had better still be here after.

permissions: allow-all

# sandbox: enforce|warn|off — comment right above the key
sandbox: warn

repos:
  lxd:   git/lxd
  incus: git/incus
`
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := SetAgent(path, "claude"); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	gotStr := string(got)
	for _, want := range []string{
		"# gummi configuration. See docs/DESIGN.md.",
		"# a human wrote this note and it had better still be here after.",
		"permissions: allow-all",
		"# sandbox: enforce|warn|off — comment right above the key",
		"sandbox: warn",
		"lxd: git/lxd",
		"incus: git/incus",
		"agent: claude",
	} {
		if !strings.Contains(gotStr, want) {
			t.Errorf("output missing %q; full output:\n%s", want, gotStr)
		}
	}

	// The file is still valid config.yaml, with every other field intact
	// alongside the new one.
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load after SetAgent: %v", err)
	}
	if c.Agent != "claude" || c.Permissions != "allow-all" || c.Sandbox != "warn" {
		t.Errorf("loaded config = %+v, want agent=claude permissions=allow-all sandbox=warn", c)
	}
	if c.Repos["lxd"] != "git/lxd" || c.Repos["incus"] != "git/incus" {
		t.Errorf("repos = %+v, want lxd/incus preserved", c.Repos)
	}

	// Writing again with a different value updates the existing entry in
	// place rather than appending a second `agent:` key.
	if err := SetAgent(path, "codex"); err != nil {
		t.Fatal(err)
	}
	got2, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(got2), "agent:"); n != 1 {
		t.Errorf("agent: appears %d times after a second write, want 1:\n%s", n, got2)
	}
	if !strings.Contains(string(got2), "agent: codex") {
		t.Errorf("second write did not update the value:\n%s", got2)
	}
	// The comments must have survived the second write too, not just the
	// first.
	if !strings.Contains(string(got2), "a human wrote this note") {
		t.Errorf("comments lost on second write:\n%s", got2)
	}
}

// TestSetAgentCreatesFileWhenAbsent covers the other half of "creating
// it if absent": a workspace whose config.yaml was deleted (or never
// existed) still gets a usable one back.
func TestSetAgentCreatesFileWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("precondition: %s should not exist", path)
	}
	if err := SetAgent(path, "zz"); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Agent != "zz" {
		t.Errorf("agent = %q, want zz", c.Agent)
	}
}

// TestLoadLayeredAgentPrecedence mirrors the permissions/sandbox
// precedence tests: the workspace value wins when both files set one,
// and the user value is the fallback when the workspace doesn't.
func TestLoadLayeredAgentPrecedence(t *testing.T) {
	dir := t.TempDir()
	userPath := filepath.Join(dir, "user.yaml")
	wsPath := filepath.Join(dir, "ws.yaml")

	if err := os.WriteFile(userPath, []byte("agent: claude\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wsPath, []byte("agent: codex\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	merged, sources, err := LoadLayered(userPath, wsPath)
	if err != nil {
		t.Fatal(err)
	}
	if merged.Agent != "codex" || sources["agent"] != wsPath {
		t.Errorf("agent = %q source %q, want codex from %q", merged.Agent, sources["agent"], wsPath)
	}

	// workspace unset: falls back to the user value.
	if err := os.WriteFile(wsPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	merged, sources, err = LoadLayered(userPath, wsPath)
	if err != nil {
		t.Fatal(err)
	}
	if merged.Agent != "claude" || sources["agent"] != userPath {
		t.Errorf("agent = %q source %q, want claude from %q", merged.Agent, sources["agent"], userPath)
	}

	// neither sets it: default.
	if err := os.WriteFile(userPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	merged, sources, err = LoadLayered(userPath, wsPath)
	if err != nil {
		t.Fatal(err)
	}
	if merged.Agent != "" || sources["agent"] != "default" {
		t.Errorf("agent = %q source %q, want empty/default", merged.Agent, sources["agent"])
	}
}
