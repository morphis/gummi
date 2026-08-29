// Package agentcli detects which coding-agent CLIs are actually
// installed on this machine.
//
// It started as internal/ui's own agentdetect.go, private to the agent
// tab's first-run picker. cmd/gummi's defaultBackendName needs the same
// probe for its final engine-backend fallback (main.go), and cmd can't
// import internal/ui — the TUI package pulls in bubbletea, the overlay
// stack, and everything else a headless command has no business
// depending on just to ask "is claude on PATH?" — so the probe moved
// here, a leaf package both sides can import without a cycle.
package agentcli

import (
	"os"
	"os/exec"
	"strings"
)

// AgentCLI describes one coding-agent CLI gummi knows how to host in the
// agent tab: its stable name (matching GUMMI_AGENT, profiles.yaml's
// `backend:` values, and agent.CapabilitiesFor's capsBase keys —
// internal/agent/capabilities.go) and the binary Detect actually probes
// for it.
type AgentCLI struct {
	Name      string // "copilot", "claude", "codex", "opencode", "zz"
	Bin       string // the binary name/path actually probed (honors *_BIN overrides)
	Installed bool   // whether Bin resolved on PATH at detection time
}

// Known is the fixed, ordered set of hosted CLIs the picker offers. It is
// deliberately narrower than defaultAttachCommand's own switch
// (internal/ui/rawattach.go): "headless" names an arbitrary operator
// command line (GUMMI_AGENT_CMD), not an installable CLI, so there is
// nothing on PATH to probe for it — it stays a valid engine backend,
// just not a picker candidate. copilot leads the list (it is
// defaultAttachCommand's own fallback, so it reads as "the default" when
// several CLIs are installed); the rest follow capsBase's declaration
// order.
//
// Every Bin honors the same *_BIN env override rawattach.go's
// defaultAttachCommand already respects, so a name detected here and a
// name chosen from the picker always resolve to the identical binary
// resolveAgentAttach would pick by hand — there is exactly one mapping
// from backend name to binary in this package, not two that could drift.
func Known() []AgentCLI {
	return []AgentCLI{
		{Name: "copilot", Bin: "copilot"},
		{Name: "claude", Bin: envOr("GUMMI_CLAUDE_BIN", "claude")},
		{Name: "codex", Bin: envOr("GUMMI_CODEX_BIN", "codex")},
		{Name: "opencode", Bin: envOr("GUMMI_OPENCODE_BIN", "opencode")},
		{Name: "zz", Bin: envOr("GUMMI_ZZ_BIN", "zz")},
	}
}

// Detect reports, for every known agent CLI, whether its binary
// (respecting *_BIN overrides) resolves on PATH right now. It is a pure,
// synchronous PATH probe — five exec.LookPath calls, no network, no
// process spawned — so callers may call it inline; internal/ui's
// agentpicker.go still wraps it in a tea.Cmd where it feeds the picker,
// not because the probe itself is slow but to keep the "never mutate
// Shell fields outside Update" discipline every other command in that
// package follows.
func Detect() []AgentCLI {
	agents := Known()
	for i := range agents {
		if _, err := exec.LookPath(agents[i].Bin); err == nil {
			agents[i].Installed = true
		}
	}
	return agents
}

// Binary looks up the binary Known() would probe for name (a config
// `agent:` value, typically). ok is false for a name outside the known
// set — e.g. a config.yaml hand-edited with a stale or unrecognized name
// — which callers (internal/ui's resolveAgentAttach) treat the same as
// nothing configured at all, rather than guessing at a binary that was
// never validated.
func Binary(name string) (bin string, ok bool) {
	for _, a := range Known() {
		if a.Name == name {
			return a.Bin, true
		}
	}
	return "", false
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
