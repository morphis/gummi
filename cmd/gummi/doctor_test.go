package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/config"
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

func TestDoctorCodexUsesNativeLoginRemediation(t *testing.T) {
	clearDoctorEnv(t)
	fakeAgentOnPath(t, "codex")
	t.Setenv("GUMMI_AGENT", "codex")
	r := buildDoctorReport(gitRepo(t))
	if c := checkByName(r, "backend:codex"); c.Status != statusOK || !strings.Contains(c.Detail, "codex") {
		t.Fatalf("backend = %+v", c)
	}
	if c := checkByName(r, "auth:codex"); !strings.Contains(c.Remediation, "codex login") {
		t.Fatalf("auth = %+v", c)
	}
}

// gitRepo makes a temp dir look like a git repo root (buildDoctorReport only
// stats .git; it never shells out).
func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
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

	r := buildDoctorReport(repo)
	if !r.Ready {
		t.Fatalf("expected ready, got not ready: %+v", r.Checks)
	}
	if c := checkByName(r, "backend:headless"); c.Status != statusOK {
		t.Errorf("backend = %+v, want ok", c)
	}
	if c := checkByName(r, "auth:headless"); c.Status != statusOK {
		t.Errorf("auth = %+v, want ok", c)
	}
	if c := checkByName(r, "envelope"); c.Status != statusOK {
		t.Errorf("envelope = %+v, want ok", c)
	}
}

// A selected backend whose binary is absent fails readiness.
func TestDoctorBackendMissingBinary(t *testing.T) {
	clearDoctorEnv(t)
	t.Setenv("GUMMI_AGENT", "claude")
	t.Setenv("GUMMI_CLAUDE_BIN", "gummi-no-such-binary-xyz")

	r := buildDoctorReport(gitRepo(t))
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

	r := buildDoctorReport(repo)
	if c := checkByName(r, "envelope"); c.Status != statusWarn {
		t.Errorf("envelope = %+v, want warn", c)
	}
	if !r.Ready {
		t.Errorf("an unset envelope should not block readiness: %+v", r.Checks)
	}

	t.Setenv("GUMMI_ENVELOPE", "5") // below one turn
	r = buildDoctorReport(gitRepo(t))
	if c := checkByName(r, "envelope"); c.Status != statusWarn {
		t.Errorf("sub-turn envelope = %+v, want warn", c)
	}
}

// With the claude backend and no profiles.yaml yet, doctor evaluates the
// seed template that WOULD be written and fails the profile check, naming
// each role whose model the Anthropic-only backend cannot drive — the
// warning fires before the first run that would hit it.
func TestDoctorClaudeBackendFlagsForeignSeedModels(t *testing.T) {
	clearDoctorEnv(t)
	t.Setenv("GUMMI_AGENT", "claude")

	r := buildDoctorReport(gitRepo(t)) // no .gummi workspace → seed template
	c := checkByName(r, "profile")
	if c.Status != statusFail {
		t.Fatalf("profile = %+v, want fail (claude can't drive the mixed thrifty default)", c)
	}
	if !strings.Contains(c.Detail, "implementer=gpt-5-mini") {
		t.Errorf("profile detail should name the incompatible role: %q", c.Detail)
	}
	if !strings.Contains(c.Detail, "would be seeded") {
		t.Errorf("profile detail should note it is the seed template: %q", c.Detail)
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

	r := buildDoctorReport(gitRepo(t))
	if c := checkByName(r, "profile"); c.Status == statusFail {
		t.Errorf("profile = %+v, want non-fail for a non-claude backend", c)
	}
}

// A non-repo directory fails the repo check and blocks readiness.
func TestDoctorNoRepoFails(t *testing.T) {
	clearDoctorEnv(t)
	r := buildDoctorReport(t.TempDir())
	if c := checkByName(r, "repo"); c.Status != statusFail {
		t.Errorf("repo = %+v, want fail", c)
	}
	if r.Ready {
		t.Error("report is ready outside a git repo")
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
	r := buildDoctorReport(repo)
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
	r := buildDoctorReport(repo)
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

// writeProfiles writes a profiles.yaml under the repo's .gummi dir so the
// report's profile and backend checks parse a real loaded profile set.
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

	r := buildDoctorReport(repo)
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

	r := buildDoctorReport(repo)
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

	r := buildDoctorReport(gitRepo(t))
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

	r := buildDoctorReport(repo)
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
	for _, c := range buildDoctorReport(repo).Checks {
		if strings.HasPrefix(c.Name, "backend:") {
			got = append(got, c.Name)
		}
	}
	want := []string{"backend:opencode", "backend:claude", "backend:headless"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("backend order = %v, want %v", got, want)
	}
	var again []string
	for _, c := range buildDoctorReport(repo).Checks {
		if strings.HasPrefix(c.Name, "backend:") {
			again = append(again, c.Name)
		}
	}
	if strings.Join(again, ",") != strings.Join(want, ",") {
		t.Errorf("backend order not deterministic: %v vs %v", again, want)
	}
}
