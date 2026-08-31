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
	if rc, ok := e.lookupRole(profileName, role); ok {
		return rc, rc.Backend
	}
	return config.RoleConfig{Model: e.cfg.Model}, ""
}

// lookupRole reports whether profileName's profile (or the default one)
// declares role, and its config if so. It is resolveRole's first half,
// split out because "the role is declared" and "the role resolved to
// something" are different questions and only the caller knows which one
// it is asking. resolveRole conflates them deliberately — an undeclared
// role there means the single-model fallback, which is the right answer
// for a stage role that every profile is expected to cover. A role no
// profile is expected to declare at all needs to tell the two apart.
func (e *Engine) lookupRole(profileName string, role agent.Role) (config.RoleConfig, bool) {
	prof, ok := e.cfg.Profiles.Profiles[profileName]
	if !ok {
		if def := e.cfg.Profiles.Default; def != "" {
			prof, ok = e.cfg.Profiles.Profiles[def]
		}
	}
	if !ok {
		return config.RoleConfig{}, false
	}
	rc, ok := prof[string(role)]
	return rc, ok
}

// resolveBoardRole picks a board session's model and backend as ONE
// decision, which is the whole point of it not being resolveRole.
//
// resolveRole's fallback returns the engine's single default model with
// an EMPTY backend, on the reasonable assumption that a profile covers
// every stage role and so the fallback is only ever reached by a repo
// with no profiles.yaml at all. RoleBoard breaks that assumption: no
// profile declares it (none existed when profiles.yaml was written, and
// requiring one would make the board tab fail on every existing
// workspace), so the fallback is the NORMAL path here, not the edge —
// and it hands back a model from one source and, via agentFor(""), a
// backend from another. A workspace whose default model is gpt-5 and
// whose default agent is claude then gets a claude session told to drive
// gpt-5, which that adapter refuses outright at session start. Model and
// backend have to travel together or they disagree.
//
// So: the board role if a profile has bothered to declare one, else the
// architect's — the closest analogue, being the role that reasons about
// the work rather than editing it, and paired by construction. Failing
// both, nothing at all: an empty model lets the default backend's own
// CLI pick whatever it normally would, which is always something that
// backend can actually drive. That is strictly better than naming a
// model chosen with no idea of who would run it.
func (e *Engine) resolveBoardRole(profileName string) (config.RoleConfig, string) {
	for _, role := range []agent.Role{agent.RoleBoard, agent.RoleArchitect} {
		if rc, ok := e.lookupRole(profileName, role); ok {
			return rc, rc.Backend
		}
	}
	return config.RoleConfig{}, ""
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
