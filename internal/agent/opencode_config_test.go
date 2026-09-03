package agent

import (
	"encoding/json"
	"reflect"
	"testing"
)

func buildConfig(t *testing.T, extra []string) map[string]any {
	t.Helper()
	raw, err := buildOpencodeConfig("/tmp/wt", "/tmp/mcp/FD-011.sock", "FD-011", "/opt/gummi", extra, false, false)
	if err != nil {
		t.Fatalf("buildOpencodeConfig: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("output not valid JSON: %v\n%s", err, raw)
	}
	return m
}

func TestBuildOpencodeConfig(t *testing.T) {
	m := buildConfig(t, nil)
	perm, ok := m["permission"].(map[string]any)
	if !ok {
		t.Fatalf("permission block missing or wrong type: %v", m["permission"])
	}
	for _, key := range []string{"edit", "write"} {
		b := perm[key].(map[string]any)
		if got := b["/tmp/wt/**"]; got != "allow" {
			t.Errorf("%s[/tmp/wt/**] = %v, want allow", key, got)
		}
		if got := b["*"]; got != "deny" {
			t.Errorf("%s[*] = %v, want deny", key, got)
		}
	}
	if perm["external_directory"] != "deny" {
		t.Errorf("external_directory = %v, want deny", perm["external_directory"])
	}
	if _, present := perm["read"]; present {
		t.Errorf("read block present without extraReadAllows: %v", perm["read"])
	}

	mcp, ok := m["mcp"].(map[string]any)
	if !ok {
		t.Fatalf("mcp block missing: %v", m["mcp"])
	}
	gummi := mcp["gummi"].(map[string]any)
	if gummi["type"] != "local" {
		t.Errorf("mcp.gummi.type = %v, want local", gummi["type"])
	}
	if !reflect.DeepEqual(gummi["command"], []any{"/opt/gummi", "__mcp", "--feature", "FD-011"}) {
		t.Errorf("mcp.gummi.command = %v, want [/opt/gummi __mcp --feature FD-011]", gummi["command"])
	}
	env := gummi["environment"].(map[string]any)
	if env["GUMMI_MCP_SOCK"] != "/tmp/mcp/FD-011.sock" {
		t.Errorf("mcp.gummi.environment.GUMMI_MCP_SOCK = %v, want /tmp/mcp/FD-011.sock", env["GUMMI_MCP_SOCK"])
	}
}

func TestBuildOpencodeConfigNoMCP(t *testing.T) {
	// an empty feature id or mcp sock must omit the whole mcp block, so a
	// transient session never spawns a __mcp child with empty flags.
	for name, args := range map[string][2]string{
		"no feature": {"", "/tmp/mcp/FD-011.sock"},
		"no sock":    {"FD-011", ""},
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := buildOpencodeConfig("/tmp/wt", args[1], args[0], "/opt/gummi", nil, false, false)
			if err != nil {
				t.Fatal(err)
			}
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatal(err)
			}
			if _, present := m["mcp"]; present {
				t.Errorf("mcp block present for featureID=%q sock=%q", args[0], args[1])
			}
			if _, present := m["permission"]; !present {
				t.Errorf("permission block missing")
			}
		})
	}
}

