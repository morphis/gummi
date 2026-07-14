package worktree

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// gummiExcludeLine is the ignore rule keeping the runtime workspace out
// of the product repo. It goes in the repo-local
// <git-common-dir>/info/exclude — never the user's committed .gitignore.
const gummiExcludeLine = "/.gummi/"

// EnsureGummiExcluded keeps .gummi out of the product repo's tracking:
// it appends /.gummi/ to the repo's info/exclude (idempotent, local to
// this clone) and, when .gummi content is already tracked — an agent
// once `git add`ed it, or a clone ships it — untracks it index-only
// (working-tree files are never touched; the staged deletion rides into
// the user's next commit). Runs at launch, before any agent session.
//
// It reports whether anything was untracked, so the caller can warn.
//
// The exclusion covers every worktree of the repo (info/exclude lives in
// the common git dir), including the feature worktrees. gummi's own
// artifact commits there (specs, bug reports — content that must travel
// with the branch, DESIGN §10.11) force-add past it (CommitFile); an
// agent's casual `git add .`/`git add -A` no longer sweeps .gummi in.
func (m *Manager) EnsureGummiExcluded(ctx context.Context) (untracked bool, err error) {
	common, err := runGit(ctx, m.root, "rev-parse", "--git-common-dir")
	if err != nil {
		return false, err
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(m.root, common)
	}
	if err := ensureExcludeLine(filepath.Join(common, "info", "exclude")); err != nil {
		return false, err
	}
	tracked, err := runGit(ctx, m.root, "ls-files", "--", ".gummi")
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(tracked) == "" {
		return false, nil
	}
	// --cached only: drops the paths from the index, never from disk.
	if _, err := runGit(ctx, m.root, "rm", "-r", "-q", "--cached", "--", ".gummi"); err != nil {
		return false, err
	}
	return true, nil
}

// ensureExcludeLine appends the .gummi rule to an exclude file unless a
// line already carries it.
func ensureExcludeLine(path string) error {
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for line := range strings.Lines(string(existing)) {
		if strings.TrimSpace(line) == gummiExcludeLine {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	content := gummiExcludeLine + "\n"
	if n := len(existing); n > 0 && existing[n-1] != '\n' {
		content = "\n" + content
	}
	if _, err := f.WriteString(content); err != nil {
		return fmt.Errorf("appending to %s: %w", path, err)
	}
	return nil
}
