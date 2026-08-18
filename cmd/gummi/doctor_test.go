package main

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/domain"
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
	if c := checkByName(r, "envelope"); c.Status != statusOK {
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
	if c := checkByName(r, "envelope"); c.Status != statusWarn {
		t.Errorf("envelope = %+v, want warn", c)
	}
	if !r.Ready {
		t.Errorf("an unset envelope should not block readiness: %+v", r.Checks)
	}

	t.Setenv("GUMMI_ENVELOPE", "5") // below one turn
	r = buildDoctorReport(gitRepo(t), doctorOpts{})
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

	r := buildDoctorReport(gitRepo(t), doctorOpts{}) // no .gummi workspace → seed template
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
		Stage: domain.StageSpec, CreatedAt: now, UpdatedAt: now,
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

// --deep parses through registerDoctorFlags and defaults off, so the
// default `gummi doctor` stays cheap and offline.
func TestDoctorDeepFlag(t *testing.T) {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags := registerDoctorFlags(fs)
	if *flags.deep {
		t.Fatal("deep defaults on")
	}
	if err := fs.Parse([]string{"--deep"}); err != nil {
		t.Fatal(err)
	}
	if !*flags.deep {
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
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
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
	if c.Status != statusFail || !strings.Contains(c.Detail, "not a git repository") {
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
