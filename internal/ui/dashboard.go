package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/morphia/gummi/internal/agent"
	"github.com/morphia/gummi/internal/domain"
	"github.com/morphia/gummi/internal/engine"
)

// dashboardView renders the selected feature's detail pane: identity,
// workflow position, derived git facts, budget, the live agent activity
// feed (when a session is running for this feature), and the audit
// trail.
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

	// live activity: shown when an engine session is running for this
	// feature (an autonomous stage in progress).
	if sess := m.sessionFor(f.ID); sess != nil {
		snap := sess.Snapshot()
		title := s.Subtitle.Render("activity")
		if snap.Busy {
			title += "  " + s.Info.Render("⣾ running")
		}
		if snap.Spend.Credits > 0 || snap.Spend.OutputTokens > 0 {
			title += "  " + s.Faint.Render(spendSummary(snap.Spend))
		}
		line(title)
		acts := snap.Activity
		if len(acts) > 6 {
			acts = acts[len(acts)-6:]
		}
		for _, a := range acts {
			line("  " + s.Success.Render("✓ ") + s.Subtle.Render(sanitize(a)))
		}
		last := lastAssistant(snap)
		if last != "" {
			for _, l := range strings.Split(wrapText(sanitize(last), max(w-4, 8)), "\n") {
				line("  " + s.Faint.Render(l))
			}
		}
		if len(acts) == 0 && last == "" {
			line("  " + s.Faint.Render("starting…"))
		}
		line("")
	} else if res := m.checks[f.ID]; len(res) > 0 {
		for _, l := range strings.Split(verifySummary(s, res), "\n") {
			if l != "" {
				line(l)
			}
		}
		line("")
	} else if len(r.History) > 0 {
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

	hints := s.KeyHint.Render("g") + s.KeyLabel.Render(" advance") +
		s.Faint.Render(" · ") + s.KeyHint.Render("b") + s.KeyLabel.Render(" bounce")
	if autonomousStage(f.Stage) {
		if m.sessionFor(f.ID) != nil {
			hints += s.Faint.Render(" · ") + s.KeyHint.Render("p") + s.KeyLabel.Render(" pause")
		} else {
			hints += s.Faint.Render(" · ") + s.KeyHint.Render("enter") + s.KeyLabel.Render(" run")
		}
	}
	hints += s.Faint.Render(" · ") + s.KeyHint.Render("x") + s.KeyLabel.Render(" delete") +
		s.Faint.Render(" · ") + s.KeyHint.Render("n") + s.KeyLabel.Render(" new")
	line(hints)
	return b.String()
}

// lastAssistant returns the most recent assistant message text.
func lastAssistant(snap engine.Snapshot) string {
	for i := len(snap.Transcript) - 1; i >= 0; i-- {
		if snap.Transcript[i].Author == engine.AuthorAssistant {
			return snap.Transcript[i].Content
		}
	}
	return ""
}

// spendSummary formats a running spend total for the activity header.
func spendSummary(u agent.Usage) string {
	if u.Credits > 0 {
		return fmt.Sprintf("%.1f credits", u.Credits)
	}
	return fmt.Sprintf("%d tok", u.OutputTokens)
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
