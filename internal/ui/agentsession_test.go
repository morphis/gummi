package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/worktree"
)

// TestAgentResumeArgsVerifiedForms pins the resume form for every backend
// the feature spec verified against the real CLI, plus the "no verified
// form" backends, which must come back unchanged rather than guessed at.
func TestAgentResumeArgsVerifiedForms(t *testing.T) {
	cases := []struct {
		backend string
		argv    []string
		want    []string
	}{
		{"claude", []string{"claude"}, []string{"claude", "--continue"}},
		{"copilot", []string{"copilot"}, []string{"copilot", "--continue"}},
		{"codex", []string{"codex"}, []string{"codex", "resume", "--last"}},
		{"opencode", []string{"opencode"}, []string{"opencode"}},
		{"zz", []string{"zz"}, []string{"zz"}},
		{"headless", []string{"/usr/bin/my-cli"}, []string{"/usr/bin/my-cli"}},
		{"", []string{"/path/to/raw-attach-cmd"}, []string{"/path/to/raw-attach-cmd"}},
	}
	for _, c := range cases {
		t.Run(c.backend, func(t *testing.T) {
			got := agentResumeArgs(c.argv, c.backend)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("agentResumeArgs(%v, %q) = %v, want %v", c.argv, c.backend, got, c.want)
			}
		})
	}
}

// TestAgentResumeArgsCodexInsertsSubcommandNotFlag proves codex's resume
// form is built structurally — the subcommand lands right after the
// binary, ahead of any of the CLI's own flags — rather than appended,
// which would read back as a stray "--continue"-shaped trailing flag once
// strings.Fields split it again.
func TestAgentResumeArgsCodexInsertsSubcommandNotFlag(t *testing.T) {
	got := agentResumeArgs([]string{"codex", "--model", "gpt-5"}, "codex")
	want := []string{"codex", "resume", "--last", "--model", "gpt-5"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("agentResumeArgs = %v, want %v (subcommand right after the binary)", got, want)
	}
	appended := []string{"codex", "--model", "gpt-5", "resume", "--last"}
	if reflect.DeepEqual(got, appended) {
		t.Fatal("resume landed appended at the end instead of inserted after the binary")
	}
}

// TestAgentSessionSaveLoadRoundTrip: the record written by saveAgentSession
// is exactly what loadAgentSession reads back, at the literal path the
// feature names — .gummi/agent-session.json.
func TestAgentSessionSaveLoadRoundTrip(t *testing.T) {
	ws, _, _ := uiRepo(t)
	at := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	if err := saveAgentSession(ws, "claude", at); err != nil {
		t.Fatal(err)
	}
	rec, ok := loadAgentSession(ws)
	if !ok {
		t.Fatal("expected a recorded session")
	}
	if rec.Backend != "claude" || !rec.At.Equal(at) {
		t.Fatalf("record = %+v, want backend claude at %v", rec, at)
	}
	if _, err := os.Stat(filepath.Join(ws.Root, ".gummi", "agent-session.json")); err != nil {
		t.Fatalf("expected .gummi/agent-session.json to exist: %v", err)
	}
}

// TestAgentSessionDetachedWorkspaceNoOp: a workspace with no root (a
// detached shell in tests, same shape as SetAgentConfig's empty
// configPath) must not attempt to read or write a path relative to "".
func TestAgentSessionDetachedWorkspaceNoOp(t *testing.T) {
	if err := saveAgentSession(state.Workspace{}, "claude", time.Now()); err != nil {
		t.Fatalf("save on a detached workspace should be a silent no-op, got %v", err)
	}
	if _, ok := loadAgentSession(state.Workspace{}); ok {
		t.Fatal("load on a detached workspace should report no record")
	}
}

// hostedShellWithBackend spawns the agent tab through the GUMMI_AGENT
// rung of resolveAgentAttach's precedence (rather than agenttab_test.go's
// hostedShell, which pins GUMMI_ATTACH_CMD — the one rung with no
// recognized backend name at all, and so never a candidate for resume).
// The fake script writes its argv to argsFile so the test can inspect
// exactly what ensureAgent built, without racing the pty's own output
// buffering the way scraping rendered cells would.
func hostedShellWithBackend(t *testing.T, ws state.Workspace, store *state.Store, wt *worktree.Pool, backend, binEnv, argsFile string) *Shell {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("needs a unix shell")
	}
	path := filepath.Join(t.TempDir(), "fake-"+backend+".sh")
	script := "#!/bin/sh\necho \"ARGS:$*\" > " + argsFile + "\nsleep 5\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GUMMI_AGENT", backend)
	t.Setenv(binEnv, path)
	m := populatedShell(100, 30)
	m.Attach(store, wt, ws)
	t.Cleanup(m.closeAgent)
	return m
}

// waitForFileContent polls path until it has content or the deadline
// passes — the fake scripts above write asynchronously, once their pty
// has actually started running.
func waitForFileContent(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
			return strings.TrimSpace(string(b))
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
	return ""
}

