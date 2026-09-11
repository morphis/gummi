package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
)

// resolveAttach validates a raw-attach request and returns the command to
// run and the directory to run it in. The command comes from
// GUMMI_ATTACH_CMD (defaulting to the selected backend's native CLI), split
// on spaces — operator config, not untrusted input. An error string
// (non-empty) means the attach can't proceed and should surface as a notice.
func (m *Shell) resolveAttach(f domain.Feature) (argv []string, dir string, problem string) {
	ctx := context.Background()
	if ok, err := m.wt.Exists(ctx, &f); err != nil {
		return nil, "", err.Error()
	} else if !ok {
		return nil, "", noWorktreeYet(f)
	}
	cmdline := strings.TrimSpace(os.Getenv("GUMMI_ATTACH_CMD"))
	if cmdline == "" {
		cmdline = defaultAttachCommand()
	}
	argv = strings.Fields(cmdline)
	if len(argv) == 0 {
		return nil, "", "GUMMI_ATTACH_CMD is empty"
	}
	if _, err := exec.LookPath(argv[0]); err != nil {
		return nil, "", "raw attach: " + argv[0] + " not found (set GUMMI_ATTACH_CMD)"
	}
	return argv, filepath.Join(m.wt.Root(), f.WorktreePath()), ""
}

func defaultAttachCommand() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("GUMMI_AGENT"))) {
	case "claude":
		return envOr("GUMMI_CLAUDE_BIN", "claude")
	case "codex":
		return envOr("GUMMI_CODEX_BIN", "codex")
	case "opencode":
		return envOr("GUMMI_OPENCODE_BIN", "opencode")
	case "zz":
		return envOr("GUMMI_ZZ_BIN", "zz")
	case "headless":
		return strings.TrimSpace(os.Getenv("GUMMI_AGENT_CMD"))
	default:
		if cmd := strings.TrimSpace(os.Getenv("GUMMI_AGENT_CMD")); cmd != "" {
			return cmd
		}
		return "copilot"
	}
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// attachRaw is the escape hatch (DESIGN §9 M5): it suspends gummi's TUI and
// runs the raw agent CLI attached to the real terminal, in the feature's
// worktree, resuming (and reloading) when the process exits. For when the
// native chat isn't enough and you want the agent's own interface.
func (m *Shell) attachRaw(f domain.Feature) tea.Cmd {
	argv, dir, problem := m.resolveAttach(f)
	if problem != "" {
		m.notice = noticeMsg{text: sanitize(problem), isErr: true}
		return nil
	}
	cmd := exec.CommandContext(context.Background(), argv[0], argv[1:]...) //nolint:gosec // argv is operator config (GUMMI_ATTACH_CMD), not repo/agent input
	cmd.Dir = dir
	name := argv[0]
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		if err == nil {
			return noticeMsg{text: fmt.Sprintf("%s: back from %s", f.ID, name), reload: true}
		}
		// A NON-ZERO EXIT IS NOT NECESSARILY A FAILURE. Every hosted CLI
		// here exits non-zero when the person at the keyboard declines
		// something — dismissing Claude Code's "do you trust the files in
		// this folder?" prompt with esc leaves status 1 — and reporting
		// that back in red as "raw agent exited: exit status 1" told a
		// user who had just cancelled on purpose that something broke.
		// gummi cannot tell a decline from a crash (the child owns its
		// own exit codes and says nothing else), so it stops asserting
		// either: the notice reports what happened, not a verdict, and it
		// is not an error style. A CLI that genuinely failed to start
		// never reaches here — resolveAttach's problem branch above
		// catches that, and it IS an error.
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return noticeMsg{text: fmt.Sprintf("%s: %s exited without finishing (status %d)", f.ID, name, exit.ExitCode()), reload: true}
		}
		return noticeMsg{text: fmt.Sprintf("%s: could not run %s — %s", f.ID, name, sanitize(err.Error())), isErr: true}
	})
}
