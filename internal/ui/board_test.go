package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/golden"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
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
		row(46, "billing sync", domain.StageFix, "thrifty", false),
	}
	m.rows[6].AutopilotDriving = true
	m.rows[1].History = []state.TransitionRecord{
		{FeatureID: "FD-042", From: domain.StageTodo, To: domain.StageBrainstorm, Actor: "user", At: fixedTime},
		{FeatureID: "FD-042", From: domain.StageBrainstorm, To: domain.StageSpec, Actor: "user", At: fixedTime},
		{FeatureID: "FD-042", From: domain.StageSpec, To: domain.StagePlan, Actor: "user", At: fixedTime},
		{FeatureID: "FD-042", From: domain.StagePlan, To: domain.StageImplement, Actor: "user", At: fixedTime},
	}
	m.rows[1].F.PullRequest = domain.PullRequestRef{Repo: "o/r", Number: 42, URL: "https://github.com/o/r/pull/42"}
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
	// grouped: todo(51), in-progress(42,47,49,46), review(44), done(39)
	want := []int{0, 1, 2, 3, 6, 4, 5}
	if len(order) != len(want) {
		t.Fatalf("order len = %d", len(order))
	}
	stages := []domain.SuperState{
		domain.SuperTodo, domain.SuperInProgress, domain.SuperInProgress,
		domain.SuperInProgress, domain.SuperInProgress, domain.SuperReviewVerify, domain.SuperDone,
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

func TestThreadShowsSelected(t *testing.T) {
	m := populatedShell(120, 34)
	golden.RequireEqual(t, []byte(m.threadView(70, 30)))
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

// TestBoardRepoBadge: a card in a non-default repo carries a repo badge on
// its board line; a card in the default repo renders no repo badge (the
// default is implicit).
func TestBoardRepoBadge(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0.1.0-test")
	m.now = func() time.Time { return fixedTime }
	named := row(51, "rate limits", domain.StageTodo, "thrifty", false)
	named.F.Repo = "lxd"
	def := row(52, "plain feature", domain.StageTodo, "thrifty", false)
	m.rows = []featureRow{named, def}
	model, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 10})
	m = model.(*Shell)
	content := m.View().Content
	if !strings.Contains(content, "[lxd]") {
		t.Error("a named-repo card should render its repo badge")
	}
	if strings.Contains(content, "[default]") {
		t.Error("a default-repo card should not render an explicit repo badge")
	}
}

// cardLine renders a linked card's PR badge off the already-in-memory row —
// never a live gh call. Pointing GUMMI_GH_CMD at a binary that always fails
// proves the render path never shells out.
func TestCardLineNeverShellsGH(t *testing.T) {
	t.Setenv("GUMMI_GH_CMD", "/bin/false")
	m := NewShell(theme.GummiDark(), "v0.1.0-test")
	r := row(42, "dark mode", domain.StageImplement, "thrifty", true)
	r.F.PullRequest = domain.PullRequestRef{Repo: "o/r", Number: 42, URL: "https://github.com/o/r/pull/42"}
	r.Landed = true
	line := m.cardLine(r, 1, false, true, 100)
	if !strings.Contains(line, "PR#42") {
		t.Errorf("card line = %q, want it to contain PR#42", line)
	}
	if !strings.Contains(line, "landed") {
		t.Errorf("card line = %q, want the landed marker still present alongside the PR badge", line)
	}
}

// TestCardLineGateMarker: the ⚡ badge marks only an explicit "auto"
// gate-approval mode. Empty reads as auto everywhere else in the code
// (domain.ValidGateApproval), but every TUI-created card stores empty —
// badging it too would light up the whole board as if each card had
// opted in to something nobody chose.
func TestCardLineGateMarker(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0.1.0-test")
	auto := row(1, "auto card", domain.StageTodo, "", false)
	auto.F.GateApproval = domain.GateGates
	empty := row(2, "empty card", domain.StageTodo, "", false)
	caller := row(3, "caller card", domain.StageTodo, "", false)
	caller.F.GateApproval = domain.GateOff

	if !strings.Contains(m.cardLine(auto, 1, false, true, 80), "⚡") {
		t.Error("explicit auto gate mode should show the ⚡ marker")
	}
	if strings.Contains(m.cardLine(empty, 2, false, true, 80), "⚡") {
		t.Error("empty gate mode reads as auto but must not show the marker")
	}
	if strings.Contains(m.cardLine(caller, 3, false, true, 80), "⚡") {
		t.Error("caller gate mode should not show the marker")
	}
}

