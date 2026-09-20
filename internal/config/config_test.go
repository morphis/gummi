package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadMissingIsDefault(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("missing config should be default, not error: %v", err)
	}
	if c.Guarded() {
		t.Error("missing config should default to allow-all")
	}
}

func TestLoadPermissions(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte("permissions: guarded\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Permissions != "guarded" || !c.Guarded() {
		t.Errorf("permissions = %q", c.Permissions)
	}
}

func TestLoadRejectsBadPermissions(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte("permissions: yolo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Error("unknown permission mode should error")
	}
}

func TestTemplateParses(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(Template), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatalf("template does not parse: %v", err)
	}
	if c.Permissions != "allow-all" {
		t.Errorf("template permissions = %q", c.Permissions)
	}
}

func TestLoadSandbox(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte("permissions: allow-all\nsandbox: enforce\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Sandbox != "enforce" {
		t.Errorf("sandbox = %q, want enforce", c.Sandbox)
	}
}

func TestLoadRejectsBadSandbox(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte("sandbox: enfrce\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Error("unknown sandbox mode should error at load")
	}
}

func TestLoadAutopilotLanes(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte("autopilot_lanes: 3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.AutopilotLanes != 3 {
		t.Errorf("autopilot_lanes = %d, want 3", c.AutopilotLanes)
	}
}

func TestLoadRejectsNegativeAutopilotLanes(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte("autopilot_lanes: -1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Error("negative autopilot_lanes should error at load")
	}
}

func TestLoadLayeredAutopilotLanesPrecedence(t *testing.T) {
	dir := t.TempDir()
	userPath := filepath.Join(dir, "user.yaml")
	if err := os.WriteFile(userPath, []byte("autopilot_lanes: 4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	wsPath := filepath.Join(dir, "ws.yaml")
	if err := os.WriteFile(wsPath, []byte("autopilot_lanes: 3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	merged, sources, err := LoadLayered(userPath, wsPath)
	if err != nil {
		t.Fatal(err)
	}
	if merged.AutopilotLanes != 3 {
		t.Errorf("autopilot_lanes = %d, want 3 (workspace wins)", merged.AutopilotLanes)
	}
	if sources["autopilot_lanes"] != wsPath {
		t.Errorf("autopilot_lanes source = %q, want %q", sources["autopilot_lanes"], wsPath)
	}

	// with no workspace value, the user-level one is used
	if err := os.WriteFile(wsPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	merged, sources, err = LoadLayered(userPath, wsPath)
	if err != nil {
		t.Fatal(err)
	}
	if merged.AutopilotLanes != 4 || sources["autopilot_lanes"] != userPath {
		t.Errorf("autopilot_lanes = %d source %q, want 4 from %q", merged.AutopilotLanes, sources["autopilot_lanes"], userPath)
	}
}

func TestLoadRepo(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte("repo: git/lxd\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Repo != "git/lxd" {
		t.Errorf("repo = %q, want git/lxd", c.Repo)
	}
}

func TestLoadAbsentRepoIsEmpty(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("missing config should be default: %v", err)
	}
	if c.Repo != "" {
		t.Errorf("repo = %q, want empty default (the workspace root)", c.Repo)
	}
}

func TestTemplateRepoParsesEmpty(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(Template), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatalf("template does not parse: %v", err)
	}
	if c.Repo != "" {
		t.Errorf("template repo = %q, want empty (sibling default)", c.Repo)
	}
}

func TestLoadEnvPrereqs(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	content := `env:
  docker:
    probe: docker info >/dev/null 2>&1
    describe: local Docker daemon
  gpu:
    probe: nvidia-smi -L >/dev/null 2>&1
    describe: NVIDIA GPU
`
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatalf("loading env config: %v", err)
	}
	if len(c.Env) != 2 {
		t.Fatalf("got %d env prereqs, want 2", len(c.Env))
	}
	if c.Env["docker"].Probe != "docker info >/dev/null 2>&1" || c.Env["docker"].Describe != "local Docker daemon" {
		t.Errorf("docker prereq = %+v", c.Env["docker"])
	}
}

func TestLoadEnvRejectsEmptyProbe(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte("env:\n  bad:\n    probe: \"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), p) || !strings.Contains(err.Error(), "empty probe") {
		t.Fatalf("expected empty-probe error naming file, got: %v", err)
	}
}

func TestLoadEnvRejectsEmptyName(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte("env:\n  \"\":\n    probe: \"true\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), p) || !strings.Contains(err.Error(), "empty name") {
		t.Fatalf("expected empty-name error naming file, got: %v", err)
	}
}

func TestLoadEnvRejectsBadNameCharacters(t *testing.T) {
	cases := []string{"has space", "has\ttab", "has]bracket", "has\nnewline"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "config.yaml")
			content := fmt.Sprintf("env:\n  %q:\n    probe: \"true\"\n", name)
			if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(p)
			if err == nil || !strings.Contains(err.Error(), p) || !strings.Contains(err.Error(), "']' or whitespace") {
				t.Fatalf("expected bad-name error naming file, got: %v", err)
			}
		})
	}
}

