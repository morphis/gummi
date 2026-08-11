package sandbox

import (
	"reflect"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/config"
)

func TestNormalizeEnum(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Mode
		ok   bool
	}{
		{"", ModeWarn, true}, // built-in default
		{"enforce", ModeEnforce, true},
		{"warn", ModeWarn, true},
		{"off", ModeOff, true},
		{"enfrce", "", false},
		{" ENFORCE ", "", false},
	} {
		got, err := Normalize(tc.in)
		if tc.ok {
			if err != nil {
				t.Fatalf("Normalize(%q) error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("Normalize(%q) = %q, want %q", tc.in, got, tc.want)
			}
		} else if err == nil {
			t.Errorf("Normalize(%q) = %q, want error", tc.in, got)
		}
	}
}

func TestResolvePrecedence(t *testing.T) {
	// Each case swaps the profile/workspace declarations and asserts the
	// effective mode: profile wins when set, else workspace, else warn.
	cases := map[string]struct {
		workspace, profile Mode
		want               Mode
	}{
		"profile-wins":           {ModeWarn, ModeEnforce, ModeEnforce},
		"workspace-wins":         {"", ModeOff, ModeOff}, // workspace="off", profile="" falls through
		"workspace-empty":        {"", ModeEnforce, ModeEnforce},
		"default":                {"", "", ModeWarn},
		"profile-over-workspace": {ModeEnforce, ModeWarn, ModeWarn},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := Resolve(tc.workspace, tc.profile, nil, nil)
			if got.Mode != tc.want {
				t.Errorf("Resolve(%q,%q).Mode = %q, want %q", tc.workspace, tc.profile, got.Mode, tc.want)
			}
		})
	}
}

// TestResolveGapsSorted routes two roles at synthetic backends that
// advertise neither ClientTools nor MCPTools (both flags false → both gap)
// and one at opencode (MCPTools only → no gap), and asserts the gaps come
// back sorted by role then backend.
func TestResolveGapsSorted(t *testing.T) {
	unregA := agent.RegisterCapabilities("uncov-a", agent.Capabilities{})
	defer unregA()
	unregB := agent.RegisterCapabilities("uncov-b", agent.Capabilities{})
	defer unregB()

	profile := config.Profile{
		"architect":   {Backend: "uncov-a", Model: "m"},
		"implementer": {Backend: "uncov-b", Model: "m"},
		"reviewer":    {Backend: "opencode", Model: "m"},
	}
	caps := map[string]agent.Capabilities{}
	for _, b := range []string{"uncov-a", "uncov-b", "opencode"} {
		c, _ := agent.CapabilitiesFor(b)
		caps[b] = c
	}

	got := Resolve(ModeEnforce, "", profile, caps)
	want := []Gap{{Backend: "uncov-a", Role: "architect"}, {Backend: "uncov-b", Role: "implementer"}}
	if !reflect.DeepEqual(got.Gaps, want) {
		t.Errorf("Gaps = %+v, want %+v", got.Gaps, want)
	}
	if len(got.Gaps) != 2 {
		t.Fatalf("len(Gaps) = %d, want 2 (reviewer at opencode must not gap)", len(got.Gaps))
	}
}

// TestResolveMissingBackendGaps: a role whose backend is absent from the
// caps map gaps too (fail-closed), so a profile referencing an
// unregistered backend is never silently treated as covered.
func TestResolveMissingBackendGaps(t *testing.T) {
	profile := config.Profile{"implementer": {Backend: "nowhere", Model: "m"}}
	got := Resolve(ModeEnforce, "", profile, map[string]agent.Capabilities{})
	if len(got.Gaps) != 1 || got.Gaps[0].Backend != "nowhere" {
		t.Errorf("Gaps = %+v, want exactly the missing-backend gap", got.Gaps)
	}
}
