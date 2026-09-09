package worktree

import (
	"context"
	"os/exec"
	"regexp"
	"strings"
)

// RemoteOrigin returns the repository's `origin` remote URL, or "" when
// the directory has no such remote (or is not a git checkout at all). It
// is a local read — `git remote get-url` never touches the network — so
// a dialog can call it every time its repository row changes.
func RemoteOrigin(ctx context.Context, dir string) string {
	if dir == "" {
		return ""
	}
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

var remoteRe = regexp.MustCompile(`^(?:(?:ssh|git|https?)://)?(?:[\w.-]+@)?([\w.-]+)[:/]([\w.-]+)/([\w.-]+?)(?:\.git)?/?$`)

// ParseRemote splits a git remote URL into its host and "owner/repo",
// in every shape git prints one: `git@github.com:o/r.git`,
// `ssh://git@github.com/o/r`, `https://github.com/o/r.git`. ok is
// false for anything it cannot read as host + two path segments.
func ParseRemote(url string) (host, ownerRepo string, ok bool) {
	m := remoteRe.FindStringSubmatch(strings.TrimSpace(url))
	if m == nil {
		return "", "", false
	}
	return strings.ToLower(m[1]), m[2] + "/" + m[3], true
}

// RootForName resolves a configured repository name to its root on disk.
// "" names the workspace default; ok is false when the name is unknown
// or the default is not configured. It answers the same question
// ManagerForName does without building a manager, for callers that only
// want a directory to run a read-only command in.
func (p *Pool) RootForName(name string) (root string, ok bool) {
	if name == "" {
		return p.defaultRoot, p.defaultRoot != ""
	}
	root, ok = p.byName[name]
	return root, ok
}
