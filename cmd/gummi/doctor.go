package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/envprobe"
	"github.com/morphis/gummi/internal/sandbox"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui"
	"github.com/morphis/gummi/internal/worktree"
)

// runDoctor implements `gummi doctor [--json] [--deep]` (DESIGN §5, G1): a
// read-only readiness checklist — repo, workspace, backend, profile, auth,
// envelope, lock, plus per-role model reachability under --deep — that the
// skill's first-run setup flow consumes via --json. It constructs no
// engine/agent and holds no lock beyond a momentary probe, so it is safe to
// run while a feature is live. It reports; it never repairs auth or writes
// secrets (G2/G4).
func runDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags := registerDoctorFlags(fs)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: gummi doctor [--json] [--deep]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	report := buildDoctorReport(cwd, doctorOpts{Deep: *flags.deep, Probe: probeModel, ZZAuthProbe: probeZZAuth})
	if *flags.json {
		b, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		return nil
	}
	renderDoctor(os.Stdout, report)
	return nil
}

// registerDoctorFlags binds `gummi doctor`'s flags, so the skill's grammar
// generator can enumerate them (see runFlagValues). deep turns on the live
// per-role reachability probe; the default stays cheap and offline.
func registerDoctorFlags(fs *flag.FlagSet) *doctorFlags {
	return &doctorFlags{
		json: fs.Bool("json", false, "emit the readiness checklist as JSON (the skill's setup path)"),
		deep: fs.Bool("deep", false, "probe per-role model reachability with a live backend turn (TTL-cached)"),
	}
}

// check statuses. fail blocks readiness; warn/unknown are advisory.
const (
	statusOK      = "ok"
	statusWarn    = "warn"
	statusFail    = "fail"
	statusUnknown = "unknown"
)

// doctorReport is the readiness payload — the JSON schema the skill parses,
// and the source of the text checklist. Ready is false iff any check failed.
type doctorReport struct {
	Ready  bool          `json:"ready"`
	Checks []doctorCheck `json:"checks"`
}

// doctorCheck is one readiness item. Remediation is the concrete next step
// (for auth, the exact command a human runs — gummi never runs it).
type doctorCheck struct {
	Name        string `json:"name"`
	Status      string `json:"status"`
	Detail      string `json:"detail"`
	Remediation string `json:"remediation,omitempty"`
}

// doctorFlags are the flag pointers `gummi doctor` binds. registerDoctorFlags
// returns them so runDoctor can thread the parsed values into the report.
type doctorFlags struct {
	json *bool
	deep *bool
}

// doctorOpts configures buildDoctorReport's reachability probe. Deep turns
// on the live per-role probe; Probe is the injectable seam that runs it
// (tests substitute a stub), defaulting to the real probeModel. ZZAuthProbe
// is the analogous seam for the offline `auth:zz` check, defaulting to
// probeZZAuth.
type doctorOpts struct {
	Deep        bool
	Probe       ProbeFn
	ZZAuthProbe zzAuthProbeFn
}

// ProbeFn runs one per-role reachability probe against a backend and model,
// bounded by timeout, returning a probeResult.
type ProbeFn func(bi backendInfo, model string, timeout time.Duration) probeResult

