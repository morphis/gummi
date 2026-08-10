package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// runDoctor implements `gummi doctor [--json]` (DESIGN §5, G1): a read-only
// readiness checklist — repo, workspace, backend, profile, auth, envelope,
// lock — that the skill's first-run setup flow consumes via --json. It
// constructs no engine/agent and holds no lock beyond a momentary probe, so
// it is safe to run while a feature is live. It reports; it never repairs
// auth or writes secrets (G2/G4).
func runDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	jsonOut := registerDoctorFlags(fs)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: gummi doctor [--json]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	report := buildDoctorReport(cwd)
	if *jsonOut {
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

// registerDoctorFlags binds `gummi doctor`'s only flag, so the skill's
// grammar generator can enumerate it (see runFlagValues).
func registerDoctorFlags(fs *flag.FlagSet) *bool {
	return fs.Bool("json", false, "emit the readiness checklist as JSON (the skill's setup path)")
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

// buildDoctorReport assembles the checklist from the environment, the
// workspace, and the filesystem — no engine, no network (auth is probed
// offline; interactive-login backends degrade to "unknown"). It is the
// testable core: tests call it with a temp repo root and env.
func buildDoctorReport(cwd string) doctorReport {
	var checks []doctorCheck
	add := func(name, status, detail, remediation string) {
		checks = append(checks, doctorCheck{Name: name, Status: status, Detail: detail, Remediation: remediation})
	}

	// 1. repo
	if isDir(filepath.Join(cwd, ".git")) || isFile(filepath.Join(cwd, ".git")) {
		add("repo", statusOK, "git repository at "+cwd, "")
	} else {
		add("repo", statusFail, cwd+" is not a git repository", "run `git init` — gummi manages worktrees and must run from the repo root")
	}

	// 2. workspace
	ws, wsErr := state.Open(cwd)
	if wsErr == nil {
		add("workspace", statusOK, ".gummi workspace present", "")
	} else {
		ws = state.Workspace{Root: cwd} // path-only fallback for later checks
		add("workspace", statusWarn, "no .gummi workspace yet", "created automatically on the first `gummi run` (or `gummi` TUI)")
	}

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

	// 5. auth (offline) — one line per required backend, pairing with the
	// backend:* checks above.
	for _, name := range ordered {
		checks = append(checks, authCheck(backendInfoFor(name)))
	}

	// 6. envelope
	checks = append(checks, envelopeCheck())

	// 7. lock (only meaningful once a workspace exists)
	if wsErr == nil {
		checks = append(checks, lockCheck(ws))
	} else {
		add("lock", statusOK, "n/a — no workspace to lock yet", "")
	}

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

// authCheck reports auth readiness without spawning anything (confirmed
// offline). Provider config lives in each backend's native store now
// (Claude Code login, `opencode auth`, headless child's env), so an
// interactive-login backend degrades to "unknown" with the exact command
// a human runs (G2); a headless backend delegates to its own child.
func authCheck(bi backendInfo) doctorCheck {
	if bi.headless {
		return doctorCheck{Name: "auth:" + bi.name, Status: statusOK, Detail: "handled by the headless command (" + bi.bin + ")"}
	}
	return doctorCheck{
		Name: "auth:" + bi.name, Status: statusUnknown, Detail: bi.name + " auth state is not checked offline",
		Remediation: "if runs fail on auth, have the human run: " + bi.login,
	}
}

// envelopeCheck validates GUMMI_ENVELOPE. It never fails readiness: a run
// can still take --envelope N, and the run itself enforces the requirement.
func envelopeCheck() doctorCheck {
	v := strings.TrimSpace(os.Getenv("GUMMI_ENVELOPE"))
	if v == "" {
		return doctorCheck{
			Name: "envelope", Status: statusWarn, Detail: "GUMMI_ENVELOPE is unset",
			Remediation: "pass --envelope N per run, or export GUMMI_ENVELOPE=<credits> (runs refuse to start without one)",
		}
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return doctorCheck{
			Name: "envelope", Status: statusWarn, Detail: "GUMMI_ENVELOPE=" + v + " is not a positive integer",
			Remediation: "set GUMMI_ENVELOPE to a positive credit count",
		}
	}
	if float64(n) < domain.TurnReserveCredits {
		return doctorCheck{
			Name: "envelope", Status: statusWarn,
			Detail:      fmt.Sprintf("GUMMI_ENVELOPE=%d is below one agent turn (~%d credits)", n, int(domain.TurnReserveCredits)),
			Remediation: "raise it so stage budgets aren't floored at a single turn and overshoot the envelope",
		}
	}
	return doctorCheck{Name: "envelope", Status: statusOK, Detail: fmt.Sprintf("envelope: %d credits", n)}
}

// lockCheck probes the workspace's exclusive lock and releases it
// immediately — reporting "busy" when a TUI or another run holds it (D13),
// so a caller learns a run would refuse before starting one.
func lockCheck(ws state.Workspace) doctorCheck {
	release, err := state.AcquireLock(ws.LockFile())
	switch {
	case errors.Is(err, state.ErrLocked):
		return doctorCheck{
			Name: "lock", Status: statusWarn, Detail: "workspace busy — a TUI or another run holds it",
			Remediation: "close the other gummi process before running",
		}
	case err != nil:
		return doctorCheck{Name: "lock", Status: statusWarn, Detail: "could not probe the workspace lock: " + err.Error()}
	default:
		release()
		return doctorCheck{Name: "lock", Status: statusOK, Detail: "workspace is free"}
	}
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
