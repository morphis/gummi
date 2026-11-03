// Package gummidir answers where a workspace keeps its gummi directory:
// <root>/.gummi by default, or the directory $GUMMI_DIR names. It is a leaf
// so that the store, the worktree manager, the recipe loader and the
// commands all ask the one place.
package gummidir

import (
	"os"
	"path/filepath"
	"strings"
)

// Env is the environment variable that points gummi at a gummi directory of
// its own: any path, inside the repository or not, whatever it is called.
const Env = "GUMMI_DIR"

// Name is the directory's name when Env is unset.
const Name = ".gummi"

// Custom reports the absolute directory Env names, if it is set.
func Custom() (string, bool) {
	v := strings.TrimSpace(os.Getenv(Env))
	if v == "" {
		return "", false
	}
	if abs, err := filepath.Abs(v); err == nil {
		v = abs
	}
	return filepath.Clean(v), true
}

// Resolve is the gummi directory of the workspace rooted at root.
func Resolve(root string) string {
	if d, ok := Custom(); ok {
		return d
	}
	return filepath.Join(root, Name)
}

// Path joins elem onto the gummi directory of the workspace at root.
func Path(root string, elem ...string) string {
	return filepath.Join(append([]string{Resolve(root)}, elem...)...)
}

// FromRel resolves a slash path stored relative to the workspace root, such
// as ".gummi/specs/FF-1/1-x.md", to a file on disk: a path under .gummi
// follows the gummi directory, any other joins onto root as it always did.
func FromRel(root, rel string) string {
	rel = filepath.ToSlash(rel)
	if rest, ok := strings.CutPrefix(rel, Name+"/"); ok {
		return Path(root, filepath.FromSlash(rest))
	}
	return filepath.Join(root, filepath.FromSlash(rel))
}
