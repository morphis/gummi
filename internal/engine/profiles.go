package engine

import (
	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/config"
)

// resolveRole picks the role config and backend name for a feature's
// profile and role. It falls back to the engine's single-model config
// (M1/M2 behavior) when profiles are absent or don't cover the
// profile/role, so a repo without profiles.yaml still works. An empty
// backend means "use the engine's default backend" — agentFor resolves
// that.
func (e *Engine) resolveRole(profileName string, role agent.Role) (config.RoleConfig, string) {
	prof, ok := e.cfg.Profiles.Profiles[profileName]
	if !ok {
		if def := e.cfg.Profiles.Default; def != "" {
			prof, ok = e.cfg.Profiles.Profiles[def]
		}
	}
	if ok {
		if rc, ok := prof[string(role)]; ok {
			return rc, rc.Backend
		}
	}
	return config.RoleConfig{Model: e.cfg.Model}, ""
}

// agentFor returns the Agent for the given backend name. An empty name,
// or an unknown backend, resolves to the engine's default agent (the
// entry stored under the "" key in cfg.Agents). Returns nil when the
// engine has no agents at all — a construction-time misconfiguration the
// callers already guard against with their own nil checks.
func (e *Engine) agentFor(backend string) agent.Agent {
	if backend != "" {
		if a, ok := e.cfg.Agents[backend]; ok {
			return a
		}
	}
	return e.cfg.Agents[""]
}

// defaultAgent returns the engine's default backend, or nil when none is
// configured. It exists because a handful of engine-owned sessions
// (discovery, ingest, estimate) don't run under a profile role and use
// the default directly.
func (e *Engine) defaultAgent() agent.Agent { return e.agentFor("") }
