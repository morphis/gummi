// Package config loads .gummi/config.yaml — the repo-controlled gummi
// settings. Since M5 this is only the permission mode: the verify-stage
// check commands live in each feature's spec as a gummi-checks block
// (auto-discovered at approval), not in static config.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/hooks"
)

// Config is the parsed .gummi/config.yaml.
type Config struct {
	// Permissions is "allow-all" (default) or "guarded" (DESIGN §4.4).
	Permissions string `yaml:"permissions"`
	// Sandbox is the workspace-wide default for the tool-coverage
	// refusal: "enforce", "warn", or "off". Empty means unset — profiles
	// that omit their own value fall back to the built-in "warn". Only
	// "enforce" does anything; warn and off both let a run start. It does
	// NOT confine writes: what keeps a role's writes inside its worktree
	// is the backend's own file-tool policy (agent.WriteCage), and no
	// backend confines the shell at all. See DESIGN §4.4 for what each
	// layer actually guarantees.
	Sandbox string `yaml:"sandbox"`
	// AutopilotLanes caps how many autopilot-pool cards — a card whose
	// gate-approval mode is domain.GateAutopilot, and ONLY that mode — can
	// drive at once (internal/engine's autopilot pool). 0 or unset means
	// the built-in default of 2; a negative value is rejected by Load.
	//
	// The ATTENDED pool is everything else, the empty default included
	// (engine.lanePoolFor resolves the field through
	// domain.Feature.GateMode, where empty reads as domain.GateAttended),
	// which makes it the pool every ordinary card competes in. It is sized
	// separately: it defaults to 1 and is overridden by GUMMI_MAX_ACTIVE,
	// not by this key — a human is expected to stay with an attended card,
	// so it must never queue behind autopilot work.
	//
	// This comment used to say the autopilot pool held both modes
	// "including the empty default", contradicting its own next sentence.
	// It described the classification from before lanePoolFor went through
	// GateMode, when an unset field pooled as autopilot.
	AutopilotLanes int `yaml:"autopilot_lanes"`
	// Repo is the git repository root gummi manages, when it is not the
	// workspace root. Empty = the workspace root (the sibling layout, where
	// .gummi and .git share a directory). A nested repo is named relative
	// to the workspace root (e.g. "git/lxd").
	Repo string `yaml:"repo"`
	// Repos maps a selectable name to a git repository path relative to
	// the workspace root. Cards may name any of these; the empty name means
	// the default repo when one is set (Repo), and is invalid when Repo is
	// absent and Repos is set. Each value must resolve inside the workspace
	// and be a git toplevel (enforced by ResolveRepos).
	Repos map[string]string `yaml:"repos"`
	// Env is the operator-configured environment prerequisite map. Each
	// entry names a prerequisite that may be referenced by [env: <name>]
	// tags in a verification plan; gummi probes each entry's Probe command
	// at Verify kickoff and in `gummi doctor`.
	Env map[string]EnvPrereq `yaml:"env"`
	// Substrates names the external environments work here is proved on —
	// a test cluster, a device farm, a staging account: scarce, slow,
	// stateful and shared, which an env prerequisite is not. A substrate
	// can be provisioned and reset as well as probed, it expires, and only
	// one job holds it at a time. Its name is citable from a verification
	// plan exactly as an env prerequisite's is ([env: <name>]).
	//
	// Like env probes these are operator config from outside every
	// worktree, and that ownership is the point: the commands that decide
	// whether work is proven must not be editable by the work.
	Substrates map[string]Substrate `yaml:"substrates"`
	// Experiments names the orchestrated live runs that prove work on a
	// substrate: deploy what was built, wait for it to settle, probe it,
	// collect what a reader will want to see. A goal's done-when item names
	// one as its means of proof (`experiment:`), beside `check:` and
	// `judge:`. Operator config for the same reason substrates are — and
	// here it is the whole point: a goal may change the rig it is tested
	// on, and must not thereby change what counts as passing.
	Experiments map[string]Experiment `yaml:"experiments"`
	// Instructions is a list of extra instruction-file paths that are
	// appended to the workspace environment card, in user-then-workspace
	// order. Every entry must be an absolute path; Load rejects relative or
	// empty entries so a path cannot silently walk out of the workspace.
	Instructions []string `yaml:"instructions"`
	// Skills selects which of the workspace's own skills are forwarded
	// into the sessions that run inside a card's worktree. See
	// SkillsConfig for why this is an explicit list rather than a
	// directory that is swept.
	Skills SkillsConfig `yaml:"skills"`
	// Checks supplies workspace-wide default verification checks. When
	// Checks.Default is non-empty, check discovery bypasses the scribe and
	// writes the configured list straight into the artifact.
	Checks ChecksConfig `yaml:"checks"`
	// Hooks are the scripts run on board events (the notification surface
	// beside GUMMI_NOTIFY's bell/desktop). Each entry runs on every event,
	// or on the events its filter names; see internal/hooks for the
	// vocabulary and the advisory contract. User-level and workspace
	// entries both run — a personal pager beside a workspace's own
	// pipeline — in that order.
	Hooks []hooks.Hook `yaml:"hooks"`
}

