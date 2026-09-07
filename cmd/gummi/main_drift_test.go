package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// envTableVars parses the first column of every row in the
// docs/CONFIGURATION.md "Environment variables" table. The README carries
// only the handful a first user meets and points here for the rest, so this
// table is the one that must stay complete. A cell may name several
// variables (`GUMMI_CLAUDE_BIN`, `GUMMI_CODEX_BIN`, …); each backticked
// span counts on its own.
func envTableVars(t *testing.T) map[string]bool {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root (go.mod) above the test working directory")
		}
		dir = parent
	}
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

// operatorVars is the curated set of operator-facing GUMMI_ vars that
// gummi reads. Every one must be documented in the configuration doc's env
// table so the hand-written table cannot drift from what the binary
// actually reads. Dev-only/_TEST and internal socket vars are intentionally
// excluded.
var operatorVars = []string{
	"GUMMI_AGENT",
	"GUMMI_AGENT_CMD",
	"GUMMI_CLAUDE_BIN",
	"GUMMI_CODEX_BIN",
	"GUMMI_OPENCODE_BIN",
	"GUMMI_ZZ_BIN",
	"GUMMI_HEADLESS_CREDITS_PER_1K",
	"GUMMI_ZZ_CREDITS_PER_1K",
	"GUMMI_ZZ_MAX_TURNS",
	"GUMMI_MODEL",
	"GUMMI_MAX_ACTIVE",
	"GUMMI_ENVELOPE",
	"GUMMI_STAGE_BUDGET",
	"GUMMI_TURN_RESERVE",
	"GUMMI_COPILOT_HINT",
	"GUMMI_THEME",
	"GUMMI_NOTIFY",
	"GUMMI_ATTACH_CMD",
}

// TestConfigDocEnvCoversOperatorVars proves every operator-facing GUMMI_
// var the binary reads appears in the docs/CONFIGURATION.md environment
// table.
func TestConfigDocEnvCoversOperatorVars(t *testing.T) {
	documented := envTableVars(t)
	if len(documented) == 0 {
		t.Fatal("no environment table found in docs/CONFIGURATION.md; the heading moved or the table is gone")
	}
	for _, v := range operatorVars {
		if !documented[v] {
			t.Errorf("operator-facing env var %s is missing from the docs/CONFIGURATION.md environment table", v)
		}
	}
}
