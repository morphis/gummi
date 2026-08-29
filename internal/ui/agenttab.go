package ui

import (
	"os"
	"os/exec"
	"strings"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
)

// agenttab.go is the Shell's half of the agent tab: it decides when to
// spawn the hosted CLI, routes keys to it, and tears it down. agentview.go
// owns the pty and the terminal emulation and knows nothing about tabs;
// everything here is the wiring that agentview.go's doc comment defers to
// its caller.
//
// The split matters for one structural reason: every other surface in this
// package renders by returning a string that mainView hands to
// uv.NewStyledString. The agent tab cannot. Its content is a cell grid the
// emulator composites straight into the screen buffer, so it is drawn in
// Shell.draw (see drawAgentTab) rather than through mainView, which
// returns only the not-configured placeholder for this tab.

// agentSpawnErr is the notice shown in place of the hosted CLI when it
// could not start. It is a field rather than a transient notice because
// the tab must keep explaining itself every frame the user looks at it,
// not for the few seconds a status-bar pill survives.
type agentSpawnErr string

// setAgentMCPSock records the workspace MCP socket the hosted agent's
// tools reach this engine through. cmd/gummi passes it after the engine
// binds the endpoint; an empty value simply means no endpoint was bound
// (a detached board, or an engine that failed to build), in which case
// the agent still runs — it just has gummi's CLI and no gummi tools.
func (m *Shell) SetAgentMCPSock(path string) { m.agentSock = path }

// ensureAgent starts the hosted CLI the first time the agent tab is
// shown, and is a no-op on every later visit — the session, its
// scrollback and its context outlive tab switches, which is the whole
// point of hosting it rather than shelling out per visit.
//
// It returns the command that begins pumping the child's output; the
// caller must return it to bubbletea or the tab renders one frame and
// then goes deaf.
func (m *Shell) ensureAgent() tea.Cmd {
	if m.agent != nil || m.agentErr != "" {
		return nil
	}
	argv, dir, problem := m.resolveAgentAttach()
	if problem != "" {
		m.agentErr = agentSpawnErr(problem)
		return nil
	}
	w, h := m.agentPaneSize()
	var env []string
	if m.agentSock != "" {
		env = append(env, "GUMMI_MCP_SOCK="+m.agentSock)
	}
	av, err := newAgentViewEnv(argv, dir, w, h, env)
	if err != nil {
		m.agentErr = agentSpawnErr(sanitize(err.Error()))
		return nil
	}
	m.agent = av
	return av.Wait()
}

// resolveAgentAttach picks the CLI to host and the directory to host it
// in. It deliberately reuses rawattach.go's backend resolution
// (GUMMI_ATTACH_CMD, else the GUMMI_AGENT backend's own binary) so the
// tab and the `a` escape hatch can never disagree about what "your
// agent" means. The directory is the workspace root, not a card's
// worktree: this agent manages the board rather than living inside one
// card, and pointing it at a worktree would silently scope it to
// whichever card happened to be selected when the tab was first opened.
func (m *Shell) resolveAgentAttach() (argv []string, dir string, problem string) {
	// same precedence as resolveAttach (rawattach.go): an explicit
	// GUMMI_ATTACH_CMD wins, else the selected backend's own binary.
	// Reading only defaultAttachCommand here would silently ignore the
	// override and make the tab disagree with the `a` escape hatch.
	cmdline := strings.TrimSpace(os.Getenv("GUMMI_ATTACH_CMD"))
	if cmdline == "" {
		cmdline = strings.TrimSpace(defaultAttachCommand())
	}
	if cmdline == "" {
		return nil, "", "no agent configured — set GUMMI_AGENT or GUMMI_ATTACH_CMD"
	}
	argv = strings.Fields(cmdline)
	if len(argv) == 0 {
		return nil, "", "no agent configured — set GUMMI_AGENT or GUMMI_ATTACH_CMD"
	}
	if _, err := exec.LookPath(argv[0]); err != nil {
		return nil, "", argv[0] + " not found — set GUMMI_ATTACH_CMD to your agent's command"
	}
	if m.wt != nil {
		dir = m.wt.Root()
	}
	return argv, dir, ""
}

// agentPaneSize is the cell size available to the hosted CLI: the main
// pane, which is everything between the tab bar and the status bar.
func (m *Shell) agentPaneSize() (w, h int) {
	return max(m.layout.Main.Dx(), 1), max(m.layout.Main.Dy(), 1)
}

// drawAgentTab composites the hosted CLI's screen into the main pane.
// Unlike every other surface it bypasses mainView's string path: the
// emulator writes ultraviolet cells directly, which is what preserves
// the child's own truecolor and styling instead of round-tripping them
// through a styled string.
func (m *Shell) drawAgentTab(scr uv.Screen) {
	m.agent.Draw(scr, m.layout.Main)
}

// agentCursor reports where the terminal cursor belongs while the agent
// tab is up: the child's own cursor, translated from pane-relative into
// screen coordinates. Without this the caret sits wherever the last
// gummi surface left it and typing feels broken even though the keys
// are arriving.
func (m *Shell) agentCursor() *tea.Cursor {
	if m.tab != TabAgent || m.agent == nil {
		return nil
	}
	p := m.agent.CursorPosition()
	x, y := m.layout.Main.Min.X+p.X, m.layout.Main.Min.Y+p.Y
	if x < m.layout.Main.Min.X || x >= m.layout.Main.Max.X ||
		y < m.layout.Main.Min.Y || y >= m.layout.Main.Max.Y {
		return nil
	}
	return tea.NewCursor(x, y)
}

// agentKey forwards a keypress to the hosted CLI. Everything reaches the
// child except the alt+N tab switches, which boardKey has already
// claimed before this is called: the hosted program needs tab for its own
// completion and esc to interrupt itself, so gummi reserves the smallest
// possible set (DESIGN's alt-key rule, chat.go:412).
func (m *Shell) agentKey(msg tea.KeyPressMsg) tea.Cmd {
	if m.agent == nil {
		return nil
	}
	m.agent.SendKey(msg)
	return nil
}

// closeAgent tears the hosted CLI down. It is called on quit, and is
// safe on a tab that was never opened.
func (m *Shell) closeAgent() {
	if m.agent == nil {
		return
	}
	if err := m.agent.Close(); err != nil {
		m.notice = noticeMsg{text: "agent exited: " + sanitize(err.Error()), isErr: true}
	}
	m.agent = nil
}

// agentTabPlaceholder is what the agent tab shows before a CLI has
// started, or instead of one that could not: mainView renders this, and
// drawAgentTab paints over the whole pane once there is a live child.
func (m *Shell) agentTabPlaceholder(w, h int) string {
	msg := m.styles.Muted.Render("starting your agent…")
	if m.agentErr != "" {
		msg = m.styles.Muted.Render(string(m.agentErr))
	}
	return centeredNotice(w, h, msg)
}
