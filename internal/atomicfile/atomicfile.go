// Package atomicfile writes a file in one atomic step: the content goes to
// a temp file in the destination directory, is flushed to disk, then
// renamed over the target. A crash or power loss leaves either the old
// file or the complete new one, never a torn half-write — the failure mode
// the drafts/artifacts under .gummi have no git backstop against before
// approval. The rename also replaces a symlink sitting at the destination
// rather than following it and writing through to its target.
package atomicfile

import (
	"os"
	"path/filepath"
)

// Write atomically writes data to path with the given permissions. The
// temp file lives in the same directory as path so the final rename is a
// same-filesystem metadata operation (cross-device rename would fail).
func Write(path string, data []byte, perm os.FileMode) (err error) {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	// If anything below fails before the rename, don't leave the temp file
	// behind. After a successful rename tmp no longer exists and this is a
	// harmless no-op.
	defer func() {
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()

	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Chmod(perm); err != nil {
		_ = f.Close()
		return err
	}
	// flush the bytes before the rename so the renamed file is never empty
	// after a crash.
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	// fsync the directory so the rename itself survives power loss.
	if d, derr := os.Open(dir); derr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