// TestEnsureAgentResumesOnBackendMatch: a workspace that last hosted
// claude, about to host claude again, gets --continue appended.
func TestEnsureAgentResumesOnBackendMatch(t *testing.T) {
	ws, store, wt := uiRepo(t)
	if err := saveAgentSession(ws, "claude", time.Now()); err != nil {
		t.Fatal(err)
	}
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	m := hostedShellWithBackend(t, ws, store, wt, "claude", "GUMMI_CLAUDE_BIN", argsFile)
	pressAlt(m, '3')

	if got, want := waitForFileContent(t, argsFile), "ARGS:--continue"; got != want {
		t.Fatalf("argv = %q, want %q (matching backend should resume)", got, want)
	}
	rec, ok := loadAgentSession(ws)
	if !ok || rec.Backend != "claude" {
		t.Fatalf("agent-session.json after spawn = %+v, ok=%v, want backend claude recorded", rec, ok)
	}
}

// TestEnsureAgentNoResumeOnFirstRun: a workspace with no recorded session
// yet is a genuine first run — no resume flag, ever.
func TestEnsureAgentNoResumeOnFirstRun(t *testing.T) {
	ws, store, wt := uiRepo(t)
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	m := hostedShellWithBackend(t, ws, store, wt, "claude", "GUMMI_CLAUDE_BIN", argsFile)
	pressAlt(m, '3')

	if got, want := waitForFileContent(t, argsFile), "ARGS:"; got != want {
		t.Fatalf("argv = %q, want %q (a first run must not resume)", got, want)
	}
}

// TestEnsureAgentNoResumeOnBackendMismatch: switching agents in the
// picker must start clean, not resume the previous backend's own
// conversation under the new CLI.
func TestEnsureAgentNoResumeOnBackendMismatch(t *testing.T) {
	ws, store, wt := uiRepo(t)
	if err := saveAgentSession(ws, "copilot", time.Now()); err != nil {
		t.Fatal(err)
	}
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	m := hostedShellWithBackend(t, ws, store, wt, "claude", "GUMMI_CLAUDE_BIN", argsFile)
	pressAlt(m, '3')

	if got, want := waitForFileContent(t, argsFile), "ARGS:"; got != want {
		t.Fatalf("argv = %q, want %q (switching backends must start clean)", got, want)
	}
}

// TestEnsureAgentCodexResumeIsSubcommandEndToEnd is
// TestAgentResumeArgsCodexInsertsSubcommandNotFlag's integration sibling:
// proves ensureAgent actually spawns "codex resume --last", not
// "codex --continue", when codex is the matching backend.
func TestEnsureAgentCodexResumeIsSubcommandEndToEnd(t *testing.T) {
	ws, store, wt := uiRepo(t)
	if err := saveAgentSession(ws, "codex", time.Now()); err != nil {
		t.Fatal(err)
	}
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	m := hostedShellWithBackend(t, ws, store, wt, "codex", "GUMMI_CODEX_BIN", argsFile)
	pressAlt(m, '3')

	if got, want := waitForFileContent(t, argsFile), "ARGS:resume --last"; got != want {
		t.Fatalf("argv = %q, want %q", got, want)
	}
}

// TestAgentRestartCrashLoopGuardStopsFastExit: a child that exits within
// agentCrashLoopWindow of starting must not be respawned — it reads as a
// CLI failing at startup, and a silent respawn loop would spin forever.
func TestAgentRestartCrashLoopGuardStopsFastExit(t *testing.T) {
	m := hostedShell(t, "sleep 5")
	pressAlt(m, '3')
	first := m.agent
	if first == nil {
		t.Fatal("no agent view spawned")
	}
	t.Cleanup(func() { _ = first.Close() })

	spawnedAt := m.agentSpawnedAt
	m.now = func() time.Time { return spawnedAt.Add(agentCrashLoopWindow - time.Millisecond) }

	model, _ := m.Update(agentExitedMsg{view: first, err: nil})
	m = model.(*Shell)

	if m.agent != nil {
		t.Fatal("a fast exit should not respawn — it should stop and explain itself")
	}
	if m.agentErr == "" {
		t.Fatal("a fast exit should leave an explanation in the tab")
	}
}

// TestAgentRestartsAfterALongLivedSessionExits: a child that ran for a
// while and then ended on its own gets a fresh child immediately, rather
// than leaving a dead pane on the tab.
func TestAgentRestartsAfterALongLivedSessionExits(t *testing.T) {
	m := hostedShell(t, "sleep 5")
	pressAlt(m, '3')
	first := m.agent
	if first == nil {
		t.Fatal("no agent view spawned")
	}
	t.Cleanup(func() { _ = first.Close() })

	spawnedAt := m.agentSpawnedAt
	m.now = func() time.Time { return spawnedAt.Add(agentCrashLoopWindow + time.Second) }

	model, _ := m.Update(agentExitedMsg{view: first, err: nil})
	m = model.(*Shell)

	if m.agent == nil {
		t.Fatal("a long-lived session's exit should respawn immediately")
	}
	if m.agent == first {
		t.Fatal("respawn should be a fresh child, not the exited one")
	}
	t.Cleanup(m.closeAgent)
}
