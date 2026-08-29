package ui

import "os/exec"

// AgentCLI describes one coding-agent CLI gummi knows how to host in the
// agent tab: its stable name (matching GUMMI_AGENT, profiles.yaml's
// `backend:` values, and agent.CapabilitiesFor's capsBase keys —
// internal/agent/capabilities.go) and the binary detectAgentCLIs actually
// probes for it.
type AgentCLI struct {
	Name      string // "copilot", "claude", "codex", "opencode", "zz"
	Bin       string // the binary name/path actually probed (honors *_BIN overrides)
	Installed bool   // whether Bin resolved on PATH at detection time
}

// knownAgentCLIs is the fixed, ordered set of hosted CLIs the picker
// offers. It is deliberately narrower than defaultAttachCommand's own
// switch (rawattach.go): "headless" names an arbitrary operator command
// line (GUMMI_AGENT_CMD), not an installable CLI, so there is nothing on
// PATH to probe for it — it stays a valid engine backend, just not a
// picker candidate. copilot leads the list (it is defaultAttachCommand's
// own fallback, so it reads as "the default" when several CLIs are
// installed); the rest follow capsBase's declaration order.
//
// Every Bin honors the same *_BIN env override rawattach.go's
// defaultAttachCommand already respects, so a name detected here and a
// name chosen from the picker always resolve to the identical binary
// resolveAgentAttach would pick by hand — there is exactly one mapping
// from backend name to binary in this package, not two that could drift.
func knownAgentCLIs() []AgentCLI {
	return []AgentCLI{
		{Name: "copilot", Bin: "copilot"},
		{Name: "claude", Bin: envOr("GUMMI_CLAUDE_BIN", "claude")},
		{Name: "codex", Bin: envOr("GUMMI_CODEX_BIN", "codex")},
		{Name: "opencode", Bin: envOr("GUMMI_OPENCODE_BIN", "opencode")},
		{Name: "zz", Bin: envOr("GUMMI_ZZ_BIN", "zz")},
	}
}

// detectAgentCLIs reports, for every known agent CLI, whether its binary
// (respecting *_BIN overrides) resolves on PATH right now. It is a pure,
// synchronous PATH probe — five exec.LookPath calls, no network, no
// process spawned — so callers may call it inline; agentpicker.go still
// wraps it in a tea.Cmd where it feeds the picker, not because the probe
// itself is slow but to keep the "never mutate Shell fields outside
// Update" discipline every other command in this package follows.
func detectAgentCLIs() []AgentCLI {
	agents := knownAgentCLIs()
	for i := range agents {
		if _, err := exec.LookPath(agents[i].Bin); err == nil {
			agents[i].Installed = true
		}
	}
	return agents
}

// agentCLIBinary looks up the binary knownAgentCLIs() would probe for
// name (a config `agent:` value, typically). ok is false for a name
// outside the known set — e.g. a config.yaml hand-edited with a stale or
// unrecognized name — which resolveAgentAttach (agenttab.go) treats the
// same as nothing configured at all, rather than guessing at a binary
// that was never validated.
func agentCLIBinary(name string) (bin string, ok bool) {
	for _, a := range knownAgentCLIs() {
		if a.Name == name {
			return a.Bin, true
		}
	}
	return "", false
}
