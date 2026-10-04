package rmtree

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRemoveAllTakesAReadOnlyTree: the Go module cache shape — 0555
// directories over 0444 files — defeats os.RemoveAll and must not defeat
// RemoveAll.
func TestRemoveAllTakesAReadOnlyTree(t *testing.T) {
	root := filepath.Join(t.TempDir(), "home")
	mod := filepath.Join(root, "go", "pkg", "mod", "x@v1")
	if err := os.MkdirAll(mod, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mod, "a.go"), []byte("package x\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(root, "locked")
	if err := os.MkdirAll(filepath.Join(locked, "inner"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{mod, filepath.Dir(mod), filepath.Join(locked, "inner")} {
		if err := os.Chmod(d, 0o555); err != nil {
			t.Fatal(err)
		}
	}
	// a directory not even the owner can list
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = RemoveAll(root) })

	if err := os.RemoveAll(root); err == nil {
		t.Skip("os.RemoveAll took the read-only tree (running as root?); nothing to prove")
	}
	if err := RemoveAll(root); err != nil {
		t.Fatalf("RemoveAll = %v, want the read-only tree removed", err)
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatalf("tree still present after RemoveAll: %v", err)
	}
}

// TestRemoveAllNeverChmodsALinkTarget: a symlink inside the tree points
// at a directory outside it; removing the tree leaves the target and its
// mode alone.
func TestRemoveAllNeverChmodsALinkTarget(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "skill")
	if err := os.MkdirAll(outside, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(outside, 0o700) })
	root := filepath.Join(t.TempDir(), "home")
	ro := filepath.Join(root, "ro")
	if err := os.MkdirAll(ro, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ro, "f"), nil, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ro, 0o555); err != nil {
		t.Fatal(err)
	}

	if err := RemoveAll(root); err != nil {
		t.Fatalf("RemoveAll = %v", err)
	}
	info, err := os.Stat(outside)
	if err != nil {
		t.Fatalf("link target gone: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o500 {
		t.Errorf("link target mode = %o, want 0500 untouched", got)
	}
}

func TestRemoveAllAbsentIsSuccess(t *testing.T) {
	if err := RemoveAll(filepath.Join(t.TempDir(), "nope")); err != nil {
		t.Fatalf("RemoveAll(absent) = %v, want nil", err)
	}
}
