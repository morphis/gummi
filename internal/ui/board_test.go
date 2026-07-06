package ui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/golden"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui/theme"
)

// fixedTime keeps goldens deterministic.
var fixedTime = time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)

func row(num int, title string, stage domain.Stage, profile string, wt bool) featureRow {
	id, _ := domain.NewFeatureID(num)
	slug, _ := domain.Slugify(title)
	f := domain.Feature{
		ID: id, Num: num, Title: title, Slug: slug, Stage: stage,
		Profile: profile, CreatedAt: fixedTime, UpdatedAt: fixedTime,
	}
	return featureRow{F: f, HasWorktree: wt}
}

// populatedShell builds a detached shell with a representative board.
func populatedShell(w, h int) *Shell {
	m := NewShell(theme.GummiDark(), "v0.1.0-test")
	m.now = func() time.Time { return fixedTime }
	m.rows = []featureRow{
		row(51, "rate limits", domain.StageTodo, "thrifty", false),
		row(42, "dark mode", domain.StageImplement, "thrifty", true),
		row(47, "csv export", domain.StageBrainstorm, "premium", false),
		row(49, "auth fix", domain.StageSpec, "thrifty", false),
		row(44, "search", domain.StageReview, "local-heavy", true),
		row(39, "onboarding", domain.StageDone, "premium", false),
	}
	m.rows[1].History = []state.TransitionRecord{
		{FeatureID: "FD-042", From: domain.StageTodo, To: domain.StageBrainstorm, Actor: "user", At: fixedTime},
		{FeatureID: "FD-042", From: domain.StageBrainstorm, To: domain.StageSpec, Actor: "user", At: fixedTime},
		{FeatureID: "FD-042", From: domain.StageSpec, To: domain.StagePlan, Actor: "user", At: fixedTime},
		{FeatureID: "FD-042", From: domain.StagePlan, To: domain.StageImplement, Actor: "user", At: fixedTime},
	}
	m.sel = 1
	model, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return model.(*Shell)
}

func TestBoardView80(t *testing.T) {
	golden.RequireEqual(t, []byte(populatedShell(80, 24).View().Content))
}

func TestBoardView120(t *testing.T) {
	golden.RequireEqual(t, []byte(populatedShell(120, 34).View().Content))
}

func TestBoardGroupsAndOrder(t *testing.T) {
	m := populatedShell(120, 34)
	order := m.displayOrder()
	// grouped: todo(51), in-progress(42,47,49), review(44), done(39)
	want := []int{0, 1, 2, 3, 4, 5}
	if len(order) != len(want) {
		t.Fatalf("order len = %d", len(order))
	}
	stages := []domain.SuperState{
		domain.SuperTodo, domain.SuperInProgress, domain.SuperInProgress,
		domain.SuperInProgress, domain.SuperReviewVerify, domain.SuperDone,
	}
	for i, idx := range order {
		if got := m.rows[idx].F.Stage.SuperState(); got != stages[i] {
			t.Errorf("position %d: super-state %s, want %s", i, got, stages[i])
		}
	}
}

func TestBoardNavigation(t *testing.T) {
	m := populatedShell(120, 34)
	m.sel = m.displayOrder()[0]
	m.moveSel(1)
	if m.rows[m.sel].F.ID != "FD-042" {
		t.Errorf("j from first should reach FD-042, got %s", m.rows[m.sel].F.ID)
	}
	m.moveSel(-1)
	m.moveSel(-1) // wraps to last
	if m.rows[m.sel].F.ID != "FD-039" {
		t.Errorf("wrap-around should reach FD-039, got %s", m.rows[m.sel].F.ID)
	}
	m.jumpSel(3) // third visible = FD-047
	if m.rows[m.sel].F.ID != "FD-047" {
		t.Errorf("jump 3 should reach FD-047, got %s", m.rows[m.sel].F.ID)
	}
	m.jumpSel(9) // out of range: no move
	if m.rows[m.sel].F.ID != "FD-047" {
		t.Errorf("jump past end moved selection to %s", m.rows[m.sel].F.ID)
	}
}

func TestDashboardShowsSelected(t *testing.T) {
	m := populatedShell(120, 34)
	golden.RequireEqual(t, []byte(m.dashboardView(70, 30)))
}

func TestHelpOverlay(t *testing.T) {
	m := populatedShell(80, 24)
	m.Overlay.Push(helpDialog{})
	golden.RequireEqual(t, []byte(m.View().Content))
}

func TestConfirmOverlay(t *testing.T) {
	m := populatedShell(80, 24)
	m.Overlay.Push(&confirmDialog{
		id: "confirm-delete", question: "delete FD-042?",
		detail:    "dark mode — removes worktree, branch, and record",
		onConfirm: func() tea.Cmd { return nil },
	})
	golden.RequireEqual(t, []byte(m.View().Content))
}

func TestFormOverlay(t *testing.T) {
	m := populatedShell(100, 30)
	form := newFeatureForm(nil, func(formResult) tea.Cmd { return nil })
	form.skip.Brainstorm = true
	form.focus = fieldOpts
	form.desc.SetValue("dark mode toggle")
	form.desc.Blur()
	m.Overlay.Push(form)
	golden.RequireEqual(t, []byte(m.View().Content))
}

func TestBoardCostColumnGolden(t *testing.T) {
	m := populatedShell(120, 34)
	m.rows[1].F.Spend = domain.Spend{Credits: 12.4, InputTokens: 3200, OutputTokens: 1800} // FD-042
	m.rows[4].F.Spend = domain.Spend{OutputTokens: 45000}                                  // FD-044, BYOK tokens
	golden.RequireEqual(t, []byte(populatedShellView(m)))
}

func TestDashboardSpendGolden(t *testing.T) {
	m := populatedShell(120, 34)
	m.sel = 1
	m.rows[1].F.Spend = domain.Spend{Credits: 12.4, InputTokens: 3200, OutputTokens: 1800}
	golden.RequireEqual(t, []byte(populatedShellView(m)))
}

func populatedShellView(m *Shell) string { return m.View().Content }