// SkillsConfig selects workspace-level skills to forward into card
// sessions.
//
// Every card runs in a worktree under <workspace>/.gummi/worktrees, which
// is a sibling of the managed repository rather than a directory inside
// it. A skill the repository carries is in that worktree and every backend
// finds it; a skill the OPERATOR keeps at the workspace root — beside
// .gummi, which in the multi-repo layout (`repos:`) is the only place
// cross-repo rules can live — is outside every backend's project scope and
// reaches nothing. Forwarding is how such a skill gets in.
//
// It is an explicit list, never "forward everything under .claude/skills",
// for a specific reason: `gummi skill install --scope project` writes
// gummi's OWN skill to that directory. Sweeping it would hand every stage
// session the instructions for driving gummi, against the rule (stated to
// every session in the stage hints) that a card never spawns a second
// gummi. Naming what to forward makes that impossible by construction.
type SkillsConfig struct {
	// Forward names the skills to forward. An entry is either a bare
	// skill name, resolved against the workspace's conventional skill
	// roots (.claude/skills, .agents/skills, .github/skills, in that
	// order), or an absolute path to a skill directory anywhere on disk.
	// A relative path is rejected: it would silently mean something
	// different depending on which directory gummi was started from.
	Forward []string `yaml:"forward"`
}

// ChecksConfig holds workspace-wide check settings.
type ChecksConfig struct {
	// Default is a list of checks used in place of scribe discovery.
	Default []domain.Check `yaml:"default"`
}

// EnvPrereq is one operator-configured environment prerequisite.
type EnvPrereq struct {
	// Probe is the shell command that decides whether the prerequisite is
	// present. It runs via `sh -c` in the card's worktree. A clean exit 0
	// means PRESENT; a clean non-zero exit (other than the shell's 126/127
	// "not executable"/"command not found" codes) means ABSENT.
	Probe string `yaml:"probe"`
	// Describe is a short human-readable description of the prerequisite,
	// included in kickoff reports and `gummi doctor` output.
	Describe string `yaml:"describe"`
}

// Substrate is one operator-configured external environment.
type Substrate struct {
	// Describe is a short human-readable description.
	Describe string `yaml:"describe"`
	// Probe decides whether the substrate is ready to take a job. It runs
	// via `sh -c` in the workspace root and is classified as an env probe
	// is: clean exit 0 READY, clean non-zero ABSENT, anything else BROKEN.
	Probe string `yaml:"probe"`
	// Provision brings the substrate up from nothing. Optional: without it
	// an absent substrate can only be waited for.
	Provision string `yaml:"provision"`
	// Reset returns a provisioned substrate to its known state — the
	// snapshot a job expects to start from. Optional.
	Reset string `yaml:"reset"`
	// TTL is how long a provisioned substrate lives (a Go duration, e.g.
	// "24h"). Empty means it does not expire. gummi never starts a job
	// that cannot finish before the substrate expires.
	TTL string `yaml:"ttl"`
	// Timeout bounds one provision or reset (a Go duration). Empty means
	// DefaultSubstrateOpTimeout.
	Timeout string `yaml:"timeout"`
}

// DefaultSubstrateOpTimeout bounds a provision or reset whose substrate
// names no timeout. Bringing machines up is slow by nature — cloud-init
// alone runs a quarter of an hour — so the default is generous and the
// ceiling is what stops a hung command holding the lease for ever.
const DefaultSubstrateOpTimeout = 45 * time.Minute

// MaxSubstrateOpTimeout is the ceiling for a configured substrate timeout.
const MaxSubstrateOpTimeout = 6 * time.Hour

