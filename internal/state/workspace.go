package state

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Workspace locates gummi's on-disk layout inside one repository.
// gummi always runs from the main checkout; Root is that checkout's
// root directory.
type Workspace struct {
	Root string
}

// GummiDir is the repo-committed gummi directory.
func (w Workspace) GummiDir() string { return filepath.Join(w.Root, ".gummi") }

// StateDir holds machinery (SQLite DB, drafts). Never committed; mode 0700.
func (w Workspace) StateDir() string { return filepath.Join(w.GummiDir(), "state") }

// DraftsDir holds spec drafts before a feature has a worktree.
func (w Workspace) DraftsDir() string { return filepath.Join(w.StateDir(), "drafts") }

// SpecsDir holds approved specs — the artifact's workspace home from
// spec approval on. Workspace content, never committed.
func (w Workspace) SpecsDir() string { return filepath.Join(w.GummiDir(), "specs") }

// BugsDir holds bug reports, the bug-workflow analog of SpecsDir.
func (w Workspace) BugsDir() string { return filepath.Join(w.GummiDir(), "bugs") }

// WorktreesDir holds the nested per-feature git worktrees.
func (w Workspace) WorktreesDir() string { return filepath.Join(w.GummiDir(), "worktrees") }

// IngestDir holds source documents an ingest pass decomposed into
// features (DESIGN §11): committed with the repo, so a seeded draft's
// provenance pointer stays resolvable.
func (w Workspace) IngestDir() string { return filepath.Join(w.GummiDir(), "ingest") }

// SeqFile is the FD-NNN monotonic counter.
func (w Workspace) SeqFile() string { return filepath.Join(w.GummiDir(), "seq") }

// ConfigFile is the repo-controlled config (verify checks, permissions).
func (w Workspace) ConfigFile() string { return filepath.Join(w.GummiDir(), "config.yaml") }

// ProfilesFile maps roles to models per profile.
func (w Workspace) ProfilesFile() string { return filepath.Join(w.GummiDir(), "profiles.yaml") }

// DBFile is the SQLite state store.
func (w Workspace) DBFile() string { return filepath.Join(w.StateDir(), "gummi.db") }

// gitignore rules written by Init: worktrees and state are machinery
// and must never be committed (state may contain transcripts).
const gummiIgnore = `# written by gummi init — machinery, never commit
/worktrees/
/state/
/seq.lock
/seq.tmp
`

// Open returns the Workspace rooted at root, requiring that gummi init
// has already run there.
func Open(root string) (Workspace, error) {
	w := Workspace{Root: root}
	fi, err := os.Lstat(w.GummiDir())
	if err != nil {
		return Workspace{}, fmt.Errorf("no .gummi directory in %s (run `gummi init` first): %w", root, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return Workspace{}, fmt.Errorf("%s is a symlink; refusing to use a redirected gummi directory", w.GummiDir())
	}
	return w, nil
}

// Init creates the .gummi skeleton in root. root must be the top of a
// git repository (worktrees and branches make no sense elsewhere).
// Init is idempotent: existing files, and in particular an existing
// seq counter, are never clobbered.
func Init(root string) (Workspace, error) {
	w := Workspace{Root: root}
	// .git is a directory in a normal checkout and a gitdir-pointer
	// file in worktrees and submodules; both are valid repo roots.
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		return Workspace{}, fmt.Errorf("%s is not the root of a git repository (no .git); gummi manages worktrees and must run from the main checkout", root)
	}
	// A cloned repo can ship .gummi (or any of its subdirs) as a committed
	// symlink pointing elsewhere, and a plain MkdirAll would follow it and
	// write outside the repo. Check each directory for a symlink *before*
	// creating it, in parent-before-child order so every parent is already
	// verified when its child is checked. gummi must only ever write inside
	// the repo's own .gummi.
	dirs := []struct {
		path string
		perm os.FileMode
	}{
		{w.GummiDir(), 0o750},
		{w.StateDir(), 0o700},
		{w.DraftsDir(), 0o750},
		{w.SpecsDir(), 0o750},
		{w.BugsDir(), 0o750},
		{w.WorktreesDir(), 0o750},
		{w.IngestDir(), 0o750},
	}
	for _, d := range dirs {
		if err := mkdirChecked(d.path, d.perm); err != nil {
			return Workspace{}, err
		}
	}
	// The dir may predate this run with looser permissions; state can
	// hold transcripts, so enforce 0700 either way.
	if err := os.Chmod(w.StateDir(), 0o700); err != nil { //nolint:gosec // directories need the execute bit; 0700 is owner-only
		return Workspace{}, fmt.Errorf("securing state dir: %w", err)
	}
	ignore := filepath.Join(w.GummiDir(), ".gitignore")
	if err := writeIfAbsent(ignore, gummiIgnore); err != nil {
		return Workspace{}, err
	}
	if err := writeIfAbsent(w.SeqFile(), "0\n"); err != nil {
		return Workspace{}, err
	}
	return w, nil
}

// mkdirChecked creates a gummi directory unless it already exists, but
// first refuses a symlink or non-directory sitting at that path — the
// committed-symlink escape a bare MkdirAll would silently follow. The
// caller creates parents before children, so a verified parent means only
// the leaf component here can be hostile.
func mkdirChecked(path string, perm os.FileMode) error {
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink; refusing to use a redirected gummi directory", path)
		}
		if !fi.IsDir() {
			return fmt.Errorf("%s exists and is not a directory", path)
		}
		return nil // already a real directory
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("inspecting %s: %w", path, err)
	}
	if err := os.MkdirAll(path, perm); err != nil {
		return fmt.Errorf("creating %s: %w", path, err)
	}
	return nil
}

// writeIfAbsent creates path with content unless it already exists.
// Stat errors other than not-exist are real failures and propagate.
func writeIfAbsent(path, content string) error {
	_, err := os.Stat(path)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrNotExist):
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			return fmt.Errorf("writing %s: %w", path, err)
		}
		return nil
	default:
		return fmt.Errorf("checking %s: %w", path, err)
	}
}
