package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readmeEnvVars parses the first column of every row in the README
// "Environment variables" table.
func readmeEnvVars(t *testing.T) map[string]bool {
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
	s, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	inTable := false
	seen := map[string]bool{}
	for _, line := range strings.Split(string(s), "\n") {
		if strings.HasPrefix(line, "## ") {
			inTable = false
		}
		if strings.HasPrefix(line, "Environment variables") {
			inTable = true
			continue
		}
		if !inTable || !strings.HasPrefix(line, "|") {
			continue
		}
		cell := strings.TrimSpace(strings.Split(strings.Trim(line, "|"), "|")[0])
		seen[strings.Trim(cell, "`")] = true
	}
	return seen
}

// operatorVars is the curated set of operator-facing GUMMI_ vars that
// cmd/gummi reads. Every one must be documented in the README env table so
// the hand-written table cannot drift from what the binary actually reads.
// Dev-only/_TEST and internal socket vars are intentionally excluded.
var operatorVars = []string{
	"GUMMI_AGENT",
	"GUMMI_AGENT_CMD",
	"GUMMI_CLAUDE_BIN",
	"GUMMI_CODEX_BIN",
	"GUMMI_OPENCODE_BIN",
	"GUMMI_HEADLESS_CREDITS_PER_1K",
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

// TestReadmeEnvCoversOperatorVars proves every operator-facing GUMMI_ var
// the binary reads appears in the README environment table.
func TestReadmeEnvCoversOperatorVars(t *testing.T) {
	readme := readmeEnvVars(t)
	for _, v := range operatorVars {
		if !readme[v] {
			t.Errorf("operator-facing env var %s is missing from the README environment table", v)
		}
	}
}
