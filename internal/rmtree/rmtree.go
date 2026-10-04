// Package rmtree removes a directory tree that may contain read-only
// entries.
//
// os.RemoveAll cannot unlink a file inside a directory the caller has no
// write permission on, and some tools leave exactly that behind on
// purpose: Go's module cache is 0555 directories over 0444 files, so a
// per-card home an agent ran `go build` under refuses to go away. The
// card is gone, the tree stays, and the cleanup reports a failure (or, on
// a best-effort path, silently leaks a tree that may still hold a
// credential copy).
package rmtree

import (
	"io/fs"
	"os"
	"path/filepath"
)

// RemoveAll is os.RemoveAll that also takes a tree with read-only
// directories in it: when the plain removal fails, every directory under
// path is made owner-writable and the removal runs again. Symbolic links
// are never followed (filepath.WalkDir does not follow them), so a link
// out of the tree — a skill directory linked into an agent home — never
// has its target's mode touched. Absent is success, as with os.RemoveAll.
func RemoveAll(path string) error {
	if err := os.RemoveAll(path); err == nil {
		return nil
	}
	// WalkDir calls fn on a directory before reading it, so a 0000
	// directory is opened up before the walk descends into it.
	_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil //nolint:nilerr // best effort: the second RemoveAll reports what is still in the way
		}
		if info, err := d.Info(); err == nil {
			_ = os.Chmod(p, info.Mode().Perm()|0o700) //nolint:gosec // the tree is being deleted; owner rwx is only what unlinking its entries needs
		}
		return nil
	})
	return os.RemoveAll(path)
}
