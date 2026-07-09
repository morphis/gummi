package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui/theme"
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
		for _, l := range stageBreakdown(s, r.StageSpend) {
			line(l)
		}
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
			line("  " + s.Success.Render("✓ ") + toolLineView(s, sanitize(a), max(w-6, 8)))
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
		parts = append(parts, fmt.Sprintf("%g credits (≈%s)", roundSpend(sp.Credits), money(sp.Credits)))
	}
	if sp.InputTokens+sp.OutputTokens > 0 {
		parts = append(parts, fmt.Sprintf("%d in / %d out tokens", sp.InputTokens, sp.OutputTokens))
	}
	return strings.Join(parts, " · ")
}

// money renders a credit figure as adaptive-precision dollars; see
// domain.FormatDollars (shared with the engine's stage-exit receipt).
func money(credits float64) string { return domain.FormatDollars(credits) }

// stageBreakdown renders the per-stage/model spend rollup inline under the
// spent line: one line per stage (its total + dominant model), and for a
// multi-model stage the per-model split indented beneath. Rows arrive
// ordered by workflow stage then descending credits, so the first row of
// each group is its dominant model. Forward-only, so it's labelled "since
// <first recorded>" rather than implying full history.
func stageBreakdown(s *theme.Styles, rows []state.StageSpend) []string {
	if len(rows) == 0 {
		return nil
	}
	since := rows[0].UpdatedAt
	for _, r := range rows {
		if r.UpdatedAt.Before(since) {
			since = r.UpdatedAt
		}
	}
	out := []string{s.Muted.Render("stages   ") + s.Faint.Render("since "+since.Format("2006-01-02"))}
	for i := 0; i < len(rows); {
		j := i
		var total float64
		for j < len(rows) && rows[j].Stage == rows[i].Stage {
			total += rows[j].Credits
			j++
		}
		grp := rows[i:j]
		dom := grp[0] // highest-credit model on this stage
		head := "  " + s.Subtle.Render(fmt.Sprintf("%-9s", string(dom.Stage))) +
			s.Base.Render(money(total)) + s.Faint.Render("  ·  "+dom.Model)
		if len(grp) > 1 {
			head += s.Faint.Render(fmt.Sprintf("  +%d more", len(grp)-1))
		}
		out = append(out, head)
		if len(grp) > 1 {
			for _, r := range grp {
				out = append(out, "     "+s.Faint.Render(fmt.Sprintf("└ %-14s %s · %s/%s",
					r.Model, money(r.Credits), humanTokens(r.InputTokens), humanTokens(r.OutputTokens))))
			}
		}
		i = j
	}
	return out
}
