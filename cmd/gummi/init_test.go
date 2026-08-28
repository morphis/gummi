package main

import (
	"os"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/config"
)

func TestInitWorkspaceCreatesFreshWorkspace(t *testing.T) {
	dir := gitRepo(t)
	ws, existed, err := initWorkspace(dir)
	if err != nil {
		t.Fatalf("initWorkspace: %v", err)
	}
	if existed {
		t.Error("initWorkspace on a fresh repo reported existed == true")
	}
	for _, p := range []string{ws.GummiDir(), ws.ConfigFile(), ws.ProfilesFile()} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("initWorkspace did not create %s: %v", p, err)
		}
	}
}

func TestInitWorkspaceIdempotentOnExisting(t *testing.T) {
	dir := gitRepo(t)
	ws, _, err := initWorkspace(dir)
	if err != nil {
		t.Fatalf("initWorkspace (first): %v", err)
	}
	custom := "# custom marker\n" + config.Template
	if err := os.WriteFile(ws.ConfigFile(), []byte(custom), 0o600); err != nil {
		t.Fatal(err)
	}
	configInfo, err := os.Stat(ws.ConfigFile())
	if err != nil {
		t.Fatal(err)
	}

	_, existed, err := initWorkspace(dir)
	if err != nil {
		t.Fatalf("initWorkspace (second): %v", err)
	}
	if !existed {
		t.Error("initWorkspace on an already-initialized repo reported existed == false")
	}
	if b, _ := os.ReadFile(ws.ConfigFile()); string(b) != custom {
		t.Error("initWorkspace clobbered an existing config.yaml")
	}
	after, err := os.Stat(ws.ConfigFile())
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(configInfo.ModTime()) {
		t.Error("initWorkspace changed config.yaml's mtime on an already-initialized repo")
	}
}

func TestRunInitMessages(t *testing.T) {
	dir := gitRepo(t)
	t.Chdir(dir)

	first := captureStdout(t, func() {
		if err := runInit(nil); err != nil {
			t.Fatalf("runInit (first): %v", err)
		}
	})
	if !strings.Contains(first, "initialized workspace") {
		t.Errorf("first runInit output = %q, want it to contain %q", first, "initialized workspace")
	}

	second := captureStdout(t, func() {
		if err := runInit(nil); err != nil {
			t.Fatalf("runInit (second): %v", err)
		}
	})
	if !strings.Contains(second, "already initialized") {
		t.Errorf("second runInit output = %q, want it to contain %q", second, "already initialized")
	}
}
