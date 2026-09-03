package ui

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
)

// hostedShell returns a shell whose agent tab spawns a throwaway script
// instead of a real coding CLI, so these tests exercise the wiring
// without needing claude/copilot installed.
//
// The script is written to a file rather than passed as `sh -c ...`
// because GUMMI_ATTACH_CMD is split with strings.Fields (rawattach.go:
// operator config, not a shell line), so an inline command with quoting
// or spaces in an argument would be silently torn apart — and a test
// that spawns something other than what it wrote proves nothing.
// pressAlt sends alt+<n> the way a terminal does, through handleKey —
// the only entry point that answers it. The tab switches used to be
// reachable via boardKey, but they are tier-1 globals now and live above
// every surface, so a test that called boardKey would be proving
// something the running program no longer does.
func pressAlt(m *Shell, n rune) tea.Cmd {
	return m.handleKey(tea.KeyPressMsg{Code: n, Mod: tea.ModAlt})
}

func hostedShell(t *testing.T, script string) *Shell {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("needs a unix shell")
	}
	path := filepath.Join(t.TempDir(), "fake-agent.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GUMMI_ATTACH_CMD", path)
	m := populatedShell(100, 30)
	t.Cleanup(m.closeAgent)
	return m
}

// waitForAgentCell pumps the agent's output until want shows up at the
// top-left of the main pane, then gives up. Draw is the only honest
// place to look: the whole point of the tab is that the child's cells
// land on gummi's own screen buffer.
//
// The pump runs on its own goroutine because agentView.Wait blocks until
// there is output or the child exits. Calling it inline would mean a
// regression that stops output reaching the emulator hangs this test
// forever instead of failing it — and a hung test is worse than a failed
// one, because CI reports it as a timeout with no message.
func waitForAgentCell(t *testing.T, m *Shell, want string) {
	t.Helper()
	// Capture the view once: the pump goroutine outlives the test body,
	// and t.Cleanup's closeAgent sets m.agent to nil — reading the field
	// from in here would race with that (caught by -race, not by eye).
	av := m.agent
	pump := make(chan tea.Msg, 1)
	go func() {
		for {
			msg := av.Wait()()
			pump <- msg
			if _, done := msg.(agentExitedMsg); done {
				return
			}
		}
	}()

	deadline := time.After(10 * time.Second)
	for {
		scr := uv.NewScreenBuffer(m.width, m.height)
		m.drawAgentTab(scr)
		if c := scr.CellAt(m.layout.Main.Min.X, m.layout.Main.Min.Y); c != nil && c.Content == want {
			return
		}
		select {
		case msg := <-pump:
			if ex, ok := msg.(agentExitedMsg); ok && ex.err != nil {
				t.Fatalf("hosted child exited before rendering %q: %v", want, ex.err)
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %q in the agent pane", want)
		}
	}
}

// Entering the agent tab no longer spawns the hosted CLI on its own:
// gotoTab now opens the board's own conversation there instead
// (boardthread.go), so every test below that still wants a live pty
// calls ensureAgent itself, right after the tab switch — the pty-hosting
// code these tests exercise (agentview.go, agenttab.go) is untouched and
// still fully reachable, just no longer wired to the tab switch. It is
// deleted in a later phase; until then these tests keep covering it via
// the one entry point still left for it.

func TestAgentTabSpawnsOnFirstVisitOnly(t *testing.T) {
	m := hostedShell(t, "sleep 30")
	pressAlt(m, '3')
	if m.tab != TabAgent {
		t.Fatalf("tab = %v, want TabAgent", m.tab)
	}

	if cmd := m.ensureAgent(); cmd == nil {
		t.Fatal("ensureAgent should return the output pump command on first spawn")
	}
	first := m.agent
	if first == nil {
		t.Fatal("no agent view spawned on first visit")
	}

	// leaving and coming back, then calling ensureAgent again — its own
	// "no-op on every later visit" contract (agenttab.go) — must not
	// restart the child: the session and its context are the thing worth
	// keeping.
	pressAlt(m, '1')
	pressAlt(m, '3')
	m.ensureAgent()
	if m.agent != first {
		t.Error("revisiting the agent tab respawned the child; the session should persist")
	}
}

func TestAgentTabRendersChildOutputIntoTheMainPane(t *testing.T) {
	m := hostedShell(t, "printf Z; sleep 30")
	pressAlt(m, '3')
	m.ensureAgent()
	waitForAgentCell(t, m, "Z")
}

func TestAgentTabForwardsKeysToTheChild(t *testing.T) {
	// `cat` echoes nothing of its own; the pty's line discipline is what
	// puts the typed byte on screen. That is exactly the round trip worth
	// proving: key -> emulator -> pty -> child's terminal -> cells.
	m := hostedShell(t, "cat")
	pressAlt(m, '3')
	m.ensureAgent()

	m.handleKey(tea.KeyPressMsg{Code: 'y', Text: "y"})
	waitForAgentCell(t, m, "y")
}

func TestAgentTabKeepsCtrlCForTheChild(t *testing.T) {
	m := hostedShell(t, "sleep 30")
	pressAlt(m, '3')
	m.ensureAgent()

	// ctrl+c is gummi's global quit everywhere else; on this tab the
	// hosted CLI needs it to interrupt itself. Update must not return
	// tea.Quit here.
	_, cmd := m.update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd != nil {
		if _, quit := cmd().(tea.QuitMsg); quit {
			t.Fatal("ctrl+c on the agent tab quit gummi; it belongs to the hosted CLI")
		}
	}
	if m.tab != TabAgent {
		t.Error("ctrl+c should not have moved off the agent tab")
	}
}

func TestAgentTabAltKeysStillLeave(t *testing.T) {
	m := hostedShell(t, "sleep 30")
	pressAlt(m, '3')
	m.ensureAgent()

	m.handleKey(tea.KeyPressMsg{Code: '1', Mod: tea.ModAlt})
	if m.tab != TabBoard {
		t.Fatalf("alt+1 did not leave the agent tab: tab = %v", m.tab)
	}
}

func TestAgentTabResizeReachesTheChild(t *testing.T) {
	m := hostedShell(t, "sleep 30")
	pressAlt(m, '3')
	m.ensureAgent()

	m.update(tea.WindowSizeMsg{Width: 90, Height: 24})
	wantW, wantH := m.agentPaneSize()
	if w, h := m.agent.emu.Width(), m.agent.emu.Height(); w != wantW || h != wantH {
		t.Errorf("emulator = %dx%d after resize, want the pane's %dx%d", w, h, wantW, wantH)
	}
}

func TestAgentTabExplainsAMissingBinary(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("needs a unix shell")
	}
	t.Setenv("GUMMI_ATTACH_CMD", "definitely-not-a-real-agent-binary")
	m := populatedShell(100, 30)
	t.Cleanup(m.closeAgent)
	pressAlt(m, '3')
	m.ensureAgent()

	if m.agent != nil {
		t.Fatal("spawned something for a binary that does not exist")
	}
	got := m.agentTabPlaceholder(60, 5)
	if !strings.Contains(got, "not found") {
		t.Errorf("placeholder should say the binary was not found, got %q", got)
	}
}

