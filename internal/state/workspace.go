package state

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/gummidir"
)

// Workspace locates gummi's on-disk layout inside one repository.
// gummi always runs from the main checkout; Root is that checkout's
// root directory.
type Workspace struct {
	// Root is where .gummi lives — the workspace root. The managed git
	// repository may live here too (sibling layout) or in a nested
	// subdirectory (RepoRoot).
	Root string
	// RepoRoot is the git repository root gummi manages. Equal to Root in
	// the sibling layout; a nested subdirectory of Root otherwise.
	RepoRoot string
}

// GummiDir is the repo-committed gummi directory.
//
// It is $GUMMI_DIR when that is set, else Root/.gummi.
func (w Workspace) GummiDir() string { return gummidir.Resolve(w.Root) }

// StateDir holds machinery (SQLite DB, drafts). Never committed; mode 0700.
func (w Workspace) StateDir() string { return filepath.Join(w.GummiDir(), "state") }

// DraftsDir holds spec drafts before a feature has a worktree.
func (w Workspace) DraftsDir() string { return filepath.Join(w.StateDir(), "drafts") }

// WebDir holds `gummi web`'s machinery: the paired-device tokens and the
// running server's address file. It sits under StateDir rather than beside
// the specs because everything in it is a secret or a per-machine
// artifact — a repository that commits its .gummi must not carry the keys
// to somebody's board.
func (w Workspace) WebDir() string { return filepath.Join(w.StateDir(), "web") }

// CredentialsDir holds the GitHub token and SSH key a person stored from
// the web page's settings (internal/credentials). Under StateDir for
// WebDir's reason: a repository that commits its .gummi must not carry
// them.
func (w Workspace) CredentialsDir() string { return filepath.Join(w.StateDir(), "credentials") }

// HooksDir holds the git hooks gummi names to git in its own environment
// (worktree.UseCoAuthorHooks), never a repository's.
func (w Workspace) HooksDir() string { return filepath.Join(w.StateDir(), "hooks") }

// SpecsDir holds approved specs — the artifact's workspace home from
// spec approval on. Workspace content, never committed.
func (w Workspace) SpecsDir() string { return filepath.Join(w.GummiDir(), "specs") }

// BugsDir holds bug reports, the bug-workflow analog of SpecsDir.
func (w Workspace) BugsDir() string { return filepath.Join(w.GummiDir(), "bugs") }

// WorktreesDir holds the nested per-feature git worktrees.
func (w Workspace) WorktreesDir() string { return filepath.Join(w.GummiDir(), "worktrees") }

// AttachmentsDir holds uploaded images, content-addressed by sha256
// (internal/attachment). Workspace content, never committed — the same
// `.gummi/attachments/<id>.<ext>` path a spec-anchored link names, always
// relative to Root.
func (w Workspace) AttachmentsDir() string { return filepath.Join(w.GummiDir(), "attachments") }

// SeqFile is the FD-NNN monotonic counter.
func (w Workspace) SeqFile() string { return filepath.Join(w.GummiDir(), "seq") }

// ScratchFilesDir is where a card's sessions put throwaway files — the
// prototype program a plan session writes to check an assumption, the
// probe a critique compiles to confirm a value.
//
// It exists because the instruction alone did not hold. The boundary hint
// has always said to keep scratch out of the worktree (a prototype left
// in the tree ships with the stage's work) and to use "a temporary
// directory of your own (mktemp -d)" — and sessions wrote /tmp/t.go,
// /tmp/t2.go, /tmp/t3.go, /tmp/roundtest.go instead, which is fine until
// two cards on one machine pick the same name and read each other's
// program. Naming a real per-card directory in the hint replaces advice
// with an address.
//
// Under .gummi (gitignored, never committed, cleaned with the card) and
// distinct from ScratchDir, which is a research card's whole detached
// checkout rather than a place to drop a file.
func (w Workspace) ScratchFilesDir(id domain.FeatureID) string {
	return filepath.Join(w.StateDir(), "scratch", string(id))
}

// HomesDir holds the agents' homes, one directory per agent card (DESIGN
// §19.3d). Workspace content, never committed, and never cleaned with the
// card: what an agent built for itself is not a cleanup's to discard.
func (w Workspace) HomesDir() string { return filepath.Join(w.GummiDir(), "homes") }

// HomeDir is one agent's home: the directory its sessions run in.
func (w Workspace) HomeDir(id domain.FeatureID) string {
	return filepath.Join(w.HomesDir(), string(id))
}

