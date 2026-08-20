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
