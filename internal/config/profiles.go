package config

import (
	"fmt"
	"os"
	"regexp"

	"gopkg.in/yaml.v3"
)

// safeKeyEnvRe restricts which environment variables a repo-committed
// profile may reference for a BYOK key. Without this, a cloned repo
// could set `api_key_env: GITHUB_TOKEN` (or any secret) and a hostile
// `base_url`, exfiltrating that secret to the attacker's endpoint. Only
// conventional API-key names and gummi's own namespace are allowed;
// GITHUB_TOKEN, AWS_SECRET_ACCESS_KEY, NPM_TOKEN, etc. do not match.
var safeKeyEnvRe = regexp.MustCompile(`^(GUMMI_[A-Z0-9_]+|[A-Z][A-Z0-9_]*_API_KEY)$`)

// ProviderConfig is a role's optional BYOK endpoint in profiles.yaml.
// The API key is referenced by env-var name only — a literal key must
// never be persisted in the repo-committed profiles.yaml (threat list).
type ProviderConfig struct {
	Type      string `yaml:"type"`
	BaseURL   string `yaml:"base_url"`
	Model     string `yaml:"model"`
	APIKeyEnv string `yaml:"api_key_env"`
	// CreditsPer1KTokens is this provider's token→credit rate for budget
	// math (0 = gummi's default). It lets a cheap local endpoint and a
	// pricey hosted one meter against the same credit budget accurately.
	CreditsPer1KTokens float64 `yaml:"credits_per_1k_tokens"`
	// APIKey is intentionally rejected: keys must be env references.
	APIKey string `yaml:"api_key"`
}

// RoleConfig maps one role to a concrete agent config.
type RoleConfig struct {
	Adapter  string          `yaml:"adapter"`
	Model    string          `yaml:"model"`
	Provider *ProviderConfig `yaml:"byok"`
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

// Names lists the profile names, always including at least the ones
// present. Used by the new-feature form.
func (p Profiles) Names() []string {
	out := make([]string, 0, len(p.Profiles))
	for name := range p.Profiles {
		out = append(out, name)
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
	var p Profiles
	if err := yaml.Unmarshal(raw, &p); err != nil {
		return Profiles{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	for name, prof := range p.Profiles {
		for role, rc := range prof {
			if rc.Model == "" {
				return Profiles{}, fmt.Errorf("%s: profile %q role %q has no model", path, name, role)
			}
			if rc.Provider != nil {
				if rc.Provider.APIKey != "" {
					return Profiles{}, fmt.Errorf("%s: profile %q role %q sets a literal byok api_key; use api_key_env with an environment variable name instead (keys must not be committed)", path, name, role)
				}
				if rc.Provider.BaseURL == "" {
					return Profiles{}, fmt.Errorf("%s: profile %q role %q byok has no base_url", path, name, role)
				}
				if k := rc.Provider.APIKeyEnv; k != "" && !safeKeyEnvRe.MatchString(k) {
					return Profiles{}, fmt.Errorf("%s: profile %q role %q api_key_env %q is not an allowed key variable (must end in _API_KEY or start with GUMMI_) — this blocks a committed profile from exfiltrating arbitrary secrets", path, name, role, k)
				}
			}
		}
	}
	return p, nil
}

// Template is the starter profiles.yaml written by `gummi init`.
const ProfilesTemplate = `# gummi profiles: map each role to a model. A feature picks a profile;
# roles indirect between the fixed workflow and concrete models, so the
# same process can run cheap or premium. See docs/DESIGN.md §5.
#
# BYOK (local/hosted OpenAI-compatible endpoints) is configured per role
# with a byok block. API keys are referenced by environment-variable
# NAME (api_key_env) — never write a literal key here; profiles.yaml is
# committed to the repo.

default: thrifty

profiles:
  premium: # ship-critical features
    architect: { model: claude-opus-4.8 }
    implementer: { model: claude-sonnet-5 }
    reviewer: { model: gpt-5-codex } # cross-model review catches more
    scribe: { model: gpt-5-mini }

  thrifty: # everyday features
    architect: { model: claude-sonnet-5 }
    implementer: { model: gpt-5-mini }
    reviewer: { model: claude-sonnet-5 }
    scribe: { model: gpt-5-mini }

  # local-heavy: near-zero cloud spend via a local llama.cpp server.
  # Uncomment and point base_url at your endpoint. credits_per_1k_tokens
  # sets the token→credit rate for budget math (default 0.5); a near-free
  # local endpoint can meter cheaply against the same credit budget.
  #
  # local-heavy:
  #   architect: { model: claude-sonnet-5 } # design still wants a big brain
  #   implementer:
  #     model: qwen2.5-coder-32b
  #     byok: { type: openai, base_url: http://127.0.0.1:8080/v1, credits_per_1k_tokens: 0.02 }
  #   reviewer:
  #     model: qwen2.5-coder-32b
  #     byok: { type: openai, base_url: http://127.0.0.1:8080/v1, credits_per_1k_tokens: 0.02 }
  #   scribe:
  #     model: qwen2.5-coder-32b
  #     byok: { type: openai, base_url: http://127.0.0.1:8080/v1, credits_per_1k_tokens: 0.02 }
`
