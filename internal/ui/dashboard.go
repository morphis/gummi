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
		for _, l := range strings.Split(wrapText(f.OneLiner, max(w, 8)), "\n") {
			line(s.Subtle.Render(l))
		}
	}
	line(s.Separator.Render(strings.Repeat("─", max(min(w, 60), 0))))

	stageLine := s.Muted.Render("stage    ") + s.StagePill(f.Stage).Render(string(f.Stage)) + "  " + s.Faint.Render(string(f.Stage.SuperState()))
	if rr := m.round(f.ID, domain.RoundKindReview); rr > 0 {
		stageLine += s.Faint.Render("  ·  review round " + itoa(rr) + "/" + itoa(maxReviewRounds))
	}
	line(stageLine)
	if loop := m.planLoopLine(f); loop != "" {
		line(s.Muted.Render("loop     ") + loop)
	}
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
		// the "press c" instruction lives in the next block below
		line(s.Muted.Render("         ") + s.Success.Render("↑ landed on main"))
	}
	if f.Budget.Envelope > 0 {
		line(s.Muted.Render("budget   ") + s.Base.Render(budgetSummary(f)))
	}
	if !f.Spend.Zero() {
		line(s.Muted.Render("spent    ") + s.Base.Render(featureSpend(f.Spend)))
		for _, l := range stageBreakdown(s, r.StageSpend) {
			line(l)
		}
	}
	if r.BaselineFails > 0 && !r.Landed {
		line(s.Muted.Render("baseline ") + s.Warning.Render(
			fmt.Sprintf("%d check(s) already failing on the fresh branch — verify labels them pre-existing", r.BaselineFails)))
	}
	if f.CommitDraftFail != "" {
		// a durable note from the last squash-merge scribe pass: the draft
		// was unavailable and why, so the failure is not just a transient
		// dialog line but survives for later inspection.
		line(s.Muted.Render("draft    ") + s.Warning.Render(sanitize(f.CommitDraftFail)))
	}
	if r.DrivenAbroad {
		// the card is moving under another process; say who owns it and
		// when it last spoke, so a quiet run is visible as quiet rather
		// than read as this board's own idle card.
		line(s.Muted.Render("driven   ") + s.Info.Render(foreignSummary(r.Foreign)))
	}
	line(s.Muted.Render("created  ") + s.Faint.Render(f.CreatedAt.Format("2006-01-02 15:04")))
	line("")

	// actions: everything valid for this card right now, recommendation
	// first (cardactions.go). This used to be a read-only "next" block of
	// ranked hints; it is now the board's second focus region — → moves
	// into it, ↑↓ move, enter runs — so the label is the interface and the
	// key beside it is a demoted accelerator the row teaches in passing.
	if l := m.cardActions(); l.Len() > 0 {
		// the header takes the accent while the list owns the arrow keys —
		// the same tell the kanban's group headers give the other pane, so
		// the two regions never both look live.
		head := s.Subtitle
		if m.actionFocused {
			head = s.PaneTitleActive
		}
		line(head.Render("actions"))
		// the same h-14 reserve the activity feed below uses, floored so a
		// very short terminal still shows the recommendation and a way to
		// reach the rest.
		for _, row := range strings.Split(l.View(s, w, max(h-14, 4), m.actionFocused), "\n") {
			line(row)
		}
		line("")
	}

	// live activity: shown when an engine session is running for this
	// feature (an autonomous stage in progress).
	if sess := m.sessionFor(f.ID); sess != nil {
		snap := sess.Snapshot()
		title := s.Subtitle.Render("activity")
		if snap.Busy {
			title += "  " + s.Info.Render(m.spinner()+" "+m.runningLabel(snap))
		}
		line(title)
		if meta := sessionMeta(snap); meta != "" {
			line("  " + s.Faint.Render(meta))
		}
		if snap.AgentSessionID != "" {
			line("  " + s.Faint.Render("session "+snap.AgentSessionID))
		}
		acts := recentTools(snap, 6)
		for _, a := range acts {
			line("  " + toolMarker(s, a.ToolStatus) + toolLineView(s, sanitize(a.Content), max(w-6, 8)))
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

// recentTools returns the last n AuthorTool transcript entries — the
// dashboard's activity ticker. The transcript (not snap.Activity, its
// plain-string twin) carries each call's outcome, so markers stay honest.
func recentTools(snap engine.Snapshot, n int) []engine.Message {
	var out []engine.Message
	for _, m := range snap.Transcript {
		if m.Author == engine.AuthorTool {
			out = append(out, m)
		}
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
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

// budgetSummary formats the budget: spend against the envelope plus
// what's left — every stage draws from the same pool, so one remainder
// is the whole story. A top-up raises the envelope itself (durably, in
// the store), so these figures already reflect it.
func budgetSummary(f domain.Feature) string {
	env := float64(f.Budget.Envelope)
	spent := f.Spend.CreditEquivalent()
	s := fmt.Sprintf("%s%g / %g credits", estMark(f.Spend), roundSpend(spent), env)
	if left := f.Budget.Remaining(spent); left > 0 {
		s += fmt.Sprintf("  ·  %g left", roundSpend(left))
	}
	return s
}

// featureSpend formats the full metered cost for the dashboard. A credit
// figure with a token-derived component is prefixed "~" and labelled
// "est." — it is a tokens×rate estimate, not a provider-metered cost.
func featureSpend(sp domain.Spend) string {
	parts := []string{}
	if sp.Credits > 0 {
		parts = append(parts, fmt.Sprintf("%s%g credits (%s≈%s)",
			estMark(sp), roundSpend(sp.Credits), estLabel(sp), money(sp.Credits)))
	}
	if sp.InputTokens+sp.OutputTokens > 0 {
		parts = append(parts, fmt.Sprintf("%d in / %d out tokens", sp.InputTokens, sp.OutputTokens))
	}
	return strings.Join(parts, " · ")
}

// estMark returns the "~" prefix for a spend whose credits are (partly)
// token-derived estimates, and estLabel the matching "est. " tag.
func estMark(sp domain.Spend) string {
	if sp.Estimated() {
		return "~"
	}
	return ""
}

func estLabel(sp domain.Spend) string {
	if sp.Estimated() {
		return "est. "
	}
	return ""
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
	legend := "since " + since.Format("2006-01-02")
	// adapters reconcile their live estimates to the provider's actual
	// cost as each turn settles, so rows with no estimated remainder are
	// real metered spend and say so; the tilde note appears only while
	// estimates are outstanding (mid-turn, or token-priced backends).
	estimated := false
	for _, r := range rows {
		if r.EstimatedCredits > 0 {
			estimated = true
			break
		}
	}
	if estimated {
		legend += "  ·  ~ estimated from tokens, not provider-metered"
	} else {
		legend += "  ·  provider-metered"
	}
	out := []string{s.Muted.Render("stages   ") + s.Faint.Render(legend)}
	for i := 0; i < len(rows); {
		j := i
		var total, estimated float64
		for j < len(rows) && rows[j].Stage == rows[i].Stage {
			total += rows[j].Credits
			estimated += rows[j].EstimatedCredits
			j++
		}
		grp := rows[i:j]
		dom := grp[0] // highest-credit model on this stage
		head := "  " + s.Subtle.Render(fmt.Sprintf("%-9s", string(dom.Stage))) +
			s.Base.Render(estPrefix(estimated)+money(total)) + s.Faint.Render("  ·  "+dom.Model)
		if len(grp) > 1 {
			head += s.Faint.Render(fmt.Sprintf("  +%d more", len(grp)-1))
		}
		out = append(out, head)
		if len(grp) > 1 {
			for _, r := range grp {
				// rows are keyed per role, so one model can appear twice on
				// a stage — the role disambiguates them
				name := r.Model
				if r.Role != "" {
					name += " (" + r.Role + ")"
				}
				out = append(out, "     "+s.Faint.Render(fmt.Sprintf("└ %-14s %s · %s/%s",
					name, estPrefix(r.EstimatedCredits)+money(r.Credits),
					humanTokens(r.InputTokens), humanTokens(r.OutputTokens))))
			}
		}
		i = j
	}
	return out
}

// estPrefix marks a money figure with a token-derived component as an
// estimate.
func estPrefix(estimated float64) string {
	if estimated > 0 {
		return "~"
	}
	return ""
}