// TestNeedsAttention: the board's lookup agrees with attnIcon's own
// per-kind glyph for every attention kind, and reports ok=false for a
// feature with no pending item.
func TestNeedsAttention(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0.1.0-test")
	cases := []attnKind{attnGate, attnFailure, attnQuestion, attnBudget}
	for _, kind := range cases {
		t.Run(string(kind), func(t *testing.T) {
			r := row(1, "gated card", domain.StageImplement, "", false)
			m.inbox.add(r.F.ID, kind, "needs a look")
			icon, ok := m.needsAttention(r)
			if !ok {
				t.Fatalf("needsAttention ok = false, want true for kind %s", kind)
			}
			if want := attnIcon(m.styles, kind); icon != want {
				t.Errorf("needsAttention icon = %q, want %q (attnIcon's own output)", icon, want)
			}
			m.inbox.remove(r.F.ID)
		})
	}
	t.Run("no item", func(t *testing.T) {
		r := row(2, "quiet card", domain.StageImplement, "", false)
		if _, ok := m.needsAttention(r); ok {
			t.Error("needsAttention ok = true for a feature with no pending item")
		}
	})
}

// TestCardLineAttentionOutranksBusy is the precedence rule's own repro: a
// card whose gate is raised while its baseline check is still going must
// show the needs-you icon, not the busy spinner/word — the user can act
// on the gate, not on a check run.
func TestCardLineAttentionOutranksBusy(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0.1.0-test")
	r := row(1, "gated card", domain.StageImplement, "", false)
	m.inbox.add(r.F.ID, attnGate, "implement finished — review & advance")
	m.baselining[r.F.ID] = true
	if !m.cardBusy(r) {
		t.Fatal("setup: want cardBusy true (baseline running)")
	}

	line := m.cardLine(r, 1, false, true, 100)
	want := attnIcon(m.styles, attnGate)
	if !strings.Contains(line, want) {
		t.Errorf("card line = %q, want it to contain the attention icon %q", line, want)
	}
	if strings.Contains(line, "checking") {
		t.Errorf("card line = %q, want no busy word once attention wins the loop slot", line)
	}
	if strings.Contains(line, m.spinner()) {
		t.Errorf("card line = %q, want no spinner frame once attention wins the loop slot", line)
	}
}

// TestCardLinePausedMarker: a card whose live session the user explicitly
// paused (StatePaused) carries the ⏸ mark; a same-stage card that was
// simply never started (no session at all) renders none — the concrete
// "paused reads distinct from parked" symptom this card fixes.
func TestCardLinePausedMarker(t *testing.T) {
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		if opts.Role == agent.RoleScribe {
			return []agent.Event{{Kind: agent.EventIdle}}
		}
		return []agent.Event{
			{Kind: agent.EventMessage, Text: "Wiring the toggle."},
			{Kind: agent.EventToolCall, Tool: "edit theme.go"},
		}
	}}
	m, eng := agentWorkspace(t, ag)
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = openAndAttach(t, m)
	waitForActivity(t, eng)

	pausedRow := m.rows[0]
	if pausedRow.F.ID != "FD-001" || pausedRow.F.Stage != domain.StageImplement {
		t.Fatalf("setup: rows[0] = %+v, want FD-001 at implement", pausedRow.F)
	}
	m = pump(t, m, m.fireVerb("park", ""))
	if got := eng.Get("FD-001"); got == nil || got.State() != engine.StatePaused {
		t.Fatalf("setup: want FD-001 paused, got %+v", got)
	}

	line := m.cardLine(pausedRow, 1, false, true, 100)
	if !strings.Contains(line, "⏸") {
		t.Errorf("paused card line = %q, want the ⏸ mark", line)
	}
	if !strings.Contains(line, stageGlyph(pausedRow.F.Stage)) {
		t.Errorf("paused card line dropped the stage glyph: %q", line)
	}

	parked := row(2, "parked card", domain.StageImplement, "", false)
	parkedLine := m.cardLine(parked, 2, false, true, 100)
	if strings.Contains(parkedLine, "⏸") {
		t.Errorf("parked (no-session) card line = %q, must not show the paused mark", parkedLine)
	}
}

