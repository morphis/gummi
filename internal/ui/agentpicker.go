package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/agentcli"
	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/ui/theme"
)

// agentChooseCommandLabel is the space-menu wording for reopening the
// picker (boardactions.go) and the wording resolveAgentAttach points to
// when nothing is configured yet (agenttab.go) — one string so the two
// can't drift into telling the user to press something that isn't there.
const agentChooseCommandLabel = "Choose the agent tab's CLI"

// agentPickerDialog is the first-run (and reopen-anytime, space menu
// popover that selects which installed coding-agent CLI hosts the
// agent tab. It mirrors repoPickerDialog's shape (repopicker.go) — a
// short, left/right-cycled candidate list plus enter to submit — because
// both pickers solve the same UI problem (choose one of a handful of
// named options), and a returning user should feel no difference in how
// it drives.
type agentPickerDialog struct {
	agents   []agentcli.AgentCLI // agentcli.Detect's result, in its fixed display order
	idx      int
	onSubmit func(name string) tea.Cmd
}

// newAgentPickerDialog builds the picker over agents (agentcli.Detect's
// result). It preselects current (the configured `agent:` value) when it
// names one of the candidates; otherwise it preselects the first
// installed candidate. That means a workspace with exactly one CLI
// installed still opens the dialog on that row already highlighted
// (never auto-applied without asking — the user may have a reason to
// pick something else), and a workspace with nothing installed opens on
// index 0 rather than an undefined cursor.
func newAgentPickerDialog(agents []agentcli.AgentCLI, current string, onSubmit func(string) tea.Cmd) *agentPickerDialog {
	idx := 0
	for i, a := range agents {
		if a.Name == current {
			idx = i
			break
		}
	}
	if current == "" {
		for i, a := range agents {
			if a.Installed {
				idx = i
				break
			}
		}
	}
	return &agentPickerDialog{agents: agents, idx: idx, onSubmit: onSubmit}
}

// ID implements overlay.Dialog.
func (d *agentPickerDialog) ID() string { return "agent-cli" }

// HandleKey implements overlay.Dialog.
func (d *agentPickerDialog) HandleKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	if len(d.agents) == 0 {
		return true, nil
	}
	switch key.String() {
	case "esc":
		return true, nil
	case "enter":
		return true, d.onSubmit(d.agents[d.idx].Name)
	case "left", "h":
		d.idx--
		if d.idx < 0 {
			d.idx = len(d.agents) - 1
		}
		return false, nil
	case "right", "l", "r":
		d.idx++
		if d.idx >= len(d.agents) {
			d.idx = 0
		}
		return false, nil
	}
	return false, nil
}

// View implements overlay.Dialog.
func (d *agentPickerDialog) View(s *theme.Styles, w, h int) string {
	var b strings.Builder
	b.WriteString(s.DialogTitle.Render("agent tab · choose your CLI") + "\n\n")
	b.WriteString(s.Faint.Render("this only picks the agent tab's hosted CLI — the engine's own") + "\n")
	b.WriteString(s.Faint.Render("per-role backend routing (profiles.yaml) is untouched") + "\n\n")
	if len(d.agents) == 0 {
		// unreachable with the real agentcli.Known() (always five fixed
		// entries), but a test double or a future edit could still pass
		// an empty slice, and an empty dialog with no way to say so would
		// be worse than this one line.
		b.WriteString(s.Warning.Render("no known coding-agent CLI") + "\n")
		b.WriteString("\n" + s.Faint.Render("esc cancel"))
		return s.DialogFrame.Render(b.String())
	}
	if !anyInstalled(d.agents) {
		// The known set is fixed and always listed in full (see
		// agentcli.Known), "none detected" means every row says so, not
		// an empty list, so there is always something to pick, install
		// later, or fix a PATH/*_BIN override for.
		b.WriteString(s.Warning.Render("none of these were found on PATH") + "\n")
		b.WriteString(s.Faint.Render("pick one anyway, or install it / set its *_BIN override first") + "\n\n")
	}
	b.WriteString(agentPickerOptions(s, d.agents, d.idx) + "\n")
	sel := d.agents[d.idx]
	detail := sel.Bin + " — installed"
	if !sel.Installed {
		detail = sel.Bin + " — not found on PATH"
	}
	b.WriteString("\n" + s.Faint.Render(detail) + "\n")
	b.WriteString("\n" + s.Faint.Render("←/→ · h/l · r cycle · enter choose · esc cancel"))
	return s.DialogFrame.Render(b.String())
}

