// Package config loads .gummi/config.yaml — the repo-controlled gummi
// settings: the permission and sandbox modes, the managed repositories,
// environment probes, instructions, hooks, co-authors and the skill
// curator. A workflow run's checks live in its spec's done-when items, not
// here.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/hooks"
	"github.com/morphis/gummi/internal/schedule"
)

// Config is the parsed .gummi/config.yaml.
type Config struct {
	// Name is what this gummi instance is called: shown in the TUI's status
	// bar and the web page's header and tab title, so several boards
	// running side by side can be told apart. Workspace-only — a name in
	// the user config would be every instance's name. Empty means unnamed.
	Name string `yaml:"name"`
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
	// Triage is whether a session's first request is read, before its
	// agent hears it, for whether it is work for a workflow recipe: "on"
	// (the default, empty) or "off". The read is one cheap call on the
	// profile's triage, scout or scribe model, charged to the card.
	Triage string `yaml:"triage"`
	// Workflows is whether sessions are offered specs and workflow runs:
	// "on" or "off" (the default, empty). A session's own switch wins.
	Workflows string `yaml:"workflows"`
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
	// Discover lists globs, relative to the workspace root, whose git
	// checkouts join the selectable set under their folder name, rescanned
	// whenever a name is asked for that the set does not hold yet. Like
	// Repos it leaves the workspace with no default; the two combine, a
	// pinned name winning. With no Repo, Repos or Discover and a workspace
	// root that is not itself a checkout, DefaultDiscover is scanned.
	Discover []string `yaml:"discover"`
	// Env is the operator-configured environment prerequisite map. Each
	// entry names a prerequisite that may be referenced by [env: <name>]
	// tags in a verification plan; gummi probes each entry's Probe command
	// at Verify kickoff and in `gummi doctor`.
	Env map[string]EnvPrereq `yaml:"env"`
	// Instructions is a list of extra instruction-file paths that are
	// appended to the workspace environment card, in user-then-workspace
	// order. Every entry must be an absolute path; Load rejects relative or
	// empty entries so a path cannot silently walk out of the workspace.
	Instructions []string `yaml:"instructions"`
	// RemovedSkills catches the retired `skills:` key (skills.forward) so
	// Load can refuse it by name. Left unparsed, a config that forwarded
	// skills would load and its skills would silently stop arriving.
	// Sessions now see what each backend discovers itself (DESIGN §4.1a).
	RemovedSkills any `yaml:"skills"`
	// Checks supplied workspace-wide default checks to a workflow card's
	// check discovery. Nothing reads it since workflow cards were retired;
	// it is still parsed so an existing config loads.
	Checks ChecksConfig `yaml:"checks"`
	// Hooks are the scripts run on board events (the notification surface
	// beside GUMMI_NOTIFY's bell/desktop). Each entry runs on every event,
	// or on the events its filter names; see internal/hooks for the
	// vocabulary and the advisory contract. User-level and workspace
	// entries both run — a personal pager beside a workspace's own
	// pipeline — in that order.
	Hooks []hooks.Hook `yaml:"hooks"`
	// CoAuthors are the people named in a Co-authored-by trailer on every
	// commit made in this workspace, each written "Name <email>": gummi
	// appends them to the commits it makes and tells a card's agent to end
	// its own with them. Empty means no trailer. A workspace list replaces
	// a user-level one.
	CoAuthors []string `yaml:"coauthors"`
	// Curator turns on the skill curator (DESIGN §23.4): the background
	// job that distils what sessions recorded into the workspace's skill
	// pack. Absent, nothing curates and nothing spends. Workspace-only.
	Curator *Curator `yaml:"curator"`
}

// Curator is the skill curator's configuration. Its budget is its own:
// Budget credits per run, Monthly across every run in a calendar month.
type Curator struct {
	Backend  string  `yaml:"backend"`
	Model    string  `yaml:"model"`
	Schedule string  `yaml:"schedule"`
	Budget   float64 `yaml:"budget"`
	Monthly  float64 `yaml:"monthly"`
	// ReviewGap is the least time between the last run's start and a run a
	// staged change starts (§23.4), a duration such as "6h"; "off" leaves
	// staged changes to the schedule and empty means the default.
	ReviewGap string `yaml:"review_gap"`
}

// DefaultCuratorSchedule is when a curator with no schedule runs.
const DefaultCuratorSchedule = "@daily"

// DefaultCuratorReviewGap is a curator's ReviewGap when it names none, and
// MinCuratorReviewGap the shortest it may name: a change the curator left
// undecided would otherwise start a run at nearly every tick.
const (
	DefaultCuratorReviewGap = 6 * time.Hour
	MinCuratorReviewGap     = 30 * time.Minute
)

// CuratorReviewGap is how long c waits between runs a staged change
// starts, or off when it starts none, or why its review_gap does not read.
func CuratorReviewGap(c *Curator) (gap time.Duration, off bool, err error) {
	v := strings.TrimSpace(c.ReviewGap)
	switch v {
	case "":
		return DefaultCuratorReviewGap, false, nil
	case "off":
		return 0, true, nil
	}
	gap, err = time.ParseDuration(v)
	if err != nil {
		return 0, false, fmt.Errorf("review_gap %q is not a duration such as 6h, or off", v)
	}
	if gap < MinCuratorReviewGap {
		return 0, false, fmt.Errorf("review_gap must be at least %s, or off", MinCuratorReviewGap)
	}
	return gap, false, nil
}

