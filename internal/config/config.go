// Package config loads .gummi/config.yaml — the repo-controlled inputs
// gummi honors: the verify-stage check commands. Because these commands
// run in the user's worktree, they are treated as untrusted input and
// surfaced in the TUI before they ever run (DESIGN §4.4 threat list).
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Check is one named verify command, run from the worktree root.
type Check struct {
	Name string `yaml:"name"`
	Cmd  string `yaml:"cmd"`
}

// Config is the parsed .gummi/config.yaml.
type Config struct {
	// Checks are the fixed build/test/lint commands the Verify stage
	// always runs (DESIGN §3, decision 7).
	Checks []Check `yaml:"checks"`
	// Permissions is "allow-all" (default) or "guarded" (DESIGN §4.4).
	Permissions string `yaml:"permissions"`
}

// Load reads and parses config.yaml. A missing file yields an empty
// (zero-check) config, not an error — a repo need not define checks.
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
	for i, ch := range c.Checks {
		if ch.Cmd == "" {
			return Config{}, fmt.Errorf("%s: check %d (%q) has an empty cmd", path, i, ch.Name)
		}
		if ch.Name == "" {
			c.Checks[i].Name = ch.Cmd
		}
	}
	switch c.Permissions {
	case "", "allow-all", "guarded":
	default:
		return Config{}, fmt.Errorf("%s: permissions must be \"allow-all\" or \"guarded\", got %q", path, c.Permissions)
	}
	return c, nil
}

// Guarded reports whether the config selects guarded permission mode
// (agents' tool calls require approval). The default (empty or "allow-all")
// is not guarded.
func (c Config) Guarded() bool { return c.Permissions == "guarded" }

// Template is the starter config.yaml written by `gummi init`. The
// checks are commented out: a repo opts in explicitly, and until it
// does, Verify runs no repo commands.
const Template = `# gummi configuration. See docs/DESIGN.md.
#
# checks: the build/test/lint commands the Verify stage runs in a
# feature's worktree. gummi surfaces these in the TUI before running
# them, since they come from the repository. Uncomment and adapt:
#
# checks:
#   - name: build
#     cmd: go build ./...
#   - name: test
#     cmd: go test ./...
#   - name: lint
#     cmd: golangci-lint run

# permissions: allow-all (default) or guarded.
permissions: allow-all
`
