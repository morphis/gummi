package ui

import (
	"os"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/agentcli"
)

// agentTabExecPath locates gummi's own executable for the hosted-tab MCP
// wiring (HostedMCPAttach's execPath argument), mirroring the other
// backend adapters' own rebindable exec-path vars (opencodeExecPath,
// zzExecPath, claudeExecPath): production uses the real os.Executable,
// tests rebind it.
var agentTabExecPath = os.Executable

// agentCrashLoopWindow is how soon after starting a child has to exit to
// read as "never really started" rather than "ran, then ended" — the
// line shell.go's agentExitedMsg handler draws before deciding whether to
// respawn it.
//
// Three seconds is comfortably longer than a CLI takes to fail on a bad
// flag, missing auth, or an absent config file — those exits are
// essentially instant — and comfortably shorter than any interactive
// session a person would call "it ran". There's no back-off or attempt
// counter behind it: one fast exit is enough to stop, on the theory that
// whatever made it fail (a missing API key, an unwritable state dir)
// isn't going to fix itself between one attempt and the next, and a
// silent retry loop is a worse failure than a CLI that stops and says why.
const agentCrashLoopWindow = 3 * time.Second

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
// point of hosting it rather than shelling out per visit. It is also
// what shell.go's agentExitedMsg case calls to respawn a child that ended
// on its own: m.agent is nil'd first there, so this is exactly the same
// "cold start" path either way.
//
// It returns the command that begins pumping the child's output; the
// caller must return it to bubbletea or the tab renders one frame and
// then goes deaf.
func (m *Shell) ensureAgent() tea.Cmd {
	if m.agent != nil || m.agentErr != "" {
		return nil
	}
	// A respawn (shell.go's agentExitedMsg handler) nils m.agent and calls
	// straight back in here without going through closeAgent, so a prior
	// backend's hosted-MCP cleanup (an opencode temp config, say) must be
	// run here too — otherwise it leaks across the respawn.
	if m.agentMCPCleanup != nil {
		m.agentMCPCleanup()
		m.agentMCPCleanup = nil
	}
	argv, dir, backend, problem := m.resolveAgentAttach()
	if problem != "" {
		m.agentErr = agentSpawnErr(problem)
		return nil
	}
	// resume only when the backend about to run is the one that last ran
	// in this workspace: switching agents in the picker (or moving to a
	// different GUMMI_ATTACH_CMD) must start clean rather than resume a
	// different vendor's conversation, and a workspace with no recorded
	// session yet (loadAgentSession's ok=false) is a genuine first run,
	// which never gets a resume flag either.
	if prev, ok := loadAgentSession(m.ws); ok && prev.Backend == backend {
		argv = agentResumeArgs(argv, backend)
	}
	w, h := m.agentPaneSize()
	var env []string
	if m.agentSock != "" {
		env = append(env, "GUMMI_MCP_SOCK="+m.agentSock)
		exe, err := agentTabExecPath()
		if err != nil {
			m.notice = noticeMsg{text: "hosted MCP wiring: " + sanitize(err.Error()), isErr: true}
		} else {
			extraArgv, extraEnv, cleanup, err := agent.HostedMCPAttach(backend, exe, m.agentSock)
			m.agentMCPCleanup = cleanup
			if err != nil {
				m.notice = noticeMsg{text: "hosted MCP wiring: " + sanitize(err.Error()), isErr: true}
			} else {
				argv = append(argv, extraArgv...)
				env = append(env, extraEnv...)
			}
		}
	}
	av, err := newAgentViewEnv(argv, dir, w, h, env)
	if err != nil {
		m.agentErr = agentSpawnErr(sanitize(err.Error()))
		return nil
	}
	m.agent = av
	m.agentSpawnedAt = m.now()
	// best-effort: a workspace that can't be written to (a read-only
	// .gummi) shouldn't stop the agent tab from hosting — it only means
	// the *next* spawn won't know to resume this one.
	_ = saveAgentSession(m.ws, backend, m.agentSpawnedAt)
	return av.Wait()
}

// agentResumeArgs appends backend's resume form onto argv, using only
// the forms this repo has actually verified against the real CLI:
//
//   - claude, copilot: a trailing --continue flag.
//   - codex: `resume --last` as a SUBCOMMAND inserted right after the
//     binary (argv[0]), never appended at the end. GUMMI_ATTACH_CMD (and
//     this argv) is built for strings.Fields, which has no concept of
//     subcommand position — appending would read back as
//     "codex --continue" on a later raw invocation, and codex's own CLI
//     parses that as an unrecognized top-level flag, not a resume.
//
// opencode, zz, and anything else (a raw GUMMI_ATTACH_CMD, or an
// unrecognized GUMMI_AGENT value) get no resume form back: there is no
// verified flag or subcommand to guess at, so they always start fresh.
func agentResumeArgs(argv []string, backend string) []string {
	switch backend {
	case "claude", "copilot":
		out := make([]string, len(argv), len(argv)+1)
		copy(out, argv)
		return append(out, "--continue")
	case "codex":
		out := make([]string, 0, len(argv)+2)
		out = append(out, argv[0], "resume", "--last")
		return append(out, argv[1:]...)
	default:
		return argv
	}
}

