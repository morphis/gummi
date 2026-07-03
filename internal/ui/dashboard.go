package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/morphia/gummi/internal/domain"
)

// dashboardView renders the selected feature's detail pane: identity,
// workflow position, derived git facts, budget, and the audit trail.
// Static/stage data only — live agent activity lands in M1.
func (m *Shell) dashboardView(w, h int) string {
	if m.sel < 0 || m.sel >= len(m.rows) {
		return ""
	}
	s := m.styles
	r := m.rows[m.sel]
	f := r.F

	var b strings.Builder
	line := func(str string) {
		b.WriteString(ansi.Truncate(str, w, "…") + "\n")
	}

	line("")
	header := s.Title.Render(string(f.ID)) + " " + s.Base.Render("· "+f.Title)
	if f.Profile != "" {
		header += "  " + s.ProfileTag.Render("["+f.Profile+"]")
	}
	line(header)
	if f.OneLiner != "" {
		line(s.Subtle.Render(f.OneLiner))
	}
	line(s.Separator.Render(strings.Repeat("─", max(min(w, 60), 0))))

	line(s.Muted.Render("stage    ") + s.StagePill(f.Stage).Render(string(f.Stage)) + "  " + s.Faint.Render(string(f.Stage.SuperState())))
	skips := skipSummary(f)
	if skips != "" {
		line(s.Muted.Render("skips    ") + s.Faint.Render(skips))
	}
	line(s.Muted.Render("branch   ") + s.Base.Render(f.BranchName()))
	wt := "not created yet (created at spec approval)"
	if r.HasWorktree {
		wt = f.WorktreePath()
	}
	line(s.Muted.Render("worktree ") + s.Base.Render(wt))
	if f.Budget.Envelope > 0 {
		line(s.Muted.Render("budget   ") + s.Base.Render(budgetSummary(f)))
	}
	line(s.Muted.Render("created  ") + s.Faint.Render(f.CreatedAt.Format("2006-01-02 15:04")))
	line("")

	if len(r.History) > 0 {
		line(s.Subtitle.Render("history"))
		hist := r.History
		// most recent last; clamp to the pane
		maxLines := max(h-14, 3)
		if len(hist) > maxLines {
			hist = hist[len(hist)-maxLines:]
		}
		for _, t := range hist {
			line("  " + s.Stage(t.To).Render("▸") + " " +
				s.Faint.Render(string(t.From)+" → ") + s.Subtle.Render(string(t.To)) +
				s.Faint.Render(" · "+t.Actor))
		}
		line("")
	}

	line(s.KeyHint.Render("g") + s.KeyLabel.Render(" advance") +
		s.Faint.Render(" · ") + s.KeyHint.Render("b") + s.KeyLabel.Render(" bounce") +
		s.Faint.Render(" · ") + s.KeyHint.Render("x") + s.KeyLabel.Render(" delete") +
		s.Faint.Render(" · ") + s.KeyHint.Render("n") + s.KeyLabel.Render(" new"))
	return b.String()
}

// skipSummary names the stages this feature was created to skip.
func skipSummary(f domain.Feature) string {
	var parts []string
	if f.Skip.Brainstorm {
		parts = append(parts, "brainstorm")
	}
	if f.Skip.Plan {
		parts = append(parts, "plan")
	}
	return strings.Join(parts, ", ")
}

// budgetSummary formats "spent/envelope credits".
func budgetSummary(f domain.Feature) string {
	return fmt.Sprintf("%d/%d credits", f.Budget.Spent, f.Budget.Envelope)
}
