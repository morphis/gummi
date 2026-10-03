package agent

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func buildConfig(t *testing.T, extra []string) map[string]any {
	t.Helper()
	raw, err := buildOpencodeConfig("/tmp/wt", "/tmp/mcp/FD-011.sock", "FD-011", "/opt/gummi", extra, false, nil, "")
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
	// a call to gummi may be a question waiting on a person: opencode's
	// own bound on an MCP request (a minute) must not apply to it
	if got, _ := gummi["timeout"].(float64); got < float64((24 * time.Hour).Milliseconds()) {
		t.Errorf("mcp.gummi.timeout = %v ms, want at least a day", gummi["timeout"])
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
			raw, err := buildOpencodeConfig("/tmp/wt", args[1], args[0], "/opt/gummi", nil, false, nil, "")
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
	raw, err := buildOpencodeConfig("/tmp/wt", "/tmp/mcp/FD-011.sock", "FD-011", "/opt/gummi", nil, true, nil, "")
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

// A session with no forwarded skills must emit no skills block at all:
// opencode merges this file over the operator's own opencode.jsonc, so an
// empty `skills` key here would overwrite a global skills config with
// nothing — the config gummi writes is a per-session addition, never a
// reset of what the operator set up.
func TestOpencodeConfigOmitsSkillsWhenNoneForwarded(t *testing.T) {
	m := buildConfig(t, nil)
	if _, present := m["skills"]; present {
		t.Errorf("skills block present with no forwarded dirs: %v", m["skills"])
	}
}

// The stage hints send a session to the card's scratch directory for
// throwaway files, so the cage must let it in: external_directory opens
// for that directory alone, and edit/write allow it beside the worktree.
// A cage that denied it answered a followed instruction with a refusal.
func TestBuildOpencodeConfigOpensTheScratchDir(t *testing.T) {
	const scratch = "/ws/.gummi/state/scratch/FD-025"
	perm := func(readOnly bool, extra []string) map[string]any {
		t.Helper()
		raw, err := buildOpencodeConfig("/tmp/wt", "", "FD-025", "/opt/gummi", extra, readOnly, nil, scratch)
		if err != nil {
			t.Fatalf("buildOpencodeConfig: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("output not valid JSON: %v\n%s", err, raw)
		}
		return m["permission"].(map[string]any)
	}

	p := perm(false, nil)
	ext, ok := p["external_directory"].(map[string]any)
	if !ok || ext["*"] != "deny" || ext[scratch+"/**"] != "allow" || len(ext) != 2 {
		t.Errorf("external_directory = %v, want the scratch dir allowed and nothing else", p["external_directory"])
	}
	for _, key := range []string{"edit", "write"} {
		m, _ := p[key].(map[string]any)
		if m[scratch+"/**"] != "allow" || m["/tmp/wt/**"] != "allow" || m["*"] != "deny" {
			t.Errorf("%s = %v, want worktree and scratch allowed, the rest denied", key, p[key])
		}
	}

	// a read-only session still may not write, scratch or not
	p = perm(true, nil)
	if p["edit"] != "deny" || p["write"] != "deny" {
		t.Errorf("read-only edit/write = %v/%v, want deny", p["edit"], p["write"])
	}

	// a caged read must not shut the scratch dir out
	p = perm(false, []string{"/ws/.gummi/specs/FD-025.md"})
	if r, _ := p["read"].(map[string]any); r[scratch+"/**"] != "allow" {
		t.Errorf("read = %v, want the scratch dir readable", p["read"])
	}
}

// Forwarded skills reach opencode as `skills.paths`. The key is additive
// on opencode's side (the worktree's own skills still load), which is why
// forwarding is safe to turn on for a repo that carries skills already.
func TestOpencodeConfigForwardsSkillPaths(t *testing.T) {
	raw, err := buildOpencodeConfig("/tmp/wt", "/tmp/mcp/FD-011.sock", "FD-011", "/opt/gummi", nil, false,
		[]string{"/ws/.agents/skills/container-env", "/ws/.claude/skills/toolchain"}, "")
	if err != nil {
		t.Fatalf("buildOpencodeConfig: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("output not valid JSON: %v\n%s", err, raw)
	}
	skills, ok := m["skills"].(map[string]any)
	if !ok {
		t.Fatalf("skills block missing or wrong type: %v", m["skills"])
	}
	paths, ok := skills["paths"].([]any)
	if !ok {
		t.Fatalf("skills.paths missing or wrong type: %v", skills["paths"])
	}
	got := make([]string, 0, len(paths))
	for _, p := range paths {
		got = append(got, p.(string))
	}
	want := []string{"/ws/.agents/skills/container-env", "/ws/.claude/skills/toolchain"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("skills.paths = %v, want %v", got, want)
	}
}