func TestUserConfigPath(t *testing.T) {
	t.Run("XDG_CONFIG_HOME", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)
		got, err := UserConfigPath()
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(dir, "gummi", "config.yaml"); got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
	})
	t.Run("home fallback", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("HOME", dir)
		got, err := UserConfigPath()
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(dir, ".config", "gummi", "config.yaml"); got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
	})
}

func TestLoadRejectsRelativeInstructionPath(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte("instructions:\n  - ./tips.md\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), p) || !strings.Contains(err.Error(), "not an absolute path") {
		t.Fatalf("expected absolute-path error naming file, got: %v", err)
	}
}

// A bare skill name and an absolute path are both legal; a relative path
// with separators is not, because it would mean a different directory
// depending on where gummi was started.
func TestLoadSkillsForward(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte("skills:\n  forward:\n    - container-env\n    - /opt/skills/hardware\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"container-env", "/opt/skills/hardware"}
	if len(c.Skills.Forward) != 2 || c.Skills.Forward[0] != want[0] || c.Skills.Forward[1] != want[1] {
		t.Errorf("skills.forward = %v, want %v", c.Skills.Forward, want)
	}
}

func TestLoadRejectsRelativeSkillPath(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte("skills:\n  forward:\n    - ./skills/env\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), p) || !strings.Contains(err.Error(), "relative path") {
		t.Fatalf("expected relative-path error naming file, got: %v", err)
	}
}

func TestLoadLayeredScalarPrecedence(t *testing.T) {
	dir := t.TempDir()
	userPath := filepath.Join(dir, "user.yaml")
	wsPath := filepath.Join(dir, "ws.yaml")
	if err := os.WriteFile(userPath, []byte("permissions: guarded\nsandbox: off\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wsPath, []byte("permissions: allow-all\nsandbox: enforce\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	merged, sources, err := LoadLayered(userPath, wsPath)
	if err != nil {
		t.Fatal(err)
	}
	if merged.Permissions != "allow-all" {
		t.Errorf("permissions = %q, want allow-all", merged.Permissions)
	}
	if merged.Sandbox != "enforce" {
		t.Errorf("sandbox = %q, want enforce", merged.Sandbox)
	}
	if sources["permissions"] != wsPath {
		t.Errorf("permissions source = %q, want %q", sources["permissions"], wsPath)
	}
	if sources["sandbox"] != wsPath {
		t.Errorf("sandbox source = %q, want %q", sources["sandbox"], wsPath)
	}
}

func TestLoadLayeredUserScalarUsedWhenWorkspaceUnset(t *testing.T) {
	dir := t.TempDir()
	userPath := filepath.Join(dir, "user.yaml")
	wsPath := filepath.Join(dir, "ws.yaml")
	if err := os.WriteFile(userPath, []byte("permissions: guarded\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wsPath, []byte("sandbox: enforce\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	merged, sources, err := LoadLayered(userPath, wsPath)
	if err != nil {
		t.Fatal(err)
	}
	if merged.Permissions != "guarded" || sources["permissions"] != userPath {
		t.Errorf("permissions = %q source %q, want guarded from %q", merged.Permissions, sources["permissions"], userPath)
	}
	if merged.Sandbox != "enforce" || sources["sandbox"] != wsPath {
		t.Errorf("sandbox = %q source %q, want enforce from %q", merged.Sandbox, sources["sandbox"], wsPath)
	}
}

func TestLoadLayeredDefaults(t *testing.T) {
	dir := t.TempDir()
	wsPath := filepath.Join(dir, "ws.yaml")
	if err := os.WriteFile(wsPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, sources, err := LoadLayered(filepath.Join(dir, "missing.yaml"), wsPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"permissions", "sandbox", "autopilot_lanes", "repo", "repos", "instructions"} {
		if sources[key] != "default" {
			t.Errorf("%s source = %q, want default", key, sources[key])
		}
	}
}

func TestLoadLayeredRepoReposWorkspaceOnly(t *testing.T) {
	dir := t.TempDir()
	userPath := filepath.Join(dir, "user.yaml")
	wsPath := filepath.Join(dir, "ws.yaml")
	if err := os.WriteFile(userPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wsPath, []byte("repo: git/lxd\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	merged, sources, err := LoadLayered(userPath, wsPath)
	if err != nil {
		t.Fatal(err)
	}
	if merged.Repo != "git/lxd" {
		t.Errorf("repo = %q, want git/lxd", merged.Repo)
	}
	if sources["repo"] != wsPath {
		t.Errorf("repo source = %q, want %q", sources["repo"], wsPath)
	}
	if sources["repos"] != "default" {
		t.Errorf("repos source = %q, want default", sources["repos"])
	}
}

func TestLoadLayeredUserSetsRepoErrors(t *testing.T) {
	dir := t.TempDir()
	userPath := filepath.Join(dir, "user.yaml")
	wsPath := filepath.Join(dir, "ws.yaml")
	if err := os.WriteFile(userPath, []byte("repo: git/lxd\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wsPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := LoadLayered(userPath, wsPath)
	if err == nil || !strings.Contains(err.Error(), userPath) {
		t.Fatalf("expected error naming user path, got: %v", err)
	}
}

func TestLoadLayeredUserSetsReposErrors(t *testing.T) {
	dir := t.TempDir()
	userPath := filepath.Join(dir, "user.yaml")
	wsPath := filepath.Join(dir, "ws.yaml")
	if err := os.WriteFile(userPath, []byte("repos:\n  lxd: git/lxd\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wsPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := LoadLayered(userPath, wsPath)
	if err == nil || !strings.Contains(err.Error(), userPath) {
		t.Fatalf("expected error naming user path, got: %v", err)
	}
}

func TestLoadLayeredEnvCollisionWorkspaceWins(t *testing.T) {
	dir := t.TempDir()
	userPath := filepath.Join(dir, "user.yaml")
	wsPath := filepath.Join(dir, "ws.yaml")
	if err := os.WriteFile(userPath, []byte("env:\n  x:\n    probe: user\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wsPath, []byte("env:\n  x:\n    probe: ws\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	merged, sources, err := LoadLayered(userPath, wsPath)
	if err != nil {
		t.Fatal(err)
	}
	if merged.Env["x"].Probe != "ws" {
		t.Errorf("env[x].probe = %q, want ws", merged.Env["x"].Probe)
	}
	if sources["env.x"] != wsPath {
		t.Errorf("env.x source = %q, want %q", sources["env.x"], wsPath)
	}
}

func TestLoadLayeredEnvUserOnly(t *testing.T) {
	dir := t.TempDir()
	userPath := filepath.Join(dir, "user.yaml")
	wsPath := filepath.Join(dir, "ws.yaml")
	if err := os.WriteFile(userPath, []byte("env:\n  y:\n    probe: user\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wsPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	merged, sources, err := LoadLayered(userPath, wsPath)
	if err != nil {
		t.Fatal(err)
	}
	if merged.Env["y"].Probe != "user" {
		t.Errorf("env[y].probe = %q, want user", merged.Env["y"].Probe)
	}
	if sources["env.y"] != userPath {
		t.Errorf("env.y source = %q, want %q", sources["env.y"], userPath)
	}
}

func TestLoadChecksDefault(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	content := `checks:
  default:
    - name: build
      cmd: go build ./...
    - name: test
      cmd: go test ./...
`
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Checks.Default) != 2 {
		t.Fatalf("got %d default checks, want 2", len(c.Checks.Default))
	}
	if c.Checks.Default[0].Name != "build" || c.Checks.Default[1].Name != "test" {
		t.Errorf("checks = %+v", c.Checks.Default)
	}
}

func TestLoadChecksDefaultRejectsEmptyCmd(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte("checks:\n  default:\n    - name: bad\n      cmd: \"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), p) || !strings.Contains(err.Error(), "entry 0 has an empty cmd") {
		t.Fatalf("expected empty-cmd error naming file, got: %v", err)
	}
}

func TestLoadChecksDefaultRejectsBadTimeout(t *testing.T) {
	for _, tc := range []struct {
		name    string
		timeout string
	}{
		{name: "malformed", timeout: "soon"},
		{name: "over-ceiling", timeout: "31m"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "config.yaml")
			content := fmt.Sprintf("checks:\n  default:\n    - name: bad\n      cmd: \"true\"\n      timeout: %s\n", tc.timeout)
			if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(p)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), p) || !strings.Contains(err.Error(), `"bad"`) {
				t.Errorf("expected error naming file and check, got: %v", err)
			}
		})
	}
}

func TestLoadLayeredInstructionsConcat(t *testing.T) {
	dir := t.TempDir()
	userPath := filepath.Join(dir, "user.yaml")
	wsPath := filepath.Join(dir, "ws.yaml")
	if err := os.WriteFile(userPath, []byte("instructions:\n  - /u/a.md\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wsPath, []byte("instructions:\n  - /w/b.md\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	merged, sources, err := LoadLayered(userPath, wsPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Instructions) != 2 || merged.Instructions[0] != "/u/a.md" || merged.Instructions[1] != "/w/b.md" {
		t.Errorf("instructions = %v, want [/u/a.md /w/b.md]", merged.Instructions)
	}
	if sources["instructions"] != userPath+","+wsPath {
		t.Errorf("instructions source = %q, want %s,%s", sources["instructions"], userPath, wsPath)
	}
}

func TestSubstratesAreValidatedAndCitableAsEnv(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) string {
		p := filepath.Join(dir, "config.yaml")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	c, err := Load(write("env:\n  docker:\n    probe: command -v docker\nsubstrates:\n  rig:\n    describe: a cluster\n    probe: rigctl healthy\n    provision: rigctl up\n    reset: rigctl restore clean\n    ttl: 24h\n    timeout: 1h\n"))
	if err != nil {
		t.Fatal(err)
	}
	rig := c.Substrates["rig"]
	if rig.Lifetime() != 24*time.Hour || rig.OpTimeout() != time.Hour {
		t.Fatalf("got %+v", rig)
	}
	if (Substrate{}).OpTimeout() != DefaultSubstrateOpTimeout || (Substrate{}).Lifetime() != 0 {
		t.Fatal("defaults: a generous timeout, and no expiry")
	}
	probes := c.EnvProbes()
	if len(probes) != 2 || probes["rig"].Probe != "rigctl healthy" || probes["docker"].Probe == "" {
		t.Fatalf("a plan cites a substrate the way it cites an env prerequisite: %+v", probes)
	}
	for name, body := range map[string]string{
		"no probe":       "substrates:\n  rig:\n    provision: up\n",
		"bad ttl":        "substrates:\n  rig:\n    probe: x\n    ttl: tomorrow\n",
		"timeout cap":    "substrates:\n  rig:\n    probe: x\n    timeout: 48h\n",
		"bad name":       "substrates:\n  \"my rig\":\n    probe: x\n",
		"also an env":    "env:\n  rig:\n    probe: x\nsubstrates:\n  rig:\n    probe: x\n",
		"negative ttl":   "substrates:\n  rig:\n    probe: x\n    ttl: -1h\n",
		"empty timeout0": "substrates:\n  rig:\n    probe: x\n    timeout: 0s\n",
	} {
		if _, err := Load(write(body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestSubstratesLayerLikeEnv(t *testing.T) {
	dir := t.TempDir()
	user, ws := filepath.Join(dir, "user.yaml"), filepath.Join(dir, "ws.yaml")
	_ = os.WriteFile(user, []byte("substrates:\n  rig:\n    probe: from-user\n  farm:\n    probe: farm\n"), 0o600)
	_ = os.WriteFile(ws, []byte("substrates:\n  rig:\n    probe: from-ws\n"), 0o600)
	c, sources, err := LoadLayered(user, ws)
	if err != nil {
		t.Fatal(err)
	}
	if c.Substrates["rig"].Probe != "from-ws" || sources["substrates.rig"] != ws || sources["substrates.farm"] != user {
		t.Fatalf("got %+v %v", c.Substrates, sources)
	}
	// a name means one thing across the layers too
	_ = os.WriteFile(user, []byte("env:\n  rig:\n    probe: x\n"), 0o600)
	if _, _, err := LoadLayered(user, ws); err == nil {
		t.Fatal("an env prerequisite in one file and a substrate in the other under one name is refused")
	}
}

func TestLoadHooks(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	content := `hooks:
  - run: ~/bin/gummi-hook
  - run: page-oncall.sh
    events: [gate.waiting, budget.exhausted]
`
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Hooks) != 2 {
		t.Fatalf("got %d hooks, want 2", len(c.Hooks))
	}
	if c.Hooks[0].Run != "~/bin/gummi-hook" || len(c.Hooks[0].Events) != 0 {
		t.Errorf("hook 0 = %+v, want the catch-all", c.Hooks[0])
	}
	if c.Hooks[1].Run != "page-oncall.sh" ||
		len(c.Hooks[1].Events) != 2 || c.Hooks[1].Events[0] != "gate.waiting" || c.Hooks[1].Events[1] != "budget.exhausted" {
		t.Errorf("hook 1 = %+v", c.Hooks[1])
	}
}

func TestLoadHooksRejectsEmptyRun(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte("hooks:\n  - events: [gate.waiting]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), p) || !strings.Contains(err.Error(), "entry 0 has an empty run") {
		t.Fatalf("expected empty-run error naming file, got: %v", err)
	}
}

func TestLoadHooksRejectsUnknownEvent(t *testing.T) {
	for _, ev := range []string{"card", "gate-waiting", "Card.Created", "stage.exit"} {
		dir := t.TempDir()
		p := filepath.Join(dir, "config.yaml")
		body := fmt.Sprintf("hooks:\n  - run: x.sh\n    events: [%q]\n", ev)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Load(p)
		if err == nil || !strings.Contains(err.Error(), p) || !strings.Contains(err.Error(), "unknown event") {
			t.Fatalf("event %q: expected unknown-event error naming file, got: %v", ev, err)
		}
	}
}

func TestLoadLayeredHooksConcat(t *testing.T) {
	dir := t.TempDir()
	userPath := filepath.Join(dir, "user.yaml")
	wsPath := filepath.Join(dir, "ws.yaml")
	if err := os.WriteFile(userPath, []byte("hooks:\n  - run: personal.sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wsPath, []byte("hooks:\n  - run: pipeline.sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	merged, sources, err := LoadLayered(userPath, wsPath)
	if err != nil {
		t.Fatal(err)
	}
	// both layers run, user first: a personal pager beside the
	// workspace's own pipeline is two hooks, not a conflict
	if len(merged.Hooks) != 2 || merged.Hooks[0].Run != "personal.sh" || merged.Hooks[1].Run != "pipeline.sh" {
		t.Errorf("hooks = %+v, want personal then pipeline", merged.Hooks)
	}
	if sources["hooks"] != userPath+","+wsPath {
		t.Errorf("hooks source = %q, want %s,%s", sources["hooks"], userPath, wsPath)
	}
}
