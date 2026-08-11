// Package sandbox resolves the effective confinement mode for a run from
// the workspace config, the selected profile, and the backends that
// profile actually routes its roles through. It is the single canonical
// place that turns "sandbox: enforce|warn|off" plus backend capabilities
// into (mode, coverage gaps), so the engine's session-start refusal and
// the doctor's per-profile report cannot drift apart.
//
// The resolver is a pure function of its inputs: it reads no filesystem,
// constructs no adapter, and mutates nothing. Callers (the engine from
// live adapters, doctor from the static agent.CapabilitiesFor helper)
// pass the backend capabilities they already have; an empty role backend
// is expected to have been resolved to the concrete default backend's
// name before Resolve is called.
package sandbox

import (
	"fmt"
	"sort"
	"strings"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/config"
)

// Mode is one of the three confinement levels. The zero value ("") means
// "unset" — the resolver treats it as no declaration for that layer, so
// precedence can fall through to the next one and finally to the built-in
// default.
type Mode string

const (
	// ModeEnforce arms the R1 tripwire and refuses to start any run whose
	// profile names a backend without tool coverage.
	ModeEnforce Mode = "enforce"
	// ModeWarn arms the R1 tripwire but never refuses on coverage gaps.
	ModeWarn Mode = "warn"
	// ModeOff disarms the R1 tripwire entirely — the escape hatch for
	// bootstrap and test sessions that legitimately touch main.
	ModeOff Mode = "off"
)

// Normalize validates a raw sandbox value. An empty string yields the
// built-in default (warn); any other value must be one of the three modes.
func Normalize(s string) (Mode, error) {
	if s == "" {
		return ModeWarn, nil
	}
	switch Mode(s) {
	case ModeEnforce, ModeWarn, ModeOff:
		return Mode(s), nil
	}
	return "", fmt.Errorf("sandbox must be \"enforce\", \"warn\", or \"off\", got %q", s)
}

// Gap names one coverage failure: the backend a role is routed at does
// not advertise any path to gummi's tools.
type Gap struct {
	Backend string
	Role    string
}

// Resolution is the outcome of resolving a profile's sandbox mode.
type Resolution struct {
	Mode Mode
	Gaps []Gap // sorted by (Role, Backend); empty when coverage is complete
}

// Resolve computes the effective sandbox mode and the coverage gaps for a
// profile. Precedence: the profile's own mode wins when set, otherwise the
// workspace mode, otherwise the built-in default (warn). Set means a
// non-empty Mode; the zero value "" means that layer declared nothing.
//
// A gap exists for every role whose backend advertises neither ClientTools
// nor MCPTools — the same tool-availability predicate the engine uses when
// deciding how a session reaches gummi's tools.
func Resolve(workspaceMode, profileMode Mode, profile config.Profile, caps map[string]agent.Capabilities) Resolution {
	effective := workspaceMode
	if profileMode != "" {
		effective = profileMode
	}
	if effective == "" {
		effective = ModeWarn
	}

	roles := make([]string, 0, len(profile))
	for role := range profile {
		roles = append(roles, role)
	}
	sort.Strings(roles)

	var gaps []Gap
	for _, role := range roles {
		backend := profile[role].Backend
		c, ok := caps[backend]
		if !ok || (!c.ClientTools && !c.MCPTools) {
			gaps = append(gaps, Gap{Backend: backend, Role: role})
		}
	}
	// Deterministic independent of map iteration: sorted by role first
	// (they already are), then backend, so two calls over the same inputs
	// render identically.
	sort.SliceStable(gaps, func(i, j int) bool {
		if gaps[i].Role != gaps[j].Role {
			return gaps[i].Role < gaps[j].Role
		}
		return gaps[i].Backend < gaps[j].Backend
	})

	return Resolution{Mode: effective, Gaps: gaps}
}

// RefusalError is returned by the engine when a run whose profile resolves
// to enforce would start despite incomplete tool coverage. Its message
// names every offending (backend, role) pair so the operator knows which
// profile to fix.
type RefusalError struct {
	Mode Mode
	Gaps []Gap
}

func (e *RefusalError) Error() string {
	pairs := make([]string, 0, len(e.Gaps))
	for _, g := range e.Gaps {
		pairs = append(pairs, g.Backend+"/"+g.Role)
	}
	return fmt.Sprintf("sandbox mode %q refuses to start: backends without tool coverage: %s",
		e.Mode, strings.Join(pairs, ", "))
}
