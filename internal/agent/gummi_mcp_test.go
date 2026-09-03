package agent

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestBuildGummiMCPServerConfig(t *testing.T) {
	raw := buildGummiMCPServerConfig("/opt/gummi", "FD-012", "/tmp/mcp/FD-012.sock", false)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("output not valid JSON: %v\n%s", err, raw)
	}
	servers, ok := m["mcpServers"].(map[string]any)
	if !ok {
		t.Fatalf("mcpServers block missing or wrong type: %v", m["mcpServers"])
	}
	g, ok := servers["gummi"].(map[string]any)
	if !ok {
		t.Fatalf("mcpServers.gummi missing: %v", servers)
	}
	if g["command"] != "/opt/gummi" {
		t.Errorf("command = %v, want /opt/gummi", g["command"])
	}
	if !reflect.DeepEqual(g["args"], []any{"__mcp", "--feature", "FD-012"}) {
		t.Errorf("args = %v, want [__mcp --feature FD-012]", g["args"])
	}
	env, ok := g["env"].(map[string]any)
	if !ok {
		t.Fatalf("env missing or wrong type: %v", g["env"])
	}
	if env["GUMMI_MCP_SOCK"] != "/tmp/mcp/FD-012.sock" {
		t.Errorf("env.GUMMI_MCP_SOCK = %v, want /tmp/mcp/FD-012.sock", env["GUMMI_MCP_SOCK"])
	}
}

// TestBuildGummiMCPServerConfigWorkspace pins the workspace-scoped shape:
// args carries --workspace and never --feature, and featureID (passed as
// junk here) is not consulted at all.
func TestBuildGummiMCPServerConfigWorkspace(t *testing.T) {
	raw := buildGummiMCPServerConfig("/opt/gummi", "should-be-ignored", "/tmp/mcp/ws.sock", true)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("output not valid JSON: %v\n%s", err, raw)
	}
	g := m["mcpServers"].(map[string]any)["gummi"].(map[string]any)
	if !reflect.DeepEqual(g["args"], []any{"__mcp", "--workspace"}) {
		t.Errorf("args = %v, want [__mcp --workspace]", g["args"])
	}
	env := g["env"].(map[string]any)
	if env["GUMMI_MCP_SOCK"] != "/tmp/mcp/ws.sock" {
		t.Errorf("env.GUMMI_MCP_SOCK = %v, want /tmp/mcp/ws.sock", env["GUMMI_MCP_SOCK"])
	}
}

type codexOverrideEnv struct {
	GUMMI_MCP_SOCK string `toml:"GUMMI_MCP_SOCK"`
}

type codexOverrideServer struct {
	Command string
	Args    []string
	Env     codexOverrideEnv
}

type codexOverrideConfig struct {
	MCPServers struct {
		Gummi codexOverrideServer `toml:"gummi"`
	} `toml:"mcp_servers"`
}

func parseCodexOverride(t *testing.T, line string) codexOverrideServer {
	t.Helper()
	var cfg codexOverrideConfig
	if err := toml.Unmarshal([]byte(line), &cfg); err != nil {
		t.Fatalf("override not valid TOML: %v\n%s", err, line)
	}
	return cfg.MCPServers.Gummi
}