// anyInstalled reports whether at least one candidate resolved on PATH.
func anyInstalled(agents []agentcli.AgentCLI) bool {
	for _, a := range agents {
		if a.Installed {
			return true
		}
	}
	return false
}

// agentPickerOptions renders the candidate row, marking each with a
// checkmark (installed) or a dimmed "not found" so it is clear at a
// glance which choices actually work right now.
func agentPickerOptions(s *theme.Styles, agents []agentcli.AgentCLI, idx int) string {
	parts := make([]string, len(agents))
	for i, a := range agents {
		label := a.Name
		if a.Installed {
			label += " ✓"
		} else {
			label += " (not found)"
		}
		if i == idx {
			parts[i] = s.Selection.Render("▸ " + label)
		} else {
			parts[i] = s.Faint.Render("  " + label)
		}
	}
	return strings.Join(parts, "   ")
}

// agentPickerLoadedMsg carries agentcli.Detect's result into Update. The
// probe itself (internal/agentcli) runs inside the command this backs
// (openAgentPickerCmd), never in Update or a dialog constructor — the
// same "IO only in commands" discipline every other surface in this
// package follows, even though the probe is fast enough it wouldn't be
// noticed either way.
type agentPickerLoadedMsg struct{ agents []agentcli.AgentCLI }

// openAgentPickerCmd builds the command that detects installed CLIs and
// delivers them for the picker. It backs the space menu's agent-cli entry
// (boardactions.go) — and, since nothing else does, the picker is now
// asked for rather than raised.
//
// It used to be pushed before the first frame from cmd/gummi's runBoard,
// which made it the FIRST THING a new user met: a modal about the agent
// tab, in front of a board they had not seen, before a card existed, in
// vocabulary nothing had taught them — and spending two of its own lines
// disclaiming that it does NOT pick the engine's per-role backends,
// which is the shape of a question asked in the wrong place.
//
// The wrong place turns out to be anywhere unprompted. The answer feeds
// resolveAgentAttach, which is read by ensureAgent — the hosted-pty
// spawn that the board thread replaced and that nothing reaches any more
// (gotoTab's own comment: "m.agent must stay nil"). A modal cannot be
// justified for a setting nothing currently consumes. The dialog stays
// exactly as it is, reachable on demand for the workspaces that set
// `agent:` by hand and for whenever a hosted tab returns; it simply
// stops interrupting people who never asked it anything.
func (m *Shell) openAgentPickerCmd() tea.Cmd {
	return func() tea.Msg { return agentPickerLoadedMsg{agents: agentcli.Detect()} }
}

// agentChosenMsg carries the outcome of persisting a picker choice. name
// is set only on success, so Update applies it to m.agentConfigName
// exactly once, off the command goroutine that produced it — chooseAgentCLI
// itself must never touch m directly, for the same reason no other
// command in this package does.
type agentChosenMsg struct {
	name string
	err  error
}

// chooseAgentCLI persists name as the workspace's `agent:` choice
// (internal/config.SetAgent) and reports the outcome as a message.
// m.agentConfigPath is read here (on the Update goroutine, before the
// command runs) rather than inside the closure, so the closure touches
// no Shell field once it starts running on its own goroutine.
func (m *Shell) chooseAgentCLI(name string) tea.Cmd {
	path := m.agentConfigPath
	return func() tea.Msg {
		if path != "" {
			if err := config.SetAgent(path, name); err != nil {
				return agentChosenMsg{err: err}
			}
		}
		return agentChosenMsg{name: name}
	}
}