// MemoryDir holds freeform cards' project memory: the global memory file
// every freeform session here reads, and one subdirectory per card
// holding that card's own session memory. Workspace content, never
// committed — like specs/, and for a sharper reason: a memory tier that
// lived on branches would fork per branch and conflict across the
// concurrent cards it exists to connect.
func (w Workspace) MemoryDir() string { return filepath.Join(w.GummiDir(), "memory") }

// GlobalMemoryFile is the global memory file: the tier every freeform
// session in this workspace is handed at spawn.
func (w Workspace) GlobalMemoryFile() string {
	return filepath.Join(w.MemoryDir(), "global.md")
}

// SessionMemoryDir is one freeform card's own memory directory (its
// memory and dead-ends). Keyed by card, so a session's memory is its own
// and outlives its worktree: a card whose tree was cleaned still has what
// it learned, and no card ever has another's.
func (w Workspace) SessionMemoryDir(id domain.FeatureID) string {
	return filepath.Join(w.MemoryDir(), string(id))
}

// SessionMemoryFile is the card's own working-notes file inside its
// session memory directory. It was named plan.md until the memory tier
// carried one name end to end; LegacyPlanRename moves an old file aside
// on first touch.
func (w Workspace) SessionMemoryFile(id domain.FeatureID) string {
	return filepath.Join(w.SessionMemoryDir(id), "memory.md")
}

// LegacyPlanRename migrates a card's session memory from its former file
// name, plan.md, to the current one: it is what a reader or writer that
// finds memory.md absent and plan.md present calls before touching the
// file, so existing cards keep their working notes under the renamed
// name without a migration pass.
//
// The rename is no-replace by construction, not by check: os.Link lands
// a second name on the old file's inode — atomic, and it fails with
// EEXIST when memory.md already exists — and only then is plan.md
// removed. A checked os.Rename would be check-then-act and would replace
// the target on collision: a web GET could find memory.md absent, watch
// a session's write land it, and clobber the fresh write. No lock is
// what makes this safe to call from anywhere: the link replaces nothing,
// so a concurrent write always wins, and EEXIST (already migrated, or a
// fresh write won the race) and ENOENT (nothing to migrate) are both
// benign — not renamed, nil error. A crash between link and remove
// leaves both files; the next touch reads EEXIST as already-migrated and
// the stray plan.md is ignored.
func (w Workspace) LegacyPlanRename(id domain.FeatureID) (bool, error) {
	old := filepath.Join(w.SessionMemoryDir(id), "plan.md")
	next := w.SessionMemoryFile(id)
	err := os.Link(old, next)
	switch {
	case err == nil:
		// the link landed on the absent target: drop the old name, best
		// effort — a failed remove is the stray-plan.md case above
		_ = os.Remove(old)
		return true, nil
	case errors.Is(err, fs.ErrExist), errors.Is(err, fs.ErrNotExist):
		return false, nil
	default:
		return false, fmt.Errorf("migrating %s to %s: %w", old, next, err)
	}
}

// ConfigFile is the repo-controlled config (verify checks, permissions).
func (w Workspace) ConfigFile() string { return filepath.Join(w.GummiDir(), "config.yaml") }

// ProfilesFile maps roles to models per profile.
func (w Workspace) ProfilesFile() string { return filepath.Join(w.GummiDir(), "profiles.yaml") }

// DBFile is the SQLite state store.
func (w Workspace) DBFile() string { return filepath.Join(w.StateDir(), "gummi.db") }

// LiveFile is where the process driving card id mirrors its live agent
// stream — the transcript deltas, tool calls, and state changes that
// otherwise never leave that process's memory — one JSON object per line
// (internal/livelog). A second gummi (a watching board, `gummi watch`)
// tails it to render the run it does not own. Unlike EventsFile it is
// per-card and truncated by each new session: it is the current view,
// not the history, which stays in the store.
func (w Workspace) LiveFile(id domain.FeatureID) string {
	return filepath.Join(w.StateDir(), "live", fmt.Sprintf("%s.jsonl", id))
}

// EventsFile mirrors the driver's NDJSON stream, one JSON object per line,
// append-only. It lives beside the pid file so a caller whose wrapper died
// mid-run can tail progress off disk instead of a lost stdout pipe.
func (w Workspace) EventsFile() string { return filepath.Join(w.StateDir(), "events.jsonl") }

// gitignore rules written by Init: the managed checkouts (worktrees, and
// the scratch trees the pre-worktree stages run in) and state are
// machinery and must never be committed (state may contain transcripts).
// Belt-and-braces: the launch pass also excludes /.gummi/ wholesale in
// the repo's info/exclude, which is what covers a workspace predating any
// line added here.
const gummiIgnore = `# written by gummi — machinery, never commit
/worktrees/
/scratch/
/state/
/seq.lock
/seq.tmp
`

