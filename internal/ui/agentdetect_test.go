package ui

import (
	"os"
	"path/filepath"
	"testing"
)

// writeFakeBin drops an executable file named name into dir so
// exec.LookPath can find it without needing the real CLI installed in
// this environment, and returns its full path.
func writeFakeBin(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestDetectAgentCLIsRespectsPathAndBinOverrides proves detection uses a
// fake PATH (never the real host's) and honors a *_BIN override even
// when that override points outside PATH entirely.
func TestDetectAgentCLIsRespectsPathAndBinOverrides(t *testing.T) {
	fakePath := t.TempDir()
	writeFakeBin(t, fakePath, "claude")
	elsewhere := t.TempDir()
	codexOverride := writeFakeBin(t, elsewhere, "my-codex")

	t.Setenv("PATH", fakePath)
	t.Setenv("GUMMI_CODEX_BIN", codexOverride)
	t.Setenv("GUMMI_OPENCODE_BIN", "")
	t.Setenv("GUMMI_ZZ_BIN", "")

	got := map[string]bool{}
	for _, a := range detectAgentCLIs() {
		got[a.Name] = a.Installed
	}
	if !got["claude"] {
		t.Error("claude should be detected on the fake PATH")
	}
	if !got["codex"] {
		t.Error("codex should be detected via GUMMI_CODEX_BIN, which bypasses PATH entirely")
	}
	for _, name := range []string{"copilot", "opencode", "zz"} {
		if got[name] {
			t.Errorf("%s should not be detected (not on the fake PATH, no override)", name)
		}
	}
}

// TestDetectAgentCLIsKnownSet pins the known set's names, independent of
// what happens to be installed — a regression here silently drops a
// backend from the picker.
func TestDetectAgentCLIsKnownSet(t *testing.T) {
	want := map[string]bool{"copilot": true, "claude": true, "codex": true, "opencode": true, "zz": true}
	agents := knownAgentCLIs()
	if len(agents) != len(want) {
		t.Fatalf("knownAgentCLIs has %d entries, want %d", len(agents), len(want))
	}
	for _, a := range agents {
		if !want[a.Name] {
			t.Errorf("unexpected agent CLI %q", a.Name)
		}
		delete(want, a.Name)
	}
	if len(want) != 0 {
		t.Errorf("missing agent CLIs: %v", want)
	}
	// headless is a real engine backend (capsBase, defaultAttachCommand)
	// but not an installable CLI — it must never appear here.
	for _, a := range agents {
		if a.Name == "headless" {
			t.Error("headless should not be a picker candidate")
		}
	}
}

// TestAgentCLIBinaryUnknownName proves an unrecognized name (a stale or
// hand-typo'd config `agent:` value) is reported as not-ok rather than
// silently resolving to some default binary.
func TestAgentCLIBinaryUnknownName(t *testing.T) {
	if _, ok := agentCLIBinary("not-a-real-backend"); ok {
		t.Error("unknown name should not resolve")
	}
	t.Setenv("GUMMI_CLAUDE_BIN", "")
	if bin, ok := agentCLIBinary("claude"); !ok || bin != "claude" {
		t.Errorf("agentCLIBinary(claude) = (%q, %v), want (claude, true)", bin, ok)
	}
	t.Setenv("GUMMI_CLAUDE_BIN", "/opt/claude/bin/claude")
	if bin, ok := agentCLIBinary("claude"); !ok || bin != "/opt/claude/bin/claude" {
		t.Errorf("agentCLIBinary(claude) with override = (%q, %v), want override honored", bin, ok)
	}
}