// buildDoctorReport assembles the checklist from the environment, the
// workspace, and the filesystem — no engine, no network (auth is probed
// offline; interactive-login backends degrade to "unknown"). It is the
// testable core: tests call it with a temp repo root and env.
func buildDoctorReport(cwd string, opts doctorOpts) doctorReport {
	var checks []doctorCheck
	add := func(name, status, detail, remediation string) {
		checks = append(checks, doctorCheck{Name: name, Status: status, Detail: detail, Remediation: remediation})
	}

	// 0. resolve the workspace root (where .gummi lives) and the managed
	// repository set — the default plus any named `repos:`. A misconfigured
	// key is a clear fail naming the offending repo, never a downstream
	// worktree error.
	wsRoot, defaultRoot, namedRepos, rerr := resolveAllRoots(cwd)
	if rerr != nil {
		wsRoot, defaultRoot = cwd, cwd
	}

	// 1. repo — one check per configured managed repository, each naming
	// its repo (the default keeps the bare "repo" name for compatibility).
	// A repos:-only workspace has no default: report only the named repos,
	// and never require the workspace root to be a git toplevel.
	if rerr != nil {
		add("repo", statusFail, rerr.Error(), "set `repo:` (and `repos:`) in .gummi/config.yaml to git toplevels inside the workspace")
	} else {
		if defaultRoot != "" {
			if isGitRepoRoot(defaultRoot) {
				add("repo", statusOK, "git repository at "+defaultRoot+" (workspace at "+wsRoot+")", "")
			} else {
				add("repo", statusFail, defaultRoot+" is not a git repository", "set `repo:` in .gummi/config.yaml to a git toplevel root, or remove it to manage the workspace root")
			}
		}
		for _, n := range namedRepos {
			add("repo:"+n.Name, statusOK, "git repository at "+n.Root+" (workspace at "+wsRoot+")", "")
		}
	}

	// 1b. git identity — one check per managed repository (the default
	// plus every named one, mirroring the repo check above), gated the
	// same way: only a root the repo check itself already confirmed is a
	// git toplevel is worth asking. A workspace with no repo resolved at
	// all (rerr != nil) has nothing to check here either.
	if rerr == nil {
		if defaultRoot != "" && isGitRepoRoot(defaultRoot) {
			c := gitIdentityCheck("git-identity", defaultRoot)
			add(c.Name, c.Status, c.Detail, c.Remediation)
		}
		for _, n := range namedRepos {
			c := gitIdentityCheck("git-identity:"+n.Name, n.Root)
			add(c.Name, c.Status, c.Detail, c.Remediation)
		}
	}

	// 2. workspace
	ws, wsErr := state.Open(wsRoot, defaultRoot)
	if wsErr == nil {
		add("workspace", statusOK, ".gummi workspace present", "")
	} else {
		ws = state.Workspace{Root: cwd} // path-only fallback for later checks
		add("workspace", statusWarn, "no .gummi workspace yet", "created automatically on the first `gummi run` (or `gummi` TUI)")
	}

	// 2b. env prerequisites — operator-configured probes reported as
	// status, never readiness. A missing/absent prerequisite is legitimate.
	userPath, upErr := config.UserConfigPath()
	if upErr != nil {
		add("config:user-path", statusWarn, upErr.Error(), "set XDG_CONFIG_HOME or ensure HOME is available")
		userPath = ""
	}
	wsCfg, sources, cfgErr := config.LoadLayered(userPath, ws.ConfigFile())
	if cfgErr != nil {
		add("config:load", statusFail, cfgErr.Error(), "fix the offending config file")
		wsCfg = config.Config{}
		sources = map[string]string{}
	}
	checks = append(checks, envChecks(wsCfg, defaultRoot)...)
	checks = append(checks, configLayeringChecks(wsCfg, sources, userPath, ws.ConfigFile())...)

	// profiles are parsed once and shared by the backend check (they decide
	// which backends are required) and the profile cross-check below.
	profiles, seeded, perr := effectiveProfiles(ws.ProfilesFile())

	// 3. backend — one check per required backend, mirroring the set
	// buildAgents (main.go) would start. The default backend is probed only
	// when the profiles actually need it; a workspace whose profiles route
	// every role elsewhere emits no line for the unused default at all. A
	// failing check names the profile/role pairs that pull the backend in,
	// so an operator knows which profile to re-point.
	def := defaultBackendName()
	rolesByBackend := requiredBackendRoles(def, profiles)
	ordered := orderedRequiredBackends(def, requiredBackends(def, profiles))
	for _, name := range ordered {
		bi := backendInfoFor(name)
		attr := roleAttribution(rolesByBackend[name])
		switch {
		case bi.bin == "":
			add("backend:"+bi.name, statusFail, "headless backend required but GUMMI_AGENT_CMD is empty"+attr, "set GUMMI_AGENT_CMD to the agent command line")
		case onPath(bi.bin):
			add("backend:"+bi.name, statusOK, fmt.Sprintf("%s (%s found on PATH)", bi.name, bi.bin), "")
		default:
			add("backend:"+bi.name, statusFail, fmt.Sprintf("%s backend selected but %q is not on PATH%s", bi.name, bi.bin, attr), bi.installHint(name == def))
		}
	}

	// 4. profile
	bi := backendInfoFor(def)
	switch {
	case perr != nil:
		add("profile", statusWarn, "profiles.yaml did not parse: "+perr.Error(), "fix .gummi/profiles.yaml")
	case len(profiles.Profiles) == 0:
		add("profile", statusWarn, "no profiles configured; gummi falls back to the single GUMMI_MODEL", nestingGuidance)
	default:
		note := ""
		if seeded {
			note = " (would be seeded on first run)"
		}
		if bad := backendModelConflicts(bi, profiles); bad != "" {
			add("profile", statusFail,
				fmt.Sprintf("default profile %q has roles the %s backend cannot drive%s: %s",
					profiles.Default, bi.name, note, bad),
				"set these roles to a claude-* model in .gummi/profiles.yaml, or select the matching backend via GUMMI_AGENT")
		} else {
			add("profile", statusOK, fmt.Sprintf("profiles: %s (default %q)%s", strings.Join(profiles.Names(), ", "), profiles.Default, note), nestingGuidance)
		}
	}

	// sandbox — one check per defined profile, reporting the resolved mode
	// and flagging any enforce profile whose tool coverage is incomplete.
	checks = append(checks, sandboxChecks(wsCfg, profiles)...)

	// guarded — one check per defined profile when permissions: guarded is
	// set, flagging any role whose resolved backend can't honor it.
	checks = append(checks, guardedChecks(wsCfg, profiles)...)

	// 5. auth (offline) — one line per required backend, pairing with the
	// backend:* checks above.
	for _, name := range ordered {
		checks = append(checks, authCheck(backendInfoFor(name), opts))
	}

	// 5b. reach — one line per declared profile/role, probing the model the
	// engine would actually run for that (profile, role). Offline (default)
	// these are all unknown ("not probed") and never touch a backend or the
	// cache; --deep runs a live, TTL-cached probe per effective model.
	checks = append(checks, reachChecks(ws, profiles, opts, time.Now())...)

	// 6. budget (GUMMI_ENVELOPE)
	checks = append(checks, envelopeCheck())

	// 7. lock (only meaningful once a workspace exists)
	if wsErr == nil {
		checks = append(checks, lockCheck(ws))
	} else {
		add("lock", statusOK, "n/a — no workspace to lock yet", "")
	}

	// 8. fork-drift — read-only report of features whose recorded fork
	// point is no longer an ancestor of main's HEAD. Advisory (warn): one
	// recoverable, per-feature condition must not block readiness.
	fd := forkDriftStatus(ws, defaultRoot, namedRepos)
	add(fd.Name, fd.Status, fd.Detail, fd.Remediation)

	// 9. decoy database — an empty .gummi/gummi.db beside the real one.
	// Advisory (warn): it breaks nothing, it just lies to whoever opens it.
	// Unconditional: this only stats paths, and ws is a usable path-only
	// value even when the workspace has not been initialized — a stray file
	// in a .gummi that predates `gummi init` is worth naming too.
	dc := decoyDBCheck(ws)
	add(dc.Name, dc.Status, dc.Detail, dc.Remediation)

	ready := true
	for _, c := range checks {
		if c.Status == statusFail {
			ready = false
		}
	}
	return doctorReport{Ready: ready, Checks: checks}
}

// effectiveProfiles returns the profiles doctor should judge: the parsed
// profiles.yaml when it exists, otherwise the ProfilesTemplate that
// ensureWorkspace WOULD seed on the first run (seeded=true) — so a
// backend/model conflict is caught before the run that would hit it, not
// after. A parse error is surfaced verbatim.
func effectiveProfiles(path string) (profiles config.Profiles, seeded bool, err error) {
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		p, perr := config.ParseProfiles([]byte(config.ProfilesTemplate), "seed template")
		return p, true, perr
	}
	p, perr := config.LoadProfiles(path)
	return p, false, perr
}

