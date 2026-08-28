package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/state"
)

func TestEnsureWorkspaceLazyInit(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	ws, err := ensureWorkspace(dir, dir)
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
	if _, err := ensureWorkspace(dir, dir); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(ws.ConfigFile()); string(b) != "custom" {
		t.Error("ensureWorkspace clobbered an existing config.yaml")
	}
}

func TestEnsureWorkspaceRejectsNonRepo(t *testing.T) {
	if _, err := ensureWorkspace(t.TempDir(), t.TempDir()); err == nil {
		t.Error("ensureWorkspace in a non-git dir should error")
	}
}

func TestEnsureWorkspaceRefusesNested(t *testing.T) {
	p := t.TempDir()
	if err := os.MkdirAll(filepath.Join(p, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(p, ".gummi", "worktrees", "FD-042")
	if err := os.MkdirAll(wt, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: ../.git/worktrees/FD-042\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureWorkspace(wt, wt); !errors.Is(err, state.ErrNestedInit) {
		t.Fatalf("ensureWorkspace in nested worktree = %v, want ErrNestedInit", err)
	}
	if _, statErr := os.Stat(filepath.Join(wt, ".gummi")); !os.IsNotExist(statErr) {
		t.Errorf("ensureWorkspace materialized nested .gummi: %v", statErr)
	}
}

func TestRunVersion(t *testing.T) {
	// (no-arg `run` launches the board, which needs a repo + TTY, so it
	// isn't exercised here.)
	for _, args := range [][]string{{"version"}, {"--version"}, {"-v"}} {
		rootCmd.SetArgs(args)
		if err := rootCmd.Execute(); err != nil {
			t.Errorf("Execute(%v) = %v, want nil", args, err)
		}
	}
}

func TestRunUnknownCommand(t *testing.T) {
	rootCmd.SetArgs([]string{"frobnicate"})
	if err := rootCmd.Execute(); err == nil {
		t.Fatal("Execute with unknown command: want error, got nil")
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

// TestNewEngineFromEnvBlocksGuardedMismatch locks in the guarded/backend
// gate at its call site: a guarded config with a role on claude must fail
// before buildAgents ever runs, not mid-session. GUMMI_CLAUDE_BIN points at
// this test binary (the TestBuildAgentsSkipsUnstartableDefault trick) so
// claude is independently startable — proving the nil engine and error come
// from the gate itself, not a build failure.
func TestNewEngineFromEnvBlocksGuardedMismatch(t *testing.T) {
	clearDoctorEnv(t)
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GUMMI_CLAUDE_BIN", bin)
	repo := gitRepo(t)
	writeConfig(t, repo, "permissions: guarded\n")
	writeProfiles(t, repo, `
default: premium
profiles:
  premium:
    architect: { backend: claude, model: m }
`)
	ws, err := state.Open(repo, repo)
	if err != nil {
		t.Fatal(err)
	}
	eng, _, _, err := newEngineFromEnv(nil, nil, ws)
	if eng != nil {
		t.Fatalf("eng = %v, want nil on a guarded/claude mismatch", eng)
	}
	if err == nil {
		t.Fatal("err = nil, want a guarded-mismatch error")
	}
	for _, want := range []string{"premium", "architect", "claude"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should name %q", err.Error(), want)
		}
	}
}

// TestNewEngineFromEnvBlocksGuardedMismatchNoProfiles pins the startup
// error's wording when profiles.yaml is absent: the message must name the
// synthetic "(default)" profile and the offending backend, not render blank
// quoted profile/role clauses (`profile "" role "" -> backend "claude"`).
func TestNewEngineFromEnvBlocksGuardedMismatchNoProfiles(t *testing.T) {
	clearDoctorEnv(t)
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GUMMI_CLAUDE_BIN", bin)
	t.Setenv("GUMMI_AGENT", "claude")
	repo := gitRepo(t)
	writeConfig(t, repo, "permissions: guarded\n")
	ws, err := state.Open(repo, repo)
	if err != nil {
		t.Fatal(err)
	}
	eng, _, _, err := newEngineFromEnv(nil, nil, ws)
	if eng != nil {
		t.Fatalf("eng = %v, want nil on a guarded/claude mismatch", eng)
	}
	if err == nil {
		t.Fatal("err = nil, want a guarded-mismatch error")
	}
	if strings.Contains(err.Error(), `role ""`) || strings.Contains(err.Error(), `profile ""`) {
		t.Errorf("error %q should not render blank profile/role clauses", err.Error())
	}
	for _, want := range []string{`profile "(default)"`, `backend "claude"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should contain %q", err.Error(), want)
		}
	}
}

// TestNewEngineFromEnvAllowsGuardedCompatibleBackend confirms the gate
// doesn't false-positive: guarded paired with a guarded-capable backend
// (opencode) starts normally with no error.
func TestNewEngineFromEnvAllowsGuardedCompatibleBackend(t *testing.T) {
	clearDoctorEnv(t)
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GUMMI_OPENCODE_BIN", bin)
	repo := gitRepo(t)
	writeConfig(t, repo, "permissions: guarded\n")
	writeProfiles(t, repo, `
default: premium
profiles:
  premium:
    architect: { backend: opencode, model: m }
`)
	ws, err := state.Open(repo, repo)
	if err != nil {
		t.Fatal(err)
	}
	eng, _, _, err := newEngineFromEnv(nil, nil, ws)
	if err != nil {
		t.Fatalf("err = %v, want nil for a guarded+opencode pairing", err)
	}
	if eng == nil {
		t.Fatal("eng = nil, want a started engine for a compatible pairing")
	}
}

// TestProfileNamesFromYaml locks in the fix: the dialogs' profile list
// must come from .gummi/profiles.yaml, ordered with the declared default
// first, and must never fall back to the built-in presets merely because
// an agent backend couldn't start. profileNames is the seam runBoard uses
// to set the forms' offerings; before the fix the names were dropped on a
// failed engine start and the forms showed fabricated presets instead.
func TestProfileNamesFromYaml(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	ws, err := state.Init(dir, dir)
	if err != nil {
		t.Fatalf("state.Init: %v", err)
	}
	body := `default: mab
profiles:
  mab:
    architect: {backend: opencode, model: m}
    implementer: {backend: opencode, model: m}
    reviewer: {backend: opencode, model: m}
    scribe: {backend: opencode, model: m}
  claude:
    architect: {backend: claude, model: m}
    implementer: {backend: opencode, model: m}
    reviewer: {backend: claude, model: m}
    scribe: {backend: opencode, model: m}
`
	if err := os.WriteFile(ws.ProfilesFile(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got := profileNames(ws)
	want := []string{"mab", "claude"}
	if len(got) != len(want) {
		t.Fatalf("profileNames = %v, want %v", got, want)
	}
	for i, name := range want {
		if got[i] != name {
			t.Errorf("profileNames[%d] = %q, want %q (default first)", i, got[i], name)
		}
	}
	for _, name := range got {
		if name == "thrifty" || name == "premium" || name == "local-heavy" {
			t.Errorf("profileNames offered hardcoded preset %q; expected profiles.yaml names %v", name, want)
		}
	}
}

// TestProfileNamesWithoutYaml ensures a workspace with no profiles.yaml
// yields no names — the forms' own preset fallback is still the last
// resort there.
func TestProfileNamesWithoutYaml(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	ws, err := state.Init(dir, dir)
	if err != nil {
		t.Fatalf("state.Init: %v", err)
	}
	if got := profileNames(ws); len(got) != 0 {
		t.Fatalf("profileNames without profiles.yaml = %v, want empty", got)
	}
}

func TestResolveRootsNested(t *testing.T) {
	ws := t.TempDir()
	writeConfig(t, ws, "repo: git/lxd\n")
	// the resolver now validates each configured root is a git toplevel.
	if err := os.MkdirAll(filepath.Join(ws, "git", "lxd", ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	gotWS, gotRepo, err := resolveRoots(ws)
	if err != nil {
		t.Fatalf("resolveRoots: %v", err)
	}
	if gotWS != ws {
		t.Errorf("workspace root = %q, want %q", gotWS, ws)
	}
	if want := filepath.Join(ws, "git", "lxd"); gotRepo != want {
		t.Errorf("repo root = %q, want %q", gotRepo, want)
	}
}

func TestResolveRootsSiblingDefault(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	gotWS, gotRepo, err := resolveRoots(dir)
	if err != nil {
		t.Fatalf("resolveRoots (no config): %v", err)
	}
	if gotWS != dir || gotRepo != dir {
		t.Errorf("roots = (%q, %q), want both %q", gotWS, gotRepo, dir)
	}
}

func TestResolveRootsRejectsEscapingRepo(t *testing.T) {
	ws := t.TempDir()
	writeConfig(t, ws, "repo: ../outside\n")
	if _, _, err := resolveRoots(ws); err == nil {
		t.Fatal("resolveRoots accepted a repo escaping the workspace")
	}
}