func TestBuildCodexGummiOverride_HappyPath(t *testing.T) {
	line, err := buildCodexGummiOverride("/opt/gummi", "FD-013", "/tmp/mcp/FD-013.sock", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(line, "mcp_servers.gummi=") {
		t.Fatalf("override missing dotted-key prefix: %q", line)
	}
	g := parseCodexOverride(t, line)
	if g.Command != "/opt/gummi" {
		t.Errorf("command = %q, want /opt/gummi", g.Command)
	}
	if !reflect.DeepEqual(g.Args, []string{"__mcp", "--feature", "FD-013"}) {
		t.Errorf("args = %#v, want [__mcp --feature FD-013]", g.Args)
	}
	if g.Env.GUMMI_MCP_SOCK != "/tmp/mcp/FD-013.sock" {
		t.Errorf("env.GUMMI_MCP_SOCK = %q, want /tmp/mcp/FD-013.sock", g.Env.GUMMI_MCP_SOCK)
	}
}

// TestBuildCodexGummiOverride_Workspace pins the workspace-scoped shape:
// args carries --workspace and never --feature, and a featureID that would
// fail tomlQuote (a control character) does not fail the call, because a
// workspace override never quotes it.
func TestBuildCodexGummiOverride_Workspace(t *testing.T) {
	line, err := buildCodexGummiOverride("/opt/gummi", "bad\x01-but-unused", "/tmp/mcp/ws.sock", true)
	if err != nil {
		t.Fatal(err)
	}
	g := parseCodexOverride(t, line)
	if !reflect.DeepEqual(g.Args, []string{"__mcp", "--workspace"}) {
		t.Errorf("args = %#v, want [__mcp --workspace]", g.Args)
	}
	if g.Env.GUMMI_MCP_SOCK != "/tmp/mcp/ws.sock" {
		t.Errorf("env.GUMMI_MCP_SOCK = %q, want /tmp/mcp/ws.sock", g.Env.GUMMI_MCP_SOCK)
	}
}

func TestBuildCodexGummiOverride_EscapesSpecials(t *testing.T) {
	line, err := buildCodexGummiOverride(`/opt/gu mmi\a"b`, "FD-013", `/tmp/mcp/x"y.sock`, false)
	if err != nil {
		t.Fatal(err)
	}
	g := parseCodexOverride(t, line)
	if g.Command != `/opt/gu mmi\a"b` {
		t.Errorf("command = %q", g.Command)
	}
	if g.Env.GUMMI_MCP_SOCK != `/tmp/mcp/x"y.sock` {
		t.Errorf("env.GUMMI_MCP_SOCK = %q", g.Env.GUMMI_MCP_SOCK)
	}
}

func TestBuildCodexGummiOverride_RejectsControlChars(t *testing.T) {
	if _, err := buildCodexGummiOverride("bad\x01", "FD-013", "/tmp/mcp/x.sock", false); err == nil {
		t.Fatal("control character in execPath accepted")
	}
	if _, err := buildCodexGummiOverride("/opt/gummi", "bad\x7f", "/tmp/mcp/x.sock", false); err == nil {
		t.Fatal("control character in featureID accepted")
	}
	if _, err := buildCodexGummiOverride("/opt/gummi", "FD-013", "bad\x01.sock", false); err == nil {
		t.Fatal("control character in sockPath accepted")
	}
}

func TestHostedMCPAttachClaude(t *testing.T) {
	argv, env, cleanup, err := HostedMCPAttach("claude", "/opt/gummi", "/tmp/mcp/ws.sock")
	if err != nil {
		t.Fatalf("HostedMCPAttach: %v", err)
	}
	if cleanup == nil {
		t.Fatal("cleanup is nil")
	}
	want := []string{"--strict-mcp-config", "--mcp-config", string(buildGummiMCPServerConfig("/opt/gummi", "", "/tmp/mcp/ws.sock", true))}
	if !reflect.DeepEqual(argv, want) {
		t.Errorf("argv = %#v, want %#v", argv, want)
	}
	if env != nil {
		t.Errorf("env = %#v, want nil", env)
	}
}

func TestHostedMCPAttachCodex(t *testing.T) {
	argv, env, cleanup, err := HostedMCPAttach("codex", "/opt/gummi", "/tmp/mcp/ws.sock")
	if err != nil {
		t.Fatalf("HostedMCPAttach: %v", err)
	}
	if cleanup == nil {
		t.Fatal("cleanup is nil")
	}
	override, err := buildCodexGummiOverride("/opt/gummi", "", "/tmp/mcp/ws.sock", true)
	if err != nil {
		t.Fatalf("buildCodexGummiOverride: %v", err)
	}
	want := []string{"-c", override}
	if !reflect.DeepEqual(argv, want) {
		t.Errorf("argv = %#v, want %#v", argv, want)
	}
	if env != nil {
		t.Errorf("env = %#v, want nil", env)
	}
}

func TestHostedMCPAttachOpencode(t *testing.T) {
	_, env, cleanup, err := HostedMCPAttach("opencode", "/opt/gummi", "/tmp/mcp/ws.sock")
	if err != nil {
		t.Fatalf("HostedMCPAttach: %v", err)
	}
	if cleanup == nil {
		t.Fatal("cleanup is nil")
	}
	defer cleanup()
	if len(env) != 1 || !strings.HasPrefix(env[0], "OPENCODE_CONFIG=") {
		t.Fatalf("env = %#v, want one OPENCODE_CONFIG= entry", env)
	}
	path := strings.TrimPrefix(env[0], "OPENCODE_CONFIG=")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading config file: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("config file not valid JSON: %v\n%s", err, raw)
	}
	if _, present := m["permission"]; present {
		t.Errorf("permission key present: %v", m["permission"])
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("config file still exists after cleanup: %v", err)
	}
}

func TestHostedMCPAttachZZ(t *testing.T) {
	argv, env, cleanup, err := HostedMCPAttach("zz", "/opt/gummi", "/tmp/mcp/ws.sock")
	if err != nil {
		t.Fatalf("HostedMCPAttach: %v", err)
	}
	if cleanup == nil {
		t.Fatal("cleanup is nil")
	}
	want := []string{"--mcp", "/opt/gummi __mcp --workspace"}
	if !reflect.DeepEqual(argv, want) {
		t.Errorf("argv = %#v, want %#v", argv, want)
	}
	if env != nil {
		t.Errorf("env = %#v, want nil", env)
	}
}

func TestHostedMCPAttachZZWhitespaceExecPath(t *testing.T) {
	_, _, cleanup, err := HostedMCPAttach("zz", "/opt/gu mmi", "/tmp/mcp/ws.sock")
	if err == nil {
		t.Fatal("expected an error for a whitespace execPath")
	}
	if cleanup == nil {
		t.Fatal("cleanup is nil")
	}
}

func TestHostedMCPAttachUnknownBackend(t *testing.T) {
	for _, backend := range []string{"copilot", "some-unknown-backend", "/usr/local/bin/mycli"} {
		argv, env, cleanup, err := HostedMCPAttach(backend, "/opt/gummi", "/tmp/mcp/ws.sock")
		if err != nil {
			t.Errorf("backend %q: unexpected error: %v", backend, err)
		}
		if argv != nil || env != nil {
			t.Errorf("backend %q: argv=%#v env=%#v, want nil/nil", backend, argv, env)
		}
		if cleanup == nil {
			t.Errorf("backend %q: cleanup is nil", backend)
		}
	}
}
