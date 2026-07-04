package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingIsEmpty(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("missing config should be empty, not error: %v", err)
	}
	if len(c.Checks) != 0 {
		t.Errorf("empty config has checks: %+v", c)
	}
}

func TestLoadChecks(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	yaml := `permissions: guarded
checks:
  - name: build
    cmd: go build ./...
  - cmd: go test ./...
`
	if err := os.WriteFile(p, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Permissions != "guarded" {
		t.Errorf("permissions = %q", c.Permissions)
	}
	if len(c.Checks) != 2 {
		t.Fatalf("checks = %+v", c.Checks)
	}
	if c.Checks[0].Name != "build" || c.Checks[0].Cmd != "go build ./..." {
		t.Errorf("check 0 = %+v", c.Checks[0])
	}
	// a check without a name defaults its name to the command
	if c.Checks[1].Name != "go test ./..." {
		t.Errorf("unnamed check should default name to cmd: %+v", c.Checks[1])
	}
}

func TestLoadRejectsEmptyCmd(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte("checks:\n  - name: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Error("check with empty cmd should error")
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
	// the template's checks are commented out
	if len(c.Checks) != 0 {
		t.Errorf("template should ship no active checks: %+v", c.Checks)
	}
	if c.Permissions != "allow-all" {
		t.Errorf("template permissions = %q", c.Permissions)
	}
}