// OpTimeout resolves the substrate's provision/reset bound.
func (s Substrate) OpTimeout() time.Duration {
	if d, err := time.ParseDuration(s.Timeout); err == nil && d > 0 {
		return d
	}
	return DefaultSubstrateOpTimeout
}

// Lifetime resolves the substrate's TTL; zero means it does not expire.
func (s Substrate) Lifetime() time.Duration {
	d, _ := time.ParseDuration(s.TTL)
	return d
}

// Experiment is one operator-configured live run. Every command runs via
// `sh -c` in the workspace root with the run's environment: GUMMI_EVIDENCE
// (a directory to write into), GUMMI_TREE_<REPO> and GUMMI_HEAD_<REPO> for
// each input, GUMMI_SUBSTRATE, GUMMI_EXPERIMENT, GUMMI_RUN, GUMMI_PURPOSE
// and GUMMI_ATTEMPT. A command that exits 75 (EX_TEMPFAIL) is saying the
// run could not be judged, whatever phase it is in.
type Experiment struct {
	Describe string `yaml:"describe"`
	// Substrate is the substrate the run needs, exclusively, for as long
	// as it takes.
	Substrate string `yaml:"substrate"`
	// Inputs names the repositories whose state the run is about. The
	// evidence a run leaves is evidence about exactly these heads, and is
	// stale the moment one of them moves. Empty means every repository the
	// goal has a tree in.
	Inputs []string `yaml:"inputs"`
	// Control proves the rig against its own reference, on a freshly reset
	// substrate, before anything of the goal's is deployed. A rig that
	// cannot pass it is not a judge, and says nothing about the work.
	Control string `yaml:"control"`
	// Deploy puts the inputs onto the substrate.
	Deploy string `yaml:"deploy"`
	// Settle waits until the deployed system is worth probing.
	Settle string `yaml:"settle"`
	// Run is the experiment itself. It may write one JSON object per line
	// to $GUMMI_EVIDENCE/results.ndjson — {"id","ok","detail"} — so that a
	// done-when item can be about some of its assertions and a reader can
	// see which moved.
	Run string `yaml:"run"`
	// Collect gathers what a reviewer will want — dumps, tables, traces —
	// into $GUMMI_EVIDENCE. It runs after every attempt, pass or fail.
	Collect string `yaml:"collect"`
	// Timeout bounds each phase (a Go duration). Empty means
	// DefaultExperimentPhaseTimeout.
	Timeout string `yaml:"timeout"`
}

// DefaultExperimentPhaseTimeout bounds a phase whose experiment names none.
const DefaultExperimentPhaseTimeout = 30 * time.Minute

// PhaseTimeout resolves the experiment's per-phase bound.
func (x Experiment) PhaseTimeout() time.Duration {
	if d, err := time.ParseDuration(x.Timeout); err == nil && d > 0 {
		return d
	}
	return DefaultExperimentPhaseTimeout
}

// Phases lists the experiment's configured phases in the order they run.
func (x Experiment) Phases() [][2]string {
	var out [][2]string
	for _, p := range [][2]string{{"deploy", x.Deploy}, {"settle", x.Settle}, {"run", x.Run}} {
		if strings.TrimSpace(p[1]) != "" {
			out = append(out, p)
		}
	}
	return out
}

// Longest is the longest one attempt can take: every phase at its bound.
// It is what a substrate's remaining lifetime is measured against.
func (x Experiment) Longest() time.Duration {
	n := len(x.Phases()) + 1 // and the reset before it
	if strings.TrimSpace(x.Control) != "" {
		n++
	}
	if strings.TrimSpace(x.Collect) != "" {
		n++
	}
	return time.Duration(n) * x.PhaseTimeout()
}

func validateExperiments(path string, c Config) error {
	for name, x := range c.Experiments {
		if err := validateEnvName(path, "experiments", name); err != nil {
			return err
		}
		if strings.TrimSpace(x.Run) == "" {
			return fmt.Errorf("%s: experiments: %q has no run command", path, name)
		}
		if strings.TrimSpace(x.Substrate) == "" {
			return fmt.Errorf("%s: experiments: %q names no substrate", path, name)
		}
		if x.Timeout != "" {
			d, err := time.ParseDuration(x.Timeout)
			if err != nil || d <= 0 {
				return fmt.Errorf("%s: experiments: %q has an invalid timeout %q", path, name, x.Timeout)
			}
			if d > MaxSubstrateOpTimeout {
				return fmt.Errorf("%s: experiments: %q timeout %s exceeds maximum %s", path, name, d, MaxSubstrateOpTimeout)
			}
		}
	}
	return nil
}

