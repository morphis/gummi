package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DefaultDiscover is what a workspace with no repository config scans when
// its root is not itself a checkout: the checkouts directly under it, and
// one level further down (git/lxd, src/incus).
var DefaultDiscover = []string{"*", "*/*"}

// RepoSet is the workspace's selectable repository set: the default root,
// the named repositories, and the folder names discovery could not give
// out because more than one checkout claims them.
type RepoSet struct {
	Default string
	Named   []NamedRepo
	// Ambiguous maps a folder name to the roots that share it. No
	// discovered root gets it: handing it to one would silently move every
	// card that already holds it the day a second checkout of that name is
	// cloned. A pinned `repos:` entry keeps a name it shares (it is listed
	// first); pinning one of the others settles the rest.
	Ambiguous map[string][]string
}

// discoverRepos expands patterns (globs relative to ws) into the git
// checkouts they match, sorted. Only a directory whose .git is itself a
// directory counts: a .git file is a linked worktree or a submodule —
// every card's own worktree under .gummi/worktrees is one — and never a
// repository to manage. Symlinks are not followed, hidden directories
// (.gummi among them) are skipped, and a checkout nested inside another
// match is left to the outer one.
func discoverRepos(ws string, patterns []string) ([]string, error) {
	seen := map[string]bool{}
	var found []string
	for _, pat := range patterns {
		if filepath.IsAbs(pat) {
			return nil, fmt.Errorf("config error: discover pattern %q must be relative to the workspace", pat)
		}
		matches, err := filepath.Glob(filepath.Join(ws, pat))
		if err != nil {
			return nil, fmt.Errorf("config error: discover pattern %q: %w", pat, err)
		}
		for _, m := range matches {
			m = filepath.Clean(m)
			if seen[m] || !withinWorkspace(ws, m) || m == ws || hiddenUnder(ws, m) {
				continue
			}
			if fi, err := os.Lstat(m); err != nil || !fi.IsDir() {
				continue
			}
			if fi, err := os.Lstat(filepath.Join(m, ".git")); err != nil || !fi.IsDir() {
				continue
			}
			seen[m] = true
			found = append(found, m)
		}
	}
	sort.Strings(found)
	// Sorted, an enclosing checkout precedes everything inside it.
	var outer []string
	for _, m := range found {
		if len(outer) > 0 && withinWorkspace(outer[len(outer)-1], m) {
			continue
		}
		outer = append(outer, m)
	}
	return outer, nil
}

// hiddenUnder reports whether any segment of p below ws starts with a dot.
func hiddenUnder(ws, p string) bool {
	rel, err := filepath.Rel(ws, p)
	if err != nil {
		return true
	}
	for _, seg := range strings.Split(rel, string(filepath.Separator)) {
		if strings.HasPrefix(seg, ".") && seg != "." {
			return true
		}
	}
	return false
}

// nameDiscovered names each discovered root after its folder, beside the
// pinned set. A root that is already pinned keeps its pinned name; a folder
// name that a pinned entry or another checkout also claims goes to no
// discovered root and is reported in the ambiguous map instead.
func nameDiscovered(found []string, pinned []NamedRepo) (named []NamedRepo, ambiguous map[string][]string) {
	pinnedRoot := map[string]bool{}
	pinnedName := map[string]string{}
	for _, n := range pinned {
		pinnedRoot[n.Root] = true
		pinnedName[n.Name] = n.Root
	}
	byName := map[string][]string{}
	for _, root := range found {
		if pinnedRoot[root] {
			continue
		}
		name := filepath.Base(root)
		byName[name] = append(byName[name], root)
	}
	for _, name := range sortedSliceKeys(byName) {
		roots := byName[name]
		if p, ok := pinnedName[name]; ok {
			roots = append([]string{p}, roots...)
		}
		if len(roots) > 1 {
			if ambiguous == nil {
				ambiguous = map[string][]string{}
			}
			ambiguous[name] = roots
			continue
		}
		named = append(named, NamedRepo{Name: name, Root: roots[0]})
	}
	return named, ambiguous
}

func sortedSliceKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