// hostedShellAgent is hostedShell's counterpart for a backend HostedMCPAttach
// actually recognizes: it points backend's own *_BIN discovery env var
// (agentcli.go) at a throwaway script and sets GUMMI_AGENT to backend, so
// resolveAgentAttach's backend identity is one of HostedMCPAttach's known
// names rather than hostedShell's raw-GUMMI_ATTACH_CMD fallback (which
// always lands HostedMCPAttach's default, unwired branch).
func hostedShellAgent(t *testing.T, backend, binEnv, script string) *Shell {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("needs a unix shell")
	}
	path := filepath.Join(t.TempDir(), "fake-agent.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GUMMI_AGENT", backend)
	t.Setenv(binEnv, path)
	m := populatedShell(100, 30)
	t.Cleanup(m.closeAgent)
	return m
}

// TestEnsureAgentWiresMCP proves ensureAgent appends HostedMCPAttach's
// extra argv/env onto the real spawned process for each backend wired
// into it, through the actual spawn path (agentview.go's pty) rather than
// just HostedMCPAttach's own pure-builder tests.
func TestEnsureAgentWiresMCP(t *testing.T) {
	old := agentTabExecPath
	agentTabExecPath = func() (string, error) { return "/opt/gummi", nil }
	t.Cleanup(func() { agentTabExecPath = old })

	const sock = "/tmp/gummi-ws-test.sock"
	cases := []struct {
		name    string
		backend string
		binEnv  string
		want    []string
	}{
		{"claude", "claude", "GUMMI_CLAUDE_BIN", []string{"--strict-mcp-config", "--mcp-config", sock, `"--workspace"`}},
		{"codex", "codex", "GUMMI_CODEX_BIN", []string{"-c", "mcp_servers.gummi", sock, `"--workspace"`}},
		{"zz", "zz", "GUMMI_ZZ_BIN", []string{"--mcp", "/opt/gummi __mcp --workspace"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "out.txt")
			// Write to a temp path and rename into place: waitForFileContent
			// reads as soon as out has any bytes, and a redirected compound
			// command ({ ...; } > out) can be observed mid-write (printf's
			// output landed, env's has not) — a rename only makes the
			// finished file visible at that name.
			script := "{ printf '%s\\n' \"$@\"; echo ---ENV---; env; } > '" + out + ".tmp' && mv '" + out + ".tmp' '" + out + "'; sleep 30"
			m := hostedShellAgent(t, tc.backend, tc.binEnv, script)
			m.SetAgentMCPSock(sock)
			pressAlt(m, '3')
			m.ensureAgent()
			got := waitForFileContent(t, out)
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("%s: recorded argv/env missing %q:\n%s", tc.name, want, got)
				}
			}
		})
	}
}