func validateSubstrates(path string, c Config) error {
	for name, sub := range c.Substrates {
		if err := validateEnvName(path, "substrates", name); err != nil {
			return err
		}
		if _, dup := c.Env[name]; dup {
			return fmt.Errorf("%s: substrates: %q is also an env prerequisite; a name cited as [env: %s] must mean one thing", path, name, name)
		}
		if strings.TrimSpace(sub.Probe) == "" {
			return fmt.Errorf("%s: substrates: %q has an empty probe", path, name)
		}
		if sub.TTL != "" {
			if d, err := time.ParseDuration(sub.TTL); err != nil || d <= 0 {
				return fmt.Errorf("%s: substrates: %q has an invalid ttl %q", path, name, sub.TTL)
			}
		}
		if sub.Timeout != "" {
			d, err := time.ParseDuration(sub.Timeout)
			if err != nil || d <= 0 {
				return fmt.Errorf("%s: substrates: %q has an invalid timeout %q", path, name, sub.Timeout)
			}
			if d > MaxSubstrateOpTimeout {
				return fmt.Errorf("%s: substrates: %q timeout %s exceeds maximum %s", path, name, d, MaxSubstrateOpTimeout)
			}
		}
	}
	return nil
}

// validateEnvName holds a name that may appear inside an [env: <name>] tag
// to what that tag can carry.
func validateEnvName(path, section, name string) error {
	if name == "" {
		return fmt.Errorf("%s: %s: entry has an empty name", path, section)
	}
	if strings.ContainsAny(name, "]") || strings.ContainsAny(name, " \t\n\r") {
		return fmt.Errorf("%s: %s: name %q contains a ']' or whitespace character", path, section, name)
	}
	return nil
}

// EnvProbes is every name a verification plan may cite with [env: <name>]
// and the command that probes it: the env prerequisites, and each
// substrate under its own name. A substrate is an environment
// prerequisite that can also be brought up; to a plan asking "is it
// there" they are the same question.
func (c Config) EnvProbes() map[string]EnvPrereq {
	out := make(map[string]EnvPrereq, len(c.Env)+len(c.Substrates))
	for k, v := range c.Env {
		out[k] = v
	}
	for k, v := range c.Substrates {
		out[k] = EnvPrereq{Probe: v.Probe, Describe: v.Describe}
	}
	return out
}

// Load reads and parses config.yaml. A missing file yields the default
// (allow-all) config, not an error.
func Load(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("reading %s: %w", path, err)
	}
	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return Config{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	switch c.Permissions {
	case "", "allow-all", "guarded":
	default:
		return Config{}, fmt.Errorf("%s: permissions must be \"allow-all\" or \"guarded\", got %q", path, c.Permissions)
	}
	switch c.Sandbox {
	case "", "enforce", "warn", "off":
	default:
		return Config{}, fmt.Errorf("%s: sandbox must be \"enforce\", \"warn\", or \"off\", got %q", path, c.Sandbox)
	}
	if c.AutopilotLanes < 0 {
		return Config{}, fmt.Errorf("%s: autopilot_lanes must be >= 0, got %d", path, c.AutopilotLanes)
	}
	for name, p := range c.Env {
		if err := validateEnvName(path, "env", name); err != nil {
			return Config{}, err
		}
		if strings.TrimSpace(p.Probe) == "" {
			return Config{}, fmt.Errorf("%s: env: %q has an empty probe", path, name)
		}
	}
	if err := validateSubstrates(path, c); err != nil {
		return Config{}, err
	}
	if err := validateExperiments(path, c); err != nil {
		return Config{}, err
	}
	for i, inst := range c.Instructions {
		if inst == "" {
			return Config{}, fmt.Errorf("%s: instructions: entry %d is empty", path, i)
		}
		if !filepath.IsAbs(inst) {
			return Config{}, fmt.Errorf("%s: instructions: entry %q is not an absolute path", path, inst)
		}
	}
	for i, name := range c.Skills.Forward {
		if strings.TrimSpace(name) == "" {
			return Config{}, fmt.Errorf("%s: skills.forward: entry %d is empty", path, i)
		}
		// A bare name is resolved later against the workspace's skill
		// roots; an absolute path is taken as given. A relative path with
		// separators is neither, so it is refused here rather than
		// resolving against whatever directory gummi was launched in.
		if strings.ContainsRune(name, '/') && !filepath.IsAbs(name) {
			return Config{}, fmt.Errorf("%s: skills.forward: entry %q is a relative path; use a bare skill name or an absolute path", path, name)
		}
	}
	for i, ch := range c.Checks.Default {
		if strings.TrimSpace(ch.Cmd) == "" {
			return Config{}, fmt.Errorf("%s: checks.default: entry %d has an empty cmd", path, i)
		}
		if err := validateCheckTimeout(ch); err != nil {
			return Config{}, fmt.Errorf("%s: checks.default: entry %d: %w", path, i, err)
		}
	}
	for i, h := range c.Hooks {
		if strings.TrimSpace(h.Run) == "" {
			return Config{}, fmt.Errorf("%s: hooks: entry %d has an empty run", path, i)
		}
		for _, ev := range h.Events {
			if !hooks.ValidEvent(ev) {
				return Config{}, fmt.Errorf("%s: hooks: entry %d names unknown event %q (known: %s)",
					path, i, ev, strings.Join(hooks.AllEvents, ", "))
			}
		}
	}
	return c, nil
}

