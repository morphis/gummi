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

// SpecsDir holds approved specs (committed with the feature branch).
func (w Workspace) SpecsDir() string { return filepath.Join(w.GummiDir(), "specs") }

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
	if _, err := os.Stat(w.GummiDir()); err != nil {
		return Workspace{}, fmt.Errorf("no .gummi directory in %s (run `gummi init` first): %w", root, err)
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
	if err := os.MkdirAll(w.GummiDir(), 0o750); err != nil {
		return Workspace{}, fmt.Errorf("creating .gummi: %w", err)
	}
	if err := os.MkdirAll(w.StateDir(), 0o700); err != nil {
		return Workspace{}, fmt.Errorf("creating state dir: %w", err)
	}
	// A cloned repo can ship .gummi or .gummi/state as a committed
	// symlink pointing elsewhere; MkdirAll follows it silently. Refuse:
	// gummi must only ever write inside the repo's own .gummi.
	for _, p := range []string{w.GummiDir(), w.StateDir()} {
		fi, err := os.Lstat(p)
		if err != nil {
			return Workspace{}, fmt.Errorf("inspecting %s: %w", p, err)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return Workspace{}, fmt.Errorf("%s is a symlink; refusing to use a redirected gummi directory", p)
		}
	}
	// The dir may predate this run with looser permissions; state can
	// hold transcripts, so enforce 0700 either way.
	if err := os.Chmod(w.StateDir(), 0o700); err != nil { //nolint:gosec // directories need the execute bit; 0700 is owner-only
		return Workspace{}, fmt.Errorf("securing state dir: %w", err)
	}
	for _, dir := range []string{w.DraftsDir(), w.SpecsDir(), w.WorktreesDir(), w.IngestDir()} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return Workspace{}, fmt.Errorf("creating %s: %w", dir, err)
		}
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