// TestEnsureAgentWiresMCPOpencode is TestEnsureAgentWiresMCP's opencode
// case, split out because its wiring is a temp config file named by an
// env var rather than an inline flag: the assertion has to open that file
// and inspect its shape, not just grep the recorded argv/env dump.
func TestEnsureAgentWiresMCPOpencode(t *testing.T) {
	old := agentTabExecPath
	agentTabExecPath = func() (string, error) { return "/opt/gummi", nil }
	t.Cleanup(func() { agentTabExecPath = old })

	out := filepath.Join(t.TempDir(), "out.txt")
	script := "env > '" + out + ".tmp' && mv '" + out + ".tmp' '" + out + "'; sleep 30"
	m := hostedShellAgent(t, "opencode", "GUMMI_OPENCODE_BIN", script)
	m.SetAgentMCPSock("/tmp/gummi-ws-test.sock")
	pressAlt(m, '3')
	m.ensureAgent()
	got := waitForFileContent(t, out)

	var cfgPath string
	for _, line := range strings.Split(got, "\n") {
		if v, ok := strings.CutPrefix(line, "OPENCODE_CONFIG="); ok {
			cfgPath = v
		}
	}
	if cfgPath == "" {
		t.Fatalf("OPENCODE_CONFIG missing from the child's env:\n%s", got)
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("reading opencode config: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("config not valid JSON: %v\n%s", err, raw)
	}
	if _, present := cfg["permission"]; present {
		t.Errorf("permission key present in hosted-tab config: %v", cfg["permission"])
	}
	if _, present := cfg["mcp"]; !present {
		t.Errorf("mcp key missing: %v", cfg)
	}
}

// TestEnsureAgentNoMCPWiringForUnrecognizedBackend covers HostedMCPAttach's
// default branch reached live: a raw GUMMI_ATTACH_CMD has no vendor name
// to recognize, so the child gets the workspace socket (unconditional,
// backend-independent) but none of the backend-specific MCP flags/files.
func TestEnsureAgentNoMCPWiringForUnrecognizedBackend(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out.txt")
	script := "{ printf '%s\\n' \"$@\"; echo ---ENV---; env; } > '" + out + ".tmp' && mv '" + out + ".tmp' '" + out + "'; sleep 30"
	m := hostedShell(t, script)
	m.SetAgentMCPSock("/tmp/gummi-ws-test.sock")
	pressAlt(m, '3')
	m.ensureAgent()
	got := waitForFileContent(t, out)

	if !strings.Contains(got, "GUMMI_MCP_SOCK=/tmp/gummi-ws-test.sock") {
		t.Errorf("workspace socket missing from child env:\n%s", got)
	}
	for _, unwanted := range []string{"--mcp-config", "mcp_servers.gummi", "OPENCODE_CONFIG", "--mcp"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("unexpected MCP wiring %q for an unrecognized backend:\n%s", unwanted, got)
		}
	}
}

func TestAgentTabPassesTheWorkspaceSocket(t *testing.T) {
	// the hosted CLI reaches *this* process's engine through this socket;
	// without it in the child's environment its `gummi __mcp --workspace`
	// child has nothing to dial and would fall back to a second gummi.
	m := hostedShell(t, "printf '%s' \"$GUMMI_MCP_SOCK\"; sleep 30")
	m.SetAgentMCPSock("/tmp/gummi-ws-test.sock")
	pressAlt(m, '3')
	m.ensureAgent()
	waitForAgentCell(t, m, "/")
}