// validateCheckTimeout mirrors the same validation in internal/spec so the
// config loader rejects per-check timeouts that would fail later parsing.
func validateCheckTimeout(c domain.Check) error {
	if c.Timeout == "" {
		return nil
	}
	d, err := time.ParseDuration(c.Timeout)
	if err != nil {
		return fmt.Errorf("check %q: invalid timeout %q: %w", c.Name, c.Timeout, err)
	}
	const maxCheckTimeout = 30 * time.Minute
	if d > maxCheckTimeout {
		return fmt.Errorf("check %q: timeout %s exceeds maximum %s", c.Name, d, maxCheckTimeout)
	}
	return nil
}

// Guarded reports whether the config selects guarded permission mode
// (agents' tool calls require approval). The default (empty or "allow-all")
// is not guarded.
func (c Config) Guarded() bool { return c.Permissions == "guarded" }

// UserConfigPath returns the path to the user-level gummi config file:
// $XDG_CONFIG_HOME/gummi/config.yaml when XDG_CONFIG_HOME is set and
// non-empty, otherwise ~/.config/gummi/config.yaml. It returns an error only
// when neither XDG_CONFIG_HOME nor os.UserHomeDir() can produce a path.
func UserConfigPath() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "gummi", "config.yaml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving user config path: %w", err)
	}
	return filepath.Join(home, ".config", "gummi", "config.yaml"), nil
}

// LoadLayered loads the user-level and workspace config files and returns a
// merged Config plus a source map describing which file supplied each value.
// A missing user config is treated as an empty Config. The returned map has
// one entry per top-level field: "permissions", "sandbox", "autopilot_lanes",
// "agent", "repo", "repos", "instructions", "skills", and "env.<name>" for each
// distinct env key. Scalar fields that are unset in both files use the
// literal "default". Instructions list both contributing paths when both
// files supply entries.
func LoadLayered(userPath, workspacePath string) (Config, map[string]string, error) {
	user, err := Load(userPath)
	if err != nil {
		return Config{}, nil, err
	}
	ws, err := Load(workspacePath)
	if err != nil {
		return Config{}, nil, err
	}
	merged, sources, err := merge(user, ws, userPath, workspacePath)
	if err != nil {
		return Config{}, nil, err
	}
	return merged, sources, nil
}

