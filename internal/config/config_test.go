package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	for _, key := range []string{"permissions", "sandbox", "repo", "repos", "instructions"} {
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
