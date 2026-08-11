package agent

import "testing"

// TestCapabilitiesForMatchesAdapters is the drift guard: the static
// capabilities (what doctor sees) must match exactly what a constructed
// adapter's Capabilities() would report, so a change to one cannot
// silently desync the pre-flight view.
func TestCapabilitiesForMatchesAdapters(t *testing.T) {
	adapters := []struct {
		name string
		caps Capabilities
	}{
		{(&Copilot{}).Name(), (&Copilot{}).Capabilities()},
		{(&ClaudeCode{}).Name(), (&ClaudeCode{}).Capabilities()},
		{(&Codex{}).Name(), (&Codex{}).Capabilities()},
		{(&Opencode{}).Name(), (&Opencode{}).Capabilities()},
		{(&Headless{argv: []string{"headless", "--serve"}}).Name(), (&Headless{argv: []string{"headless", "--serve"}}).Capabilities()},
	}
	for _, a := range adapters {
		got, ok := CapabilitiesFor(a.name)
		if !ok {
			t.Fatalf("CapabilitiesFor(%q) not found in the static map", a.name)
		}
		if got != a.caps {
			t.Errorf("CapabilitiesFor(%q) = %+v, want the adapter's %+v", a.name, got, a.caps)
		}
	}
}

func TestCapabilitiesForUnknown(t *testing.T) {
	if _, ok := CapabilitiesFor("no-such-backend"); ok {
		t.Error("unknown backend should report not-found")
	}
}

func TestRegisterCapabilitiesOverlayAndUndo(t *testing.T) {
	unreg := RegisterCapabilities("synthetic", Capabilities{})
	if c, ok := CapabilitiesFor("synthetic"); !ok || c.ClientTools || c.MCPTools {
		t.Fatalf("registered synthetic = %+v ok=%v, want zero-value caps present", c, ok)
	}
	unreg()
	if _, ok := CapabilitiesFor("synthetic"); ok {
		t.Error("after unregister, synthetic should be unknown again")
	}
	// undo must be idempotent and leave the base map intact.
	unreg()
	if _, ok := CapabilitiesFor("copilot"); !ok {
		t.Error("unregister must not disturb the base map")
	}
}
