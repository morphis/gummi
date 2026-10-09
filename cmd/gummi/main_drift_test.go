package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
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
	"GUMMI_GH_CMD":                 "test seam for swapping the gh binary (pr/gh.go)",
	"GUMMI_WEB_PUSH_ALLOW_PRIVATE": "test seam: lets gummi web accept and dial push endpoints on loopback/private addresses, for the e2e suite's stand-in push service",
	"GUMMI_SPAWNED":                "internal marker: gummi sets it on everything it starts so the publish verbs can refuse inside a session; nobody sets it from outside",
	"GUMMI_MCP_SOCK":               "internal handoff: gummi sets it on a child it spawns so the child can dial back; nobody sets it from outside",
	"GUMMI_TREE_":                  "a prefix gummi SETS for an experiment's commands (GUMMI_TREE_<REPO>), never reads; documented with the experiments key",
	"GUMMI_HEAD_":                  "a prefix gummi SETS for an experiment's commands (GUMMI_HEAD_<REPO>), never reads; documented with the experiments key",
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

// --- docs vs. the real command surface --------------------------------

// gummiInvocationRe matches a `gummi …` command line as the docs write one:
// everything from the word to the end of the line or the closing backtick.
var gummiInvocationRe = regexp.MustCompile("\\bgummi\\s+[^\\n`]*")

// argumentRe strips the parts of an invocation that are a VALUE rather than
// a flag of gummi's — a quoted description, a `<placeholder>` — so a
// documented `gummi run "Add a --format=json flag"` is not read as a claim
// that gummi has a --format flag. It is exactly the flag of the command the
// example asks gummi to build.
var argumentRe = regexp.MustCompile(`"[^"]*"|'[^']*'|<[^>]*>`)

var longFlagRe = regexp.MustCompile(`--([a-z][a-z0-9-]*[a-z0-9])\b`)

// declaredFlags is every long flag anywhere on the cobra tree.
func declaredFlags() map[string]bool {
	out := map[string]bool{}
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		c.Flags().VisitAll(func(f *pflag.Flag) { out[f.Name] = true })
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(rootCmd)
	return out
}

// TestDocsNameNoFlagTheBinaryLacks reads every `gummi …` invocation in the
// user-facing docs and proves each flag it types is one the binary really
// accepts.
//
// A doc that tells a reader to type a flag gummi rejects is worse than one
// that omits it: the reader follows it and gets "unknown flag". That was
// live — the generated skill grammar documented `gummi merge --m`, which
// the binary has never accepted, because the grammar was built from a
// second set of flag declarations that nothing kept in step with the set
// cobra parses. There is one set now, and this keeps the hand-written
// prose honest about it too.
func TestDocsNameNoFlagTheBinaryLacks(t *testing.T) {
	known := declaredFlags()
	root := repoRoot(t)
	for _, doc := range []string{
		"README.md",
		filepath.Join("docs", "HEADLESS.md"),
		filepath.Join("docs", "CONFIGURATION.md"),
		"AGENTS.md",
	} {
		b, err := os.ReadFile(filepath.Join(root, doc))
		if err != nil {
			t.Fatalf("reading %s: %v", doc, err)
		}
		for _, inv := range gummiInvocationRe.FindAllString(string(b), -1) {
			for _, m := range longFlagRe.FindAllStringSubmatch(argumentRe.ReplaceAllString(inv, " "), -1) {
				if !known[m[1]] {
					t.Errorf("%s documents `%s`, but no gummi command declares --%s",
						doc, strings.TrimSpace(inv), m[1])
				}
			}
		}
	}
}
