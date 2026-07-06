package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/ui/theme"
	"github.com/morphis/gummi/internal/verify"
)

// verifyResultMsg carries the outcome of a verify run.
type verifyResultMsg struct {
	feature domain.FeatureID
	results []verify.Result
	err     error
}

// runChecks surfaces the repo's verify commands, then (on confirm) runs
// them in the feature's worktree. The surface-before-run step is the
// safety for repo-controlled commands (DESIGN §4.4 threat list).
func (m *Shell) runChecks(f domain.Feature) tea.Cmd {
	cfg, err := config.Load(m.ws.ConfigFile())
	if err != nil {
		// the error may quote repo-controlled config bytes
		m.notice = noticeMsg{text: sanitize(err.Error()), isErr: true}
		return nil
	}
	if len(cfg.Checks) == 0 {
		m.notice = noticeMsg{text: "no checks in .gummi/config.yaml — add build/test/lint to run verify"}
		return nil
	}
	root := m.wt.Root()
	workDir := filepath.Join(root, f.WorktreePath())
	m.Overlay.Push(newVerifyDialog(f, cfg.Checks, func() tea.Cmd {
		return m.execChecks(f, workDir, cfg.Checks)
	}))
	return nil
}

// verifyTimeout bounds a whole verify run so a hung repo command can't
// wedge the run goroutine forever.
const verifyTimeout = 10 * time.Minute

// execChecks runs the checks in a command (off the UI goroutine).
func (m *Shell) execChecks(f domain.Feature, workDir string, checks []config.Check) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), verifyTimeout)
		defer cancel()
		results := verify.Run(ctx, workDir, checks)
		return verifyResultMsg{feature: f.ID, results: results}
	}
}

// verifyDialog surfaces the check commands and asks for confirmation
// before running them.
type verifyDialog struct {
	feature domain.FeatureID
	checks  []config.Check
	onRun   func() tea.Cmd
}

func newVerifyDialog(f domain.Feature, checks []config.Check, onRun func() tea.Cmd) *verifyDialog {
	return &verifyDialog{feature: f.ID, checks: checks, onRun: onRun}
}

func (d *verifyDialog) ID() string { return "verify" }

func (d *verifyDialog) HandleKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	switch key.String() {
	case "esc", "n", "q":
		return true, nil
	case "enter", "y":
		return true, d.onRun()
	}
	return false, nil
}

func (d *verifyDialog) View(s *theme.Styles, w, h int) string {
	var b strings.Builder
	b.WriteString(s.DialogTitle.Render("verify "+string(d.feature)) + "\n")
	b.WriteString(s.Warning.Render("these commands run in the feature's worktree") + "\n\n")
	width := max(min(w-10, 72), 24)
	for _, ch := range d.checks {
		b.WriteString("  " + s.CardID.Render(sanitize(ch.Name)) + "\n")
		// show the FULL command, wrapped — never truncated. The user must
		// see exactly what will run (the surface-before-run safety); a
		// hidden tail would let a repo smuggle in extra commands.
		for _, l := range strings.Split(wrapText(sanitize(ch.Cmd), width), "\n") {
			b.WriteString("    " + s.Faint.Render(l) + "\n")
		}
	}
	b.WriteString("\n" + s.KeyHint.Render("enter") + s.KeyLabel.Render(" run") +
		s.Faint.Render(" · ") + s.KeyHint.Render("esc") + s.KeyLabel.Render(" cancel"))
	return s.DialogFrame.Render(b.String())
}

// verifySummary renders the last verify results for the dashboard.
func verifySummary(s *theme.Styles, results []verify.Result) string {
	if len(results) == 0 {
		return ""
	}
	passed := 0
	for _, r := range results {
		if r.OK {
			passed++
		}
	}
	var b strings.Builder
	head := s.Subtitle.Render("verify")
	if passed == len(results) {
		head += "  " + s.Success.Render(fmt.Sprintf("✓ %d/%d passed", passed, len(results)))
	} else {
		head += "  " + s.Error.Render(fmt.Sprintf("✗ %d/%d passed", passed, len(results)))
	}
	b.WriteString(head + "\n")
	for _, r := range results {
		mark := s.Success.Render("✓")
		if !r.OK {
			mark = s.Error.Render("✗")
		}
		b.WriteString("  " + mark + " " + s.Subtle.Render(sanitize(r.Name)) +
			s.Faint.Render(" · "+sanitize(r.Cmd)) + "\n")
	}
	return b.String()
}
