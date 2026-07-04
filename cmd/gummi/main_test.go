package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureWorkspaceLazyInit(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	ws, err := ensureWorkspace(dir)
	if err != nil {
		t.Fatalf("ensureWorkspace: %v", err)
	}
	for _, p := range []string{ws.GummiDir(), ws.ConfigFile(), ws.ProfilesFile()} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("lazy init did not create %s: %v", p, err)
		}
	}
	// idempotent: an existing config is never clobbered
	if err := os.WriteFile(ws.ConfigFile(), []byte("custom"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureWorkspace(dir); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(ws.ConfigFile()); string(b) != "custom" {
		t.Error("ensureWorkspace clobbered an existing config.yaml")
	}
}

func TestEnsureWorkspaceRejectsNonRepo(t *testing.T) {
	if _, err := ensureWorkspace(t.TempDir()); err == nil {
		t.Error("ensureWorkspace in a non-git dir should error")
	}
}

func TestRunVersion(t *testing.T) {
	// (no-arg `run` launches the board, which needs a repo + TTY, so it
	// isn't exercised here.)
	for _, args := range [][]string{{"version"}, {"--version"}, {"-v"}} {
		if err := run(args); err != nil {
			t.Errorf("run(%v) = %v, want nil", args, err)
		}
	}
}

func TestRunUnknownCommand(t *testing.T) {
	if err := run([]string{"frobnicate"}); err == nil {
		t.Fatal("run with unknown command: want error, got nil")
	}
}

func TestVersionNonEmpty(t *testing.T) {
	if version() == "" {
		t.Fatal("version() returned empty string")
	}
	old := Version
	defer func() { Version = old }()
	Version = "v1.2.3"
	if got := version(); got != "v1.2.3" {
		t.Fatalf("version() = %q, want ldflags value to win", got)
	}
}
