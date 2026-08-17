package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
)

// depFeature builds a feature with a literal ID/num for picker tests.
func depFeature(id string, num int, title string) *domain.Feature {
	return &domain.Feature{
		ID: domain.FeatureID(id), Num: num, Title: title, Slug: strings.ToLower(title),
		Stage: domain.StageTodo, CreatedAt: fixedTime, UpdatedAt: fixedTime,
	}
}

// pickWorkspace builds a workspace seeded with the given features.
func pickWorkspace(t *testing.T, feats ...*domain.Feature) *Shell {
	t.Helper()
	m, _ := newWorkspace(t)
	m.now = func() time.Time { return fixedTime }
	ctx := context.Background()
	for _, f := range feats {
		if err := m.store.CreateFeature(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	m = pump(t, m, m.Init())
	return m
}

// selRow sets the board selection onto the given card.
func selRow(t *testing.T, m *Shell, id domain.FeatureID) *Shell {
	t.Helper()
	for i := range m.rows {
		if m.rows[i].F.ID == id {
			m.sel = i
			return m
		}
	}
	t.Fatalf("feature %s not on the board", id)
	return m
}

// openDepsPick selects f and presses p to open its dependency picker.
func openDepsPick(t *testing.T, m *Shell) *Shell {
	t.Helper()
	m = selRow(t, m, "FD-001")
	return press(t, m, tea.KeyPressMsg{Code: 'p', Text: "p"})
}

// TestPickOpensReturns: pressing p on a selected card surfaces the picker;
// esc/q return to the board unchanged.
func TestPickOpensReturns(t *testing.T) {
	m := pickWorkspace(t,
		depFeature("FD-001", 1, "Alpha"),
		depFeature("FD-002", 2, "Beta"))
	before := len(m.rows)
	sel := m.sel

	m = openDepsPick(t, m)
	if m.deps == nil {
		t.Fatal("picker did not open on p")
	}
	if m.deps.f.ID != "FD-001" {
		t.Fatalf("picker opened on %s, want FD-001", m.deps.f.ID)
	}

	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.deps != nil {
		t.Fatal("picker did not close on esc")
	}
	if len(m.rows) != before || m.sel != sel {
		t.Fatalf("board changed after esc: rows=%d sel=%d, want %d/%d", len(m.rows), m.sel, before, sel)
	}

	// q also returns to the board.
	m = openDepsPick(t, m)
	m = press(t, m, tea.KeyPressMsg{Code: 'q', Text: "q"})
	if m.deps != nil {
		t.Fatal("picker did not close on q")
	}
}

// TestPickAdds: picking a valid target writes the edge and the picker
// rebuilds to show it attached; the board reloads for the badge.
func TestPickAdds(t *testing.T) {
	m := pickWorkspace(t,
		depFeature("FD-001", 1, "Alpha"),
		depFeature("FD-002", 2, "Beta"))
	m = openDepsPick(t, m)

	// cursor lands on the first navigable row: Beta (candOK).
	if got := m.deps.cands[m.deps.cursor].f.ID; got != "FD-002" {
		t.Fatalf("cursor on %s, want FD-002", got)
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	ctx := context.Background()
	deps, err := m.store.ListDependencies(ctx, "FD-001")
	if err != nil || len(deps) != 1 || deps[0] != "FD-002" {
		t.Fatalf("deps = %v, err=%v; want [FD-002]", deps, err)
	}
	// the picker rebuilt: Beta is now attached (navigable, no second write).
	if got := m.deps.cands[m.deps.cursor].state; got != candAttached {
		t.Fatalf("Beta state = %v, want candAttached", got)
	}
	if m.deps.attachedCount() != 1 {
		t.Fatalf("attachedCount = %d, want 1", m.deps.attachedCount())
	}
}

// TestPickAttachedNavigable: an attached row stays navigable in add mode —
// the cursor can land on it, enter writes no second edge, and x removes.
func TestPickAttachedNavigable(t *testing.T) {
	m := pickWorkspace(t,
		depFeature("FD-001", 1, "Alpha"),
		depFeature("FD-002", 2, "Beta"))
	ctx := context.Background()
	if err := m.store.AddDependency(ctx, "FD-001", "FD-002"); err != nil {
		t.Fatal(err)
	}
	m = openDepsPick(t, m)

	// the attached row is navigable: cursor lands on it.
	if got := m.deps.cands[m.deps.cursor].state; got != candAttached {
		t.Fatalf("cursor on %v, want candAttached", got)
	}
	// enter is a no-op: no duplicate edge.
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	deps, _ := m.store.ListDependencies(ctx, "FD-001")
	if len(deps) != 1 {
		t.Fatalf("enter wrote a second edge: %v", deps)
	}
	// x removes the edge.
	m = press(t, m, tea.KeyPressMsg{Code: 'x', Text: "x"})
	deps, _ = m.store.ListDependencies(ctx, "FD-001")
	if len(deps) != 0 {
		t.Fatalf("x did not remove the edge: %v", deps)
	}
}

// TestPickCycleInline: a cycle-creating target is selectable, shows the
// cycle reason, and enter is a no-op that never reaches the store.
func TestPickCycleInline(t *testing.T) {
	m := pickWorkspace(t,
		depFeature("FD-001", 1, "Alpha"),
		depFeature("FD-002", 2, "Beta"),
		depFeature("FD-003", 3, "Gamma"))
	ctx := context.Background()
	// Beta depends on Alpha, so Alpha→Beta would close a cycle.
	if err := m.store.AddDependency(ctx, "FD-002", "FD-001"); err != nil {
		t.Fatal(err)
	}
	m = openDepsPick(t, m)

	// walk to the candCycle row (Beta).
	for dp := m.deps; dp.cands[dp.cursor].f.ID != "FD-002"; {
		m = press(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
		dp = m.deps
	}
	if got := m.deps.cands[m.deps.cursor].state; got != candCycle {
		t.Fatalf("Beta state = %v, want candCycle", got)
	}
	// the detail panel shows the cycle reason.
	detail := m.deps.renderDepDetail(m.styles, 40)
	if !strings.Contains(detail, depReasonCycle) {
		t.Fatalf("detail = %q, want cycle reason", detail)
	}
	// enter is a no-op: no edge written.
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	deps, _ := m.store.ListDependencies(ctx, "FD-001")
	if len(deps) != 0 {
		t.Fatalf("cycle edge reached the store: %v", deps)
	}
}

// TestPickRemovalOnly: on a card at/past coding the picker opens removal-only
// — the list is just its forward deps, the late reason is shown, and x works.
func TestPickRemovalOnly(t *testing.T) {
	m := pickWorkspace(t,
		depFeature("FD-001", 1, "Alpha"),
		depFeature("FD-002", 2, "Beta"),
		depFeature("FD-003", 3, "Gamma"))
	ctx := context.Background()
	if err := m.store.AddDependency(ctx, "FD-001", "FD-002"); err != nil {
		t.Fatal(err)
	}
	// move the source to a coding stage after the edge exists — an
	// at-coding card can't take on a new dependency.
	toStage(t, m, "FD-001", domain.StageBrainstorm, domain.StageSpec, domain.StagePlan, domain.StageImplement)
	m = openDepsPick(t, m)

	if !m.deps.removeOnly {
		t.Fatal("coding card picker is not removal-only")
	}
	if m.deps.attachedCount() != 1 || len(m.deps.cands) != 1 {
		t.Fatalf("cands = %d (attached %d), want only the one dep", len(m.deps.cands), m.deps.attachedCount())
	}
	// the late-attachment reason is rendered as a banner.
	view := m.depPickerView(80, 20)
	if !strings.Contains(view, depReasonLate) {
		t.Fatalf("view missing late reason:\n%s", view)
	}
	// x removes the dep.
	m = press(t, m, tea.KeyPressMsg{Code: 'x', Text: "x"})
	deps, _ := m.store.ListDependencies(ctx, "FD-001")
	if len(deps) != 0 {
		t.Fatalf("x did not remove in removal-only mode: %v", deps)
	}
}

// TestPickRender: the picker view shows candidate IDs and the selected
// target's detail (one-liner + outcome).
func TestPickRender(t *testing.T) {
	b := depFeature("FD-002", 2, "Beta")
	b.OneLiner = "a side feature"
	m := pickWorkspace(t,
		depFeature("FD-001", 1, "Alpha"),
		b)
	m = openDepsPick(t, m)
	view := m.depPickerView(80, 20)
	for _, want := range []string{"dependencies", "FD-001", "FD-002"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
	if !strings.Contains(view, "a side feature") {
		t.Fatalf("view missing target one-liner:\n%s", view)
	}
	if !strings.Contains(view, "press enter to add") {
		t.Fatalf("view missing add outcome:\n%s", view)
	}
}

// TestPickBindings: the picker declares its key table, which feeds the
// status bar and the ? help overlay.
func TestPickBindings(t *testing.T) {
	m := pickWorkspace(t,
		depFeature("FD-001", 1, "Alpha"))
	m = openDepsPick(t, m)

	name, bs := m.activeSurface()
	if name != "deps" {
		t.Fatalf("activeSurface = %q, want deps", name)
	}
	has := func(key string) bool {
		for _, b := range bs {
			if b.key == key {
				return true
			}
		}
		return false
	}
	for _, key := range []string{"j/k", "enter", "x", "esc", "?"} {
		if !has(key) {
			t.Fatalf("picker bindings missing %q: %+v", key, bs)
		}
	}
	// the help overlay renders the picker rows.
	help := m.helpOverlay()
	for _, b := range bs {
		found := false
		for _, row := range help.rows {
			if row[0] == b.key && row[1] == helpText(b) {
				found = true
			}
		}
		if !found {
			t.Fatalf("help overlay missing binding %q (%q)", b.key, helpText(b))
		}
	}
}

// TestBuildCands: every board card is classified into the source's forward
// dependency set — self, attached, would-cycle, or addable.
func TestBuildCands(t *testing.T) {
	m := pickWorkspace(t,
		depFeature("FD-001", 1, "Alpha"),
		depFeature("FD-002", 2, "Beta"),
		depFeature("FD-003", 3, "Gamma"),
		depFeature("FD-004", 4, "Delta"))
	ctx := context.Background()
	// Alpha depends on Beta (attached); Gamma depends on Alpha (so Alpha→Gamma
	// would close a cycle); Delta is a plain addable target.
	if err := m.store.AddDependency(ctx, "FD-001", "FD-002"); err != nil {
		t.Fatal(err)
	}
	if err := m.store.AddDependency(ctx, "FD-003", "FD-001"); err != nil {
		t.Fatal(err)
	}

	dp := &depPicker{}
	if err := dp.buildCands(ctx, m.store, *mustFeature(t, m, "FD-001")); err != nil {
		t.Fatal(err)
	}
	if dp.removeOnly {
		t.Fatal("todo source should not be removal-only")
	}
	stateOf := map[domain.FeatureID]depCandState{}
	for _, c := range dp.cands {
		stateOf[c.f.ID] = c.state
	}
	want := map[domain.FeatureID]depCandState{
		"FD-001": candSelf, "FD-002": candAttached, "FD-003": candCycle, "FD-004": candOK,
	}
	if len(stateOf) != len(want) {
		t.Fatalf("classified %d cards, want %d: %v", len(stateOf), len(want), stateOf)
	}
	for id, st := range want {
		if got := stateOf[id]; got != st {
			t.Errorf("state[%s] = %v, want %v", id, got, st)
		}
	}
	// the cycle candidate carries its inline reason.
	for _, c := range dp.cands {
		if c.state == candCycle && c.reason != depReasonCycle {
			t.Errorf("cycle reason = %q, want %q", c.reason, depReasonCycle)
		}
	}
}

// TestBuildCandsRemovalOnly: an at-coding source lists only its forward
// deps (removal-only), never the full candidate set.
func TestBuildCandsRemovalOnly(t *testing.T) {
	m := pickWorkspace(t,
		depFeature("FD-001", 1, "Alpha"),
		depFeature("FD-002", 2, "Beta"),
		depFeature("FD-003", 3, "Gamma"))
	ctx := context.Background()
	if err := m.store.AddDependency(ctx, "FD-001", "FD-002"); err != nil {
		t.Fatal(err)
	}
	toStage(t, m, "FD-001", domain.StageBrainstorm, domain.StageSpec, domain.StagePlan, domain.StageImplement, domain.StageReview)
	dp := &depPicker{}
	if err := dp.buildCands(ctx, m.store, *mustFeature(t, m, "FD-001")); err != nil {
		t.Fatal(err)
	}
	if !dp.removeOnly {
		t.Fatal("review source should be removal-only")
	}
	if len(dp.cands) != 1 || dp.cands[0].f.ID != "FD-002" || dp.cands[0].state != candRemove {
		t.Fatalf("cands = %+v, want only the dep as candRemove", dp.cands)
	}
}

// mustFeature fetches a feature from the store, fataling on error.
func mustFeature(t *testing.T, m *Shell, id domain.FeatureID) *domain.Feature {
	t.Helper()
	f, err := m.store.GetFeature(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return &f
}

// toStage walks a feature through the legal transitions to a target, then
// reloads the board so the row reflects the new stage.
func toStage(t *testing.T, m *Shell, id domain.FeatureID, stages ...domain.Stage) {
	t.Helper()
	for _, st := range stages {
		if _, err := m.store.Transition(context.Background(), id, st, "test"); err != nil {
			t.Fatal(err)
		}
	}
	pump(t, m, m.loadRows)
}

// helpText mirrors helpRows' label fallback for assertion.
func helpText(b binding) string {
	if b.help != "" {
		return b.help
	}
	return b.label
}
