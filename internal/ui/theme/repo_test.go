package theme

import (
	"testing"
)

// TestRepoSlotIsStable pins the hash so the web rail (assets/dom.js) and
// this package agree on a repo's color: change one, change both.
func TestRepoSlotIsStable(t *testing.T) {
	want := map[string]int{"lxd": 1, "api": 5, "web": 1}
	for name, slot := range want {
		if got := RepoSlot(name); got != slot {
			t.Errorf("RepoSlot(%q) = %d, want %d", name, got, slot)
		}
	}
}

// TestRepoSlotStaysInPalette: every name lands on a real palette slot, and
// the palette is the size the web page's --r0 … --r5 assume.
func TestRepoSlotStaysInPalette(t *testing.T) {
	seen := map[int]bool{}
	for _, name := range []string{"api", "web", "infra", "mobile", "lxd", "docs", "core", "ops", "ui", "sdk"} {
		slot := RepoSlot(name)
		if slot < 0 || slot >= RepoSlots {
			t.Fatalf("RepoSlot(%q) = %d, outside [0,%d)", name, slot, RepoSlots)
		}
		seen[slot] = true
	}
	if len(seen) < 3 {
		t.Errorf("ten plausible repo names collapsed onto %d slots", len(seen))
	}
}
