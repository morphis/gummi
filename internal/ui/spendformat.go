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

// liveCardSpent returns the card's total spend as the session driving it
// has it — engine.Session.CardSpent, which moves with every usage event
// the engine books against the store row. 0 when nothing is live on the
// card (or a restored session has not been dispatched again), which
// leaves the caller on the board row's own copy.
func (m *Shell) liveCardSpent(id domain.FeatureID) float64 {
	if m.engine == nil {
		return 0
	}
	s := m.engine.Get(id)
	if s == nil {
		return 0
	}
	return s.CardSpent()
}

// budgetSummary formats the budget: what the card has spent against what
// it was given — every stage draws from the same pool, so one pair is the
// whole story. A top-up raises the budget itself (durably, in the store),
// so these figures already reflect it.
//
// live is the running session's view of the card's total (0 = none), and
// it wins over f's when there is one. f comes from the board's row
// snapshot, which is reloaded on a handful of events and never on a
// usage one, so during a run — and after a stage parks on its budget —
// it is behind by everything the session has spent. The engine's own
// budget arithmetic reads the store, so a stale figure here is exactly
// the case where the card claims headroom the stage was already denied.
//
// THERE IS NO "· N left" CLAUSE. There used to be, and it never said
// anything the two figures beside it did not: "324 / 2400 credits ·
// 2076 left" is one subtraction printed twice. It was dropped at zero
// spend for exactly that reason ("0 / 2000 credits · 2000 left" repeats
// the envelope), and the same argument holds at every other value — the
// reader is not being told a third fact, they are being shown the
// arithmetic.
//
// The columns it cost were not free. This string sits in the card
// masthead, which measures the badge cluster FIRST and gives the title
// whatever is left (threadHeader, thread.go): at 120 columns the
// redundant clause was pushing a card's own title down to eight
// characters — "FD-001 · tally to…". Spending a fifth of the widest line
// on the screen to restate a number is what made the title the least
// legible thing on a page about that card.
func budgetSummary(f domain.Feature, live float64) string {
	env := float64(f.Budget.Envelope)
	spent := f.Spend.CreditEquivalent()
	if live > 0 {
		spent = live
	}
	return fmt.Sprintf("%s%g / %g credits", estMark(f.Spend), roundSpend(spent), env)
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
