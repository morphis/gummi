package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/morphis/gummi/internal/config"
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

func TestDefaultBackendCodex(t *testing.T) {
	t.Setenv("GUMMI_AGENT", "codex")
	t.Setenv("GUMMI_AGENT_CMD", "")
	if got := defaultBackendName(); got != "codex" {
		t.Fatalf("default backend = %q", got)
	}
}

func TestRequiredBackendsSkipsUnneededDefault(t *testing.T) {
	// BG-001: when every role in every profile names an explicit
	// non-default backend, the default backend (here "copilot", what an
	// unset GUMMI_AGENT selects) must NOT be required. buildAgents used to
	// start the default unconditionally, aborting startup when the default
	// CLI isn't installed before the profile backends even get a chance.
	all := func(backend string) config.Profile {
		return config.Profile{
			"architect":   {Backend: backend, Model: "m"},
			"implementer": {Backend: backend, Model: "m"},
			"reviewer":    {Backend: backend, Model: "m"},
			"scribe":      {Backend: backend, Model: "m"},
		}
	}
	cases := []struct {
		name     string
		profiles config.Profiles
		wantDef  bool
	}{
		{
			name: "all roles explicit non-default backend",
			profiles: config.Profiles{
				Default:  "test",
				Profiles: map[string]config.Profile{"test": all("headless")},
			},
			wantDef: false,
		},
		{
			name:     "no profiles falls back to the default",
			profiles: config.Profiles{},
			wantDef:  true,
		},
		{
			name: "role with omitted backend needs the default",
			profiles: config.Profiles{
				Default: "test",
				Profiles: map[string]config.Profile{
					"test": {
						"architect": {Backend: "headless", Model: "m"},
						"scribe":    {Model: "m"}, // no backend → default
					},
				},
			},
			wantDef: true,
		},
		{
			name: "role referencing the default directly needs it",
			profiles: config.Profiles{
				Default: "test",
				Profiles: map[string]config.Profile{
					"test": all("copilot"),
				},
			},
			wantDef: true,
		},
	}
	const def = "copilot"
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			needed := requiredBackends(def, tc.profiles)
			_, gotDef := needed[def]
			if gotDef != tc.wantDef {
				t.Errorf("requiredBackends(%q) default required = %v, want %v (needed: %v)",
					def, gotDef, tc.wantDef, needed)
			}
		})
	}
}

// TestBuildAgentsSkipsUnstartableDefault exercises the bug at its call
// site: with GUMMI_AGENT unset the default backend is copilot, which is
// often not installed. When every profile role names a usable non-default
// backend, buildAgents must start only those backends and must not abort
// on the missing default. opencode is pointed at the test binary via
// GUMMI_OPENCODE_BIN so it starts without any external CLI, keeping the
// case hermetic; it intentionally stays a different name than the
// "copilot" default. Before the fix this failed — buildAgents tried to
// start copilot first and returned its error.
func TestBuildAgentsSkipsUnstartableDefault(t *testing.T) {
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GUMMI_AGENT", "")
	t.Setenv("GUMMI_AGENT_CMD", "")
	t.Setenv("GUMMI_OPENCODE_BIN", bin)
	profiles := config.Profiles{
		Default: "test",
		Profiles: map[string]config.Profile{
			"test": {
				"architect":   {Backend: "opencode", Model: "m"},
				"implementer": {Backend: "opencode", Model: "m"},
				"reviewer":    {Backend: "opencode", Model: "m"},
				"scribe":      {Backend: "opencode", Model: "m"},
			},
		},
	}
	if got := defaultBackendName(); got != "copilot" {
		t.Fatalf("precondition: default backend = %q, want copilot", got)
	}
	agents, err := buildAgents(profiles)
	if err != nil {
		t.Fatalf("buildAgents failed when only the (absent) default backend was skipped: %v", err)
	}
	if _, ok := agents["opencode"]; !ok {
		t.Errorf("buildAgents did not start the opencode backend required by the profile (got %v)", agents)
	}
	if _, ok := agents["copilot"]; ok {
		t.Errorf("buildAgents started the copilot default even though no role references it")
	}
}
