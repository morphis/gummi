package ui

import (
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
	pump := make(chan tea.Msg, 1)
	go func() {
		for {
			msg := m.agent.Wait()()
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

func TestAgentTabSpawnsOnFirstVisitOnly(t *testing.T) {
	m := hostedShell(t, "sleep 30")

	if cmd := m.boardKey("alt+3"); cmd == nil {
		t.Fatal("entering the agent tab should return the output pump command")
	}
	if m.tab != TabAgent {
		t.Fatalf("tab = %v, want TabAgent", m.tab)
	}
	first := m.agent
	if first == nil {
		t.Fatal("no agent view spawned on first visit")
	}

	// leaving and coming back must not restart the child: the session and
	// its context are the thing worth keeping across tab switches.
	m.boardKey("alt+1")
	m.boardKey("alt+3")
	if m.agent != first {
		t.Error("revisiting the agent tab respawned the child; the session should persist")
	}
}

func TestAgentTabRendersChildOutputIntoTheMainPane(t *testing.T) {
	m := hostedShell(t, "printf Z; sleep 30")
	m.boardKey("alt+3")
	waitForAgentCell(t, m, "Z")
}

func TestAgentTabForwardsKeysToTheChild(t *testing.T) {
	// `cat` echoes nothing of its own; the pty's line discipline is what
	// puts the typed byte on screen. That is exactly the round trip worth
	// proving: key -> emulator -> pty -> child's terminal -> cells.
	m := hostedShell(t, "cat")
	m.boardKey("alt+3")

	m.handleKey(tea.KeyPressMsg{Code: 'y', Text: "y"})
	waitForAgentCell(t, m, "y")
}

func TestAgentTabKeepsCtrlCForTheChild(t *testing.T) {
	m := hostedShell(t, "sleep 30")
	m.boardKey("alt+3")

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
	m.boardKey("alt+3")

	m.handleKey(tea.KeyPressMsg{Code: '1', Mod: tea.ModAlt})
	if m.tab != TabBoard {
		t.Fatalf("alt+1 did not leave the agent tab: tab = %v", m.tab)
	}
}

func TestAgentTabResizeReachesTheChild(t *testing.T) {
	m := hostedShell(t, "sleep 30")
	m.boardKey("alt+3")

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
	m.boardKey("alt+3")

	if m.agent != nil {
		t.Fatal("spawned something for a binary that does not exist")
	}
	got := m.agentTabPlaceholder(60, 5)
	if !strings.Contains(got, "not found") {
		t.Errorf("placeholder should say the binary was not found, got %q", got)
	}
}

func TestAgentTabPassesTheWorkspaceSocket(t *testing.T) {
	// the hosted CLI reaches *this* process's engine through this socket;
	// without it in the child's environment its `gummi __mcp --workspace`
	// child has nothing to dial and would fall back to a second gummi.
	m := hostedShell(t, "printf '%s' \"$GUMMI_MCP_SOCK\"; sleep 30")
	m.SetAgentMCPSock("/tmp/gummi-ws-test.sock")
	m.boardKey("alt+3")
	waitForAgentCell(t, m, "/")
}
