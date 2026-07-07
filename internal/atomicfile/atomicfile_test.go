package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "spec.md")
	if err := Write(p, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Errorf("content = %q, want hello", got)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
	// overwriting replaces cleanly
	if err := Write(p, []byte("world!"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(p); string(got) != "world!" {
		t.Errorf("after overwrite = %q, want world!", got)
	}
	// no leftover temp files
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("dir has %d entries, want 1 (a temp file leaked)", len(entries))
	}
}

// TestWriteReplacesSymlink verifies the rename replaces a symlink sitting at
// the destination rather than following it and writing through to its
// target — the containment property CommitFile relies on.
func TestWriteReplacesSymlink(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(dir, "outside.txt")
	if err := os.WriteFile(outside, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.md")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if err := Write(link, []byte("new content"), 0o600); err != nil {
		t.Fatal(err)
	}
	// the symlink target must be untouched
	if got, _ := os.ReadFile(outside); string(got) != "original" {
		t.Errorf("symlink target was written through: %q", got)
	}
	// link is now a regular file with the new content
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Error("destination is still a symlink; rename followed it")
	}
	if got, _ := os.ReadFile(link); string(got) != "new content" {
		t.Errorf("destination content = %q, want 'new content'", got)
	}
}
