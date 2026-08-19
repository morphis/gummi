package config

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitInit(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.CommandContext(context.Background(), "git", "-C", dir, "init", "-q", "-b", "main").CombinedOutput(); err != nil {
		t.Fatalf("git init %s: %v\n%s", dir, err, out)
	}
}

// TestResolveReposDefaultWhenNothingConfigured: no repo:, no repos: — the
// workspace root is the sole (default) repository, exactly as before.
func TestResolveReposDefaultWhenNothingConfigured(t *testing.T) {
	ws := t.TempDir()
	gitInit(t, ws)
	def, named, err := ResolveRepos(ws, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if def != ws {
		t.Errorf("default root = %q, want workspace root %q", def, ws)
	}
	if len(named) != 0 {
		t.Errorf("named = %v, want none", named)
	}
}

// TestResolveReposRepoKeyIsDefault: a repo: key names the default; with no
// repos: there are no named entries.
func TestResolveReposRepoKeyIsDefault(t *testing.T) {
	ws := t.TempDir()
	gitInit(t, ws)
	gitInit(t, filepath.Join(ws, "git", "lxd"))

	c := Config{Repo: "git/lxd"}
	def, named, err := ResolveRepos(ws, c)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(ws, "git", "lxd"); def != want {
		t.Errorf("default root = %q, want %q", def, want)
	}
	if len(named) != 0 {
		t.Errorf("named = %+v, want none with only repo: set", named)
	}
}

// TestResolveReposNamedSortedNoDefault: a repos: map (with no repo:)
// yields no default plus the named entries, sorted by name. The workspace
// root is not resolved at all, so it need not be a git toplevel.
func TestResolveReposNamedSortedNoDefault(t *testing.T) {
	ws := t.TempDir()
	gitInit(t, filepath.Join(ws, "git", "lxd"))
	gitInit(t, filepath.Join(ws, "git", "incus"))

	c := Config{Repos: map[string]string{
		"incus": "git/incus",
		"lxd":   "git/lxd",
	}}
	def, named, err := ResolveRepos(ws, c)
	if err != nil {
		t.Fatal(err)
	}
	if def != "" {
		t.Errorf("default root = %q, want none (repos:-only workspace)", def)
	}
	if len(named) != 2 || named[0].Name != "incus" || named[1].Name != "lxd" {
		t.Fatalf("named = %+v, want sorted [incus lxd]", named)
	}
	if named[0].Root != filepath.Join(ws, "git", "incus") {
		t.Errorf("incus root = %q", named[0].Root)
	}
}

// TestResolveReposNonGitWorkspaceRootOK: a repos:-only workspace whose root
// is not a git repository (the natural multi-repo parent layout) resolves
// fine — the named repos are validated, the workspace root never is.
func TestResolveReposNonGitWorkspaceRootOK(t *testing.T) {
	ws := t.TempDir() // ws is not a git repo
	gitInit(t, filepath.Join(ws, "git", "a"))
	c := Config{Repos: map[string]string{"a": "git/a"}}
	def, named, err := ResolveRepos(ws, c)
	if err != nil {
		t.Fatalf("repos:-only workspace with non-git root should resolve: %v", err)
	}
	if def != "" {
		t.Errorf("default root = %q, want none", def)
	}
	if len(named) != 1 || named[0].Name != "a" || named[0].Root != filepath.Join(ws, "git", "a") {
		t.Fatalf("named = %+v, want [a git/a]", named)
	}
}

// TestResolveReposBothKeysRejected: setting both repo: and repos: is a
// config error naming both, so the default can never shift silently.
func TestResolveReposBothKeysRejected(t *testing.T) {
	ws := t.TempDir()
	gitInit(t, ws)
	c := Config{Repo: "git/lxd", Repos: map[string]string{"incus": "git/incus"}}
	if _, _, err := ResolveRepos(ws, c); err == nil {
		t.Fatal("expected an error when both repo: and repos: are set")
	} else if !strings.Contains(err.Error(), "repo:") || !strings.Contains(err.Error(), "repos:") {
		t.Errorf("error %q should name both keys", err)
	}
}

// TestResolveReposEscapesRejected: a named repo escaping the workspace is
// a resolution-time error naming the repo.
func TestResolveReposEscapesRejected(t *testing.T) {
	ws := t.TempDir()
	gitInit(t, ws)
	gitInit(t, filepath.Join(ws, "outside"))
	c := Config{Repos: map[string]string{"bad": "../outside"}}
	if _, _, err := ResolveRepos(ws, c); err == nil {
		t.Fatal("expected an error for a repo escaping the workspace")
	} else if !strings.Contains(err.Error(), "bad") {
		t.Errorf("error %q should name the offending repo", err)
	}
}

// TestResolveReposNonGitRejected: a named entry that is not a git toplevel
// is a resolution-time error naming the repo.
func TestResolveReposNonGitRejected(t *testing.T) {
	ws := t.TempDir()
	gitInit(t, ws)
	c := Config{Repos: map[string]string{"plain": "not-a-repo"}}
	if _, _, err := ResolveRepos(ws, c); err == nil {
		t.Fatal("expected an error for a non-git repo")
	} else if !strings.Contains(err.Error(), "plain") {
		t.Errorf("error %q should name the offending repo", err)
	}
}