// TestCardLineBaselineBusyWithNoSession is FD-029's other core repro: a
// card running its gummi-checks baseline has no engine session at all —
// m.baselining is the only signal — but the board row must still spin
// with a "checking" word, and the stage glyph must survive rather than
// being overwritten by the spinner.
func TestCardLineBaselineBusyWithNoSession(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0.1.0-test")
	idle := row(1, "idle card", domain.StageSpec, "", false)
	baselining := row(2, "checking card", domain.StageSpec, "", false)
	m.baselining[baselining.F.ID] = true

	idleLine := m.cardLine(idle, 1, false, true, 80)
	if strings.Contains(idleLine, "checking") {
		t.Errorf("idle card line should not show a busy word: %q", idleLine)
	}

	line := m.cardLine(baselining, 2, false, true, 80)
	if !strings.Contains(line, stageGlyph(baselining.F.Stage)) {
		t.Errorf("baselining card line dropped the stage glyph: %q", line)
	}
	if strings.Contains(line, "◔") {
		t.Errorf("baselining card must not show the queued marker: %q", line)
	}
	if !strings.Contains(line, "checking") {
		t.Errorf("baselining card line missing the checking word: %q", line)
	}
}

// TestCardLineForeignRows goldens the two foreign-driven card states this
// feature adds: a foreign-busy row (elsewhere badge + spinner + the
// fixed "running" word) and elsewhere-idle (elsewhere badge + a plain,
// still stage glyph — owned by another process but not currently
// moving). Pinning both keeps elsewhere-idle from later being mistaken
// for a missing or broken marker: it renders identically to a card that
// never spun, which is correct, but is a distinct case worth keeping a
// test on.
func TestCardLineForeignRows(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0.1.0-test")
	busy := row(4, "foreign busy card", domain.StageImplement, "", false)
	busy.DrivenAbroad = true
	busy.Foreign = state.ForeignDrive{Busy: true}
	idle := row(5, "foreign idle card", domain.StageImplement, "", false)
	idle.DrivenAbroad = true
	idle.Foreign = state.ForeignDrive{Busy: false}

	busyLine := m.cardLine(busy, 1, false, true, 80)
	if !strings.Contains(busyLine, "◉ elsewhere") {
		t.Errorf("foreign-busy card line missing the elsewhere badge: %q", busyLine)
	}
	if !strings.Contains(busyLine, "running") {
		t.Errorf("foreign-busy card line missing the running word: %q", busyLine)
	}
	if !strings.Contains(busyLine, m.spinner()) {
		t.Errorf("foreign-busy card line missing the spinner frame: %q", busyLine)
	}

	idleLine := m.cardLine(idle, 2, false, true, 80)
	if !strings.Contains(idleLine, "◉ elsewhere") {
		t.Errorf("elsewhere-idle card line missing the elsewhere badge: %q", idleLine)
	}
	if !strings.Contains(idleLine, stageGlyph(idle.F.Stage)) {
		t.Errorf("elsewhere-idle card line dropped the still stage glyph: %q", idleLine)
	}
	if strings.Contains(idleLine, "running") {
		t.Errorf("elsewhere-idle card line must not show a busy word: %q", idleLine)
	}
	if strings.Contains(idleLine, m.spinner()) {
		t.Errorf("elsewhere-idle card line must not show a spinner: %q", idleLine)
	}

	golden.RequireEqual(t, []byte(m.cardLine(busy, 1, false, true, 80)+"\n"+m.cardLine(idle, 2, false, true, 80)))
}

