package agent

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestBuildGummiMCPServerConfig(t *testing.T) {
	raw := buildGummiMCPServerConfig("/opt/gummi", "FD-012", "/tmp/mcp/FD-012.sock")
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

// Claude Code gives up on an MCP call that has been silent for a while
// (sent no response or progress; its own default idle bound), and tells
// gummi nothing. ask_user is silent for as long as a person reads, so the
// server carries its own timeout, which is what lifts that bound.
func TestBuildGummiMCPServerConfigOutwaitsAPerson(t *testing.T) {
	raw := buildGummiMCPServerConfig("/opt/gummi", "FD-012", "/tmp/mcp/FD-012.sock")
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	g := m["mcpServers"].(map[string]any)["gummi"].(map[string]any)
	ms, ok := g["timeout"].(float64)
	if !ok || ms < float64(24*60*60*1000) {
		t.Errorf("timeout = %v ms, want at least a day", g["timeout"])
	}
}

type codexOverrideEnv struct {
	GUMMI_MCP_SOCK string `toml:"GUMMI_MCP_SOCK"`
}

type codexOverrideServer struct {
	Command        string
	Args           []string
	Env            codexOverrideEnv
	ToolTimeoutSec int64 `toml:"tool_timeout_sec"`
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
	line, err := buildCodexGummiOverride("/opt/gummi", "FD-013", "/tmp/mcp/FD-013.sock")
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
	// codex bounds an MCP tool call at a minute unless told otherwise,
	// and a question waits on a person
	if g.ToolTimeoutSec < 24*60*60 {
		t.Errorf("tool_timeout_sec = %d, want at least a day", g.ToolTimeoutSec)
	}
}

func TestBuildCodexGummiOverride_EscapesSpecials(t *testing.T) {
	line, err := buildCodexGummiOverride(`/opt/gu mmi\a"b`, "FD-013", `/tmp/mcp/x"y.sock`)
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
	if _, err := buildCodexGummiOverride("bad\x01", "FD-013", "/tmp/mcp/x.sock"); err == nil {
		t.Fatal("control character in execPath accepted")
	}
	if _, err := buildCodexGummiOverride("/opt/gummi", "bad\x7f", "/tmp/mcp/x.sock"); err == nil {
		t.Fatal("control character in featureID accepted")
	}
	if _, err := buildCodexGummiOverride("/opt/gummi", "FD-013", "bad\x01.sock"); err == nil {
		t.Fatal("control character in sockPath accepted")
	}
}