// merge applies the per-field layering rules and returns the merged Config
// alongside a source map for doctor to render. Repo/Repos are workspace-only:
// if the user file sets either, merge returns an error.
func merge(user, ws Config, userPath, workspacePath string) (Config, map[string]string, error) {
	if user.Repo != "" {
		return Config{}, nil, fmt.Errorf("%s: repo is workspace-only and cannot be set in the user config", userPath)
	}
	if len(user.Repos) > 0 {
		return Config{}, nil, fmt.Errorf("%s: repos is workspace-only and cannot be set in the user config", userPath)
	}

	sources := map[string]string{}
	var merged Config

	if ws.Permissions != "" {
		merged.Permissions = ws.Permissions
		sources["permissions"] = workspacePath
	} else if user.Permissions != "" {
		merged.Permissions = user.Permissions
		sources["permissions"] = userPath
	} else {
		sources["permissions"] = "default"
	}

	if ws.Sandbox != "" {
		merged.Sandbox = ws.Sandbox
		sources["sandbox"] = workspacePath
	} else if user.Sandbox != "" {
		merged.Sandbox = user.Sandbox
		sources["sandbox"] = userPath
	} else {
		sources["sandbox"] = "default"
	}

	if ws.AutopilotLanes != 0 {
		merged.AutopilotLanes = ws.AutopilotLanes
		sources["autopilot_lanes"] = workspacePath
	} else if user.AutopilotLanes != 0 {
		merged.AutopilotLanes = user.AutopilotLanes
		sources["autopilot_lanes"] = userPath
	} else {
		sources["autopilot_lanes"] = "default"
	}

	if ws.Repo != "" {
		merged.Repo = ws.Repo
		sources["repo"] = workspacePath
	} else {
		sources["repo"] = "default"
	}

	if len(ws.Repos) > 0 {
		merged.Repos = ws.Repos
		sources["repos"] = workspacePath
	} else {
		sources["repos"] = "default"
	}

	merged.Env = make(map[string]EnvPrereq, len(user.Env)+len(ws.Env))
	for k, v := range user.Env {
		merged.Env[k] = v
		sources["env."+k] = userPath
	}
	for k, v := range ws.Env {
		merged.Env[k] = v
		sources["env."+k] = workspacePath
	}

	// Substrates layer as env does: a machine an operator can reach from
	// every workspace may live in the user file, and a workspace entry of
	// the same name replaces it whole.
	merged.Substrates = make(map[string]Substrate, len(user.Substrates)+len(ws.Substrates))
	for k, v := range user.Substrates {
		merged.Substrates[k] = v
		sources["substrates."+k] = userPath
	}
	for k, v := range ws.Substrates {
		merged.Substrates[k] = v
		sources["substrates."+k] = workspacePath
	}
	for name := range merged.Substrates {
		if _, dup := merged.Env[name]; dup {
			return Config{}, nil, fmt.Errorf("substrates: %q is also an env prerequisite (%s); a name cited as [env: %s] must mean one thing", name, sources["env."+name], name)
		}
	}

	merged.Experiments = make(map[string]Experiment, len(user.Experiments)+len(ws.Experiments))
	for k, v := range user.Experiments {
		merged.Experiments[k] = v
		sources["experiments."+k] = userPath
	}
	for k, v := range ws.Experiments {
		merged.Experiments[k] = v
		sources["experiments."+k] = workspacePath
	}
	// Checked on the merged view, not per file: an experiment in the
	// workspace file may well run on a substrate the user file describes.
	for name, x := range merged.Experiments {
		if _, ok := merged.Substrates[x.Substrate]; !ok {
			return Config{}, nil, fmt.Errorf("experiments: %q runs on substrate %q, which is not configured", name, x.Substrate)
		}
	}

	merged.Instructions = make([]string, 0, len(user.Instructions)+len(ws.Instructions))
	merged.Instructions = append(merged.Instructions, user.Instructions...)
	merged.Instructions = append(merged.Instructions, ws.Instructions...)
	switch {
	case len(user.Instructions) > 0 && len(ws.Instructions) > 0:
		sources["instructions"] = userPath + "," + workspacePath
	case len(user.Instructions) > 0:
		sources["instructions"] = userPath
	case len(ws.Instructions) > 0:
		sources["instructions"] = workspacePath
	default:
		sources["instructions"] = "default"
	}

	// skills.forward layers like instructions: both levels contribute, user
	// first. A personal skill an operator wants in every workspace and a
	// skill this workspace defines are both legitimate, and neither should
	// silence the other.
	merged.Skills.Forward = make([]string, 0, len(user.Skills.Forward)+len(ws.Skills.Forward))
	merged.Skills.Forward = append(merged.Skills.Forward, user.Skills.Forward...)
	merged.Skills.Forward = append(merged.Skills.Forward, ws.Skills.Forward...)
	switch {
	case len(user.Skills.Forward) > 0 && len(ws.Skills.Forward) > 0:
		sources["skills"] = userPath + "," + workspacePath
	case len(user.Skills.Forward) > 0:
		sources["skills"] = userPath
	case len(ws.Skills.Forward) > 0:
		sources["skills"] = workspacePath
	default:
		sources["skills"] = "default"
	}

	// checks.default is layered like permissions/sandbox: a workspace list
	// takes precedence, and a user-level list is the fallback.
	if len(ws.Checks.Default) > 0 {
		merged.Checks.Default = ws.Checks.Default
		sources["checks.default"] = workspacePath
	} else if len(user.Checks.Default) > 0 {
		merged.Checks.Default = user.Checks.Default
		sources["checks.default"] = userPath
	} else {
		sources["checks.default"] = "default"
	}

	// hooks layer like instructions: user entries then workspace entries,
	// both running — a personal notification script beside the workspace's
	// own pipeline is not a conflict to resolve but two hooks to run.
	merged.Hooks = make([]hooks.Hook, 0, len(user.Hooks)+len(ws.Hooks))
	merged.Hooks = append(merged.Hooks, user.Hooks...)
	merged.Hooks = append(merged.Hooks, ws.Hooks...)
	switch {
	case len(user.Hooks) > 0 && len(ws.Hooks) > 0:
		sources["hooks"] = userPath + "," + workspacePath
	case len(user.Hooks) > 0:
		sources["hooks"] = userPath
	case len(ws.Hooks) > 0:
		sources["hooks"] = workspacePath
	default:
		sources["hooks"] = "default"
	}

	return merged, sources, nil
}

