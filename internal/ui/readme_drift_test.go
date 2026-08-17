package ui

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/ui/theme"
)

// readmePath locates the repo-root README.md by walking up from the test's
// working directory (the package dir) to the module root.
func readmePath(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "README.md")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root (go.mod) above the test working directory")
		}
		dir = parent
	}
}

// keyTokenRe extracts the individual key tokens from a key-string like
// "j/k ↓↑", "1..9", "s" or "pgup/pgdn". It is deliberately lenient so the
// hand-written README prose and the keymap table can be compared token by
// token (case preserved: "P" and "p" are distinct keys).
var keyTokenRe = regexp.MustCompile(`[A-Za-z0-9?]+`)

func keyTokens(s string) []string { return keyTokenRe.FindAllString(s, -1) }

// readmeBoardKeyTokens parses the first column of every row in the README
// "Key surfaces on the board" table.
func readmeBoardKeyTokens(t *testing.T) map[string]bool {
	t.Helper()
	s, err := os.ReadFile(readmePath(t))
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	inTable := false
	seen := map[string]bool{}
	for _, line := range strings.Split(string(s), "\n") {
		if strings.HasPrefix(line, "## ") {
			inTable = false
		}
		if strings.HasPrefix(line, "Key surfaces on the board") {
			inTable = true
			continue
		}
		if !inTable || !strings.HasPrefix(line, "|") {
			continue
		}
		cell := strings.Split(strings.Trim(line, "|"), "|")[0]
		for _, tok := range keyTokens(cell) {
			seen[tok] = true
		}
	}
	return seen
}

// boardBindingKeyTokens collects the key tokens declared by the board's
// binding table, the single source of truth for what the board answers to.
func boardBindingKeyTokens(m *Shell) map[string]bool {
	seen := map[string]bool{}
	for _, b := range m.boardBindings() {
		for _, tok := range keyTokens(b.key) {
			seen[tok] = true
		}
	}
	return seen
}

// TestReadmeBoardKeysCoverBindings proves the hand-written README board-key
// table cannot silently drift from the real board bindings: every key the
// board answers to must appear there, except the global keys (? and q) that
// the README covers in prose ("press `?` anywhere for the full table").
func TestReadmeBoardKeysCoverBindings(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0.1.0-test")
	readme := readmeBoardKeyTokens(t)
	for key := range boardBindingKeyTokens(m) {
		if key == "?" || key == "q" {
			continue
		}
		if !readme[key] {
			t.Errorf("board binding key %q is missing from the README board-key table", key)
		}
	}
}
