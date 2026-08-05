package engine

import "github.com/morphis/gummi/internal/agent"

// singleAgent wraps one adapter into the map shape Config.Agents wants,
// aliasing it under both its Name() and the "" default key so the
// engine's per-role lookup resolves whether or not a profile mentions a
// backend. Used by tests that don't set up a multi-backend fleet.
func singleAgent(a agent.Agent) map[string]agent.Agent {
	return map[string]agent.Agent{"": a, a.Name(): a}
}
