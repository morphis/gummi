package ui

// Formatting helpers shared by every surface that shows what a card cost
// or what its session is doing: the board row, the thread, the chat pane
// and the envelope dialog. They hold no layout of their own.

import (
	"fmt"
	"strings"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
)

// sessionMeta is the who-is-running line under the activity header:
// backend · model · provider · running spend · context occupancy, each
// shown once known. The context window appears only once the agent
// reports any occupancy — a fraction of the limit when one is known.
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
	if c := snap.Context; c.Tokens > 0 {
		ctx := humanTokens(c.Tokens) + " ctx"
		if c.Limit > 0 {
			ctx = fmt.Sprintf("%s/%s ctx (%d%%)", humanTokens(c.Tokens), humanTokens(c.Limit), c.Tokens*100/c.Limit)
		}
		parts = append(parts, ctx)
	}
	return strings.Join(parts, " · ")
}

// humanTokens renders a token count compactly: 1234 → "1.2k", 2e6 → "2M".
func humanTokens(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d", n)
	}
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