// Open returns the Workspace rooted at root, requiring that a board has
// already created it there.
func Open(ws, repo string) (Workspace, error) {
	w := Workspace{Root: ws, RepoRoot: repo}
	fi, err := os.Lstat(w.GummiDir())
	if err != nil {
		return Workspace{}, fmt.Errorf("no gummi directory %s (run `gummi` there first): %w", ws, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return Workspace{}, fmt.Errorf("%s is a symlink; refusing to use a redirected gummi directory", w.GummiDir())
	}
	return w, nil
}

// ErrNestedInit marks an Init attempt whose root lives inside another
// workspace's managed worktree. It is a distinct sentinel so callers and
// tests can recognize the refusal without string matching.
var ErrNestedInit = errors.New("refusing to init inside a managed worktree")

// managedTreeDirs are the .gummi children that hold a card's managed
// checkouts: its branch worktree, and the scratch tree its pre-worktree
// stages run in. Both are gummi-owned working directories an agent is
// handed, so both are places a nested workspace must never be initialized.
var managedTreeDirs = []string{"worktrees", "scratch"}

// enclosingWorkspace walks from root toward the filesystem root and
// reports the first managed checkout (parent workspace root P and entry
// <id>) that contains (or equals) root. The walk starts at
// filepath.Dir(root) so that root itself — where .gummi/ sits as a direct
// child, not inside any managed tree — is never inspected. Each ancestor
// .gummi/ and each managed tree directory must be a real directory (not a
// symlink), matching the anti-symlink-smuggle convention of MkdirChecked.
func enclosingWorkspace(root string) (parent, worktreeID string, ok bool) {
	for p := filepath.Dir(root); ; p = filepath.Dir(p) {
		if fi, err := os.Lstat(gummidir.Resolve(p)); err != nil || fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
			if p == filepath.VolumeName(p) || filepath.Dir(p) == p {
				return "", "", false
			}
			continue
		}
		for _, sub := range managedTreeDirs {
			base := gummidir.Path(p, sub)
			// An absent one is normal — a workspace whose cards have never
			// reached a worktree has no worktrees/ — so skip it and keep
			// checking the others rather than abandoning the walk.
			if fi, err := os.Lstat(base); err != nil || fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
				continue
			}
			entries, err := os.ReadDir(base)
			if err != nil {
				return "", "", false
			}
			for _, e := range entries {
				id := e.Name()
				dir := filepath.Join(base, id)
				if rel, err := filepath.Rel(dir, root); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
					return p, id, true
				}
			}
		}
		if p == filepath.VolumeName(p) || filepath.Dir(p) == p {
			return "", "", false
		}
	}
}

// Init creates the .gummi skeleton in ws (the workspace root). The managed
// repository repo must be the top of a git repository (worktrees and
// branches make no sense elsewhere) unless it is empty — the repos:-only
// layout, where there is no default repo and the workspace root is a mere
// parent of checkouts. It may equal ws or sit in a nested subdirectory.
// Init is idempotent: existing files, and in particular an existing seq
// counter, are never clobbered.
func Init(ws, repo string) (Workspace, error) {
	w := Workspace{Root: ws, RepoRoot: repo}
	// Refuse to materialize a workspace inside another workspace's managed
	// worktree: such an init would nest and silently mask the real
	// workspace for any future command that runs there. This check runs
	// before the .git check because a nested worktree has a .git
	// gitdir-pointer file that would otherwise pass and mask the more
	// specific diagnosis.
	if parent, id, ok := enclosingWorkspace(ws); ok {
		return Workspace{}, fmt.Errorf("%s is inside %s's worktree %s; run gummi from %s instead: %w", ws, parent, id, parent, ErrNestedInit)
	}
	// .git is a directory in a normal checkout and a gitdir-pointer
	// file in worktrees and submodules; both are valid repo roots. The
	// empty repo marks a repos:-only workspace with no default: there is
	// no single repo to validate, so the check is skipped (the workspace
	// root is a mere parent of checkouts in that layout).
	if repo != "" {
		if _, err := os.Stat(filepath.Join(repo, ".git")); err != nil {
			return Workspace{}, fmt.Errorf("%s is not the root of a git repository (no .git); gummi manages worktrees and must run from the main checkout", repo)
		}
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
	}
	for _, d := range dirs {
		if err := MkdirChecked(d.path, d.perm); err != nil {
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

// MkdirChecked creates a gummi directory unless it already exists, but
// first refuses a symlink or non-directory sitting at that path — the
// committed-symlink escape a bare MkdirAll would silently follow. The
// caller creates parents before children, so a verified parent means only
// the leaf component here can be hostile.
func MkdirChecked(path string, perm os.FileMode) error {
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
