package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
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
	if r.Landed {
		line(s.Muted.Render("         ") + s.Success.Render("↑ landed on main") +
			s.Faint.Render("  · press c to clean up"))
	}
	if f.Budget.Envelope > 0 {
		released := m.engine != nil && m.engine.ReserveReleased(f.ID)
		line(s.Muted.Render("budget   ") + s.Base.Render(budgetSummary(f, released)))
	}
	if !f.Spend.Zero() {
		line(s.Muted.Render("spent    ") + s.Base.Render(featureSpend(f.Spend)))
	}
	line(s.Muted.Render("created  ") + s.Faint.Render(f.CreatedAt.Format("2006-01-02 15:04")))
	line("")

	// live activity: shown when an engine session is running for this
	// feature (an autonomous stage in progress).
	if sess := m.sessionFor(f.ID); sess != nil {
		snap := sess.Snapshot()
		title := s.Subtitle.Render("activity")
		if snap.Busy {
			title += "  " + s.Info.Render(m.spinner()+" running")
		}
		line(title)
		if meta := sessionMeta(snap); meta != "" {
			line("  " + s.Faint.Render(meta))
		}
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

// sessionMeta is the who-is-running line under the activity header:
// backend · model · provider · running spend, each shown once known.
func sessionMeta(snap engine.Snapshot) string {
	var parts []string
	if snap.AgentName != "" {
		parts = append(parts, snap.AgentName)
	}
	if m := runModel(snap); m != "" {
		parts = append(parts, m)
	}
	if p := snap.Provider.Describe(); p != "" {
		parts = append(parts, p)
	}
	if sp := spendSummary(snap); sp != "" {
		parts = append(parts, sp)
	}
	return strings.Join(parts, " · ")
}

// runModel prefers the model the agent reported in usage events over the
// profile-resolved one (the reported one is ground truth).
func runModel(snap engine.Snapshot) string {
	if snap.Spend.Model != "" {
		return snap.Spend.Model
	}
	return snap.Model
}

// spendSummary formats the running spend: metered credits when the
// backend reports them, otherwise tokens priced at the provider's rate.
func spendSummary(snap engine.Snapshot) string {
	if snap.Spend.Credits > 0 {
		return fmt.Sprintf("%g credits", roundSpend(snap.Spend.Credits))
	}
	if tok := snap.Spend.InputTokens + snap.Spend.OutputTokens; tok > 0 {
		out := humanTokens(tok) + " tok"
		if snap.SpentCredits > 0 {
			out += fmt.Sprintf(" ≈%g credits", roundSpend(snap.SpentCredits))
		}
		return out
	}
	return ""
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

// budgetSummary formats the spend plan: spend against the envelope, plus
// the current stage's cap (allocation + rollover) and the protected
// reserve, so the card shows where this stage's money comes from.
func budgetSummary(f domain.Feature, reserveReleased bool) string {
	env := float64(f.Budget.Envelope)
	spent := f.Spend.CreditEquivalent()
	plan := domain.DefaultPlan(env)
	s := fmt.Sprintf("%g / %g credits", roundSpend(spent), env)
	if cap := plan.StageBudget(f.Stage, spent, reserveReleased); cap > 0 {
		s += fmt.Sprintf("  ·  %s stage cap %g", f.Stage, roundSpend(cap))
	}
	if r := env * plan.Reserve; r > 0 {
		if reserveReleased {
			s += fmt.Sprintf("  ·  %g reserve released", roundSpend(r))
		} else {
			s += fmt.Sprintf("  ·  %g reserve", roundSpend(r))
		}
	}
	return s
}

// featureSpend formats the full metered cost for the dashboard.
func featureSpend(sp domain.Spend) string {
	parts := []string{}
	if sp.Credits > 0 {
		parts = append(parts, fmt.Sprintf("%g credits (≈$%.2f)", roundSpend(sp.Credits), sp.Credits*0.01))
	}
	if sp.InputTokens+sp.OutputTokens > 0 {
		parts = append(parts, fmt.Sprintf("%d in / %d out tokens", sp.InputTokens, sp.OutputTokens))
	}
	return strings.Join(parts, " · ")
}
