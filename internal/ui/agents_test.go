package ui

import "github.com/morphis/gummi/internal/agent"

// singleAgent wraps one adapter into the map shape engine.Config.Agents
// wants, aliasing it under both its Name() and the empty-string default
// key so the engine's per-role lookup resolves whether or not a profile
// mentions a backend.
func singleAgent(a agent.Agent) map[string]agent.Agent {
	return map[string]agent.Agent{"": a, a.Name(): a}
}