// TestBuildOpencodeConfigWorkspace pins the workspace-scoped mcp.gummi
// command shape: ["execPath","__mcp","--workspace"], no "--feature", and
// featureID (passed as junk here) is not consulted.
func TestBuildOpencodeConfigWorkspace(t *testing.T) {
	raw, err := buildOpencodeConfig("/tmp/wt", "/tmp/mcp/ws.sock", "should-be-ignored", "/opt/gummi", nil, false, true)
	if err != nil {
		t.Fatalf("buildOpencodeConfig: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("output not valid JSON: %v\n%s", err, raw)
	}
	mcp, ok := m["mcp"].(map[string]any)
	if !ok {
		t.Fatalf("mcp block missing: %v", m["mcp"])
	}
	gummi := mcp["gummi"].(map[string]any)
	if !reflect.DeepEqual(gummi["command"], []any{"/opt/gummi", "__mcp", "--workspace"}) {
		t.Errorf("mcp.gummi.command = %v, want [/opt/gummi __mcp --workspace]", gummi["command"])
	}
	env := gummi["environment"].(map[string]any)
	if env["GUMMI_MCP_SOCK"] != "/tmp/mcp/ws.sock" {
		t.Errorf("mcp.gummi.environment.GUMMI_MCP_SOCK = %v, want /tmp/mcp/ws.sock", env["GUMMI_MCP_SOCK"])
	}
}

func TestBuildOpencodeConfigExtraReads(t *testing.T) {
	extra := []string{"/ws/.gummi/specs/FD-011-artifact.md"}
	m := buildConfig(t, extra)
	perm := m["permission"].(map[string]any)
	if perm["external_directory"] != "allow" {
		t.Errorf("external_directory = %v, want allow with extraReadAllows", perm["external_directory"])
	}
	for _, key := range []string{"edit", "write"} {
		b := perm[key].(map[string]any)
		if b["/tmp/wt/**"] != "allow" {
			t.Errorf("%s[/tmp/wt/**] changed with extraReadAllows = %v", key, b["/tmp/wt/**"])
		}
		if b["*"] != "deny" {
			t.Errorf("%s[*] = %v, want deny", key, b["*"])
		}
	}
	read, ok := perm["read"].(map[string]any)
	if !ok {
		t.Fatalf("read block missing: %v", perm["read"])
	}
	if read["/tmp/wt/**"] != "allow" {
		t.Errorf("read[/tmp/wt/**] = %v, want allow", read["/tmp/wt/**"])
	}
	if read[extra[0]] != "allow" {
		t.Errorf("read[%s] = %v, want allow", extra[0], read[extra[0]])
	}
	if read["*"] != "deny" {
		t.Errorf("read[*] = %v, want deny", read["*"])
	}
}

// A ReadOnly research session runs in the main checkout with no worktree:
// edit and write are pinned to "deny" outright (never a worktree-shaped
// pattern map), while read stays open — the deny is structural, so
// enforce/warn/off sandbox modes cannot re-arm the write tools.
func TestBuildOpencodeConfigReadOnly(t *testing.T) {
	raw, err := buildOpencodeConfig("/tmp/wt", "/tmp/mcp/FD-011.sock", "FD-011", "/opt/gummi", nil, true, false)
	if err != nil {
		t.Fatalf("buildOpencodeConfig: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("output not valid JSON: %v\n%s", err, raw)
	}
	perm := m["permission"].(map[string]any)
	for _, key := range []string{"edit", "write"} {
		if perm[key] != "deny" {
			t.Errorf("%s = %v, want deny for a ReadOnly session", key, perm[key])
		}
	}
	if perm["external_directory"] != "deny" {
		t.Errorf("external_directory = %v, want deny", perm["external_directory"])
	}
}

// TestBuildHostedOpencodeMCPConfig pins the hosted-tab shape: exactly the
// mcp.gummi block bound to --workspace, and specifically no "permission"
// key — unlike buildOpencodeConfig's scripted-session shape, the hosted tab
// never cages opencode's file tools.
func TestBuildHostedOpencodeMCPConfig(t *testing.T) {
	raw := buildHostedOpencodeMCPConfig("/opt/gummi", "/tmp/mcp/ws.sock")
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("output not valid JSON: %v\n%s", err, raw)
	}
	if _, present := m["permission"]; present {
		t.Errorf("permission key present: %v", m["permission"])
	}
	mcp, ok := m["mcp"].(map[string]any)
	if !ok {
		t.Fatalf("mcp block missing: %v", m["mcp"])
	}
	gummi, ok := mcp["gummi"].(map[string]any)
	if !ok {
		t.Fatalf("mcp.gummi missing: %v", mcp)
	}
	if gummi["type"] != "local" {
		t.Errorf("mcp.gummi.type = %v, want local", gummi["type"])
	}
	if !reflect.DeepEqual(gummi["command"], []any{"/opt/gummi", "__mcp", "--workspace"}) {
		t.Errorf("mcp.gummi.command = %v, want [/opt/gummi __mcp --workspace]", gummi["command"])
	}
	env, ok := gummi["environment"].(map[string]any)
	if !ok {
		t.Fatalf("mcp.gummi.environment missing: %v", gummi)
	}
	if env["GUMMI_MCP_SOCK"] != "/tmp/mcp/ws.sock" {
		t.Errorf("environment.GUMMI_MCP_SOCK = %v, want /tmp/mcp/ws.sock", env["GUMMI_MCP_SOCK"])
	}
}