// TestCardLineAutopilotBadge: a card with AutopilotDriving true wears the
// "◐ autopilot" badge, a card with it false does not, and on a card that
// is both foreign-driven and autopilot-driving the elsewhere badge comes
// first — the two read closest in meaning and sit next to each other by
// construction, not by accident of append order.
func TestCardLineAutopilotBadge(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0.1.0-test")
	driving := row(1, "driven card", domain.StageImplement, "", false)
	driving.AutopilotDriving = true
	idle := row(2, "idle card", domain.StageImplement, "", false)
	idle.AutopilotDriving = false

	if !strings.Contains(m.cardLine(driving, 1, false, true, 80), "◐ autopilot") {
		t.Error("AutopilotDriving=true should show the ◐ autopilot badge")
	}
	if strings.Contains(m.cardLine(idle, 2, false, true, 80), "◐ autopilot") {
		t.Error("AutopilotDriving=false must not show the ◐ autopilot badge")
	}

	both := row(3, "both card", domain.StageImplement, "", false)
	both.DrivenAbroad = true
	both.AutopilotDriving = true
	line := m.cardLine(both, 3, false, true, 100)
	elsewhereAt := strings.Index(line, "◉ elsewhere")
	autopilotAt := strings.Index(line, "◐ autopilot")
	if elsewhereAt < 0 || autopilotAt < 0 || elsewhereAt > autopilotAt {
		t.Errorf("card line = %q, want elsewhere badge before autopilot badge", line)
	}
}

// TestCardLineNarrowWidthGolden locks in BG-037's shed order: as the row
// runs out of room, cost goes first, then the profile tag, then the
// worktree mark, then the PR badge — landed survives longest because it
// and the PR badge are what change what the user should DO with the
// card. The title gives up columns before any badge does.
func TestCardLineNarrowWidthGolden(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0.1.0-test")
	r := row(3, "Warn when a profile is applied across projects", domain.StageImplement, "thrifty", true)
	r.Landed = true
	r.F.PullRequest = domain.PullRequestRef{Repo: "o/r", Number: 72, URL: "https://github.com/o/r/pull/72"}
	r.F.Spend.Credits = 1.23

	var b strings.Builder
	for _, w := range []int{62, 50, 40} {
		b.WriteString(m.cardLine(r, 3, false, true, w) + "\n")
	}
	golden.RequireEqual(t, []byte(b.String()))
}

func TestFormOverlay(t *testing.T) {
	m := populatedShell(100, 30)
	form := newFeatureForm(nil, nil, false, 0, func(formResult) tea.Cmd { return nil })
	form.route = 1 // "skip brainstorm"
	form.focus = featureFieldRoute
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

func TestThreadSpendGolden(t *testing.T) {
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

// bugRow builds a bug card parked at Todo with the given severity for
// cardLine tests.
func bugRow(num int, title string, sev domain.Severity) featureRow {
	id, _ := domain.NewID(domain.KindBug, num)
	slug, _ := domain.Slugify(title)
	f := domain.Feature{
		ID: id, Num: num, Kind: domain.KindBug, Title: title, Slug: slug,
		Stage: domain.StageTodo, Severity: sev, CreatedAt: fixedTime, UpdatedAt: fixedTime,
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
	b.WriteString(m.cardLine(blocked, 1, false, true, 80) + "\n\n")
	b.WriteString("met@plan\n")
	b.WriteString(m.cardLine(met, 2, false, true, 80) + "\n\n")
	b.WriteString("design@brainstorm\n")
	b.WriteString(m.cardLine(design, 3, false, true, 80) + "\n")
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
		r := bugRow(1, c.name, c.sev)
		b.WriteString(m.cardLine(r, 1, false, true, 80) + "\n\n")
	}
	golden.RequireEqual(t, []byte(b.String()))
}

// TestBoardBusyMarkersGolden golden-captures cardLine's busy rendering:
// the stage glyph is kept (never swapped for the spinner) and the busy
// word trails it in the loop slot. A live engine session is exercised as
// a plain assertion test (run_test.go's TestCardBusyStateRunning /
// TestCardBusyStateInteractive) rather than here — an in-flight fake
// agent turn is inherently async and would make a byte-diffed golden
// flaky. This golden instead pins the deterministic busy source
// (m.baselining) and the two non-busy cases (idle, queued) side by side.
func TestBoardBusyMarkersGolden(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0.1.0-test")
	idle := row(1, "idle card", domain.StageSpec, "", false)
	checking := row(2, "checking card", domain.StageSpec, "", false)
	m.baselining[checking.F.ID] = true

	var b strings.Builder
	b.WriteString("idle\n" + m.cardLine(idle, 1, false, true, 80) + "\n\n")
	b.WriteString("checking (baseline, no session)\n" + m.cardLine(checking, 2, false, true, 80) + "\n\n")
	golden.RequireEqual(t, []byte(b.String()))
}

// TestBoardAttentionAndPausedGolden golden-captures the three scenarios
// this card adds to the card line: a pending attention item winning the
// loop slot over a busy baseline, an explicitly paused session's own
// trailing mark, and a card with neither signal.
func TestBoardAttentionAndPausedGolden(t *testing.T) {
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		if opts.Role == agent.RoleScribe {
			return []agent.Event{{Kind: agent.EventIdle}}
		}
		return []agent.Event{
			{Kind: agent.EventMessage, Text: "Wiring the toggle."},
			{Kind: agent.EventToolCall, Tool: "edit theme.go"},
		}
	}}
	m, eng := agentWorkspace(t, ag)
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = openAndAttach(t, m)
	waitForActivity(t, eng)
	pausedRow := m.rows[0]
	m = pump(t, m, m.fireVerb("park", ""))
	if got := eng.Get("FD-001"); got == nil || got.State() != engine.StatePaused {
		t.Fatalf("setup: want FD-001 paused, got %+v", got)
	}

	attnBusy := row(2, "gated and busy", domain.StageImplement, "", false)
	m.inbox.add(attnBusy.F.ID, attnGate, "implement finished — review & advance")
	m.baselining[attnBusy.F.ID] = true

	neither := row(3, "quiet card", domain.StageImplement, "", false)

	var b strings.Builder
	b.WriteString("attention-plus-busy\n")
	b.WriteString(m.cardLine(attnBusy, 1, false, true, 80) + "\n\n")
	b.WriteString("paused\n")
	b.WriteString(m.cardLine(pausedRow, 2, false, true, 80) + "\n\n")
	b.WriteString("neither\n")
	b.WriteString(m.cardLine(neither, 3, false, true, 80) + "\n")
	golden.RequireEqual(t, []byte(b.String()))
}

