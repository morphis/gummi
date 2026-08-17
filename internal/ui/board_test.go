package ui

import (
	"strings"
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
	order := m.displayOrder(m.sortMode)
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
	m.sel = m.displayOrder(m.sortMode)[0]
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
	m.Overlay.Push(m.helpOverlay())
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
	form := newFeatureForm(nil, 0, func(formResult) tea.Cmd { return nil })
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

// TestEstimatedSpendGolden covers the estimate labeling: a feature whose
// credits are token-derived (no provider-metered cost) shows "~" on the
// board tick, the spent/budget lines, and the stage breakdown, plus the
// one-line legend explaining the marker.
func TestEstimatedSpendGolden(t *testing.T) {
	m := populatedShell(120, 34)
	m.sel = 1
	m.rows[1].F.Spend = domain.Spend{Credits: 163.8, EstimatedCredits: 163.8, OutputTokens: 327539}
	m.rows[1].StageSpend = []state.StageSpend{
		{
			Stage: domain.StageImplement, Model: "claude-sonnet-4.6", Role: "implementer",
			Credits: 61.6, EstimatedCredits: 61.6, OutputTokens: 123219, UpdatedAt: time.Date(2026, 7, 9, 14, 0, 0, 0, time.UTC),
		},
		{
			Stage: domain.StageReview, Model: "claude-sonnet-4.6", Role: "reviewer",
			Credits: 11.1, EstimatedCredits: 11.1, OutputTokens: 22266, UpdatedAt: time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC),
		},
	}
	golden.RequireEqual(t, []byte(populatedShellView(m)))
}

// TestMeteredSpendGolden is the settled sibling: every estimate has been
// reconciled to the provider's actual cost (adapters settle each turn),
// so no tilde appears and the legend says the figures are metered.
func TestMeteredSpendGolden(t *testing.T) {
	m := populatedShell(120, 34)
	m.sel = 1
	m.rows[1].F.Spend = domain.Spend{Credits: 163.8, OutputTokens: 327539}
	m.rows[1].StageSpend = []state.StageSpend{
		{
			Stage: domain.StageImplement, Model: "claude-sonnet-4.6", Role: "implementer",
			Credits: 61.6, OutputTokens: 123219, UpdatedAt: time.Date(2026, 7, 9, 14, 0, 0, 0, time.UTC),
		},
		{
			Stage: domain.StageReview, Model: "claude-sonnet-4.6", Role: "reviewer",
			Credits: 11.1, OutputTokens: 22266, UpdatedAt: time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC),
		},
	}
	golden.RequireEqual(t, []byte(populatedShellView(m)))
}

func populatedShellView(m *Shell) string { return m.View().Content }

// bugRow builds a bug card with the given severity for cardLine tests.
func bugRow(num int, title string, stage domain.Stage, sev domain.Severity) featureRow {
	id, _ := domain.NewID(domain.KindBug, num)
	slug, _ := domain.Slugify(title)
	f := domain.Feature{
		ID: id, Num: num, Kind: domain.KindBug, Title: title, Slug: slug,
		Stage: stage, Severity: sev, CreatedAt: fixedTime, UpdatedAt: fixedTime,
	}
	r := featureRow{F: f}
	return r
}

// withCreated returns a copy of the row with a specific creation time
// (used to exercise the severity sort's creation-time tiebreaker).
func (r featureRow) withCreated(t time.Time) featureRow {
	r.F.CreatedAt = t
	return r
}

// TestBoardBlockedBadgeGolden: a card at its coding gate with an unmet
// dependency renders the blocked badge; the same card with all deps done,
// and a brainstorm card whose next step isn't the coding stage, render none.
func TestBoardBlockedBadgeGolden(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0.1.0-test")
	blocked := row(1, "blocked card", domain.StagePlan, "", true)
	blocked.DepBlocked = true
	met := row(2, "deps met", domain.StagePlan, "", true)
	met.DepBlocked = false
	design := row(3, "design card", domain.StageBrainstorm, "", false)
	design.DepBlocked = false
	m.rows = []featureRow{blocked, met, design}
	var b strings.Builder
	b.WriteString("blocked@plan\n")
	b.WriteString(m.cardLine(blocked, 1, false, 80) + "\n\n")
	b.WriteString("met@plan\n")
	b.WriteString(m.cardLine(met, 2, false, 80) + "\n\n")
	b.WriteString("design@brainstorm\n")
	b.WriteString(m.cardLine(design, 3, false, 80) + "\n")
	golden.RequireEqual(t, []byte(b.String()))
}

// TestBoardCardLineSeverity golden-captures the severity badge rendering
// for each canonical level plus the unclassified (empty) case.
func TestBoardCardLineSeverity(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0.1.0-test")
	cases := []struct {
		name string
		sev  domain.Severity
	}{
		{"critical", domain.SeverityCritical},
		{"high", domain.SeverityHigh},
		{"medium", domain.SeverityMedium},
		{"low", domain.SeverityLow},
		{"empty", ""},
	}
	var b strings.Builder
	for _, c := range cases {
		b.WriteString(c.name + "\n")
		r := bugRow(1, c.name, domain.StageTodo, c.sev)
		b.WriteString(m.cardLine(r, 1, false, 80) + "\n\n")
	}
	golden.RequireEqual(t, []byte(b.String()))
}

