package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/worktree"
)

// clearDoctorEnv neutralizes every environment variable buildDoctorReport
// reads, so a test sees exactly what it sets (the suite runs inside other
// agents whose env would otherwise leak in). t.Setenv also restores them.
func clearDoctorEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"GUMMI_AGENT", "GUMMI_AGENT_CMD", "GUMMI_CLAUDE_BIN", "GUMMI_CODEX_BIN", "GUMMI_OPENCODE_BIN",
		"GUMMI_ENVELOPE",
	} {
		t.Setenv(k, "")
	}
}

// A forwarded skill is reported by resolving it the way the engine will,
// so a name that will never reach a session fails here instead of going
// missing at run time.
func TestConfigLayeringChecksReportsForwardedSkills(t *testing.T) {
	clearDoctorEnv(t)
	wsDir := gitRepo(t)
	wsPath := filepath.Join(wsDir, ".gummi", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(wsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	skill := filepath.Join(wsDir, ".agents", "skills", "container-env")
	if err := os.MkdirAll(skill, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("---\nname: container-env\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	body := "skills:\n  forward:\n    - container-env\n    - not-there\n"
	if err := os.WriteFile(wsPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, sources, err := config.LoadLayered(filepath.Join(t.TempDir(), "missing.yaml"), wsPath)
	if err != nil {
		t.Fatal(err)
	}
	checks := configLayeringChecks(cfg, sources, "", wsPath, wsDir)

	ok := checkByName(doctorReport{Checks: checks}, "config:skills.forward.container-env")
	if ok.Status != statusOK || !strings.Contains(ok.Detail, skill) {
		t.Errorf("resolved skill check = %+v, want ok naming %s", ok, skill)
	}
	bad := checkByName(doctorReport{Checks: checks}, "config:skills.forward.not-there")
	if bad.Status != statusFail {
		t.Errorf("unresolvable skill check = %+v, want fail", bad)
	}
}

func TestConfigLayeringChecksSourceFiles(t *testing.T) {
	clearDoctorEnv(t)
	wsDir := gitRepo(t)
	userPath := filepath.Join(t.TempDir(), "user.yaml")
	wsPath := filepath.Join(wsDir, ".gummi", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(wsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userPath, []byte("permissions: guarded\nenv:\n  docker:\n    probe: docker info\n    describe: Docker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wsPath, []byte("permissions: allow-all\nenv:\n  docker:\n    probe: docker version\n    describe: Docker daemon\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, sources, err := config.LoadLayered(userPath, wsPath)
	if err != nil {
		t.Fatal(err)
	}
	checks := configLayeringChecks(cfg, sources, userPath, wsPath, wsDir)
	c := checkByName(doctorReport{Checks: checks}, "config:permissions")
	if c.Status != statusOK || !strings.Contains(c.Detail, "workspace: "+wsPath) {
		t.Errorf("permissions check = %+v", c)
	}
	c = checkByName(doctorReport{Checks: checks}, "config:env.docker")
	if c.Status != statusOK || !strings.Contains(c.Detail, "workspace: "+wsPath) {
		t.Errorf("env.docker check = %+v", c)
	}
	c = checkByName(doctorReport{Checks: checks}, "config:repo")
	if c.Status != statusOK || !strings.Contains(c.Detail, "(default)") {
		t.Errorf("repo check = %+v", c)
	}
}

func TestConfigLayeringChecksMissingInstruction(t *testing.T) {
	clearDoctorEnv(t)
	wsDir := gitRepo(t)
	wsPath := filepath.Join(wsDir, ".gummi", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(wsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wsPath, []byte("instructions:\n  - /no/such/file.md\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, sources, err := config.LoadLayered(filepath.Join(t.TempDir(), "missing.yaml"), wsPath)
	if err != nil {
		t.Fatal(err)
	}
	checks := configLayeringChecks(cfg, sources, "", wsPath, wsDir)
	c := checkByName(doctorReport{Checks: checks}, "config:instructions./no/such/file.md")
	if c.Status != statusFail || !strings.Contains(c.Detail, "/no/such/file.md") {
		t.Errorf("missing instruction check = %+v", c)
	}
}

func TestDoctorConfigLoadErrorProducesFailingCheck(t *testing.T) {
	clearDoctorEnv(t)
	wsDir := gitRepo(t)
	wsPath := filepath.Join(wsDir, ".gummi", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(wsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	// Invalid YAML triggers a config:load fail, but the rest of the report
	// still renders (workspace falls back to empty config).
	if err := os.WriteFile(wsPath, []byte("permissions: yolo\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	r := buildDoctorReport(wsDir, doctorOpts{})
	if c := checkByName(r, "config:load"); c.Status != statusFail || !strings.Contains(c.Detail, "permissions") {
		t.Errorf("config:load check = %+v", c)
	}
	if c := checkByName(r, "config:permissions"); c.Status != statusOK {
		t.Errorf("config:permissions should still render, got %+v", c)
	}
}

func TestDoctorCodexUsesNativeLoginRemediation(t *testing.T) {
	clearDoctorEnv(t)
	fakeAgentOnPath(t, "codex")
	t.Setenv("GUMMI_AGENT", "codex")
	r := buildDoctorReport(gitRepo(t), doctorOpts{})
	if c := checkByName(r, "backend:codex"); c.Status != statusOK || !strings.Contains(c.Detail, "codex") {
		t.Fatalf("backend = %+v", c)
	}
	if c := checkByName(r, "auth:codex"); !strings.Contains(c.Remediation, "codex login") {
		t.Fatalf("auth = %+v", c)
	}
}

// gitRepo makes a temp dir a real, minimal git repository with a local
// commit identity configured. Most of buildDoctorReport only stats .git
// and never shells out, but the git-identity check does run real git
// against the resolved repo root, so a fixture merely named ".git" is no
// longer enough — this gives every caller a repo git will actually work
// with, identity included, for free.
func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.CommandContext(context.Background(), "git",
			append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.name", "t")
	git("config", "user.email", "t@e.invalid")
	return dir
}

// fakeAgentOnPath drops an executable named bin into a fresh dir and puts
// that dir on PATH, so a headless backend check finds it hermetically.
func fakeAgentOnPath(t *testing.T, bin string) {
	t.Helper()
	fakeAgentsOnPath(t, bin)
}

// fakeAgentsOnPath drops several executables into one fresh dir and puts
// that dir on PATH, so a report can probe multiple backends at once.
func fakeAgentsOnPath(t *testing.T, bins ...string) {
	t.Helper()
	dir := t.TempDir()
	for _, bin := range bins {
		p := filepath.Join(dir, bin)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// The whole point of a hermetic PATH is that only these named fakes
	// are found — a real backend binary the dev/CI machine happens to have
	// installed must not leak into a check's result. git-identity now
	// shells out to real git regardless, though, so a symlink to the real
	// `git` joins the fakes: real git init/config, still nothing else on
	// PATH. Best-effort — a caller with no git on its own PATH gets the
	// hermetic dir unchanged, same as before this check existed.
	if realGit, err := exec.LookPath("git"); err == nil {
		_ = os.Symlink(realGit, filepath.Join(dir, "git"))
	}
	t.Setenv("PATH", dir)
}

func checkByName(r doctorReport, name string) doctorCheck {
	for _, c := range r.Checks {
		if c.Name == name {
			return c
		}
	}
	return doctorCheck{}
}

// headlessProfiles routes every role to the headless backend, so a test
// that only exercises headless gets no unrelated backend:<name> checks.
const headlessProfiles = `
default: thrifty
profiles:
  thrifty:
    architect: { backend: headless, model: qwen }
    implementer: { backend: headless, model: qwen }
    reviewer: { backend: headless, model: qwen }
    scribe: { backend: headless, model: qwen }
`

// A repo with a present headless backend binary and a healthy envelope
// reports ready (workspace/profile warns don't block). auth is handled
// by the headless child, so it reads as ok.
func TestDoctorReadyWithHeadlessAuth(t *testing.T) {
	clearDoctorEnv(t)
	repo := gitRepo(t)
	writeProfiles(t, repo, headlessProfiles)
	fakeAgentOnPath(t, "fakeagent")
	t.Setenv("GUMMI_AGENT", "headless")
	t.Setenv("GUMMI_AGENT_CMD", "fakeagent --serve")
	t.Setenv("GUMMI_ENVELOPE", "500")

	r := buildDoctorReport(repo, doctorOpts{})
	if !r.Ready {
		t.Fatalf("expected ready, got not ready: %+v", r.Checks)
	}
	if c := checkByName(r, "backend:headless"); c.Status != statusOK {
		t.Errorf("backend = %+v, want ok", c)
	}
	if c := checkByName(r, "auth:headless"); c.Status != statusOK {
		t.Errorf("auth = %+v, want ok", c)
	}
	if c := checkByName(r, "budget"); c.Status != statusOK {
		t.Errorf("envelope = %+v, want ok", c)
	}
}

// A selected backend whose binary is absent fails readiness.
func TestDoctorBackendMissingBinary(t *testing.T) {
	clearDoctorEnv(t)
	t.Setenv("GUMMI_AGENT", "claude")
	t.Setenv("GUMMI_CLAUDE_BIN", "gummi-no-such-binary-xyz")

	r := buildDoctorReport(gitRepo(t), doctorOpts{})
	if c := checkByName(r, "backend:claude"); c.Status != statusFail {
		t.Errorf("backend = %+v, want fail", c)
	}
	if r.Ready {
		t.Error("report is ready with a missing backend binary")
	}
}

// An unset envelope warns but does not block readiness (a run can still pass
// --envelope); a sub-turn envelope also warns.
func TestDoctorEnvelopeWarnDoesNotBlock(t *testing.T) {
	clearDoctorEnv(t)
	repo := gitRepo(t)
	writeProfiles(t, repo, headlessProfiles)
	fakeAgentOnPath(t, "fakeagent")
	t.Setenv("GUMMI_AGENT", "headless")
	t.Setenv("GUMMI_AGENT_CMD", "fakeagent")
	// no envelope, no BYOK (auth becomes n/a for headless).

	r := buildDoctorReport(repo, doctorOpts{})
	if c := checkByName(r, "budget"); c.Status != statusWarn {
		t.Errorf("envelope = %+v, want warn", c)
	}
	if !r.Ready {
		t.Errorf("an unset envelope should not block readiness: %+v", r.Checks)
	}

	t.Setenv("GUMMI_ENVELOPE", "5") // below one turn
	r = buildDoctorReport(gitRepo(t), doctorOpts{})
	if c := checkByName(r, "budget"); c.Status != statusWarn {
		t.Errorf("sub-turn envelope = %+v, want warn", c)
	}
}

// With the claude backend and no profiles.yaml yet, doctor evaluates the
// seed template that WOULD be written — the one seeded for claude, whose
// default profile the Anthropic-only backend can drive.
func TestDoctorClaudeBackendSeedsModelsItCanDrive(t *testing.T) {
	clearDoctorEnv(t)
	t.Setenv("GUMMI_AGENT", "claude")

	r := buildDoctorReport(gitRepo(t), doctorOpts{}) // no .gummi workspace → seed template
	c := checkByName(r, "profile")
	if c.Status == statusFail {
		t.Fatalf("profile = %+v, want the claude seed template to pass", c)
	}
	if !strings.Contains(c.Detail, "would be seeded") {
		t.Errorf("profile detail should note it is the seed template: %q", c.Detail)
	}
}

// A profiles.yaml whose default profile hands the claude backend a model
// it cannot drive fails the profile check, naming each such role — the
// warning fires before the first run that would hit it.
func TestDoctorClaudeBackendFlagsForeignModels(t *testing.T) {
	clearDoctorEnv(t)
	t.Setenv("GUMMI_AGENT", "claude")
	repo := gitRepo(t)
	writeProfiles(t, repo, `
default: thrifty
profiles:
  thrifty:
    architect: { model: claude-sonnet-5 }
    implementer: { model: gpt-5-mini }
    reviewer: { model: claude-sonnet-5 }
    scribe: { model: gpt-5-mini }
`)

	r := buildDoctorReport(repo, doctorOpts{})
	c := checkByName(r, "profile")
	if c.Status != statusFail {
		t.Fatalf("profile = %+v, want fail (claude can't drive gpt-5-mini)", c)
	}
	if !strings.Contains(c.Detail, "implementer=gpt-5-mini") {
		t.Errorf("profile detail should name the incompatible role: %q", c.Detail)
	}
	if r.Ready {
		t.Error("report is ready despite a backend/model conflict")
	}
}

// The same mixed seed template is fine for a non-Anthropic backend: only
// the claude backend is cross-checked, so headless stays a warn/ok, not a
// fail.
func TestDoctorNonClaudeBackendIgnoresSeedModels(t *testing.T) {
	clearDoctorEnv(t)
	fakeAgentOnPath(t, "fakeagent")
	t.Setenv("GUMMI_AGENT", "headless")
	t.Setenv("GUMMI_AGENT_CMD", "fakeagent")

	r := buildDoctorReport(gitRepo(t), doctorOpts{})
	if c := checkByName(r, "profile"); c.Status == statusFail {
		t.Errorf("profile = %+v, want non-fail for a non-claude backend", c)
	}
}

// A non-repo directory fails the repo check and blocks readiness.
func TestDoctorNoRepoFails(t *testing.T) {
	clearDoctorEnv(t)
	r := buildDoctorReport(t.TempDir(), doctorOpts{})
	if c := checkByName(r, "repo"); c.Status != statusFail {
		t.Errorf("repo = %+v, want fail", c)
	}
	if r.Ready {
		t.Error("report is ready outside a git repo")
	}
}

// A repo whose git identity cannot be resolved in any scope fails the
// git-identity check and blocks readiness — the gap that let a full run
// reach the final keystroke of its squash merge before discovering "***
// Please tell me who you are.", after every credit had already been spent.
func TestDoctorGitIdentityFails(t *testing.T) {
	clearDoctorEnv(t)
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.CommandContext(context.Background(), "git",
			append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	// Isolate every scope git would otherwise resolve an identity from —
	// this test must not pass or fail depending on whatever global/system
	// config happens to exist on the machine running it.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(k, "")
	}

	r := buildDoctorReport(repo, doctorOpts{})
	c := checkByName(r, "git-identity")
	if c.Status != statusFail {
		t.Fatalf("git-identity = %+v, want fail", c)
	}
	if !strings.Contains(c.Detail, "ident") {
		t.Errorf("detail %q should surface git's own identity error", c.Detail)
	}
	if !strings.Contains(c.Remediation, "git") || !strings.Contains(c.Remediation, "config") {
		t.Errorf("remediation %q should name the git config commands to run", c.Remediation)
	}
	if r.Ready {
		t.Error("report is ready with no resolvable git identity")
	}
}

// A repo with a configured local identity reports ok and names it —
// repo-local is one of the three scopes (local/global/system) that all
// count.
func TestDoctorGitIdentityOK(t *testing.T) {
	clearDoctorEnv(t)
	repo := gitRepo(t) // configures user.name "t", user.email "t@e.invalid"
	r := buildDoctorReport(repo, doctorOpts{})
	c := checkByName(r, "git-identity")
	if c.Status != statusOK || !strings.Contains(c.Detail, "t@e.invalid") {
		t.Fatalf("git-identity = %+v, want ok naming the configured identity", c)
	}
}

// writeConfig writes a config.yaml under the repo's .gummi dir so tests can
// set the workspace sandbox default doctor judges.
func writeConfig(t *testing.T, repo, body string) {
	t.Helper()
	dir := filepath.Join(repo, ".gummi")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A default (warn) profile with a covered backend reports ok carrying the
// resolved mode, wired through the full buildDoctorReport path.
func TestDoctorSandboxOk(t *testing.T) {
	clearDoctorEnv(t)
	repo := gitRepo(t)
	writeProfiles(t, repo, `
default: thrifty
profiles:
  thrifty:
    architect: { backend: opencode, model: m }
    implementer: { backend: opencode, model: m }
    reviewer: { backend: opencode, model: m }
    scribe: { backend: opencode, model: m }
`)
	r := buildDoctorReport(repo, doctorOpts{})
	c := checkByName(r, "sandbox:thrifty")
	if c.Status != statusOK {
		t.Fatalf("sandbox:thrifty = %+v, want ok", c)
	}
	if !strings.Contains(c.Detail, "mode=warn") {
		t.Errorf("detail %q should carry mode=warn", c.Detail)
	}
}

// An enforce profile whose only backend reaches tools over MCP (opencode)
// satisfies enforce — MCP-only coverage is no gap.
func TestDoctorSandboxCoveredByMCP(t *testing.T) {
	clearDoctorEnv(t)
	repo := gitRepo(t)
	writeConfig(t, repo, `permissions: allow-all
sandbox: enforce
`)
	writeProfiles(t, repo, `
default: opencode-only
profiles:
  opencode-only:
    implementer: { backend: opencode, model: m }
`)
	r := buildDoctorReport(repo, doctorOpts{})
	c := checkByName(r, "sandbox:opencode-only")
	if c.Status != statusOK {
		t.Fatalf("sandbox:opencode-only = %+v, want ok (MCP-only coverage)", c)
	}
	if !strings.Contains(c.Detail, "mode=enforce") {
		t.Errorf("detail %q should carry mode=enforce", c.Detail)
	}
}

// TestDoctorSandboxFail: an enforce profile whose role routes at a backend
// with no tool coverage at all fails, naming the (backend, role) pair. The
// synthetic "uncovered" backend (registered into the static capabilities
// view) stands in for a real tool-less backend, since every compile-time
// known backend advertises some tool path.
func TestDoctorSandboxFail(t *testing.T) {
	clearDoctorEnv(t)
	unreg := agent.RegisterCapabilities("uncovered", agent.Capabilities{})
	defer unreg()

	cfg := config.Config{Sandbox: "enforce"}
	profiles := config.Profiles{Profiles: map[string]config.Profile{
		"risky": {"implementer": {Backend: "uncovered", Model: "m"}},
	}}
	checks := sandboxChecks(cfg, profiles)
	c := checkByName(doctorReport{Checks: checks}, "sandbox:risky")
	if c.Status != statusFail {
		t.Fatalf("sandbox:risky = %+v, want fail", c)
	}
	if !strings.Contains(c.Detail, "uncovered/implementer") {
		t.Errorf("detail %q should name uncovered/implementer", c.Detail)
	}
}

// A profile that omits its own sandbox value inherits the workspace
// default — so a workspace-wide enforce with a gap fails the omitted
// profile too.
func TestDoctorSandboxUsesWorkspaceDefault(t *testing.T) {
	clearDoctorEnv(t)
	unreg := agent.RegisterCapabilities("uncovered", agent.Capabilities{})
	defer unreg()

	cfg := config.Config{Sandbox: "enforce"}
	profiles := config.Profiles{
		Profiles: map[string]config.Profile{
			"bare": {"implementer": {Backend: "uncovered", Model: "m"}},
		},
	}
	checks := sandboxChecks(cfg, profiles)
	c := checkByName(doctorReport{Checks: checks}, "sandbox:bare")
	if c.Status != statusFail {
		t.Fatalf("sandbox:bare = %+v, want fail via inherited enforce", c)
	}
	if !strings.Contains(c.Detail, "mode=enforce") {
		t.Errorf("detail %q should carry mode=enforce", c.Detail)
	}
}

// TestGuardedIncompatibilitiesMultiProfile: a non-default profile's role
// paired with claude is reported, while a clean opencode profile is not —
// confirming every profile is checked, not just the default one.
func TestGuardedIncompatibilitiesMultiProfile(t *testing.T) {
	profiles := config.Profiles{
		Default: "default",
		Profiles: map[string]config.Profile{
			"default": {"implementer": {Backend: "opencode", Model: "m"}},
			"premium": {"architect": {Backend: "claude", Model: "m"}},
		},
	}
	issues := guardedIncompatibilities("opencode", profiles)
	if len(issues) != 1 {
		t.Fatalf("issues = %+v, want exactly one", issues)
	}
	got := issues[0]
	want := guardedIncompatibility{Profile: "premium", Role: "architect", Backend: "claude"}
	if got != want {
		t.Errorf("issue = %+v, want %+v", got, want)
	}
}

// TestGuardedIncompatibilitiesCleanCompatible: profiles routed entirely at
// guarded-capable backends report no issues.
func TestGuardedIncompatibilitiesCleanCompatible(t *testing.T) {
	profiles := config.Profiles{Profiles: map[string]config.Profile{
		"safe": {
			"architect":   {Backend: "copilot", Model: "m"},
			"implementer": {Backend: "opencode", Model: "m"},
			"reviewer":    {Backend: "codex", Model: "m"},
		},
	}}
	if issues := guardedIncompatibilities("copilot", profiles); len(issues) != 0 {
		t.Errorf("issues = %+v, want none", issues)
	}
}

// TestGuardedIncompatibilitiesHeadlessSkipped: a headless role is never
// reported, since gummi cannot tell whether the wrapped tool honors guarded.
func TestGuardedIncompatibilitiesHeadlessSkipped(t *testing.T) {
	profiles := config.Profiles{Profiles: map[string]config.Profile{
		"wrapped": {"implementer": {Backend: "headless", Model: "m"}},
	}}
	if issues := guardedIncompatibilities("headless", profiles); len(issues) != 0 {
		t.Errorf("issues = %+v, want headless silently skipped", issues)
	}
}

// TestDoctorGuardedFail: a guarded config with a claude role fails the
// guarded:<profile> check, naming the profile/role/backend, wired through
// the full buildDoctorReport path.
func TestDoctorGuardedFail(t *testing.T) {
	clearDoctorEnv(t)
	repo := gitRepo(t)
	writeConfig(t, repo, "permissions: guarded\n")
	writeProfiles(t, repo, `
default: premium
profiles:
  premium:
    architect: { backend: claude, model: m }
`)
	r := buildDoctorReport(repo, doctorOpts{})
	c := checkByName(r, "guarded:premium")
	if c.Status != statusFail {
		t.Fatalf("guarded:premium = %+v, want fail", c)
	}
	for _, want := range []string{`profile "premium"`, `role "architect"`, `backend "claude"`} {
		if !strings.Contains(c.Detail, want) {
			t.Errorf("detail %q should contain %q", c.Detail, want)
		}
	}
}

// TestDoctorGuardedOkOnCompatibleBackend: a guarded config routed at a
// guarded-capable backend reports the profile clean.
func TestDoctorGuardedOkOnCompatibleBackend(t *testing.T) {
	clearDoctorEnv(t)
	repo := gitRepo(t)
	writeConfig(t, repo, "permissions: guarded\n")
	writeProfiles(t, repo, `
default: premium
profiles:
  premium:
    architect: { backend: copilot, model: m }
`)
	r := buildDoctorReport(repo, doctorOpts{})
	c := checkByName(r, "guarded:premium")
	if c.Status != statusOK {
		t.Fatalf("guarded:premium = %+v, want ok", c)
	}
}

// TestDoctorGuardedSilentOnAllowAll: with permissions: allow-all, no
// guarded:* check is emitted at all, even with a claude role present.
func TestDoctorGuardedSilentOnAllowAll(t *testing.T) {
	clearDoctorEnv(t)
	repo := gitRepo(t)
	writeConfig(t, repo, "permissions: allow-all\n")
	writeProfiles(t, repo, `
default: premium
profiles:
  premium:
    architect: { backend: claude, model: m }
`)
	r := buildDoctorReport(repo, doctorOpts{})
	for _, c := range r.Checks {
		if strings.HasPrefix(c.Name, "guarded:") {
			t.Errorf("unexpected guarded check emitted under allow-all: %+v", c)
		}
	}
}

// TestDoctorGuardedFailNoProfiles: with a present-but-empty profiles.yaml
// (so effectiveProfiles doesn't seed the starter template), a guarded
// config backed by an incompatible default backend must still surface a
// guarded:* check — mirroring the "no profiles configured; gummi falls back
// to the single GUMMI_MODEL" state doctor's own profile check already
// treats as legitimate, rather than silently dropping the mismatch because
// there's no profile name in profiles.Profiles to attach it to.
func TestDoctorGuardedFailNoProfiles(t *testing.T) {
	clearDoctorEnv(t)
	t.Setenv("GUMMI_AGENT", "claude")
	repo := gitRepo(t)
	writeConfig(t, repo, "permissions: guarded\n")
	writeProfiles(t, repo, "profiles: {}\n")
	r := buildDoctorReport(repo, doctorOpts{})
	c := checkByName(r, "guarded:(default)")
	if c.Status != statusFail {
		t.Fatalf("guarded:(default) = %+v, want fail", c)
	}
	if !strings.Contains(c.Detail, `backend "claude"`) {
		t.Errorf("detail %q should name backend \"claude\"", c.Detail)
	}
}

// writeProfiles writes a profiles.yaml under the repo's .gummi dir so the
// report's profile and backend checks parse a real loaded profile set.
// stubBackendBins puts no-op executables for the named backends on PATH.
//
// doctor's backend check asks whether the CLI a profile routes at is
// actually reachable, which is exactly right in production and a coupling
// in a test: whether the host running `go test` happens to have copilot or
// claude installed has nothing to do with the check under test. Two tests
// asserting that an ADVISORY finding does not clear readiness failed on any
// machine without a copilot binary, for a reason neither test was about.
func stubBackendBins(t *testing.T, names ...string) {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func writeProfiles(t *testing.T, repo, body string) {
	t.Helper()
	dir := filepath.Join(repo, ".gummi")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "profiles.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The bug fix: the default backend (copilot, since GUMMI_AGENT is unset)
// is unused because every role names backend: opencode, so doctor judges
// only opencode and reports ready despite copilot being absent from PATH.
func TestDoctorProfileRoutesAwayFromDefault(t *testing.T) {
	clearDoctorEnv(t)
	repo := gitRepo(t)
	writeProfiles(t, repo, `
default: thrifty
profiles:
  thrifty:
    architect: { backend: opencode, model: gpt-5 }
    implementer: { backend: opencode, model: gpt-5 }
    reviewer: { backend: opencode, model: gpt-5 }
    scribe: { backend: opencode, model: gpt-5 }
`)
	fakeAgentOnPath(t, "opencode") // PATH has opencode but not copilot

	r := buildDoctorReport(repo, doctorOpts{})
	if !r.Ready {
		t.Fatalf("expected ready, got not ready: %+v", r.Checks)
	}
	if c := checkByName(r, "backend:opencode"); c.Status != statusOK {
		t.Errorf("backend:opencode = %+v, want ok", c)
	}
	for _, c := range r.Checks {
		if strings.HasPrefix(c.Name, "backend:copilot") || strings.HasPrefix(c.Name, "auth:copilot") {
			t.Errorf("unexpected check for the unused default backend: %q", c.Name)
		}
	}
}

// A backend the profiles actually reference but whose binary is missing is
// now caught — no blind spot for profile-referenced non-default backends.
func TestDoctorRequiredNonDefaultBackendMissing(t *testing.T) {
	clearDoctorEnv(t)
	repo := gitRepo(t)
	writeProfiles(t, repo, `
default: thrifty
profiles:
  thrifty:
    architect: { backend: opencode, model: gpt-5 }
    implementer: { backend: opencode, model: gpt-5 }
    reviewer: { backend: opencode, model: gpt-5 }
    scribe: { backend: opencode, model: gpt-5 }
`)
	// PATH holds nothing — opencode (and the copilot default) are absent.
	t.Setenv("PATH", t.TempDir())

	r := buildDoctorReport(repo, doctorOpts{})
	if c := checkByName(r, "backend:opencode"); c.Status != statusFail {
		t.Errorf("backend:opencode = %+v, want fail", c)
	}
	if r.Ready {
		t.Error("report is ready with a missing required backend")
	}
	if c := checkByName(r, "backend:copilot"); c.Name != "" {
		t.Errorf("expected no backend:copilot check, got %+v", c)
	}
}

// With no profiles.yaml the seed template applies (omits backend on every
// role), so the default backend is required and still probed.
func TestDoctorDefaultRequiredWhenSeedTemplate(t *testing.T) {
	clearDoctorEnv(t)
	t.Setenv("GUMMI_AGENT", "claude")
	t.Setenv("GUMMI_CLAUDE_BIN", "gummi-no-such-binary-xyz")

	r := buildDoctorReport(gitRepo(t), doctorOpts{})
	if c := checkByName(r, "backend:claude"); c.Status != statusFail {
		t.Errorf("backend:claude = %+v, want fail", c)
	}
	if r.Ready {
		t.Error("report is ready with a missing default backend binary")
	}
}

// A missing non-default backend's fail detail names the profile/role pairs
// that pull it in, so an operator knows which profile to re-point.
func TestDoctorBackendMissingNamesReferencingRoles(t *testing.T) {
	clearDoctorEnv(t)
	repo := gitRepo(t)
	writeProfiles(t, repo, `
default: premium
profiles:
  premium:
    architect: { backend: opencode, model: gpt-5 }
    implementer: { backend: headless, model: qwen }
    reviewer: { backend: opencode, model: gpt-5 }
    scribe: { backend: headless, model: qwen }
`)
	// PATH holds nothing — opencode (and the copilot default) are absent.
	fakeAgentsOnPath(t, "fakeagent")
	t.Setenv("GUMMI_AGENT", "opencode")
	t.Setenv("GUMMI_AGENT_CMD", "fakeagent")

	r := buildDoctorReport(repo, doctorOpts{})
	c := checkByName(r, "backend:opencode")
	if c.Status != statusFail {
		t.Fatalf("backend:opencode = %+v, want fail", c)
	}
	for _, want := range []string{"premium/architect", "premium/reviewer"} {
		if !strings.Contains(c.Detail, want) {
			t.Errorf("backend:opencode detail %q does not name referring role %q", c.Detail, want)
		}
	}
}

// The required set is ordered — default first, then the rest lexicographic
// — and the ordering is stable across repeated runs.
func TestDoctorRequiredBackendsOrder(t *testing.T) {
	clearDoctorEnv(t)
	repo := gitRepo(t)
	writeProfiles(t, repo, `
default: thrifty
profiles:
  thrifty:
    architect: { backend: claude, model: claude-opus-4.8 }
    implementer: { backend: headless, model: qwen }
    reviewer: { backend: opencode, model: gpt-5 }
    scribe: { backend: opencode, model: gpt-5 }
`)
	fakeAgentsOnPath(t, "opencode", "claude", "fakeagent")
	t.Setenv("GUMMI_AGENT", "opencode")
	t.Setenv("GUMMI_AGENT_CMD", "fakeagent")

	var got []string
	for _, c := range buildDoctorReport(repo, doctorOpts{}).Checks {
		if strings.HasPrefix(c.Name, "backend:") {
			got = append(got, c.Name)
		}
	}
	want := []string{"backend:opencode", "backend:claude", "backend:headless"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("backend order = %v, want %v", got, want)
	}
	var again []string
	for _, c := range buildDoctorReport(repo, doctorOpts{}).Checks {
		if strings.HasPrefix(c.Name, "backend:") {
			again = append(again, c.Name)
		}
	}
	if strings.Join(again, ",") != strings.Join(want, ",") {
		t.Errorf("backend order not deterministic: %v vs %v", again, want)
	}
}

// TestDoctorForkDrift: a feature whose recorded fork is no longer an
// ancestor of main reports an advisory warn naming it, with the shared
// remedy and its fork left unchanged; a clean repo passes quietly. The
// check is present in the --json payload too (same report shape).
func TestDoctorForkDrift(t *testing.T) {
	clearDoctorEnv(t)
	fi := newDoctorFixture(t)

	// a feature with a worktree — Create stamps its fork in the store.
	f := fi.feature()
	if _, err := fi.wt.Create(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	fork := f.ForkPoint
	if fork == "" {
		fork, _ = fi.store.ForkPoint(context.Background(), f.ID)
	}
	if fork == "" {
		t.Fatal("no fork recorded for the feature worktree")
	}

	// clean: no drift.
	r := buildDoctorReport(fi.root, doctorOpts{})
	if c := checkByName(r, "fork-drift"); c.Status != statusOK {
		t.Fatalf("clean repo fork-drift = %+v, want ok", c)
	}

	// drift: rewrite main under the worktree.
	fi.rewindMain()

	r = buildDoctorReport(fi.root, doctorOpts{})
	c := checkByName(r, "fork-drift")
	if c.Status != statusWarn {
		t.Fatalf("drifted fork-drift = %+v, want warn", c)
	}
	if !strings.Contains(c.Detail, string(f.ID)) || !strings.Contains(c.Detail, f.BranchName()) {
		t.Errorf("detail %q should name the drifted feature and branch", c.Detail)
	}
	if c.Remediation == "" || !strings.Contains(c.Remediation, "press r") {
		t.Errorf("remediation %q should carry the shared r-gesture remedy", c.Remediation)
	}
	// doctor writes nothing: the recorded fork is unchanged.
	if got, err := fi.store.ForkPoint(context.Background(), f.ID); err != nil || got != fork {
		t.Fatalf("doctor changed the recorded fork: got %q (err %v), want %q", got, err, fork)
	}
	// it appears in the --json payload without error.
	if _, err := json.Marshal(r); err != nil {
		t.Fatalf("report does not marshal: %v", err)
	}
}

// doctorFixture is a real repo + gummi workspace + store + worktree manager,
// the shape buildDoctorReport reads against live.
type doctorFixture struct {
	root  string
	store *state.Store
	wt    *worktree.Manager
}

func newDoctorFixture(t *testing.T) *doctorFixture {
	t.Helper()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.CommandContext(context.Background(), "git",
			append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.name", "t")
	git("config", "user.email", "t@e.invalid")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-q", "-m", "init")

	ws, err := state.Init(root, root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	wt, err := worktree.NewManager(context.Background(), root, root, store)
	if err != nil {
		t.Fatal(err)
	}
	return &doctorFixture{root: root, store: store, wt: wt}
}

func (f *doctorFixture) feature() domain.Feature {
	id, _ := domain.NewFeatureID(1)
	slug, _ := domain.Slugify("drift me")
	now := time.Now()
	feat := domain.Feature{
		ID: id, Num: 1, Kind: domain.KindFeature, Title: "Drift me", Slug: slug,
		Stage: domain.StagePlan, CreatedAt: now, UpdatedAt: now,
	}
	if err := f.store.CreateFeature(context.Background(), &feat); err != nil {
		panic(err)
	}
	return feat
}

// rewindMain rewinds main to an unrelated lineage under the feature's
// worktree, so the recorded fork is no longer an ancestor of main's HEAD.
func (f *doctorFixture) rewindMain() {
	git := func(args ...string) {
		if err := exec.CommandContext(context.Background(), "git",
			append([]string{"-C", f.root}, args...)...).Run(); err != nil {
			panic(err)
		}
	}
	if err := os.WriteFile(filepath.Join(f.root, "rewound.ts"), []byte("rewound\n"), 0o600); err != nil {
		panic(err)
	}
	git("add", ".")
	git("checkout", "-q", "--orphan", "tmp-rewound")
	git("commit", "-q", "-m", "rewound main")
	git("branch", "-M", "tmp-rewound", "main")
}

// --deep defaults off, so the default `gummi doctor` stays cheap and
// offline, and parses through the command that actually runs it.
func TestDoctorDeepFlag(t *testing.T) {
	resetFlags(rootCmd)
	t.Cleanup(func() { resetFlags(rootCmd) })
	cmd, _, err := rootCmd.Find([]string{"doctor"})
	if err != nil {
		t.Fatalf("finding doctor: %v", err)
	}
	if cmdFlags(cmd).Bool("deep") {
		t.Fatal("deep defaults on")
	}
	if err := cmd.Flags().Parse([]string{"--deep"}); err != nil {
		t.Fatal(err)
	}
	if !cmdFlags(cmd).Bool("deep") {
		t.Fatal("--deep did not parse")
	}
}

// headless has no model to reach (the role routes through the env command),
// so its probe is trivially satisfied without constructing anything.
func TestProbeModelHeadless(t *testing.T) {
	clearDoctorEnv(t)
	bi := backendInfoFor("headless")
	if r := probeModel(bi, "qwen", time.Second); r != reachOK {
		t.Fatalf("headless probe = %q, want reachOK", r)
	}
}

// An opencode backend with no binary on PATH is unknown, not fail: the
// backend:<name> check owns "not on PATH". No network is touched.
func TestProbeModelUnknownOnMissingBackend(t *testing.T) {
	clearDoctorEnv(t)
	t.Setenv("PATH", t.TempDir())
	bi := backendInfoFor("opencode")
	if r := probeModel(bi, "m", time.Second); r != reachUnknown {
		t.Fatalf("probe = %q, want reachUnknown (no binary, no network)", r)
	}
}

// TestBackendInfoForAntigravity: doctor knows the backend's binary
// (honoring GUMMI_ANTIGRAVITY_BIN) and names the re-login path — the
// hint an operator with a dead token is shown.
func TestBackendInfoForAntigravity(t *testing.T) {
	clearDoctorEnv(t)
	bi := backendInfoFor("antigravity")
	if bi.name != "antigravity" || bi.bin != "agy" {
		t.Fatalf("backendInfoFor(antigravity) = %+v, want bin agy", bi)
	}
	if !strings.Contains(bi.login, "agy") {
		t.Errorf("login hint = %q, want it to name the agy re-login path", bi.login)
	}
	t.Setenv("GUMMI_ANTIGRAVITY_BIN", "/opt/agy")
	bi = backendInfoFor("antigravity")
	if bi.bin != "/opt/agy" {
		t.Errorf("bin with override = %q, want the override", bi.bin)
	}
}

// TestPricingCheckWarnsWithoutAnAntigravityRate: agy reports tokens only,
// so doctor warns when nothing prices them, names a bad value as bad, and
// reports a set rate — and says nothing for a backend that reports money.
func TestPricingCheckWarnsWithoutAnAntigravityRate(t *testing.T) {
	clearDoctorEnv(t)
	t.Setenv("GUMMI_ANTIGRAVITY_CREDITS_PER_1K", "")
	c, ok := pricingCheck("antigravity")
	if !ok || c.Status != statusWarn || !strings.Contains(c.Detail, "unset") || c.Remediation == "" {
		t.Fatalf("unset rate: %+v ok=%v, want a warn naming it unset with a remediation", c, ok)
	}
	t.Setenv("GUMMI_ANTIGRAVITY_CREDITS_PER_1K", "abc")
	if c, _ := pricingCheck("antigravity"); c.Status != statusWarn || !strings.Contains(c.Detail, "not a positive number") {
		t.Fatalf("bad rate: %+v, want a warn naming the value bad", c)
	}
	t.Setenv("GUMMI_ANTIGRAVITY_CREDITS_PER_1K", "0.08")
	if c, _ := pricingCheck("antigravity"); c.Status != statusOK || !strings.Contains(c.Detail, "0.08") {
		t.Fatalf("set rate: %+v, want ok reporting 0.08", c)
	}
	if _, ok := pricingCheck("claude"); ok {
		t.Fatal("pricing check emitted for claude, which reports its own cost")
	}
}

// TestConsultChecksReportConfinementPerBackend: doctor names, per backend
// that answers a consult, whether the consult runs read-only there — the
// consult role first, the architect when a profile has none, the default
// backend when neither names one — and warns, naming the profiles, where
// it can write to the checkout.
func TestConsultChecksReportConfinementPerBackend(t *testing.T) {
	profiles := config.Profiles{Profiles: map[string]config.Profile{
		"thrifty": {"architect": {Backend: "claude"}, "implementer": {Backend: "copilot"}},
		"premium": {"consult": {Backend: "codex"}, "architect": {Backend: "claude"}},
		"plain":   {"implementer": {Backend: "opencode"}},
	}}
	got := map[string]doctorCheck{}
	for _, c := range consultChecks("copilot", profiles) {
		got[c.Name] = c
	}
	if len(got) != 3 {
		t.Fatalf("checks = %+v, want one each for claude, codex and copilot", got)
	}
	if c := got["consult:claude"]; c.Status != statusOK || !strings.Contains(c.Detail, "read-only") {
		t.Errorf("claude: %+v, want ok and read-only", c)
	}
	if c := got["consult:codex"]; c.Status != statusWarn || !strings.Contains(c.Detail, engine.ConsultNotice("codex")) || !strings.Contains(c.Detail, "premium") || c.Remediation == "" {
		t.Errorf("codex: %+v, want a warn carrying the notice and naming premium", c)
	}
	if c := got["consult:copilot"]; c.Status != statusWarn || !strings.Contains(c.Detail, "plain") {
		t.Errorf("copilot (the default, for a profile naming neither role): %+v, want a warn naming plain", c)
	}

	none := consultChecks("claude", config.Profiles{})
	if len(none) != 1 || none[0].Name != "consult:claude" || none[0].Status != statusOK {
		t.Errorf("no profiles: %+v, want one ok check for the default backend", none)
	}
}

// A fresh TTL cache entry is reused verbatim: the live probe is never
// called and the cached servable result is reported.
func TestProbeCacheFreshHit(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".gummi"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".gummi", probeCacheFile)
	now := time.Now()
	if err := recordProbe(path, "opencode|m", true, now); err != nil {
		t.Fatal(err)
	}
	ws := state.Workspace{Root: dir}
	profiles := config.Profiles{Default: "p", Profiles: map[string]config.Profile{
		"p": {
			"architect":   {Backend: "opencode", Model: "m"},
			"implementer": {Backend: "opencode", Model: "m"},
			"reviewer":    {Backend: "opencode", Model: "m"},
			"scribe":      {Backend: "opencode", Model: "m"},
		},
	}}
	calls := 0
	probe := func(bi backendInfo, model string, timeout time.Duration) probeResult {
		calls++
		return reachFail
	}
	checks := reachChecks(ws, profiles, doctorOpts{Deep: true, Probe: probe}, now)
	if calls != 0 {
		t.Fatalf("fresh cache hit should skip the live probe, got %d calls", calls)
	}
	if c := checkByName(doctorReport{Checks: checks}, "reach:p/architect"); c.Status != statusOK {
		t.Fatalf("reach:p/architect = %+v, want ok from cache", c)
	}
}

// An entry older than the TTL is a miss: the live probe runs and the fresh
// result (here a fail) is reported.
func TestProbeCacheExpired(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".gummi"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".gummi", probeCacheFile)
	now := time.Now()
	// recorded longer ago than the TTL, so the entry is stale at `now`.
	if err := recordProbe(path, "opencode|m", true, now.Add(-probeCacheTTL-time.Minute)); err != nil {
		t.Fatal(err)
	}
	ws := state.Workspace{Root: dir}
	profiles := config.Profiles{Default: "p", Profiles: map[string]config.Profile{
		"p": {
			"architect":   {Backend: "opencode", Model: "m"},
			"implementer": {Backend: "opencode", Model: "m"},
			"reviewer":    {Backend: "opencode", Model: "m"},
			"scribe":      {Backend: "opencode", Model: "m"},
		},
	}}
	calls := 0
	probe := func(bi backendInfo, model string, timeout time.Duration) probeResult {
		calls++
		return reachFail
	}
	checks := reachChecks(ws, profiles, doctorOpts{Deep: true, Probe: probe}, now)
	if calls != 1 {
		t.Fatalf("expired cache should trigger a live probe, got %d calls", calls)
	}
	if c := checkByName(doctorReport{Checks: checks}, "reach:p/architect"); c.Status != statusFail {
		t.Fatalf("reach:p/architect = %+v, want fail", c)
	}
}

// A plain second TUI — no GUMMI_MCP_SOCK in its environment — still gets
// told to close the other TUI, named by its holder record; a lock with no
// record (an older gummi) is reported without guessing which host it is.
func TestLockCheckSecondTUI(t *testing.T) {
	t.Setenv("GUMMI_MCP_SOCK", "")
	dir := t.TempDir()
	ws := state.Workspace{Root: dir}

	release, err := state.AcquireLock(ws.LockFile())
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}

	c := lockCheck(ws)
	if c.Status != statusWarn || !strings.Contains(c.Detail, "another gummi board holds it") {
		t.Fatalf("lockCheck = %+v, want the busy warning", c)
	}

	// with the holder's record beside the lock, it is named
	release()
	release, err = state.AcquireInstance(ws, state.InstanceHolder{Host: state.HostTUI, PID: 4242, Hostname: "box"})
	if err != nil {
		t.Fatalf("AcquireInstance: %v", err)
	}
	defer release()
	c = lockCheck(ws)
	if c.Status != statusWarn || !strings.Contains(c.Detail, "another TUI holds it (pid 4242 on box)") {
		t.Fatalf("lockCheck = %+v, want the second-TUI warning naming its pid", c)
	}
	if !strings.Contains(c.Remediation, "close the other TUI") {
		t.Fatalf("lockCheck remediation = %q, want it to name closing the other TUI", c.Remediation)
	}
}

// `gummi web` holds the same lock as the TUI. doctor must say that it is
// the web host that has the board, and where, rather than send the reader
// looking for a TUI that is not running.
func TestLockCheckNamesTheWebHost(t *testing.T) {
	t.Setenv("GUMMI_MCP_SOCK", "")
	ws := state.Workspace{Root: t.TempDir()}
	release, err := state.AcquireInstance(ws, state.InstanceHolder{Host: state.HostWeb, URL: "http://127.0.0.1:4711/", PID: 99, Hostname: "box"})
	if err != nil {
		t.Fatalf("AcquireInstance: %v", err)
	}
	defer release()

	c := lockCheck(ws)
	if c.Status != statusWarn || !strings.Contains(c.Detail, "gummi web serves this board at http://127.0.0.1:4711/ (pid 99 on box)") {
		t.Fatalf("lockCheck = %+v, want the web host named with its URL and pid", c)
	}
	if strings.Contains(c.Detail+c.Remediation, "TUI holds") || !strings.Contains(c.Remediation, "http://127.0.0.1:4711/") {
		t.Fatalf("lockCheck = %+v, want a remediation that points at the web board", c)
	}
}

// gummi web's own Doctor view runs doctor inside the process that holds
// the lock. It used to warn the reader to "stop gummi web before opening
// the TUI" — about the page they were reading. The board's own lock is
// expected, and says so.
func TestLockCheckHeldByThisBoardIsOK(t *testing.T) {
	t.Setenv("GUMMI_MCP_SOCK", "")
	ws := state.Workspace{Root: t.TempDir()}
	release, err := state.AcquireInstance(ws, state.InstanceHolder{Host: state.HostWeb, URL: "http://127.0.0.1:4711/"})
	if err != nil {
		t.Fatalf("AcquireInstance: %v", err)
	}
	defer release()

	c := lockCheck(ws)
	if c.Status != statusOK || !strings.Contains(c.Detail, "held by this board") || !strings.Contains(c.Detail, "http://127.0.0.1:4711/") {
		t.Fatalf("lockCheck = %+v, want ok, held by this board, naming where it serves", c)
	}
	if strings.Contains(c.Remediation, "stop gummi web") {
		t.Fatalf("lockCheck still tells the web board to stop itself: %+v", c)
	}
}

// A claude-backed role spelled the way another CLI spells the model
// (claude-haiku-4.5) is refused by the claude CLI on its first turn. The
// checklist says so without --deep, and names the spelling that works.
func TestDoctorFlagsAClaudeModelIDTheCLIRefuses(t *testing.T) {
	clearDoctorEnv(t)
	t.Setenv("GUMMI_AGENT", "claude")
	root := gitRepo(t)
	if err := os.MkdirAll(filepath.Join(root, ".gummi"), 0o750); err != nil {
		t.Fatal(err)
	}
	prof := "default: std\nprofiles:\n  std:\n    architect: {model: claude-sonnet-5}\n    implementer: {model: claude-sonnet-5}\n" +
		"    reviewer: {model: claude-sonnet-5}\n    scribe: {model: claude-haiku-4.5}\n"
	if err := os.WriteFile(filepath.Join(root, ".gummi", "profiles.yaml"), []byte(prof), 0o600); err != nil {
		t.Fatal(err)
	}
	r := buildDoctorReport(root, doctorOpts{})
	var scribe, architect doctorCheck
	for _, c := range r.Checks {
		switch c.Name {
		case "reach:std/scribe":
			scribe = c
		case "reach:std/architect":
			architect = c
		}
	}
	if scribe.Status != statusFail || !strings.Contains(scribe.Remediation, "claude-haiku-4-5") {
		t.Fatalf("scribe reach = %+v, want a failure naming claude-haiku-4-5", scribe)
	}
	if architect.Status != statusUnknown {
		t.Errorf("architect reach = %+v, want the ordinary not-probed line for a well-formed id", architect)
	}
	if r.Ready {
		t.Error("doctor reports ready while the scribe's model id is one the backend refuses")
	}
}

// The running board's own view of profiles.yaml, for a doctor it serves.
func TestProfilesLiveCheck(t *testing.T) {
	if c := profilesLiveCheck(engine.ProfilesState{Reloads: 2}); c.Status != statusOK || !strings.Contains(c.Detail, "2 edits picked up") {
		t.Errorf("clean state = %+v", c)
	}
	c := profilesLiveCheck(engine.ProfilesState{Refused: "parsing: bad"})
	if c.Status != statusWarn || !strings.Contains(c.Detail, "did not apply") {
		t.Errorf("refused state = %+v, want a warning saying the edit was not applied", c)
	}
}

// The default (non-deep) doctor reports every reach:* as unknown ("not
// probed"), stays ready, and never creates the probe-cache sidecar — the
// offline invariant.
func TestDoctorReachUnknownWhenNotDeep(t *testing.T) {
	clearDoctorEnv(t)
	repo := gitRepo(t)
	writeProfiles(t, repo, headlessProfiles)
	fakeAgentOnPath(t, "fakeagent")
	t.Setenv("GUMMI_AGENT", "headless")
	t.Setenv("GUMMI_AGENT_CMD", "fakeagent")
	t.Setenv("GUMMI_ENVELOPE", "500")

	r := buildDoctorReport(repo, doctorOpts{})
	var reach []string
	for _, c := range r.Checks {
		if strings.HasPrefix(c.Name, "reach:") {
			reach = append(reach, c.Name)
		}
	}
	if len(reach) != 4 {
		t.Fatalf("expected 4 reach checks, got %v", reach)
	}
	for _, c := range r.Checks {
		if strings.HasPrefix(c.Name, "reach:") {
			if c.Status != statusUnknown {
				t.Errorf("%s = %s, want unknown (not probed)", c.Name, c.Status)
			}
			if !strings.Contains(c.Detail, "not probed") {
				t.Errorf("%s detail %q should say not probed", c.Name, c.Detail)
			}
		}
	}
	if !r.Ready {
		t.Errorf("default doctor should stay ready with reach unknown: %+v", r.Checks)
	}
	if _, err := os.Stat(filepath.Join(repo, ".gummi", probeCacheFile)); !os.IsNotExist(err) {
		t.Errorf("non-deep doctor must not create the probe cache sidecar (err=%v)", err)
	}
}

// With an injected probe, reach:* reports ok/fail/unknown per role, a fail
// flips Ready false, and unknown never does — all offline.
func TestDoctorReachWithInjectedProbe(t *testing.T) {
	clearDoctorEnv(t)
	fakeAgentOnPath(t, "opencode")
	repo := gitRepo(t)
	writeProfiles(t, repo, `
default: thrifty
profiles:
  thrifty:
    architect: { backend: opencode, model: good }
    implementer: { backend: opencode, model: bad }
    reviewer: { backend: opencode, model: unknown }
    scribe: { backend: opencode, model: good }
`)
	probe := func(bi backendInfo, model string, timeout time.Duration) probeResult {
		switch model {
		case "good":
			return reachOK
		case "bad":
			return reachFail
		default:
			return reachUnknown
		}
	}
	r := buildDoctorReport(repo, doctorOpts{Deep: true, Probe: probe})
	if c := checkByName(r, "reach:thrifty/architect"); c.Status != statusOK {
		t.Errorf("architect = %+v, want ok", c)
	}
	if c := checkByName(r, "reach:thrifty/implementer"); c.Status != statusFail {
		t.Errorf("implementer = %+v, want fail", c)
	}
	if c := checkByName(r, "reach:thrifty/reviewer"); c.Status != statusUnknown {
		t.Errorf("reviewer = %+v, want unknown", c)
	}
	if c := checkByName(r, "reach:thrifty/scribe"); c.Status != statusOK {
		t.Errorf("scribe = %+v, want ok", c)
	}
	if r.Ready {
		t.Errorf("a failing reach check must flip Ready false: %+v", r.Checks)
	}

	// an unknown-only deep run never flips readiness (fresh repo so the
	// probe cache from the run above is not reused).
	repo2 := gitRepo(t)
	writeProfiles(t, repo2, `
default: thrifty
profiles:
  thrifty:
    architect: { backend: opencode, model: m }
    implementer: { backend: opencode, model: m }
    reviewer: { backend: opencode, model: m }
    scribe: { backend: opencode, model: m }
`)
	r2 := buildDoctorReport(repo2, doctorOpts{Deep: true, Probe: func(bi backendInfo, model string, timeout time.Duration) probeResult {
		return reachUnknown
	}})
	if !r2.Ready {
		t.Errorf("unknown reach checks must not flip Ready: %+v", r2.Checks)
	}
}

// An inconclusive probe (unknown) is never cached, so it is not replayed as
// a hard fail on a later --deep run: a transient timeout, closed stream, or
// auth-blocked interactive backend must report unknown, not fail. The sidecar
// is left without the key (or uncreated) so the next deep run re-probes.
func TestProbeUnknownNotCachedAsFail(t *testing.T) {
	clearDoctorEnv(t)
	fakeAgentOnPath(t, "opencode")
	repo := gitRepo(t)
	writeProfiles(t, repo, `
default: thrifty
profiles:
  thrifty:
    architect: { backend: opencode, model: m }
    implementer: { backend: opencode, model: m }
    reviewer: { backend: opencode, model: m }
    scribe: { backend: opencode, model: m }
`)
	r := buildDoctorReport(repo, doctorOpts{Deep: true, Probe: func(bi backendInfo, model string, timeout time.Duration) probeResult {
		return reachUnknown
	}})
	for _, c := range r.Checks {
		if strings.HasPrefix(c.Name, "reach:") && c.Status != statusUnknown {
			t.Errorf("%s = %s, want unknown (an inconclusive probe is never fail)", c.Name, c.Status)
		}
	}
	if !r.Ready {
		t.Errorf("unknown reach checks must not flip Ready: %+v", r.Checks)
	}
	// The inconclusive result must not be persisted: a corrupt or absent
	// sidecar degrades to a live probe, and a stale ok:false would be
	// replayed as a fail. Here the sidecar must be absent (or lack the key).
	if raw, err := os.ReadFile(filepath.Join(repo, ".gummi", probeCacheFile)); err == nil {
		m := map[string]probeCacheEntry{}
		if uerr := json.Unmarshal(raw, &m); uerr != nil {
			t.Fatalf("sidecar unreadable: %v", uerr)
		}
		for k, e := range m {
			if !e.OK {
				t.Errorf("inconclusive probe persisted as ok:false for %q; would replay as a fail", k)
			}
		}
	}
}

// A fresh workspace's first --deep run has no sidecar to seed the in-memory
// dedupe map, so several roles resolving to the identical (backend, model)
// pair must still probe it only once. Regression for the found gap: cache
// started nil (loadProbeCache errors on a missing file), silently
// disabling the within-run write and letting every role probe
// independently on exactly the run this dedupe exists to protect.
func TestDoctorReachDedupesWithinFreshRun(t *testing.T) {
	clearDoctorEnv(t)
	fakeAgentOnPath(t, "opencode")
	repo := gitRepo(t)
	writeProfiles(t, repo, `
default: thrifty
profiles:
  thrifty:
    architect: { backend: opencode, model: same }
    implementer: { backend: opencode, model: same }
    reviewer: { backend: opencode, model: same }
    scribe: { backend: opencode, model: same }
`)
	calls := map[string]int{}
	probe := func(bi backendInfo, model string, timeout time.Duration) probeResult {
		calls[probeCacheKey(bi.name, model)]++
		return reachOK
	}
	r := buildDoctorReport(repo, doctorOpts{Deep: true, Probe: probe})
	key := probeCacheKey("opencode", "same")
	if calls[key] != 1 {
		t.Errorf("probe called %d time(s) for %q on a fresh --deep run, want exactly 1 (four roles share this model)", calls[key], key)
	}
	for _, role := range []string{"architect", "implementer", "reviewer", "scribe"} {
		if c := checkByName(r, "reach:thrifty/"+role); c.Status != statusOK {
			t.Errorf("%s = %+v, want ok", role, c)
		}
	}
}

// TestDoctorNestedReady: a correctly configured nested layout — .gummi at
// ws, the git repo at ws/git/lxd — reports ready and names both roots.
func TestDoctorNestedReady(t *testing.T) {
	clearDoctorEnv(t)
	ws := t.TempDir()
	repo := filepath.Join(ws, "git", "lxd")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.CommandContext(context.Background(), "git",
			append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.name", "t")
	git("config", "user.email", "t@e.invalid")
	writeConfig(t, ws, "repo: git/lxd\n")
	writeProfiles(t, ws, headlessProfiles)
	fakeAgentOnPath(t, "fakeagent")
	t.Setenv("GUMMI_AGENT", "headless")
	t.Setenv("GUMMI_AGENT_CMD", "fakeagent --serve")
	t.Setenv("GUMMI_ENVELOPE", "500")

	r := buildDoctorReport(ws, doctorOpts{})
	if !r.Ready {
		t.Fatalf("nested layout not ready: %+v", r.Checks)
	}
	c := checkByName(r, "repo")
	if c.Status != statusOK || !strings.Contains(c.Detail, repo) || !strings.Contains(c.Detail, ws) {
		t.Errorf("repo check = %+v, want ok naming repo %s and workspace %s", c, repo, ws)
	}
	if c := checkByName(r, "workspace"); c.Status != statusOK {
		t.Errorf("workspace check = %+v, want ok", c)
	}
}

// TestDoctorNestedRepoNotToplevel: a repo: key pointing at a directory with
// no .git is a clear fail naming the offending root, not a downstream error.
func TestDoctorNestedRepoNotToplevel(t *testing.T) {
	clearDoctorEnv(t)
	ws := t.TempDir()
	writeConfig(t, ws, "repo: git/lxd\n") // no .git under it
	writeProfiles(t, ws, headlessProfiles)
	fakeAgentOnPath(t, "fakeagent")
	t.Setenv("GUMMI_AGENT", "headless")
	t.Setenv("GUMMI_AGENT_CMD", "fakeagent --serve")
	t.Setenv("GUMMI_ENVELOPE", "500")

	r := buildDoctorReport(ws, doctorOpts{})
	c := checkByName(r, "repo")
	if c.Status != statusFail || !strings.Contains(c.Detail, "not the root of a git repository") {
		t.Errorf("repo check = %+v, want fail for a repo without .git", c)
	}
}

// TestDoctorNestedRepoEscapesWorkspace: a repo: key that escapes the
// workspace is a clear fail at resolve time.
func TestDoctorNestedRepoEscapesWorkspace(t *testing.T) {
	clearDoctorEnv(t)
	ws := t.TempDir()
	writeConfig(t, ws, "repo: ../outside\n")
	writeProfiles(t, ws, headlessProfiles)
	fakeAgentOnPath(t, "fakeagent")
	t.Setenv("GUMMI_AGENT", "headless")
	t.Setenv("GUMMI_AGENT_CMD", "fakeagent --serve")
	t.Setenv("GUMMI_ENVELOPE", "500")

	r := buildDoctorReport(ws, doctorOpts{})
	c := checkByName(r, "repo")
	if c.Status != statusFail || !strings.Contains(c.Remediation, "repo:") {
		t.Errorf("repo check = %+v, want a fail with repo: remediation for an escaping root", c)
	}
}

func TestDoctorEnvSection(t *testing.T) {
	clearDoctorEnv(t)
	repo := gitRepo(t)
	writeProfiles(t, repo, headlessProfiles)
	writeConfig(t, repo, `env:
  present-tool:
    probe: "true"
    describe: a tool that is present
  absent-tool:
    probe: "false"
    describe: a tool that is absent
`)
	// Put a fake headless agent on PATH without stripping /bin/sh.
	agentDir := t.TempDir()
	p := filepath.Join(agentDir, "fakeagent")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", agentDir+":"+os.Getenv("PATH"))
	t.Setenv("GUMMI_AGENT", "headless")
	t.Setenv("GUMMI_AGENT_CMD", "fakeagent --serve")
	t.Setenv("GUMMI_ENVELOPE", "500")

	r := buildDoctorReport(repo, doctorOpts{})
	if !r.Ready {
		t.Fatalf("env section must not flip readiness: %+v", r.Checks)
	}

	present := checkByName(r, "env:present-tool")
	if present.Status != statusOK {
		t.Errorf("present env check = %+v, want ok", present)
	}
	if !strings.Contains(present.Detail, "a tool that is present") || !strings.Contains(present.Detail, "PRESENT") {
		t.Errorf("present env detail = %q, want describe and PRESENT", present.Detail)
	}

	absent := checkByName(r, "env:absent-tool")
	if absent.Status != statusWarn {
		t.Errorf("absent env check = %+v, want warn", absent)
	}
	if !strings.Contains(absent.Detail, "a tool that is absent") || !strings.Contains(absent.Detail, "ABSENT") {
		t.Errorf("absent env detail = %q, want describe and ABSENT", absent.Detail)
	}
}

// An empty .gummi/gummi.db is a decoy: gummi's store is at
// .gummi/state/gummi.db, and sqlite3 creates the shorter path for anyone
// who types it. doctor must name both paths, so the reader learns which
// one holds the data rather than concluding the data is missing.
func TestDoctorFlagsDecoyDatabase(t *testing.T) {
	clearDoctorEnv(t)
	// a workspace that is otherwise ready, so the readiness assertion at
	// the end is about the decoy file and nothing else
	t.Setenv("GUMMI_AGENT", "headless")
	t.Setenv("GUMMI_AGENT_CMD", "fakeagent --serve")
	t.Setenv("GUMMI_ENVELOPE", "500")
	stubBackendBins(t, "fakeagent")
	repo := gitRepo(t)
	writeProfiles(t, repo, `
default: p
profiles:
  p:
    architect: { backend: headless, model: m }
    implementer: { backend: headless, model: m }
    reviewer: { backend: headless, model: m }
    scribe: { backend: headless, model: m }
`)
	if r := buildDoctorReport(repo, doctorOpts{}); checkByName(r, "decoy-db").Status != statusOK {
		t.Fatalf("decoy-db = %+v on a clean workspace, want ok", checkByName(r, "decoy-db"))
	}

	decoy := filepath.Join(repo, ".gummi", "gummi.db")
	if err := os.MkdirAll(filepath.Dir(decoy), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(decoy, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	c := checkByName(buildDoctorReport(repo, doctorOpts{}), "decoy-db")
	if c.Status != statusWarn {
		t.Fatalf("decoy-db = %+v, want warn", c)
	}
	if !strings.Contains(c.Detail, filepath.Join(".gummi", "state", "gummi.db")) {
		t.Errorf("detail %q must point at the real store", c.Detail)
	}
	if !strings.Contains(c.Remediation, "sqlite3") {
		t.Errorf("remediation %q should say what created the file", c.Remediation)
	}
	// advisory only: a stray file must not block readiness
	if !buildDoctorReport(repo, doctorOpts{}).Ready {
		t.Error("a decoy database blocked readiness; it is advisory")
	}
}

// TestDoctorWriteCageReportsTierPerRole: worktree discipline is the
// backend's to keep, and the two tiers are not the same guarantee.
// doctor names which roles have only the weaker one, so the choice is
// visible before a role is routed rather than after a run goes wrong.
func TestDoctorWriteCageReportsTierPerRole(t *testing.T) {
	clearDoctorEnv(t)
	// the profiles below name claude, opencode and copilot to exercise the
	// two cage tiers; whether this host has those CLIs is beside the point
	t.Setenv("GUMMI_ENVELOPE", "500")
	stubBackendBins(t, "claude", "opencode", "copilot")

	repo := gitRepo(t)
	writeProfiles(t, repo, `
default: caged
profiles:
  caged:
    architect: { backend: claude, model: m }
    implementer: { backend: claude, model: m }
    reviewer: { backend: opencode, model: m }
    scribe: { backend: claude, model: m }
`)
	c := checkByName(buildDoctorReport(repo, doctorOpts{}), "write-cage:caged")
	if c.Status != statusOK {
		t.Fatalf("write-cage:caged = %+v, want ok (claude and opencode both pin file tools)", c)
	}
	// the shell caveat rides on every profile, caged ones included
	if !strings.Contains(c.Detail, "no backend cages shell commands") {
		t.Errorf("a fully caged profile still hides the shell gap: %q", c.Detail)
	}

	repo2 := gitRepo(t)
	writeProfiles(t, repo2, `
default: mixed
profiles:
  mixed:
    architect: { backend: claude, model: m }
    implementer: { backend: claude, model: m }
    reviewer: { backend: copilot, model: m }
    scribe: { backend: claude, model: m }
`)
	r := buildDoctorReport(repo2, doctorOpts{})
	c = checkByName(r, "write-cage:mixed")
	if c.Status != statusWarn {
		t.Fatalf("write-cage:mixed = %+v, want warn (copilot is cwd-only)", c)
	}
	if !strings.Contains(c.Detail, "reviewer (copilot)") {
		t.Errorf("detail %q must name the uncaged role and its backend", c.Detail)
	}
	if strings.Contains(c.Detail, "architect") {
		t.Errorf("detail %q named a role that IS caged", c.Detail)
	}
	if c.Remediation == "" {
		t.Error("no remediation naming the backends that do cage")
	}
	// advisory: the cwd tier is a legitimate configuration under the tripwire
	if !r.Ready {
		t.Error("an uncaged role blocked readiness; the tier is a choice, not a fault")
	}
}

// TestSeededProfilesRunOnEveryBackend: whatever GUMMI_AGENT selects, the
// profiles.yaml a first run seeds names, for every role, a model the
// backend that role resolves to accepts — the check the session picker
// and SwitchSessionModel apply — and doctor finds no backend/model
// conflict in it. A role without `backend:` follows the default backend,
// which is the case a single shared template got wrong.
func TestSeededProfilesRunOnEveryBackend(t *testing.T) {
	for _, backend := range engine.SessionBackends {
		p, err := config.ParseProfiles([]byte(config.ProfilesTemplateFor(backend)), "seed template")
		if err != nil {
			t.Fatalf("%s: the seeded template does not parse: %v", backend, err)
		}
		for name, prof := range p.Profiles {
			for role, rc := range prof {
				eff := rc.Backend
				if eff == "" {
					eff = backend
				}
				if err := engine.CheckSessionModel(eff, rc.Model); err != nil {
					t.Errorf("%s: profile %s role %s on %s: %v", backend, name, role, eff, err)
				}
			}
		}
		if bad := backendModelConflicts(backendInfo{name: backend}, p); bad != "" {
			t.Errorf("%s: doctor flags the seeded template: %s", backend, bad)
		}
	}
}
