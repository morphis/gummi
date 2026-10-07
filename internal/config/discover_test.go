package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func namesOf(named []NamedRepo) map[string]string {
	out := map[string]string{}
	for _, n := range named {
		out[n.Name] = n.Root
	}
	return out
}

// TestDiscoverWhenTheRootIsNoCheckout: nothing configured and a workspace
// root that is not a checkout scans DefaultDiscover, naming each checkout
// after its folder and leaving no default.
func TestDiscoverWhenTheRootIsNoCheckout(t *testing.T) {
	ws := t.TempDir()
	gitInit(t, filepath.Join(ws, "lxd"))
	gitInit(t, filepath.Join(ws, "git", "incus"))
	set, err := ResolveRepoSet(ws, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if set.Default != "" {
		t.Errorf("default = %q, want none", set.Default)
	}
	want := map[string]string{"lxd": filepath.Join(ws, "lxd"), "incus": filepath.Join(ws, "git", "incus")}
	if got := namesOf(set.Named); !reflect.DeepEqual(got, want) {
		t.Errorf("named = %v, want %v", got, want)
	}
}

// TestDiscoverSkipsWhatIsNoRepository: a .git file (a card's linked
// worktree, a submodule), a hidden directory and a checkout nested inside
// another are never repositories of their own.
func TestDiscoverSkipsWhatIsNoRepository(t *testing.T) {
	ws := t.TempDir()
	gitInit(t, filepath.Join(ws, "lxd"))
	gitInit(t, filepath.Join(ws, "lxd", "vendored"))
	gitInit(t, filepath.Join(ws, ".gummi", "hidden"))
	linked := filepath.Join(ws, "linked")
	if err := os.MkdirAll(linked, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(linked, ".git"), []byte("gitdir: /elsewhere\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	set, err := ResolveRepoSet(ws, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if got := namesOf(set.Named); !reflect.DeepEqual(got, map[string]string{"lxd": filepath.Join(ws, "lxd")}) {
		t.Errorf("named = %v, want only lxd", got)
	}
}

// TestDiscoverLeavesAClashedNameToNobody: two checkouts sharing a folder
// name get neither, so a later clone can never move a card; a pinned
// entry keeps a name it shares with a discovered one.
func TestDiscoverLeavesAClashedNameToNobody(t *testing.T) {
	ws := t.TempDir()
	gitInit(t, filepath.Join(ws, "a", "lxd"))
	gitInit(t, filepath.Join(ws, "b", "lxd"))
	gitInit(t, filepath.Join(ws, "a", "incus"))
	gitInit(t, filepath.Join(ws, "b", "incus"))
	c := Config{Repos: map[string]string{"incus": "a/incus"}, Discover: []string{"*/*"}}
	set, err := ResolveRepoSet(ws, c)
	if err != nil {
		t.Fatal(err)
	}
	if got := namesOf(set.Named); !reflect.DeepEqual(got, map[string]string{"incus": filepath.Join(ws, "a", "incus")}) {
		t.Errorf("named = %v, want only the pinned incus", got)
	}
	if got := set.Ambiguous["lxd"]; len(got) != 2 {
		t.Errorf("ambiguous lxd = %v, want both checkouts", got)
	}
	if got := set.Ambiguous["incus"]; !reflect.DeepEqual(got, []string{filepath.Join(ws, "a", "incus"), filepath.Join(ws, "b", "incus")}) {
		t.Errorf("ambiguous incus = %v, want the pinned root first", got)
	}
}

// TestDiscoverKeepsACheckoutRootSingleRepo: a workspace root that is a
// checkout stays the sole default unless discover: is set.
func TestDiscoverKeepsACheckoutRootSingleRepo(t *testing.T) {
	ws := t.TempDir()
	gitInit(t, ws)
	gitInit(t, filepath.Join(ws, "sub"))
	set, err := ResolveRepoSet(ws, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if set.Default != ws || len(set.Named) != 0 {
		t.Errorf("set = %+v, want the root alone", set)
	}
}

// TestDiscoverFindingNothingIsTheOldError: an implicit scan that finds no
// checkout fails the way an unconfigured non-checkout root always did.
func TestDiscoverFindingNothingIsTheOldError(t *testing.T) {
	_, err := ResolveRepoSet(t.TempDir(), Config{})
	if err == nil || !strings.Contains(err.Error(), "not the root of a git repository") {
		t.Errorf("err = %v, want the not-a-git-root error", err)
	}
}

// TestDiscoverBesideRepoIsAnError: repo: and discover: both define the set.
func TestDiscoverBesideRepoIsAnError(t *testing.T) {
	ws := t.TempDir()
	gitInit(t, ws)
	if _, err := ResolveRepoSet(ws, Config{Repo: ".", Discover: []string{"*"}}); err == nil {
		t.Error("repo: with discover: resolved, want a config error")
	}
}
