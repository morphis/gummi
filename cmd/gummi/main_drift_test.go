package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// repoRoot walks up from the test's working directory (the package dir) to
// the module root, so a test can reach the repo's docs and sources without
// depending on where `go test` was invoked from.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root (go.mod) above the test working directory")
		}
		dir = parent
	}
}

// envTableVars parses the first column of every row in the
// docs/CONFIGURATION.md "Environment variables" table. The README carries
// only the handful a first user meets and points here for the rest, so this
// table is the one that must stay complete. A cell may name several
// variables (`GUMMI_CLAUDE_BIN`, `GUMMI_CODEX_BIN`, …); each backticked
// span counts on its own.
func envTableVars(t *testing.T) map[string]bool {
	t.Helper()
	dir := repoRoot(t)
	s, err := os.ReadFile(filepath.Join(dir, "docs", "CONFIGURATION.md"))
	if err != nil {
		t.Fatalf("read docs/CONFIGURATION.md: %v", err)
	}
	inTable := false
	seen := map[string]bool{}
	for _, line := range strings.Split(string(s), "\n") {
		if strings.HasPrefix(line, "## ") {
			inTable = strings.TrimLeft(line, "# ") == "Environment variables"
			continue
		}
		if !inTable || !strings.HasPrefix(line, "|") {
			continue
		}
		cell := strings.Split(strings.Trim(line, "|"), "|")[0]
		for _, m := range envCellRe.FindAllStringSubmatch(cell, -1) {
			seen[m[1]] = true
		}
	}
	return seen
}

var envCellRe = regexp.MustCompile("`(GUMMI_[A-Z0-9_]+)`")

// nonOperatorVars are the GUMMI_ names that appear in non-test source but
// are NOT operator configuration, so the doc table is not expected to carry
// them. This is the only way to opt out of the coverage check below, so
// every entry carries the reason it is excused.
var nonOperatorVars = map[string]string{
	"GUMMI_GH_CMD":   "test seam for swapping the gh binary (pr/gh.go)",
	"GUMMI_MCP_SOCK": "internal handoff: gummi sets it on a child it spawns so the child can dial back; nobody sets it from outside",
}

// sourceEnvVars scans the non-test Go sources under cmd/ and internal/ for
// quoted GUMMI_ names, which is every env var the shipped binary can read.
//
// It deliberately DERIVES the set instead of reading a hand-kept list. A
// curated allowlist can only prove that the names someone remembered to add
// are documented — it says nothing about a var added later, which is the
// only drift that actually happens. The list this replaced had gone stale
// exactly that way: GUMMI_MOTION and GUMMI_REVIEW_DIFF_MAX were both
// operator-facing, both documented, and neither was guarded by anything.
//
// Test files are excluded so a var a test sets but the binary never reads
// cannot demand documentation for a knob that does not exist —
// GUMMI_ALLOW_NESTED is precisely that: TestInitNestingHasNoOverride sets it
// to prove no such override exists, and documenting it would advertise an
// escape hatch gummi does not have.
func sourceEnvVars(t *testing.T, root string) map[string]bool {
	t.Helper()
	found := map[string]bool{}
	for _, dir := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, m := range quotedEnvRe.FindAllStringSubmatch(string(b), -1) {
				found[m[1]] = true
			}
			return nil
		})
		if err != nil {
			t.Fatalf("scanning %s for env vars: %v", dir, err)
		}
	}
	return found
}

// quotedEnvRe matches a GUMMI_ name as it appears in source: a quoted
// string literal. Unquoted mentions in comments are prose about a var, not
// a read of one.
var quotedEnvRe = regexp.MustCompile(`"(GUMMI_[A-Z0-9_]+)"`)

// TestConfigDocEnvCoversOperatorVars proves every operator-facing GUMMI_
// var the binary reads appears in the docs/CONFIGURATION.md environment
// table — deriving that set from the sources rather than from a list that
// has to be remembered. A new var is covered the moment it is written.
func TestConfigDocEnvCoversOperatorVars(t *testing.T) {
	documented := envTableVars(t)
	if len(documented) == 0 {
		t.Fatal("no environment table found in docs/CONFIGURATION.md; the heading moved or the table is gone")
	}
	root := repoRoot(t)
	inSource := sourceEnvVars(t, root)
	if len(inSource) == 0 {
		t.Fatal("scanned the sources and found no GUMMI_ env vars at all; the scan is broken, not the docs")
	}
	for v := range inSource {
		if _, skip := nonOperatorVars[v]; skip {
			continue
		}
		if !documented[v] {
			t.Errorf("env var %s is read by the binary but missing from the docs/CONFIGURATION.md environment table "+
				"(if it is not operator configuration, add it to nonOperatorVars with a reason)", v)
		}
	}
	// The opt-out list must not outlive the code it excuses, or it quietly
	// becomes a way to hide a var from the check.
	for v := range nonOperatorVars {
		if !inSource[v] {
			t.Errorf("nonOperatorVars still excuses %s, which no non-test source reads any more; drop the entry", v)
		}
	}
}