// NamedRepo is one selectable named repository: a configured name and its
// resolved absolute root (joined against the workspace root).
type NamedRepo struct {
	Name string
	Root string
}

// ResolveRepos resolves the workspace's selectable repository set from ws
// (the workspace root) and the config. It returns the default repo root
// (the `repo:` key when set) and the ordered list of named repositories
// (sorted by name, deterministic). Every configured root must resolve to a
// directory inside the workspace and be the top of a git repository; a
// violation is a resolution-time error naming the offending repo.
//
// The default root is empty when `repos:` is configured with no `repo:` key:
// there is no default at all, so a card must name one of the configured
// repos and the workspace root is never validated as a git toplevel (in the
// natural multi-repo layout it is a parent directory of checkouts, not a
// repo itself). An absent `repo:`/`repos:` yields exactly the workspace
// root as the sole (default) repository, so upgrading needs no config edit.
func ResolveRepos(ws string, c Config) (defaultRoot string, named []NamedRepo, err error) {
	// Setting both `repo:` and `repos:` is a config error: they are two
	// ways to define the default repository, and composing them would need
	// rules for whether (and under what name) the `repo:` path joins the
	// selectable set. Erroring keeps one source of truth and fails at load
	// rather than letting the default repository shift silently.
	if c.Repo != "" && len(c.Repos) > 0 {
		return "", nil, fmt.Errorf("config error: set either `repo:` or `repos:`, not both (got repo:%q and repos:{%s})", c.Repo, strings.Join(sortedKeys(c.Repos), ", "))
	}
	ws, err = filepath.Abs(ws)
	if err != nil {
		return "", nil, err
	}
	resolve := func(rel string) (string, error) {
		var root string
		if rel == "" {
			root = ws
		} else if filepath.IsAbs(rel) {
			root = filepath.Clean(rel)
		} else {
			root = filepath.Clean(filepath.Join(ws, rel))
		}
		if !withinWorkspace(ws, root) {
			return "", fmt.Errorf("config error: repo %q escapes the workspace %s; every managed repository must be the workspace root or a subdirectory of it", rel, ws)
		}
		if !isGitRoot(root) {
			return "", fmt.Errorf("config error: repo %q at %s is not the root of a git repository; configure a git toplevel inside the workspace", rel, root)
		}
		return root, nil
	}
	if c.Repo != "" {
		defaultRoot, err = resolve(c.Repo)
		if err != nil {
			return "", nil, err
		}
	} else if len(c.Repos) == 0 {
		// No repo: and no repos: — the workspace root is the sole default,
		// so a single-repo upgrade needs no config edit.
		defaultRoot, err = resolve("")
		if err != nil {
			return "", nil, err
		}
	}
	// repos: configured with no repo: — no default at all: a card must name
	// a configured repo, and the workspace root (a mere parent of checkouts
	// in the multi-repo layout) is never validated as a git toplevel.
	names := make([]string, 0, len(c.Repos))
	for name := range c.Repos {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		root, rerr := resolve(c.Repos[name])
		if rerr != nil {
			return "", nil, fmt.Errorf("config error: repos.%s: %w", name, rerr)
		}
		named = append(named, NamedRepo{Name: name, Root: root})
	}
	return defaultRoot, named, nil
}

