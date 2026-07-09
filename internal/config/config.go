// Package config loads .gummi/config.yaml — the repo-controlled gummi
// settings. Since M5 this is only the permission mode: the verify-stage
// check commands live in each feature's spec as a gummi-checks block
// (auto-discovered at approval), not in static config.
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config is the parsed .gummi/config.yaml.
type Config struct {
	// Permissions is "allow-all" (default) or "guarded" (DESIGN §4.4).
	Permissions string `yaml:"permissions"`
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
	return c, nil
}

// Guarded reports whether the config selects guarded permission mode
// (agents' tool calls require approval). The default (empty or "allow-all")
// is not guarded.
func (c Config) Guarded() bool { return c.Permissions == "guarded" }

// Template is the starter config.yaml written by `gummi init`.
const Template = `# gummi configuration. See docs/DESIGN.md.
#
# Verify-stage check commands are not configured here: gummi discovers
# the repo's build/test/lint commands at spec approval and records them
# in each feature's spec (Verification plan, gummi-checks block), where
# you can review and edit them.

# permissions: allow-all (default) or guarded.
permissions: allow-all
`
