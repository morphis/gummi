package agent

import "sync"

// capabilities.go is the static, adapter-free view of each backend's
// capabilities. doctor (and any other pre-flight path that must not
// construct a backend) uses CapabilitiesFor to answer "can this backend
// reach gummi's tools?" without starting it. The base map mirrors exactly
// what each adapter's Capabilities() method would return, and
// TestCapabilitiesForMatchesAdapters guards that the two never drift.
//
// A test-only overlay (RegisterCapabilities) lets fixtures inject
// synthetic backends for coverage-gap scenarios without a live adapter —
// including, across the package boundary, doctor tests in cmd/gummi.
var (
	capsMu      sync.Mutex
	capsOverlay map[string]Capabilities
)

var capsBase = map[string]Capabilities{
	"copilot":  {Resume: true, UsageEvents: true, Interrupt: true, ClientTools: true},
	"claude":   {Resume: true, UsageEvents: true, Interrupt: true, MCPTools: true, ReadOnlyEnforce: true},
	"codex":    {Resume: true, UsageEvents: true, Interrupt: true, MCPTools: true},
	"opencode": {Resume: true, UsageEvents: true, Interrupt: true, MCPTools: true, ReadOnlyEnforce: true},
	"headless": {Interrupt: true, UsageEvents: true, ClientTools: true},
}

// CapabilitiesFor returns the capabilities a constructed adapter named
// name would advertise, without constructing it. It consults the test-only
// overlay first, then the static base map. Unknown names report (_, false).
func CapabilitiesFor(name string) (Capabilities, bool) {
	capsMu.Lock()
	defer capsMu.Unlock()
	if c, ok := capsOverlay[name]; ok {
		return c, true
	}
	c, ok := capsBase[name]
	return c, ok
}

// RegisterCapabilities installs a synthetic capabilities entry for name on
// top of the base map, returning an unregister that removes just that key
// (leaving the base map untouched). It is intended for tests: fixtures can
// stand in for backends that are not registered in capsBase — e.g. a
// deliberately tool-less backend to prove a coverage gap — independently
// of any constructed adapter. The returned undo is idempotent.
func RegisterCapabilities(name string, caps Capabilities) (unregister func()) {
	capsMu.Lock()
	if capsOverlay == nil {
		capsOverlay = map[string]Capabilities{}
	}
	capsOverlay[name] = caps
	capsMu.Unlock()
	return func() {
		capsMu.Lock()
		delete(capsOverlay, name)
		capsMu.Unlock()
	}
}