// withinWorkspace reports whether root is the workspace or a descendant of
// it.
func withinWorkspace(ws, root string) bool {
	rel, err := filepath.Rel(ws, root)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// sortedKeys returns the config's repo keys in deterministic order, for
// stable error messages and the ordered named-repo list.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// isGitRoot reports whether dir is the top of a git working tree: .git is
// a directory in a normal checkout and a gitdir-pointer file in worktrees
// and submodules; both are valid repo roots. This mirrors the check
// state.Init performs for the default repo.
func isGitRoot(dir string) bool {
	fi, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil && (fi.IsDir() || !fi.IsDir())
}

// Template is the starter config.yaml written by `gummi init`.
const Template = `# gummi configuration. See docs/DESIGN.md.
#
# Verify-stage check commands are not configured here: gummi discovers
# the repo's build/test/lint commands at spec approval and records them
# in each feature's spec (Verification plan, gummi-checks block), where
# you can review and edit them.

# permissions: allow-all (default) or guarded.
permissions: allow-all

# sandbox: enforce|warn|off — the tool-coverage guarantee a run is held
# to (default warn). enforce refuses to start any run whose profile routes
# a role at a backend without tool coverage; warn and off let such a run
# start anyway. Profiles may override this per-profile in
# .gummi/profiles.yaml.
# sandbox: warn

# autopilot_lanes: how many autopilot cards (gate-approval mode autopilot,
# and only that mode) can drive at once. Default 2. The attended pool —
# every other card, the everyday default included, where a human is
# expected to stay with it — is sized separately, defaulting to 1 and
# overridden by GUMMI_MAX_ACTIVE, not this key: an attended card must
# never queue behind autopilot work.
# autopilot_lanes: 2

# instructions: — a list of absolute paths to extra instruction files that
# are appended to the workspace environment card, in user-then-workspace
# order. User-level instructions live at $XDG_CONFIG_HOME/gummi/config.yaml
# (falling back to ~/.config/gummi/config.yaml); workspace instructions live
# here. Every path must be absolute.
# instructions:
#   - /home/you/.config/gummi/instructions.md

# Which git repository (or repositories) gummi manages. Set AT MOST ONE of
# repo: and repos: — setting both is a config error, because each defines
# the managed set and composing them would leave the default ambiguous.
#
# repo: <path> — the single repository gummi manages, when .gummi does not
# sit at its root. A path relative to the workspace root (e.g. git/lxd),
# which must be the workspace root or a subdirectory of it. Omit it when
# .gummi and .git share a directory — that is the default, and a
# single-repo workspace needs no repo config at all.
# repo: git/lxd
#
# repos: — several selectable repositories instead of one, each a path
# relative to the workspace root. A single entry is allowed: the creation
# dialogs then name it rather than asking. A workspace with repos: has NO
# default repository: every card names one of these, and the workspace root
# is never managed (in this layout it is just the parent of the checkouts).
# The creation dialogs make you pick before they will create a card, the
# board's o key retargets a card that has no worktree yet, and
# run/bugs new/ingest take --repo <name>.
# repos:
#   lxd: git/lxd
#   incus: git/incus

# hooks: — scripts run when the board changes (the notification surface
# beside GUMMI_NOTIFY's bell/desktop). Each entry is a shell command line
# run via "sh -c" in the workspace root, with a JSON payload on stdin and
# GUMMI_EVENT/GUMMI_CARD/GUMMI_WORKSPACE in the environment. The event is
# the shell's "$1": forward it (as below) if the script wants it as an
# argument — a line naming a script and nothing else passes it none.
# An entry without "events:" fires on every event; with it, only on the
# events named. Hooks are advisory: exit codes are the script's business,
# stdout goes nowhere, a slow script is killed after 15s, and none of it
# can fail a run — but failures and undelivered events are counted and
# reported in one line when the process exits. Event vocabulary:
# card.created, stage.enter, card.verified, card.parked, card.merged,
# gate.waiting, question.waiting, budget.exhausted, card.failed.
# hooks:
#   - run: ~/bin/gummi-hook "$1"       # every event
#   - run: page-oncall.sh "$1"
#     events: [gate.waiting, budget.exhausted]
`
