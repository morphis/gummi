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
)

// Config is the parsed .gummi/config.yaml.
type Config struct {
	// Permissions is "allow-all" (default) or "guarded" (DESIGN §4.4).
	Permissions string `yaml:"permissions"`
	// Sandbox is the workspace-wide confinement default: "enforce", "warn",
	// or "off". Empty means unset — profiles that omit their own value fall
	// back to the built-in "warn".
	Sandbox string `yaml:"sandbox"`
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
	// Instructions is a list of extra instruction-file paths that are
	// appended to the workspace environment card, in user-then-workspace
	// order. Every entry must be an absolute path; Load rejects relative or
	// empty entries so a path cannot silently walk out of the workspace.
	Instructions []string `yaml:"instructions"`
	// Checks supplies workspace-wide default verification checks. When
	// Checks.Default is non-empty, check discovery bypasses the scribe and
	// writes the configured list straight into the artifact.
	Checks ChecksConfig `yaml:"checks"`
	// Agent selects which installed coding-agent CLI hosts the **agent
	// tab** — the pty the TUI composites into its own screen
	// (internal/ui/agenttab.go). It does NOT select any of the engine's
	// per-role backends: those are routed entirely by profiles.yaml's
	// `backend:` field (falling back to GUMMI_AGENT / defaultBackendName
	// when a role omits one). The two are different programs doing
	// different jobs — one drives autonomous stages headlessly, the other
	// is the interactive CLI a human drives by hand — so conflating them
	// would mean a person who wants `claude` for their own agent tab but
	// routes autonomous work through `copilot` (or vice versa) couldn't
	// express that with one key.
	//
	// Empty means unset: nothing has been picked yet, and the agent tab's
	// resolveAgentAttach falls through GUMMI_ATTACH_CMD, then GUMMI_AGENT,
	// then a first-run picker rather than guessing (the bug this field
	// exists to fix — a hardcoded "copilot" default the user might not
	// even have installed). The picker persists its choice here via
	// internal/config.SetAgent, which rewrites only this one key and
	// leaves the rest of config.yaml — comments included — untouched;
	// hand-editing the key to one of internal/ui's known CLI names
	// (copilot, claude, codex, opencode, zz) works exactly the same way.
	Agent string `yaml:"agent"`
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
	for name, p := range c.Env {
		if name == "" {
			return Config{}, fmt.Errorf("%s: env: entry has an empty name", path)
		}
		if strings.ContainsAny(name, "]") || strings.ContainsAny(name, " \t\n\r") {
			return Config{}, fmt.Errorf("%s: env: name %q contains a ']' or whitespace character", path, name)
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
	for i, ch := range c.Checks.Default {
		if strings.TrimSpace(ch.Cmd) == "" {
			return Config{}, fmt.Errorf("%s: checks.default: entry %d has an empty cmd", path, i)
		}
		if err := validateCheckTimeout(ch); err != nil {
			return Config{}, fmt.Errorf("%s: checks.default: entry %d: %w", path, i, err)
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
// one entry per top-level field: "permissions", "sandbox", "agent", "repo",
// "repos", "instructions", and "env.<name>" for each distinct env key. Scalar fields
// that are unset in both files use the literal "default". Instructions list
// both contributing paths when both files supply entries.
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

	if ws.Agent != "" {
		merged.Agent = ws.Agent
		sources["agent"] = workspacePath
	} else if user.Agent != "" {
		merged.Agent = user.Agent
		sources["agent"] = userPath
	} else {
		sources["agent"] = "default"
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

# sandbox: enforce|warn|off — the confinement guarantee a run is held to
# (default warn). enforce refuses to start any run whose profile routes a
# role at a backend without tool coverage; warn arms the same tripwire but
# never refuses on coverage gaps; off disarms the tripwire entirely (only
# for bootstrap/test sessions that legitimately touch main). Profiles may
# override this per-profile in .gummi/profiles.yaml.
# sandbox: warn

# instructions: — a list of absolute paths to extra instruction files that
# are appended to the workspace environment card, in user-then-workspace
# order. User-level instructions live at $XDG_CONFIG_HOME/gummi/config.yaml
# (falling back to ~/.config/gummi/config.yaml); workspace instructions live
# here. Every path must be absolute.
# instructions:
#   - /home/you/.config/gummi/instructions.md

# agent: claude|codex|opencode|zz|copilot — which installed CLI hosts the
# TUI's agent tab (a pty running your own coding assistant). This is NOT
# the engine's backend routing — that's profiles.yaml's backend: field.
# Left unset, the TUI's first-run picker asks once and writes the answer
# back here; GUMMI_ATTACH_CMD and GUMMI_AGENT both take priority over it
# when set.
# agent: claude

# repo: <path> — the git repository root gummi manages. Omit when .gummi
# and .git share the same directory (the default); name a nested repo
# relative to the workspace root (e.g. git/lxd). Must be the workspace
# root or a subdirectory of it.
#
# repos:
#   lxd:   git/lxd   — additional selectable managed repositories, each a
#   incus: git/incus   path relative to the workspace root. Cards may name
#                      any of these; omitting the name selects the default
#                      repo above.
`
