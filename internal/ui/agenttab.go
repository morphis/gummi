package ui

import (
	"os"
	"os/exec"
	"strings"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/morphis/gummi/internal/agentcli"
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

// SetAgentConfig wires the workspace's persisted agent-tab CLI choice
// (config.Config.Agent, already loaded by the caller — cmd/gummi's
// runBoard) and the path a later picker choice should be written back to
// (config.SetAgent). name may be empty (nothing picked yet, or a
// detached shell in tests); configPath may also be empty, which disables
// persistence but not the choice itself for the rest of this run.
func (m *Shell) SetAgentConfig(name, configPath string) {
	m.agentConfigName, m.agentConfigPath = name, configPath
}

// agentConfigured reports whether the agent tab already knows which CLI
// to host, by resolveAgentAttach's own precedence minus its last rung
// (the picker): an explicit GUMMI_ATTACH_CMD or GUMMI_AGENT env var, or a
// persisted config `agent:` value. MaybeShowAgentPicker (agentpicker.go)
// uses this to decide whether the picker needs to show unasked on first
// start; the space menu's agent-cli entry (boardactions.go) ignores it and
// opens the picker unconditionally, since re-asking on demand is exactly
// what that entry is for.
func (m *Shell) agentConfigured() bool {
	return strings.TrimSpace(os.Getenv("GUMMI_ATTACH_CMD")) != "" ||
		strings.TrimSpace(os.Getenv("GUMMI_AGENT")) != "" ||
		m.agentConfigName != ""
}

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
// in.
//
// Precedence, checked in this order (this is the only place it is
// applied — rawattach.go's `a` raw-attach hatch and the engine's own
// per-role backend selection in cmd/gummi are separate features with
// their own precedence, deliberately untouched by this one):
//
//  1. GUMMI_ATTACH_CMD — an explicit full command line, the operator
//     escape hatch that always wins, exactly as it does for `a`.
//  2. GUMMI_AGENT — an explicit backend name, resolved through
//     defaultAttachCommand's own name→binary mapping (rawattach.go) so
//     an operator using this one env var for both the engine and the
//     agent tab gets one consistent answer instead of two that could
//     drift apart.
//  3. config `agent:` — the workspace config.yaml selection
//     agentpicker.go's picker persists via config.SetAgent
//     (m.agentConfigName, loaded once at startup by SetAgentConfig).
//     Chosen once, it survives restarts with no env var and no re-ask.
//  4. nothing configured — reported as a problem rather than guessed at.
//     This is the fix for the bug this feature exists to close: the old
//     code fell through to a hardcoded "copilot" here, so a user without
//     copilot installed got a silent missing-binary error instead of a
//     choice. Init's first-run trigger (agentConfigured) is what keeps
//     step 4 from actually being reached on a normal first start — this
//     problem string is what a user sees only if they dismiss the
//     picker with esc and then visit the tab anyway.
//
// The directory is the workspace root, not a card's worktree: this agent
// manages the board rather than living inside one card, and pointing it
// at a worktree would silently scope it to whichever card happened to be
// selected when the tab was first opened.
func (m *Shell) resolveAgentAttach() (argv []string, dir string, problem string) {
	cmdline := strings.TrimSpace(os.Getenv("GUMMI_ATTACH_CMD"))
	switch {
	case cmdline != "":
		// level 1, already resolved above.
	case strings.TrimSpace(os.Getenv("GUMMI_AGENT")) != "":
		cmdline = strings.TrimSpace(defaultAttachCommand())
	case m.agentConfigName != "":
		if bin, ok := agentcli.Binary(m.agentConfigName); ok {
			cmdline = bin
		}
	}
	notConfigured := "no agent chosen for the agent tab yet — press space then \"" +
		agentChooseCommandLabel + "\" (or set GUMMI_AGENT / GUMMI_ATTACH_CMD)"
	if cmdline == "" {
		return nil, "", notConfigured
	}
	argv = strings.Fields(cmdline)
	if len(argv) == 0 {
		return nil, "", notConfigured
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
