package agent

import "testing"

func TestGuardedSupportMatrix(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"claude", false},
		{"zz", false},
		{"copilot", true},
		{"opencode", true},
		{"codex", true},
	}
	for _, c := range cases {
		support, known := GuardedSupport(c.name)
		if !known {
			t.Errorf("GuardedSupport(%q) known = false, want true", c.name)
		}
		if support != c.want {
			t.Errorf("GuardedSupport(%q) support = %v, want %v", c.name, support, c.want)
		}
	}
}

func TestGuardedSupportUnmatrixedBackendExcluded(t *testing.T) {
	for _, name := range []string{"headless", "some-unregistered-backend"} {
		if _, known := GuardedSupport(name); known {
			t.Errorf("GuardedSupport(%q) known = true, want false", name)
		}
	}
}
