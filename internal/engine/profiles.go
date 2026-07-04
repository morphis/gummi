package engine

import (
	"github.com/morphia/gummi/internal/agent"
	"github.com/morphia/gummi/internal/config"
)

// resolveRole picks the model and provider for a feature's profile and
// role. It falls back to the engine's single-model config (M1/M2
// behavior) when profiles are absent or don't cover the profile/role,
// so a repo without profiles.yaml still works.
func (e *Engine) resolveRole(profileName string, role agent.Role) (string, agent.Provider) {
	prof, ok := e.cfg.Profiles.Profiles[profileName]
	if !ok {
		// try the profiles file's declared default
		if def := e.cfg.Profiles.Default; def != "" {
			prof, ok = e.cfg.Profiles.Profiles[def]
		}
	}
	if ok {
		if rc, ok := prof[string(role)]; ok {
			return rc.Model, providerFrom(rc.Provider)
		}
	}
	return e.cfg.Model, e.cfg.Provider
}

// providerFrom converts a profile BYOK block to an agent.Provider. A nil
// block means native routing (no BYOK).
func providerFrom(p *config.ProviderConfig) agent.Provider {
	if p == nil {
		return agent.Provider{}
	}
	typ := p.Type
	if typ == "" {
		typ = "openai"
	}
	return agent.Provider{
		Type:      typ,
		BaseURL:   p.BaseURL,
		APIKeyEnv: p.APIKeyEnv,
	}
}
