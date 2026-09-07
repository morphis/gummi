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

// backtickRe picks the `key` spans out of a README table cell, so the prose
// around them ("or", the header row's "key") is never mistaken for a key.
var backtickRe = regexp.MustCompile("`([^`]+)`")

func readmeCellKeyTokens(cell string) []string {
	var out []string
	for _, m := range backtickRe.FindAllStringSubmatch(cell, -1) {
		out = append(out, keyTokens(m[1])...)
	}
	return out
}

// readmeBoardKeyTokens parses the first column of every row in the README
// "The keys you need first" table.
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
		if strings.HasPrefix(line, "The keys you need first") {
			inTable = true
			continue
		}
		if !inTable || !strings.HasPrefix(line, "|") {
			continue
		}
		cell := strings.Split(strings.Trim(line, "|"), "|")[0]
		for _, tok := range readmeCellKeyTokens(cell) {
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

// TestReadmeBoardKeysAreRealBindings proves the hand-written README key
// table cannot name a key the board no longer answers to. The table is a
// deliberate subset — the README points at `?` for the full table, which is
// rendered from the bindings themselves — so the check runs README → keymap,
// never the other way round.
func TestReadmeBoardKeysAreRealBindings(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0.1.0-test")
	readme := readmeBoardKeyTokens(t)
	if len(readme) == 0 {
		t.Fatal("no key table found under \"The keys you need first\" in the README; the heading moved or the table is gone")
	}
	bindings := boardBindingKeyTokens(m)
	for key := range readme {
		if key == "?" {
			continue
		}
		if !bindings[key] {
			t.Errorf("README key table names %q, which is not a board binding", key)
		}
	}
}
