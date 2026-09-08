package ui

import (
	"context"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
)

// rowSpend is the spend the board's row snapshot holds for a card — what
// the masthead and the run chip read, as opposed to the store row the
// engine's budget arithmetic reads.
func rowSpend(t *testing.T, m *Shell, id domain.FeatureID) float64 {
	t.Helper()
	for _, r := range m.rows {
		if r.F.ID == id {
			return r.F.Spend.Credits
		}
	}
	t.Fatalf("no row for %s", id)
	return 0
}

// A stage that parks on its envelope must leave the board telling the
// truth about what is left. The engine suppresses the trailing idle of
// an exhausted turn, so the EventIdle branch that reloads rows after a
// finished stage never fires here — without a reload of its own the row
// keeps the spend it had before the run, and "budget exhausted" lands
// beside a masthead still offering the credits the stage was denied.
func TestExhaustionRefreshesTheBoardsSpend(t *testing.T) {
	m, _ := chatWorkspace(t, verdictAgent(func(agent.SessionOpts) string { return "done" }))
	ctx := context.Background()

	// the run's usage, booked against the store the way recordUsage books it
	if err := m.store.AddSpend(ctx, "FD-001", 283, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	if got := rowSpend(t, m, "FD-001"); got != 0 {
		t.Fatalf("row spend = %v before any reload, want 0 (the fixture's own snapshot)", got)
	}

	m = pump(t, m, m.handleEngineEvent(engine.Event{
		Kind: engine.EventExhausted, Feature: "FD-001", Stage: domain.StagePlan,
	}))

	if got := rowSpend(t, m, "FD-001"); got != 283 {
		t.Errorf("row spend after the budget park = %v, want 283 — the board is still showing pre-run credits", got)
	}
}

// The same for a threshold crossing: it is the one usage-driven event
// the board reloads on, so a card deep into its envelope stops quoting
// the figure it had at spawn.
func TestBudgetThresholdRefreshesTheBoardsSpend(t *testing.T) {
	m, _ := chatWorkspace(t, verdictAgent(func(agent.SessionOpts) string { return "done" }))
	if err := m.store.AddSpend(context.Background(), "FD-001", 50, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	m = pump(t, m, m.handleEngineEvent(engine.Event{
		Kind: engine.EventBudget, Feature: "FD-001", Stage: domain.StagePlan, Threshold: 80,
	}))
	if got := rowSpend(t, m, "FD-001"); got != 50 {
		t.Errorf("row spend after an 80%% nudge = %v, want 50", got)
	}
}

// While a session runs nothing reloads the board at all (usage events
// signal "re-render", not "re-read"), so the masthead takes the running
// session's own view of the card total over its snapshot. This is the
// number the engine enforces against: the hint the agent is given says
// what budgetSummary must agree with.
func TestBudgetSummaryPrefersTheLiveTotal(t *testing.T) {
	f := domain.Feature{ID: "BG-007", Budget: domain.Budget{Envelope: 2000}}
	f.Spend.Credits = 339.1

	if got := budgetSummary(f, 0); !strings.Contains(got, "1660.9 left") {
		t.Errorf("with no live session the row is all there is: %q", got)
	}
	got := budgetSummary(f, 622)
	if !strings.Contains(got, "1378 left") {
		t.Errorf("budgetSummary(live 622) = %q, want the live remainder (1378)", got)
	}
	if strings.Contains(got, "1660.9") {
		t.Errorf("budgetSummary kept the stale remainder: %q", got)
	}
}