// TestDisplayOrderSortsTodoBySeverity: with SortSeverity active the todo
// column ranks bugs critical→high→medium→low→empty while other columns
// (in-progress, done) keep creation order.
func TestDisplayOrderSortsTodoBySeverity(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0.1.0-test")
	m.rows = []featureRow{
		bugRow(1, "low bug", domain.SeverityLow),
		bugRow(2, "critical bug", domain.SeverityCritical),
		bugRow(3, "unclassified", ""),
		bugRow(4, "high bug", domain.SeverityHigh),
		bugRow(5, "medium bug", domain.SeverityMedium),
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
		bugRow(3, "newest high", domain.SeverityHigh).withCreated(late),
		bugRow(1, "oldest high", domain.SeverityHigh).withCreated(early),
		bugRow(2, "middle high", domain.SeverityHigh).withCreated(mid),
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

// TestBG038ClearTransientNoticeScopesErrorToFeature: a routine status
// notice clears when the user opens another surface; an error notice
// stamped with the selected feature's id is kept while that feature stays
// selected, but has no standing once the selection moves elsewhere. Before
// the fix, clearTransientNotice exempted every error unconditionally, so an
// error raised about one card rode forward across every later view change,
// including onto an unrelated card's surface.
func TestBG038ClearTransientNoticeScopesErrorToFeature(t *testing.T) {
	m := populatedShell(120, 34)
	m.notice = noticeMsg{text: "critiquing"}
	m.clearTransientNotice()
	if m.notice.text != "" {
		t.Errorf("transient status not cleared: %q", m.notice.text)
	}
	selected := m.rows[m.sel].F.ID
	m.notice = noticeMsg{text: "merge failed", isErr: true, id: selected}
	m.clearTransientNotice()
	if m.notice.text == "" {
		t.Error("an error notice about the selected feature must survive a view change on that feature")
	}
	m.sel = 0
	if m.rows[m.sel].F.ID == selected {
		t.Fatalf("test fixture assumption broken: row 0 is still %s", selected)
	}
	m.clearTransientNotice()
	if m.notice.text != "" {
		t.Errorf("error notice about %s must not survive moving selection to %s, got %q", selected, m.rows[0].F.ID, m.notice.text)
	}
}

// TestBG038QueuedNoticeNotLeftBehind: dispatching a run must not leave a
// free-text "queued" notice on the status bar — the ◔ queued / ⬤ running
// pills already say it live from engine state, so a stale copy of the same
// fact can't survive the run leaving the queue the way the free text did.
// The fake agent settles instantly, so by the time the engine's events are
// drained the session has reached done, not merely running — the notice
// must already be clear well before that.
func TestBG038QueuedNoticeNotLeftBehind(t *testing.T) {
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		if opts.Role == agent.RoleScribe {
			return []agent.Event{{Kind: agent.EventIdle}}
		}
		return []agent.Event{
			{Kind: agent.EventMessage, Text: "Wiring the toggle."},
			{Kind: agent.EventIdle},
		}
	}}
	m, eng := agentWorkspace(t, ag)
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	if m.rows[0].F.Stage != domain.StageImplement {
		t.Fatalf("setup: want FD-001 at implement, got %s", m.rows[0].F.Stage)
	}

	m = openAndAttach(t, m) // opens the card, then answers its pinned decision: start the run
	if strings.Contains(m.notice.text, "queued") {
		t.Errorf("BUG BG-038(2): notice %q still names the queue right after dispatch", m.notice.text)
	}

	m = drainEngineLoop(t, m)
	if s := eng.Get("FD-001"); s == nil || s.State() != engine.StateDone {
		t.Fatalf("setup: want FD-001 done, got %+v", s)
	}
	if strings.Contains(m.notice.text, "queued") {
		t.Errorf("BUG BG-038(2): notice still says %q while the session state is done (left Queued)", m.notice.text)
	}
}