// resolveAgentAttach picks the CLI to host, the directory to host it in,
// and the backend identity agent-session.json tracks for resume
// (agentResumeArgs, ensureAgent).
//
// Precedence, checked in this order (this is the only place it is
// applied — rawattach.go's `a` raw-attach hatch and the engine's own
// per-role backend selection in cmd/gummi are separate features with
// their own precedence, deliberately untouched by this one):
//
//  1. GUMMI_ATTACH_CMD — an explicit full command line, the operator
//     escape hatch that always wins, exactly as it does for `a`. There is
//     no vendor name to recognize here, so backend falls back to the
//     resolved binary itself below — enough identity to detect "the same
//     raw command ran last time", never enough to guess a resume flag
//     from (agentResumeArgs treats every unrecognized backend the same).
//  2. GUMMI_AGENT — an explicit backend name, resolved through
//     defaultAttachCommand's own name→binary mapping (rawattach.go) so
//     an operator using this one env var for both the engine and the
//     agent tab gets one consistent answer instead of two that could
//     drift apart. The env value itself (lowercased) is the backend
//     identity — including values with no resume form (opencode, zz,
//     headless), which is fine: they simply never match a case in
//     agentResumeArgs.
//  3. config `agent:` — the workspace config.yaml selection
//     agentpicker.go's picker persists via config.SetAgent
//     (m.agentConfigName, loaded once at startup by SetAgentConfig).
//     Chosen once, it survives restarts with no env var and no re-ask.
//     agentConfigName is already one of agentcli.Known()'s stable names,
//     so it doubles as the backend identity directly.
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
func (m *Shell) resolveAgentAttach() (argv []string, dir string, backend string, problem string) {
	cmdline := strings.TrimSpace(os.Getenv("GUMMI_ATTACH_CMD"))
	switch {
	case cmdline != "":
		// level 1, already resolved above; backend is filled in below,
		// once argv[0] is known, from the resolved binary itself.
	case strings.TrimSpace(os.Getenv("GUMMI_AGENT")) != "":
		backend = strings.ToLower(strings.TrimSpace(os.Getenv("GUMMI_AGENT")))
		cmdline = strings.TrimSpace(defaultAttachCommand())
	case m.agentConfigName != "":
		backend = m.agentConfigName
		if bin, ok := agentcli.Binary(m.agentConfigName); ok {
			cmdline = bin
		}
	}
	notConfigured := "no agent chosen for the agent tab yet — press space then \"" +
		agentChooseCommandLabel + "\" (or set GUMMI_AGENT / GUMMI_ATTACH_CMD)"
	if cmdline == "" {
		return nil, "", "", notConfigured
	}
	argv = strings.Fields(cmdline)
	if len(argv) == 0 {
		return nil, "", "", notConfigured
	}
	if _, err := exec.LookPath(argv[0]); err != nil {
		return nil, "", "", argv[0] + " not found — set GUMMI_ATTACH_CMD to your agent's command"
	}
	if backend == "" {
		backend = argv[0]
	}
	if m.wt != nil {
		dir = m.wt.Root()
	}
	return argv, dir, backend, ""
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

// forwardMouse hands a mouse event to the hosted CLI, translated from
// screen into pane coordinates.
//
// Mouse capture follows the keyboard lock rather than the tab, because
// taking the mouse is not free: while gummi captures it the terminal's
// own click-drag selection stops working, and selecting a block of agent
// output to copy is something people do far more often than clicking
// inside a CLI. Unlocked you keep the terminal's selection; locked the
// child gets the mouse along with everything else. One gesture, one
// meaning — the lock hands over the input, not just the keyboard.
//
// Events over the tab bar or the status bar are dropped: those rows are
// gummi's own chrome and the child has no idea they exist, so a click
// there would arrive at the wrong cell of its screen.
func (m *Shell) forwardMouse(msg tea.MouseMsg) {
	if ev, ok := m.paneMouse(msg); ok {
		m.agent.SendMouse(ev)
	}
}

// paneMouse translates a screen-space mouse message into the hosted
// pane's own coordinates, reporting false when the event is not the
// child's to see. Split out from forwardMouse because the arithmetic is
// the part that can be wrong in a way no compiler catches — an off-by-one
// here lands every click one row from where the user aimed.
func (m *Shell) paneMouse(msg tea.MouseMsg) (uv.MouseEvent, bool) {
	if !m.keyboardLocked() {
		return nil, false
	}
	e := msg.Mouse()
	area := m.layout.Main
	if e.X < area.Min.X || e.X >= area.Max.X || e.Y < area.Min.Y || e.Y >= area.Max.Y {
		return nil, false
	}
	pane := uv.Mouse{
		X:      e.X - area.Min.X,
		Y:      e.Y - area.Min.Y,
		Button: e.Button,
		Mod:    e.Mod,
	}
	// converted per concrete type rather than passed along as an
	// interface: x/vt reads the event's dynamic type to decide whether it
	// is encoding a press, a release or motion, so the distinction has to
	// survive the translation.
	switch msg.(type) {
	case tea.MouseClickMsg:
		return uv.MouseClickEvent(pane), true
	case tea.MouseReleaseMsg:
		return uv.MouseReleaseEvent(pane), true
	case tea.MouseWheelMsg:
		return uv.MouseWheelEvent(pane), true
	case tea.MouseMotionMsg:
		return uv.MouseMotionEvent(pane), true
	}
	return nil, false
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
	if m.agentMCPCleanup != nil {
		m.agentMCPCleanup()
		m.agentMCPCleanup = nil
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