// TestDisplayOrderSortsTodoBySeverity: with SortSeverity active the todo
// column ranks bugs critical→high→medium→low→empty while other columns
// (in-progress, done) keep creation order.
func TestDisplayOrderSortsTodoBySeverity(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0.1.0-test")
	m.rows = []featureRow{
		bugRow(1, "low bug", domain.StageTodo, domain.SeverityLow),
		bugRow(2, "critical bug", domain.StageTodo, domain.SeverityCritical),
		bugRow(3, "unclassified", domain.StageTodo, ""),
		bugRow(4, "high bug", domain.StageTodo, domain.SeverityHigh),
		bugRow(5, "medium bug", domain.StageTodo, domain.SeverityMedium),
	}
	order := m.displayOrder(SortSeverity)
	var got []domain.Severity
	for _, i := range order {
		got = append(got, m.rows[i].F.Severity)
	}
	want := []domain.Severity{
		domain.SeverityCritical, domain.SeverityHigh, domain.SeverityMedium,
		domain.SeverityLow, "",
	}
	if len(got) != len(want) {
		t.Fatalf("order len = %d, want %d", len(got), len(want))
	}
	for i, sev := range want {
		if got[i] != sev {
			t.Errorf("position %d: severity %q, want %q", i, got[i], sev)
		}
	}
	// creation order is restored with SortCreation, preserving the
	// original row order within the todo column.
	creation := m.displayOrder(SortCreation)
	for i, wantNum := range []int{1, 2, 3, 4, 5} {
		if got := m.rows[creation[i]].F.Num; got != wantNum {
			t.Errorf("creation position %d: num %d, want %d", i, got, wantNum)
		}
	}
}

// TestDisplayOrderStable: rows sharing a severity keep creation time as a
// stable tiebreaker across repeated sorts.
func TestDisplayOrderStable(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0.1.0-test")
	early := fixedTime
	late := fixedTime.Add(2 * time.Hour)
	mid := fixedTime.Add(time.Hour)
	m.rows = []featureRow{
		bugRow(3, "newest high", domain.StageTodo, domain.SeverityHigh).withCreated(late),
		bugRow(1, "oldest high", domain.StageTodo, domain.SeverityHigh).withCreated(early),
		bugRow(2, "middle high", domain.StageTodo, domain.SeverityHigh).withCreated(mid),
	}
	first := m.displayOrder(SortSeverity)
	second := m.displayOrder(SortSeverity)
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("sort not stable across calls: %v vs %v", first, second)
		}
	}
	want := []int{1, 2, 3} // creation time ascending: oldest, middle, newest
	for i, idx := range first {
		if want[i] != m.rows[idx].F.Num {
			t.Errorf("position %d: num %d, want %d", i, m.rows[idx].F.Num, want[i])
		}
	}
}

// TestLongErrorNoticeUsesBand: a long error/remedy renders wrapped in the
// band above the status bar (not truncated to a one-line pill), and is
// omitted from the pill row so it isn't shown twice.
func TestLongErrorNoticeUsesBand(t *testing.T) {
	m := populatedShell(120, 34)
	remedy := "guarded mode denied the write — set permissions: allow-all in .gummi/config.yaml, or approve the request from the inbox, then re-run the stage"
	m.notice = noticeMsg{text: remedy, isErr: true}
	if !m.noticeInBand() {
		t.Fatal("a long error notice should render in the band")
	}
	band := m.noticeBand(90)
	if len(band) < 2 {
		t.Fatalf("band should wrap to multiple lines, got %d", len(band))
	}
	joined := stripANSI(strings.Join(band, " "))
	if !strings.Contains(joined, "allow-all in .gummi/config.yaml") {
		t.Errorf("band dropped the middle of the remedy:\n%s", joined)
	}
	// a short notice stays a pill, not the band
	m.notice = noticeMsg{text: "FD-001 queued"}
	if m.noticeInBand() {
		t.Error("a short notice should stay a status pill")
	}
}

// TestClearTransientNoticeOnViewChange: a routine status notice clears
// when the user opens another surface; an error notice is kept.
func TestClearTransientNoticeOnViewChange(t *testing.T) {
	m := populatedShell(120, 34)
	m.notice = noticeMsg{text: "critiquing"}
	m.clearTransientNotice()
	if m.notice.text != "" {
		t.Errorf("transient status not cleared: %q", m.notice.text)
	}
	m.notice = noticeMsg{text: "merge failed", isErr: true}
	m.clearTransientNotice()
	if m.notice.text == "" {
		t.Error("an error notice must survive a view change")
	}
}
