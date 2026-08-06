package config

import (
	"fmt"
	"os"
	"sort"

	"gopkg.in/yaml.v3"
)

// knownBackends is the set of adapter names a profile role may target via
// its `backend:` field. It mirrors the switch in cmd/gummi/main.go's
// buildAgents — anything else in a profile is a typo, so parsing fails
// fast rather than silently falling through to the default backend.
var knownBackends = map[string]struct{}{
	"copilot":  {},
	"claude":   {},
	"opencode": {},
	"codex":    {},
	"headless": {},
}

// RoleConfig maps one role to a concrete backend+model. Backend is optional;
// empty means "use the engine's default backend" (the one GUMMI_AGENT picks).
type RoleConfig struct {
	Backend string `yaml:"backend"`
	Model   string `yaml:"model"`
}

// Profile maps role names (architect/implementer/reviewer/scribe) to
// their agent configs.
type Profile map[string]RoleConfig

// Profiles is the parsed profiles.yaml.
type Profiles struct {
	// Default names the profile used when a feature has none.
	Default  string             `yaml:"default"`
	Profiles map[string]Profile `yaml:"profiles"`
}

// Names lists the profile names for the new-feature form: the declared
// default first (it becomes the form's initial selection), the rest
// sorted, so the order is stable rather than map-iteration luck.
func (p Profiles) Names() []string {
	out := make([]string, 0, len(p.Profiles))
	for name := range p.Profiles {
		if name != p.Default {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	if _, ok := p.Profiles[p.Default]; ok {
		out = append([]string{p.Default}, out...)
	}
	return out
}

// LoadProfiles reads and validates profiles.yaml. A missing file yields
// an empty set (the engine then falls back to its single-model config).
func LoadProfiles(path string) (Profiles, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Profiles{}, nil
	}
	if err != nil {
		return Profiles{}, fmt.Errorf("reading %s: %w", path, err)
	}
	return ParseProfiles(raw, path)
}

// ParseProfiles parses and validates profiles YAML from raw bytes. name is
// used only in error messages (a file path, or a label like the seed
// template). It lets callers validate the ProfilesTemplate that WOULD be
// seeded, before any file exists on disk.
func ParseProfiles(raw []byte, path string) (Profiles, error) {
	// A pre-parse pass rejects the old `byok:` field with a migration
	// pointer, rather than silently ignoring it. yaml.v3 drops unknown
	// fields on strict-mode-off unmarshal, so a stale profile from before
	// the per-role-backend change would parse clean but not do what its
	// author expected.
	var probe map[string]any
	if err := yaml.Unmarshal(raw, &probe); err == nil {
		if profs, ok := probe["profiles"].(map[string]any); ok {
			for name, p := range profs {
				roles, ok := p.(map[string]any)
				if !ok {
					continue
				}
				for role, rc := range roles {
					rcm, ok := rc.(map[string]any)
					if !ok {
						continue
					}
					if _, has := rcm["byok"]; has {
						return Profiles{}, fmt.Errorf("%s: profile %q role %q uses the removed `byok:` field; "+
							"per-role BYOK is gone — configure the endpoint in the backend itself "+
							"(claude/opencode/headless) and pick it with `backend:` instead", path, name, role)
					}
				}
			}
		}
	}
	var p Profiles
	if err := yaml.Unmarshal(raw, &p); err != nil {
		return Profiles{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	for name, prof := range p.Profiles {
		for role, rc := range prof {
			if rc.Model == "" {
				return Profiles{}, fmt.Errorf("%s: profile %q role %q has no model", path, name, role)
			}
			if rc.Backend != "" {
				if _, ok := knownBackends[rc.Backend]; !ok {
					return Profiles{}, fmt.Errorf("%s: profile %q role %q backend %q is not one of copilot|claude|codex|opencode|headless",
						path, name, role, rc.Backend)
				}
			}
		}
	}
	return p, nil
}

// ProfilesTemplate is the starter profiles.yaml written by `gummi init`.
const ProfilesTemplate = `# gummi profiles: map each role to a backend + model. A feature picks a
# profile; roles indirect between the fixed workflow and concrete backends,
# so the same process can run cheap or premium, or mix providers. See
# docs/DESIGN.md §5.
#
# backend: (optional) copilot | claude | codex | opencode | headless. Omit to use
# the engine's default (whatever GUMMI_AGENT selects; copilot otherwise).
# The backend owns provider config natively — Claude Code login, Codex login, opencode
# auth, GUMMI_AGENT_CMD for headless — so no keys or endpoints live here.

default: thrifty

profiles:
  premium: # ship-critical features — cross-model review catches more
    architect: { backend: claude, model: claude-opus-4.8 }
    implementer: { backend: copilot, model: claude-sonnet-5 }
    reviewer: { backend: claude, model: claude-sonnet-5 }
    scribe: { backend: copilot, model: gpt-5-mini }

  thrifty: # everyday features — backend omitted → engine default
    architect: { model: claude-sonnet-5 }
    implementer: { model: gpt-5-mini }
    reviewer: { model: claude-sonnet-5 }
    scribe: { model: gpt-5-mini }

  # local-heavy: mix a local llama.cpp endpoint (via the headless adapter)
  # with cloud models. Point GUMMI_AGENT_CMD at a wrapper that speaks
  # OpenAI to your local runner; set GUMMI_HEADLESS_CREDITS_PER_1K to a
  # small rate so local spend meters cheaply against the same budget.
  #
  # local-heavy:
  #   architect: { backend: claude, model: claude-sonnet-5 }
  #   implementer: { backend: headless, model: qwen2.5-coder-32b }
  #   reviewer: { backend: headless, model: qwen2.5-coder-32b }
  #   scribe: { backend: copilot, model: gpt-5-mini }
`
