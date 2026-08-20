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

	"gopkg.in/yaml.v3"
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
	return c, nil
}

// Guarded reports whether the config selects guarded permission mode
// (agents' tool calls require approval). The default (empty or "allow-all")
// is not guarded.
func (c Config) Guarded() bool { return c.Permissions == "guarded" }

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