// backendModelConflicts reports the "role=model" pairs in the default
// profile that the selected backend cannot drive, or "" when there is no
// conflict. Only the Claude backend is Anthropic-locked today, so it is
// the only one cross-checked (agent.ForeignModel is the shared predicate
// the claude adapter rejects on at session start). A role that explicitly
// picks a different `backend:` is not flagged — the default backend never
// sees it.
func backendModelConflicts(bi backendInfo, p config.Profiles) string {
	if bi.name != "claude" {
		return ""
	}
	prof, ok := p.Profiles[p.Default]
	if !ok {
		return ""
	}
	roles := make([]string, 0, len(prof))
	for role := range prof {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	var bad []string
	for _, role := range roles {
		rc := prof[role]
		if rc.Backend != "" && rc.Backend != "claude" {
			continue // routed at a different backend; claude never sees it
		}
		if foreign, _ := agent.ForeignModel(rc.Model); foreign {
			bad = append(bad, role+"="+rc.Model)
		}
	}
	return strings.Join(bad, ", ")
}

const nestingGuidance = "steer to a cost-tiered profile: frontier models for architect/reviewer, a cheaper model for implementer/scribe; avoid pointing gummi's roles at the same frontier model your own session runs on (you'd pay for it twice)"

// authCheck reports auth readiness without spawning anything for most
// backends (confirmed offline). Provider config lives in each backend's
// native store now (Claude Code login, `opencode auth`, headless child's
// env), so an interactive-login backend degrades to "unknown" with the exact
// command a human runs (G2); a headless backend delegates to its own child.
// zz is the exception: it can answer its own config offline via `zz status`,
// so opts.ZZAuthProbe (defaulting to probeZZAuth) supplies a real check.
func authCheck(bi backendInfo, opts doctorOpts) doctorCheck {
	if bi.headless {
		return doctorCheck{Name: "auth:" + bi.name, Status: statusOK, Detail: "handled by the headless command (" + bi.bin + ")"}
	}
	if bi.name == "zz" {
		probe := opts.ZZAuthProbe
		if probe == nil {
			probe = probeZZAuth
		}
		res := probe(bi.bin, zzAuthProbeTimeout)
		// Detail is derived from Status alone, never from the probe's
		// free-text Summary: probeZZAuth's own Summary values are drawn from
		// a small safe set, but authCheck must not blindly trust an
		// injected probe (production or test) to uphold that contract.
		check := doctorCheck{Name: "auth:zz", Status: res.Status, Detail: zzAuthDetail(res.Status)}
		if res.Status != statusOK {
			check.Remediation = "zz setup"
		}
		return check
	}
	return doctorCheck{
		Name: "auth:" + bi.name, Status: statusUnknown, Detail: bi.name + " auth state is not checked offline",
		Remediation: "if runs fail on auth, have the human run: " + bi.login,
	}
}

// zzAuthDetail renders auth:zz's Detail from the probe's classified Status
// only — a fixed, enumerable string per status, so no probe implementation
// (including a misbehaving one) can smuggle probe output into the report.
func zzAuthDetail(status string) string {
	switch status {
	case statusOK:
		return "zz reports a configured provider"
	case statusFail:
		return "zz reports no provider configured"
	default:
		return "zz auth state could not be determined offline"
	}
}

// resolveRoleModel mirrors the engine's per-role fallback (Engine.resolveRole)
// to find the effective (model, backend) a feature using profile would run
// role with: the declared role, then the declared default profile's role,
// then the single engine model (GUMMI_MODEL) on the default backend. An
// empty backend means "use the default backend".
func resolveRoleModel(profiles config.Profiles, profileName string, role agent.Role) (model, backend string) {
	prof, ok := profiles.Profiles[profileName]
	if !ok {
		if def := profiles.Default; def != "" {
			prof, ok = profiles.Profiles[def]
		}
	}
	if ok {
		if rc, ok := prof[string(role)]; ok {
			return rc.Model, rc.Backend
		}
	}
	return cmp.Or(os.Getenv("GUMMI_MODEL"), "gpt-5"), ""
}

// reachChecks emits one reach:<profile>/<role> check per declared profile
// and each of the four roles, treating all four uniformly. Offline (no
// --deep) every check is unknown ("not probed") and no backend is ever
// constructed. Under --deep it resolves the effective (backend, model) for
// each role and probes it: a fresh TTL cache hit reuses the recorded result
// (skipping the live call), otherwise opts.Probe runs a real turn and the
// outcome is recorded. A fail check flips readiness via the existing loop;
// unknown never does.
func reachChecks(ws state.Workspace, profiles config.Profiles, opts doctorOpts, now time.Time) []doctorCheck {
	if len(profiles.Profiles) == 0 {
		return nil
	}
	def := defaultBackendName()
	probe := opts.Probe
	if probe == nil {
		probe = probeModel // documented default: doctorOpts.Probe
	}
	var cache map[string]probeCacheEntry
	cachePath := filepath.Join(ws.GummiDir(), probeCacheFile)
	if opts.Deep {
		// always a non-nil map on the deep path, even when no sidecar
		// exists yet (a fresh workspace's first --deep run), so the
		// within-run dedupe write below applies on that run too — not
		// just on a run that finds a pre-populated cache on disk.
		cache = map[string]probeCacheEntry{}
		if loaded, err := loadProbeCache(cachePath); err == nil {
			cache = loaded
		}
	}
	var checks []doctorCheck
	for _, pname := range profiles.Names() {
		for _, role := range []agent.Role{agent.RoleArchitect, agent.RoleImplementer, agent.RoleReviewer, agent.RoleScribe} {
			model, backend := resolveRoleModel(profiles, pname, role)
			if backend == "" {
				backend = def
			}
			bi := backendInfoFor(backend)
			name := "reach:" + pname + "/" + string(role)
			if !opts.Deep {
				checks = append(checks, doctorCheck{
					Name:        name,
					Status:      statusUnknown,
					Detail:      "not probed — run gummi doctor --deep",
					Remediation: "run `gummi doctor --deep` to probe per-role model reachability",
				})
				continue
			}
			var res probeResult
			key := probeCacheKey(bi.name, model)
			if e, ok := cache[key]; ok && now.Sub(e.ProbedAt) < probeCacheTTL {
				if e.OK {
					res = reachOK
				} else {
					res = reachFail
				}
			} else {
				res = probe(bi, model, probeTimeout)
				// Only definitive outcomes (ok/fail) are cached; an
				// inconclusive probe (timeout, closed stream, missing
				// binary, auth-blocked interactive backend) is left
				// uncached so it always re-probes and reports unknown —
				// a transient hiccup must never be replayed as a hard
				// fail on a later run. Within a run, the in-memory map
				// is updated too, so several roles that resolve to the
				// same (backend, model) probe once.
				if res == reachOK || res == reachFail {
					if cache != nil {
						cache[key] = probeCacheEntry{OK: res == reachOK, ProbedAt: now}
					}
					_ = recordProbe(cachePath, key, res == reachOK, now)
				}
			}
			switch res {
			case reachOK:
				checks = append(checks, doctorCheck{Name: name, Status: statusOK, Detail: fmt.Sprintf("%s on %s is servable", model, bi.name)})
			case reachFail:
				checks = append(checks, doctorCheck{
					Name:        name,
					Status:      statusFail,
					Detail:      fmt.Sprintf("%s on %s is not servable", model, bi.name),
					Remediation: "re-point this role's model in .gummi/profiles.yaml, or fix the backend's provider/auth config",
				})
			default:
				checks = append(checks, doctorCheck{Name: name, Status: statusUnknown, Detail: fmt.Sprintf("could not probe %s on %s", model, bi.name)})
			}
		}
	}
	return checks
}

// envelopeCheck validates GUMMI_ENVELOPE. It never fails readiness: a run
// can still take --envelope N, and the run itself enforces the requirement.
//
// The check REPORTS itself as "budget", not "envelope". The env var and
// the flag keep their names — they are an API scripts already call — but
// the word a human reads is the plain one, matching the TUI, which stopped
// calling a spend cap an "envelope" for the same reason: nobody outside
// this codebase knows what an envelope is.
func envelopeCheck() doctorCheck {
	v := strings.TrimSpace(os.Getenv("GUMMI_ENVELOPE"))
	if v == "" {
		return doctorCheck{
			Name: "budget", Status: statusWarn,
			Detail:      fmt.Sprintf("GUMMI_ENVELOPE is unset — no default spend budget (the board prefills %d credits; headless runs have none)", ui.DefaultEnvelopeCredits),
			Remediation: "pass --envelope N per run, or export GUMMI_ENVELOPE=<credits> (headless runs refuse to start without a budget)",
		}
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return doctorCheck{
			Name: "budget", Status: statusWarn, Detail: "GUMMI_ENVELOPE=" + v + " is not a positive integer",
			Remediation: "set GUMMI_ENVELOPE to a positive credit count",
		}
	}
	if float64(n) < domain.TurnReserveCredits {
		return doctorCheck{
			Name: "budget", Status: statusWarn,
			Detail:      fmt.Sprintf("GUMMI_ENVELOPE=%d is below one agent turn (~%d credits)", n, int(domain.TurnReserveCredits)),
			Remediation: "raise it so stage budgets aren't floored at a single turn and overshoot the cap",
		}
	}
	return doctorCheck{Name: "budget", Status: statusOK, Detail: fmt.Sprintf("spend budget: %d credits per run", n)}
}

// lockCheck probes the workspace's exclusive lock and releases it
// immediately — reporting "busy" when another TUI holds it, so a caller
// learns a second board would refuse before opening one. Headless drives
// hold per-card locks (not this one), so a live run does not show up here.
//
// A locked workspace has two very different callers, though: a genuinely
// separate second TUI, and the agent tab's hosted CLI — a descendant of
// the very TUI holding the lock, whose entire purpose is to drive it. The
// two report ErrLocked identically, so hostedInThisWorkspace tells them
// apart via GUMMI_MCP_SOCK (see its doc comment) rather than telling a
// hosted agent to close its own host.
func lockCheck(ws state.Workspace) doctorCheck {
	release, err := state.AcquireLock(ws.LockFile())
	switch {
	case errors.Is(err, state.ErrLocked):
		if hostedInThisWorkspace(ws) {
			return doctorCheck{
				Name: "lock", Status: statusOK,
				Detail:      "running inside this board's hosted agent — the lock is held by your own host",
				Remediation: "drive it with the workspace MCP tools instead of a second gummi process; status/spec/diff/watch/doctor take no lock and stay available either way",
			}
		}
		return doctorCheck{
			Name: "lock", Status: statusWarn, Detail: "workspace busy — another TUI holds it",
			Remediation: "close the other TUI before opening the board",
		}
	case err != nil:
		return doctorCheck{Name: "lock", Status: statusWarn, Detail: "could not probe the workspace lock: " + err.Error()}
	default:
		release()
		return doctorCheck{Name: "lock", Status: statusOK, Detail: "workspace is free"}
	}
}

// hostedInThisWorkspace reports whether the calling process is the agent
// tab's hosted CLI for ws's own TUI. ensureAgent injects GUMMI_MCP_SOCK
// into every hosted agent's environment, and that path always lives under
// this workspace's own StateDir()/mcp/ (workspaceMCPSockPath, bound by the
// TUI process itself). Env vars are inherited at fork, not derived from a
// live ppid chain, so this signal survives reparenting or double-forking
// in a way a ppid walk would not.
//
// A GUMMI_MCP_SOCK pointing outside this workspace's state dir names a
// hosted agent for a *different* board — that case must still fall
// through to the "close the other TUI" remediation, since it names a real
// second board relative to ws.
func hostedInThisWorkspace(ws state.Workspace) bool {
	sock := strings.TrimSpace(os.Getenv("GUMMI_MCP_SOCK"))
	if sock == "" {
		return false
	}
	mcpDir := filepath.Clean(filepath.Join(ws.StateDir(), "mcp"))
	rel, err := filepath.Rel(mcpDir, filepath.Clean(sock))
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// backendInfo describes the selected agent backend without starting it — the
// binary it needs, whether it is a generic headless command, and the
// interactive login command to hand a human if auth is missing.
type backendInfo struct {
	name     string
	bin      string // primary binary the adapter needs ("" = headless with an empty command)
	login    string // interactive login command surfaced to the human
	headless bool
}

func (bi backendInfo) installHint(isDefault bool) string {
	if bi.headless {
		return "install " + bi.bin + " (the GUMMI_AGENT_CMD agent), or point GUMMI_AGENT_CMD at an available one"
	}
	if !isDefault {
		return "install the " + bi.name + " CLI (" + bi.bin + "), or re-point the roles that need it in .gummi/profiles.yaml"
	}
	return "install the " + bi.name + " CLI (" + bi.bin + "), or select another backend via GUMMI_AGENT"
}

// backendInfoFor describes one named backend — the same mapping
// startAdapter (main.go) uses, without constructing the agent, so doctor
// can report each required backend offline.
func backendInfoFor(name string) backendInfo {
	switch name {
	case "claude":
		return backendInfo{name: "claude", bin: cmp.Or(os.Getenv("GUMMI_CLAUDE_BIN"), "claude"), login: "authenticate the Claude Code CLI (`claude`)"}
	case "opencode":
		return backendInfo{name: "opencode", bin: cmp.Or(os.Getenv("GUMMI_OPENCODE_BIN"), "opencode"), login: "opencode auth login"}
	case "codex":
		return backendInfo{name: "codex", bin: cmp.Or(os.Getenv("GUMMI_CODEX_BIN"), "codex"), login: "codex login"}
	case "headless":
		return backendInfo{name: "headless", bin: firstField(os.Getenv("GUMMI_AGENT_CMD")), headless: true}
	case "zz":
		return backendInfo{name: "zz", bin: cmp.Or(os.Getenv("GUMMI_ZZ_BIN"), "zz"), login: "zz setup"}
	case "copilot":
		return backendInfo{name: "copilot", bin: "copilot", login: "gh auth login  (authenticate GitHub Copilot)"}
	}
	return backendInfo{name: ""}
}

// orderedRequiredBackends returns the backends doctor must probe: the
// default first when it is required, then the rest in lexicographic order.
// The ordering is deterministic so two runs over the same state agree.
func orderedRequiredBackends(def string, req map[string]struct{}) []string {
	names := make([]string, 0, len(req))
	for name := range req {
		if name != def {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if _, ok := req[def]; ok {
		names = append([]string{def}, names...)
	}
	return names
}

// requiredBackendRoles mirrors requiredBackends (main.go) but keeps the
// provenance the set discards: for each backend the loaded profiles
// reference, the "profile/role" pairs that pull it in, in deterministic
// order. Doctor uses it to name who needs a backend, so an operator can
// re-point the right profile when a backend is missing. With no profiles at
// all the default backend is required but has no profile to name.
func requiredBackendRoles(def string, profiles config.Profiles) map[string][]string {
	roles := map[string][]string{}
	if len(profiles.Profiles) == 0 {
		roles[def] = nil
	}
	for pname, p := range profiles.Profiles {
		byRole := map[string][]string{}
		for role, rc := range p {
			name := rc.Backend
			if name == "" {
				name = def
			}
			byRole[name] = append(byRole[name], role)
		}
		for b, rs := range byRole {
			sort.Strings(rs)
			for _, r := range rs {
				roles[b] = append(roles[b], pname+"/"+r)
			}
		}
	}
	for b := range roles {
		sort.Strings(roles[b])
	}
	return roles
}

// guardedIncompatibility names one profile/role pairing whose resolved
// backend cannot honor permissions: guarded.
type guardedIncompatibility struct {
	Profile, Role, Backend string
}

// guardedIncompatibilities walks every profile's resolved roles — applying
// the same default-backend substitution and no-profiles fallback as
// requiredBackendRoles — and reports each role whose backend is known to
// reject guarded (agent.GuardedSupport reports known && !support). A
// backend absent from the matrix (headless, or unrecognized) is silently
// skipped rather than flagged: gummi cannot tell whether it honors guarded,
// so it is never reported either way. With no profiles configured, a
// mismatch on the default backend is reported under the synthetic profile
// name "(default)" with no role, mirroring the "no profiles configured;
// gummi falls back to the single GUMMI_MODEL" state doctor's own profile
// check already treats as legitimate. The result is sorted by profile then
// role for a deterministic report.
func guardedIncompatibilities(def string, profiles config.Profiles) []guardedIncompatibility {
	var issues []guardedIncompatibility
	if len(profiles.Profiles) == 0 {
		if support, known := agent.GuardedSupport(def); known && !support {
			issues = append(issues, guardedIncompatibility{Profile: "(default)", Backend: def})
		}
		return issues
	}
	pnames := make([]string, 0, len(profiles.Profiles))
	for pname := range profiles.Profiles {
		pnames = append(pnames, pname)
	}
	sort.Strings(pnames)
	for _, pname := range pnames {
		p := profiles.Profiles[pname]
		roles := make([]string, 0, len(p))
		for role := range p {
			roles = append(roles, role)
		}
		sort.Strings(roles)
		for _, role := range roles {
			name := p[role].Backend
			if name == "" {
				name = def
			}
			if support, known := agent.GuardedSupport(name); known && !support {
				issues = append(issues, guardedIncompatibility{Profile: pname, Role: role, Backend: name})
			}
		}
	}
	return issues
}

// formatGuardedIncompatibilities renders each offending pairing as
// `profile %q role %q -> backend %q`, joined by "; ". An issue with no role
// (the no-profiles-configured fallback, keyed under "(default)") renders as
// `profile %q -> backend %q` instead, omitting the empty role clause. Doctor's
// guarded:* check detail and the startup config-validation error share this
// verbatim so the two surfaces can't drift in wording.
func formatGuardedIncompatibilities(issues []guardedIncompatibility) string {
	parts := make([]string, 0, len(issues))
	for _, i := range issues {
		if i.Role == "" {
			parts = append(parts, fmt.Sprintf("profile %q -> backend %q", i.Profile, i.Backend))
			continue
		}
		parts = append(parts, fmt.Sprintf("profile %q role %q -> backend %q", i.Profile, i.Role, i.Backend))
	}
	return strings.Join(parts, "; ")
}

// roleAttribution renders the referencing-profile hint appended to a failing
// backend check's detail — e.g. " (required by premium/architect, ...)".
// It is empty when there is no profile provenance to name (unprofiled
// default).
func roleAttribution(roles []string) string {
	if len(roles) == 0 {
		return ""
	}
	return " (required by " + strings.Join(roles, ", ") + ")"
}

func firstField(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return ""
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// envChecks emits one env:<name> check per configured prerequisite,
// probing it in dir. The result is advisory: PRESENT maps to ok, while
// ABSENT or errored maps to warn so that missing machinery never flips
// doctor's Ready bit.
func envChecks(cfg config.Config, dir string) []doctorCheck {
	if len(cfg.Env) == 0 {
		return nil
	}
	results := envprobe.Run(context.Background(), dir, cfg.Env)
	checks := make([]doctorCheck, 0, len(results))
	for _, r := range results {
		status := statusWarn
		if r.Present && r.Err == nil {
			status = statusOK
		}
		probe := cfg.Env[r.Name].Probe
		detail := fmt.Sprintf("%s — probe: %s — %s", r.Describe, probe, envprobe.StatusString(r))
		checks = append(checks, doctorCheck{Name: "env:" + r.Name, Status: status, Detail: detail})
	}
	return checks
}

// sandboxChecks emits one sandbox:<profile> check per defined profile. It
// resolves each profile the same way a run would — profile mode overrides
// the workspace default, which overrides the built-in warn — but with the
// static agent.CapabilitiesFor view rather than live adapters, so doctor
// never constructs an engine or backend. A profile that resolves to enforce
// with at least one coverage gap fails, naming every (backend, role) pair;
// everything else reports ok with the resolved mode.
func sandboxChecks(cfg config.Config, profiles config.Profiles) []doctorCheck {
	def := defaultBackendName()
	var checks []doctorCheck
	for name, prof := range profiles.Profiles {
		// expand omitted backends to the concrete default so the resolver
		// can look each up by name in the static capabilities view.
		resolved := make(config.Profile, len(prof))
		for role, rc := range prof {
			r := rc
			if r.Backend == "" {
				r.Backend = def
			}
			resolved[role] = r
		}
		caps := map[string]agent.Capabilities{}
		for _, rc := range resolved {
			if _, ok := caps[rc.Backend]; ok {
				continue
			}
			c, _ := agent.CapabilitiesFor(rc.Backend)
			caps[rc.Backend] = c
		}
		res := sandbox.Resolve(sandbox.Mode(cfg.Sandbox), sandbox.Mode(profiles.Sandboxes[name]), resolved, caps)
		detail := "mode=" + string(res.Mode)
		if res.Mode == sandbox.ModeEnforce && len(res.Gaps) > 0 {
			pairs := make([]string, 0, len(res.Gaps))
			for _, g := range res.Gaps {
				pairs = append(pairs, g.Backend+"/"+g.Role)
			}
			checks = append(checks, doctorCheck{
				Name:        "sandbox:" + name,
				Status:      statusFail,
				Detail:      detail + " — no tool coverage: " + strings.Join(pairs, ", "),
				Remediation: "route the flagged roles at a backend that reaches gummi's tools (ClientTools or MCPTools), or lower the profile's sandbox to warn/off",
			})
		} else {
			checks = append(checks, doctorCheck{Name: "sandbox:" + name, Status: statusOK, Detail: detail})
		}
		checks = append(checks, writeCageCheck(name, resolved, caps))
	}
	return checks
}

// writeCageCheck reports, per profile, how far each role's backend
// confines its own file writes — the fact an operator needs BEFORE
// routing a role, not after a run goes wrong.
//
// It exists because "sandbox: enforce" reads like a promise it does not
// make. That mode refuses backends without tool coverage and arms the
// main-checkout tripwire; it confines nothing. What actually keeps a
// role's writes in its worktree is the backend's own file-tool policy,
// and that varies: claude, opencode and zz pin their file tools to the
// session's working directory, while copilot, codex and headless are
// merely started there. A weaker model routed at the second tier is held
// by the prompt alone — which is how a reviewer once wrote a feature's
// files into the operator's main checkout.
//
// Warn, never fail: the cwd tier is a legitimate configuration under the
// tripwire, and this check's job is to make the choice visible, not to
// make it for the operator. And the caveat rides on every profile,
// including a fully caged one: NO backend confines shell commands.
func writeCageCheck(name string, resolved config.Profile, caps map[string]agent.Capabilities) doctorCheck {
	const shellNote = "no backend cages shell commands on any tier — the main-checkout tripwire is the only backstop there, and it kills a run rather than preventing the write"

	roles := make([]string, 0, len(resolved))
	for role := range resolved {
		roles = append(roles, role)
	}
	sort.Strings(roles)

	var uncaged []string
	for _, role := range roles {
		if caps[resolved[role].Backend].WriteCage != agent.WriteCagePaths {
			uncaged = append(uncaged, role+" ("+resolved[role].Backend+")")
		}
	}
	if len(uncaged) == 0 {
		return doctorCheck{
			Name:   "write-cage:" + name,
			Status: statusOK,
			Detail: "every role's file tools are caged to its worktree; " + shellNote,
		}
	}
	return doctorCheck{
		Name:   "write-cage:" + name,
		Status: statusWarn,
		Detail: "file tools uncaged for " + strings.Join(uncaged, ", ") + " — started in the worktree, free to write outside it; " + shellNote,
		Remediation: "route these roles at a backend that pins its file tools to the working directory (claude, opencode, zz) " +
			"if you want worktree discipline held by the harness rather than by the model",
	}
}

// guardedChecks emits one guarded:<profile> check per defined profile, but
// only when permissions: guarded is set — with allow-all there is nothing
// to check and this returns nil. Each profile fails when
// guardedIncompatibilities reports a mismatch scoped to it, naming the
// offending profile/role/backend triples; a profile with no mismatches
// reports ok. With no profiles configured at all, guardedIncompatibilities'
// fallback issue is keyed under the synthetic profile "(default)" rather
// than any name in profiles.Profiles, so the check set is built from the
// union of both — otherwise that fallback issue would have no profile to
// attach a check to and silently vanish from the report.
func guardedChecks(cfg config.Config, profiles config.Profiles) []doctorCheck {
	if !cfg.Guarded() {
		return nil
	}
	issues := guardedIncompatibilities(defaultBackendName(), profiles)
	byProfile := map[string][]guardedIncompatibility{}
	for _, i := range issues {
		byProfile[i.Profile] = append(byProfile[i.Profile], i)
	}
	seen := map[string]bool{}
	names := make([]string, 0, len(profiles.Profiles))
	for name := range profiles.Profiles {
		names = append(names, name)
		seen[name] = true
	}
	for name := range byProfile {
		if !seen[name] {
			names = append(names, name)
			seen[name] = true
		}
	}
	sort.Strings(names)
	var checks []doctorCheck
	for _, name := range names {
		if is := byProfile[name]; len(is) > 0 {
			checks = append(checks, doctorCheck{
				Name:        "guarded:" + name,
				Status:      statusFail,
				Detail:      formatGuardedIncompatibilities(is),
				Remediation: "route the flagged roles at a guarded-capable backend (copilot), or set permissions: allow-all",
			})
		} else {
			checks = append(checks, doctorCheck{Name: "guarded:" + name, Status: statusOK, Detail: "guarded permissions honored by every resolved backend"})
		}
	}
	return checks
}

// configLayeringChecks renders one line per top-level config field showing
// which file supplied the winning value, plus one existence check per
// configured instruction path so bad entries are visible rather than silently
// skipped.
func configLayeringChecks(cfg config.Config, sources map[string]string, userPath, workspacePath string) []doctorCheck {
	var checks []doctorCheck

	sourceLabel := func(src string) string {
		switch src {
		case "default":
			return "default"
		case userPath:
			return "user: " + userPath
		case workspacePath:
			return "workspace: " + workspacePath
		default:
			// Instructions concatenates both layers; render the combined
			// source as explicit user/workspace labels to match scalar fields.
			parts := strings.Split(src, ",")
			labels := make([]string, 0, len(parts))
			for _, p := range parts {
				switch p {
				case userPath:
					labels = append(labels, "user: "+p)
				case workspacePath:
					labels = append(labels, "workspace: "+p)
				default:
					labels = append(labels, "source: "+p)
				}
			}
			return strings.Join(labels, ", ")
		}
	}

	render := func(key string) {
		src := sources[key]
		if src == "" {
			src = "default"
		}
		var value string
		switch key {
		case "permissions":
			value = cfg.Permissions
		case "sandbox":
			value = cfg.Sandbox
		case "repo":
			value = cfg.Repo
		case "repos":
			value = strings.Join(sortedStringKeys(cfg.Repos), ", ")
		case "instructions":
			value = strings.Join(cfg.Instructions, ", ")
		}
		if value == "" {
			value = "(unset)"
		}
		detail := fmt.Sprintf("%s (%s)", value, sourceLabel(src))
		checks = append(checks, doctorCheck{Name: "config:" + key, Status: statusOK, Detail: detail})
	}

	render("permissions")
	render("sandbox")
	render("repo")
	render("repos")
	render("instructions")

	var envKeys []string
	for k := range cfg.Env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	for _, k := range envKeys {
		src := sources["env."+k]
		if src == "" {
			src = "default"
		}
		detail := cfg.Env[k].Describe
		if detail == "" {
			detail = cfg.Env[k].Probe
		}
		detail += " (" + sourceLabel(src) + ")"
		checks = append(checks, doctorCheck{Name: "config:env." + k, Status: statusOK, Detail: detail})
	}

	for _, inst := range cfg.Instructions {
		var status, detail string
		if _, err := os.Stat(inst); err != nil {
			status = statusFail
			detail = fmt.Sprintf("%s: %v", inst, err)
		} else {
			status = statusOK
			detail = inst + " exists"
		}
		detail += " (" + sourceLabel(sources["instructions"]) + ")"
		checks = append(checks, doctorCheck{Name: "config:instructions." + inst, Status: status, Detail: detail})
	}

	return checks
}

func sortedStringKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// forkDriftStatus reports whether any feature's recorded fork point is no
// longer an ancestor of its own repository's main HEAD. It is read-only and
// decoyDBCheck reports an empty .gummi/gummi.db sitting beside the real
// store at .gummi/state/gummi.db. gummi never creates that path — sqlite3
// does, silently, for anyone who types the shorter one:
//
//	sqlite3 .gummi/gummi.db "select ..."   # creates an empty db, returns nothing
//
// The query then succeeds with no rows, and the file stays behind to make
// the same promise to the next reader. It has already cost one session a
// wrong conclusion (per-stage spend read as absent when it was merely in
// the other file), which is exactly the failure a decoy produces: not an
// error, an answer that is quietly empty. Advisory, and never removed
// automatically — deleting a file that might be someone's own is not
// doctor's call.
func decoyDBCheck(ws state.Workspace) doctorCheck {
	decoy := filepath.Join(ws.GummiDir(), "gummi.db")
	ok := doctorCheck{Name: "decoy-db", Status: statusOK, Detail: "no stray database beside the state store"}
	fi, err := os.Lstat(decoy)
	if err != nil || fi.IsDir() {
		return ok
	}
	// A non-empty file at that path is somebody's real database, not the
	// accident this check is about; say so rather than calling it a decoy.
	what := "an empty"
	if fi.Size() > 0 {
		what = fmt.Sprintf("a %d-byte", fi.Size())
	}
	return doctorCheck{
		Name:   "decoy-db",
		Status: statusWarn,
		Detail: fmt.Sprintf("%s is %s file; the real state store is %s", decoy, what, ws.DBFile()),
		Remediation: fmt.Sprintf("a tool was pointed at the wrong path (sqlite3 creates the file it cannot find). "+
			"Query %s instead, and `rm %s` once you have checked it holds nothing you want", ws.DBFile(), decoy),
	}
}

// safe to run while a feature is live: it reads the store's fork points,
// resolves each card to its repository's manager, and runs local git
// ancestry checks against that repo only, never re-anchoring or backfilling.
// Each drifted card is listed with its repository name. It degrades to a
// quiet ok when the store or a main HEAD is unreadable, so doctor never
// fails on a workspace it cannot read.
func forkDriftStatus(ws state.Workspace, defaultRoot string, named []worktree.NamedRepo) doctorCheck {
	detail := "no drifted work items"
	ok := func() doctorCheck {
		return doctorCheck{Name: "fork-drift", Status: statusOK, Detail: detail}
	}
	// Read-only: never create a store that isn't there, so doctor running
	// against an un-initialized workspace writes nothing.
	if _, err := os.Stat(ws.DBFile()); err != nil {
		return ok()
	}
	st, err := state.OpenStore(ws.DBFile())
	if err != nil {
		return ok()
	}
	defer st.Close()
	// A read-only pool (no exclusion pass): one manager per configured repo,
	// so each card is probed against its own repository's main. The default
	// manager is created eagerly; named repos lazily on first card access.
	pool, err := worktree.NewPool(context.Background(), ws.Root, defaultRoot, named, st, false)
	if err != nil {
		return ok()
	}
	features, err := st.ListFeatures(context.Background())
	if err != nil {
		return ok()
	}
	var drifted []string
	for i := range features {
		f := &features[i]
		if f.ForkPoint == "" {
			continue
		}
		m, err := pool.ManagerFor(context.Background(), f)
		if err != nil {
			// a stored-but-unconfigured repository: name the card and the
			// missing repo so the operator knows where to look.
			drifted = append(drifted, fmt.Sprintf("%s (%s) [%s — not configured]", f.ID, f.BranchName(), repoLabel(f.Repo)))
			continue
		}
		head := exec.CommandContext(context.Background(), "git", "-C", m.RepoRoot(), "merge-base", "--is-ancestor", f.ForkPoint, "HEAD") //nolint:gosec // read-only ancestry probe against a validated repo root and a stored fork SHA
		if err := head.Run(); err != nil {
			// HEAD is either unreadable (degrade to ok, never a false
			// failure) or the fork is genuinely no longer an ancestor —
			// which needs the command to have started, so treat "not an
			// ancestor" (the process exit status path) as drift below and
			// anything else as unreadable.
			if isAncestorFailure(err) {
				drifted = append(drifted, fmt.Sprintf("%s (%s) [%s]", f.ID, f.BranchName(), repoLabel(f.Repo)))
			}
		}
	}
	if len(drifted) == 0 {
		return ok()
	}
	sort.Strings(drifted)
	return doctorCheck{
		Name:        "fork-drift",
		Status:      statusWarn,
		Detail:      "fork point is no longer in main's history for: " + strings.Join(drifted, ", "),
		Remediation: worktree.ForkDriftRemedy,
	}
}

// repoLabel renders a card's repository name for doctor's fork-drift
// report; the empty name (the workspace default) reads as "default".
func repoLabel(name string) string {
	if name == "" {
		return "default"
	}
	return name
}

// gitIdentityCheck resolves whether root can author a commit — a
// repo-local, global, or system user.name/user.email all count, since any
// of them lets a squash merge in this repo succeed. `git var
// GIT_AUTHOR_IDENT` performs the exact resolution git itself runs
// immediately before writing a commit (checking with git rather than
// reading a config file ourselves, so a scope this process doesn't know
// to look at — global, system, an include.path — still counts). A repo
// with no identity anywhere fails here exactly the way a real squash
// merge would — "*** Please tell me who you are." — but for free, before
// any stage has run or any credit has been spent, instead of at the last
// keystroke of a full run.
//
// A *git that ran* and said so (an *exec.ExitError) is a confirmed gap and
// fails readiness; anything else — git missing from PATH, unreadable
// worktree, some other launch failure — is a probe limitation doctor
// cannot tell apart from a healthy repo, and reads unknown rather than
// asserting a fail it did not actually establish (mirroring reach's own
// unknown-never-blocks rule).
func gitIdentityCheck(name, root string) doctorCheck {
	out, err := exec.CommandContext(context.Background(), "git", "-C", root, "var", "GIT_AUTHOR_IDENT").Output() //nolint:gosec // read-only identity probe against a validated repo root
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			return doctorCheck{
				Name: name, Status: statusUnknown,
				Detail: "could not probe git identity: " + err.Error(),
			}
		}
		detail := "git cannot resolve a commit identity"
		if line := lastNonEmptyLine(string(ee.Stderr)); line != "" {
			detail = line
		}
		return doctorCheck{
			Name:   name,
			Status: statusFail,
			Detail: detail,
			Remediation: fmt.Sprintf(
				"run `git -C %s config user.name \"Your Name\"` and `git -C %s config user.email you@example.com` (add --global to set it once for every repo)",
				root, root),
		}
	}
	return doctorCheck{
		Name:   name,
		Status: statusOK,
		Detail: "commits will be authored as " + identNameEmail(strings.TrimSpace(string(out))),
	}
}

// identNameEmail trims GIT_AUTHOR_IDENT's trailing "<epoch> <tz>" down to
// "Name <email>" for a readable check detail.
func identNameEmail(ident string) string {
	if i := strings.Index(ident, ">"); i >= 0 {
		return strings.TrimSpace(ident[:i+1])
	}
	return ident
}

// lastNonEmptyLine returns the last non-blank line of s, trimmed. git's own
// missing-identity error is a multi-line nudge ("*** Please tell me who you
// are." plus the two commands to run) ending in the one line — `fatal:
// empty ident name ...` — worth surfacing in a one-line check detail.
func lastNonEmptyLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return ""
}

// isGitRepoRoot reports whether dir looks like a git working-tree root:
// .git is either a directory (normal checkout) or a gitdir-pointer file
// (worktrees, submodules).
func isGitRepoRoot(dir string) bool {
	return isDir(filepath.Join(dir, ".git")) || isFile(filepath.Join(dir, ".git"))
}

// isAncestorFailure reports whether err means "not an ancestor" (git's
// --is-ancestor exit status 1) rather than "could not run / unreadable
// repo". Only the former is treated as drift; anything else degrades to a
// readable-but-quiet ok.
func isAncestorFailure(err error) bool {
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode() == 1
	}
	return false
}

// renderDoctor prints the human-readable checklist with a status glyph and
// an indented remediation line where present.
func renderDoctor(w io.Writer, r doctorReport) {
	head := "not ready"
	if r.Ready {
		head = "ready"
	}
	fmt.Fprintf(w, "gummi doctor — %s\n", head)
	for _, c := range r.Checks {
		fmt.Fprintf(w, "  %s %-10s %s\n", statusGlyph(c.Status), c.Name, c.Detail)
		if c.Remediation != "" && c.Status != statusOK {
			fmt.Fprintf(w, "      → %s\n", c.Remediation)
		}
	}
}

func statusGlyph(status string) string {
	switch status {
	case statusOK:
		return "✓"
	case statusWarn:
		return "!"
	case statusFail:
		return "✗"
	default:
		return "?"
	}
}