// TestBoardOpensAtTheTopOfTheList: the cursor starts on the first card in
// the order the board paints, not on m.rows[0]. Those are different rows
// and were never meant to be the same one — rows arrive ORDER BY num, so
// row zero is the oldest card, and on any board with history the oldest
// card is finished. The board opened parked on done work.
func TestBoardOpensAtTheTopOfTheList(t *testing.T) {
	m := populatedShell(120, 34)
	// arrange the fixture the way a real board ages: the lowest-numbered
	// card is long done, and it is the one the store returns first.
	m.rows = []featureRow{
		row(1, "the first thing we ever shipped", domain.StageDone, "thrifty", false),
		row(7, "dark mode", domain.StageImplement, "thrifty", true),
		row(9, "rate limits", domain.StageTodo, "thrifty", false),
	}
	m.selectFirstDisplayed()

	got := m.rows[m.sel].F
	if got.Stage == domain.StageDone {
		t.Fatalf("the board opened on a done card (%s)", got.ID)
	}
	// todo leads the display order, so that is where the cursor belongs
	if got.Stage != domain.StageTodo {
		t.Errorf("opened on %s at %s, want the first card in display order (todo)", got.ID, got.Stage)
	}
}

// TestSelectionSurvivesAReload: the cursor is kept on its card by id. It
// used to be an index clamped to the new length, so a reload that added
// or removed a row sliding underneath it moved the cursor onto a
// different card with no keypress — while the user was reading it.
func TestSelectionSurvivesAReload(t *testing.T) {
	m := populatedShell(120, 34)
	m.rows = []featureRow{
		row(7, "dark mode", domain.StageImplement, "thrifty", true),
		row(9, "rate limits", domain.StageTodo, "thrifty", false),
	}
	m.selectFirstDisplayed()
	watching := m.rows[m.sel].F.ID

	// a card lands ahead of it in the store's order, as a fresh one does
	was := m.selectedID()
	m.rows = []featureRow{
		row(2, "brand new", domain.StageTodo, "thrifty", false),
		row(7, "dark mode", domain.StageImplement, "thrifty", true),
		row(9, "rate limits", domain.StageTodo, "thrifty", false),
	}
	m.restoreSel(was)

	if got := m.rows[m.sel].F.ID; got != watching {
		t.Errorf("the reload moved the cursor from %s to %s", watching, got)
	}

	// and when the card really is gone, it falls to the top of the list
	was = m.selectedID()
	m.rows = []featureRow{row(2, "brand new", domain.StageTodo, "thrifty", false)}
	m.restoreSel(was)
	if m.sel != 0 || m.rows[m.sel].F.ID != "FD-002" {
		t.Errorf("a deleted selection did not fall to the top: sel=%d", m.sel)
	}
}