func validateCurator(path string, c *Curator) error {
	if err := CheckCurator(c); err != nil {
		return fmt.Errorf("%s: curator: %w", path, err)
	}
	return nil
}

// CheckCurator is why Load would refuse the curator block c, or nil; a nil
// c (no curator) is fine.
func CheckCurator(c *Curator) error {
	if c == nil {
		return nil
	}
	if strings.TrimSpace(c.Backend) == "" {
		return errors.New("backend is required (the agent the curator runs on)")
	}
	if c.Budget <= 0 {
		return errors.New("budget must be a positive number of credits per run")
	}
	if c.Monthly < 0 || (c.Monthly > 0 && c.Monthly < c.Budget) {
		return errors.New("monthly must be at least one run's budget, or 0 for no monthly cap")
	}
	if _, err := CuratorCron(c); err != nil {
		return fmt.Errorf("schedule does not compile: %w", err)
	}
	if _, _, err := CuratorReviewGap(c); err != nil {
		return err
	}
	return nil
}

// CuratorCron is the cron expression c's schedule compiles to (the default
// when it names none), or why it does not compile.
func CuratorCron(c *Curator) (string, error) {
	expr := strings.TrimSpace(c.Schedule)
	if expr == "" {
		expr = DefaultCuratorSchedule
	}
	if strings.HasPrefix(expr, "@") || !strings.Contains(expr, " ") {
		return schedule.Compile(expr)
	}
	if err := schedule.Check(expr, ""); err != nil {
		return "", err
	}
	return expr, nil
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
// and the command that probes it: the env prerequisites.
func (c Config) EnvProbes() map[string]EnvPrereq {
	out := make(map[string]EnvPrereq, len(c.Env))
	for k, v := range c.Env {
		out[k] = v
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
	if c.Name != "" {
		name, err := ValidateName(c.Name)
		if err != nil {
			return Config{}, fmt.Errorf("%s: name: %w", path, err)
		}
		c.Name = name
	}
	if c.CoAuthors, err = ParseCoAuthors(c.CoAuthors); err != nil {
		return Config{}, fmt.Errorf("%s: coauthors: %w", path, err)
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
	switch c.Triage {
	case "", "on", "off":
	default:
		return Config{}, fmt.Errorf("%s: triage must be \"on\" or \"off\", got %q", path, c.Triage)
	}
	switch c.Workflows {
	case "", "on", "off":
	default:
		return Config{}, fmt.Errorf("%s: workflows must be \"on\" or \"off\", got %q", path, c.Workflows)
	}
	for name, p := range c.Env {
		if err := validateEnvName(path, "env", name); err != nil {
			return Config{}, err
		}
		if strings.TrimSpace(p.Probe) == "" {
			return Config{}, fmt.Errorf("%s: env: %q has an empty probe", path, name)
		}
	}
	for i, inst := range c.Instructions {
		if inst == "" {
			return Config{}, fmt.Errorf("%s: instructions: entry %d is empty", path, i)
		}
		if !filepath.IsAbs(inst) {
			return Config{}, fmt.Errorf("%s: instructions: entry %q is not an absolute path", path, inst)
		}
	}
	if err := validateCurator(path, c.Curator); err != nil {
		return Config{}, err
	}
	if c.RemovedSkills != nil {
		return Config{}, fmt.Errorf("%s: skills: this key was removed — gummi no longer forwards skills; "+
			"each backend loads the skills it discovers itself, so move them into the repository's or your "+
			"own skill directory for that agent and delete the key", path)
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

// TriageOn reports whether a session's first request is triaged: unless
// the config says "off".
func (c Config) TriageOn() bool { return c.Triage != "off" }

// WorkflowsOn reports whether sessions are offered workflows and specs when
// they have no switch of their own: only when the config says "on" (the
// opposite default to triage).
func (c Config) WorkflowsOn() bool { return c.Workflows == "on" }

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
// one entry per top-level field: "permissions", "sandbox",
// "repo", "repos", "instructions", and "env.<name>" for each
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
	if user.Name != "" {
		return Config{}, nil, fmt.Errorf("%s: name is workspace-only and cannot be set in the user config", userPath)
	}
	if len(user.Discover) > 0 {
		return Config{}, nil, fmt.Errorf("%s: discover is workspace-only and cannot be set in the user config", userPath)
	}
	if user.Curator != nil {
		return Config{}, nil, fmt.Errorf("%s: curator is workspace-only and cannot be set in the user config", userPath)
	}

	sources := map[string]string{}
	var merged Config
	merged.Curator = ws.Curator

	merged.Name = ws.Name
	if ws.Name != "" {
		sources["name"] = workspacePath
	} else {
		sources["name"] = "default"
	}

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

	if ws.Triage != "" {
		merged.Triage = ws.Triage
		sources["triage"] = workspacePath
	} else if user.Triage != "" {
		merged.Triage = user.Triage
		sources["triage"] = userPath
	} else {
		sources["triage"] = "default"
	}

	if ws.Workflows != "" {
		merged.Workflows = ws.Workflows
		sources["workflows"] = workspacePath
	} else if user.Workflows != "" {
		merged.Workflows = user.Workflows
		sources["workflows"] = userPath
	} else {
		sources["workflows"] = "default"
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

	if len(ws.Discover) > 0 {
		merged.Discover = ws.Discover
		sources["discover"] = workspacePath
	} else {
		sources["discover"] = "default"
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

	// coauthors layer like permissions: a workspace list replaces a
	// user-level one, which is the fallback.
	if len(ws.CoAuthors) > 0 {
		merged.CoAuthors = ws.CoAuthors
		sources["coauthors"] = workspacePath
	} else if len(user.CoAuthors) > 0 {
		merged.CoAuthors = user.CoAuthors
		sources["coauthors"] = userPath
	} else {
		sources["coauthors"] = "default"
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
	set, err := ResolveRepoSet(ws, c)
	return set.Default, set.Named, err
}

// ResolveRepoSet is ResolveRepos with discovery's leftovers: alongside the
// default and the named set it reports the folder names discovery could
// not hand out (RepoSet.Ambiguous). Discovery runs when `discover:` is set,
// or when nothing is configured and the workspace root is not itself a
// checkout; an implicit scan that finds nothing is the same error an
// unconfigured non-checkout root always was.
func ResolveRepoSet(ws string, c Config) (RepoSet, error) {
	// Setting both `repo:` and `repos:` is a config error: they are two
	// ways to define the default repository, and composing them would need
	// rules for whether (and under what name) the `repo:` path joins the
	// selectable set. Erroring keeps one source of truth and fails at load
	// rather than letting the default repository shift silently. `discover:`
	// is a third way to define the set, so it is refused beside `repo:` too.
	if c.Repo != "" && len(c.Repos) > 0 {
		return RepoSet{}, fmt.Errorf("config error: set either `repo:` or `repos:`, not both (got repo:%q and repos:{%s})", c.Repo, strings.Join(sortedKeys(c.Repos), ", "))
	}
	if c.Repo != "" && len(c.Discover) > 0 {
		return RepoSet{}, fmt.Errorf("config error: set either `repo:` or `discover:`, not both (got repo:%q)", c.Repo)
	}
	abs, err := filepath.Abs(ws)
	if err != nil {
		return RepoSet{}, err
	}
	patterns := c.Discover
	if c.Repo == "" && len(c.Repos) == 0 && len(patterns) == 0 && !isGitRoot(abs) {
		patterns = DefaultDiscover
	}
	def, pinned, err := resolvePinned(abs, c, len(patterns) > 0)
	if err != nil {
		return RepoSet{}, err
	}
	set := RepoSet{Default: def, Named: pinned}
	if len(patterns) == 0 {
		return set, nil
	}
	found, err := discoverRepos(abs, patterns)
	if err != nil {
		return RepoSet{}, err
	}
	discovered, ambiguous := nameDiscovered(found, pinned)
	if len(c.Repos) == 0 && len(c.Discover) == 0 && len(discovered) == 0 && len(ambiguous) == 0 {
		return RepoSet{}, fmt.Errorf("config error: repo %q at %s is not the root of a git repository, and no checkout was found under it; configure a git toplevel inside the workspace", "", abs)
	}
	set.Named = append(set.Named, discovered...)
	sort.Slice(set.Named, func(i, j int) bool { return set.Named[i].Name < set.Named[j].Name })
	set.Ambiguous = ambiguous
	return set, nil
}

// resolvePinned resolves the `repo:` and `repos:` keys. discovering marks a
// workspace whose set discovery fills, which has no default of its own.
func resolvePinned(ws string, c Config, discovering bool) (defaultRoot string, named []NamedRepo, err error) {
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
	} else if len(c.Repos) == 0 && !discovering {
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

// Template is the starter config.yaml written into a new workspace.
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

# coauthors: — people named in a Co-authored-by trailer on every commit
# made in this workspace, each written "Name <email>". gummi appends them
# to the commits it makes and tells a card's agent to end its own with
# them. Unset means no trailer. The web page's settings write this key.
# coauthors:
#   - Ada Lovelace <ada@example.com>

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
# run takes --repo <name>.
# repos:
#   lxd: git/lxd
#   incus: git/incus
#
# discover: — globs relative to the workspace root; every git checkout they
# match joins the selectable set under its folder name, and a name gummi
# does not know yet triggers a rescan, so a fresh clone is usable without
# a restart. It combines with repos: (a pinned name wins) and, like repos:,
# leaves the workspace with no default. Two checkouts sharing a folder name
# get neither: pin one under repos: to name it. Hidden directories, linked
# worktrees and submodules are never matched. When repo:, repos: and
# discover: are all absent and the workspace root is not itself a
# checkout, gummi scans ["*", "*/*"].
# discover:
#   - git/*

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
