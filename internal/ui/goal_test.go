package ui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui/theme"
	"github.com/morphis/gummi/internal/worktree"
)

func goalRow(num int, title string, stage domain.Stage) featureRow {
	id, _ := domain.NewID(domain.KindGoal, num)
	slug, _ := domain.Slugify(title)
	return featureRow{F: domain.Feature{ID: id, Num: num, Kind: domain.KindGoal, Title: title, Slug: slug, Stage: stage,
		CreatedAt: fixedTime, UpdatedAt: fixedTime}}
}

func goalBoard() *Shell {
	m := NewShell(theme.GummiDark(), "v0.1.0-test")
	m.now = func() time.Time { return fixedTime }
	g := goalRow(10, "export works offline", domain.StageImplement)
	g.Goal = &engine.GoalReport{
		ID: g.F.ID, Stage: domain.StageImplement,
		DoneWhen: []engine.DoneWhenStatus{{ID: "DW-1", Status: engine.DoneWhenMet}, {ID: "DW-2", Status: engine.DoneWhenUnknown}},
		Cards:    []engine.GoalReportCard{{ID: "FD-011", State: "landed"}, {ID: "FD-012", State: "running"}, {ID: "FD-013", State: "dropped"}},
		Budget:   engine.GoalReportBudget{Envelope: 4000, Total: 1240},
	}
	c1 := row(11, "local cache", domain.StageDone, "", false)
	c1.F.GoalID = g.F.ID
	c2 := row(12, "offline flag", domain.StageImplement, "", true)
	c2.F.GoalID = g.F.ID
	c3 := row(13, "offline flag, first try", domain.StageVerify, "", false)
	c3.F.GoalID, c3.F.GoalDroppedAt = g.F.ID, fixedTime
	m.rows = []featureRow{row(5, "dark mode", domain.StagePlan, "", false), g, c1, c2, c3}
	model, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	return model.(*Shell)
}

func TestBoardFoldsGoalCardsUnderTheirGoal(t *testing.T) {
	m := goalBoard()
	order := m.displayOrder(m.sortMode)
	if len(order) != 2 {
		t.Fatalf("a folded goal hides its cards: order %v", order)
	}
	m.sel = 1 // the goal
	if !m.toggleGoalFold() {
		t.Fatal("f on a goal should fold/unfold it")
	}
	order = m.displayOrder(m.sortMode)
	if len(order) != 5 || order[1] != 1 || order[2] != 2 || order[3] != 3 || order[4] != 4 {
		t.Fatalf("an unfolded goal lists its cards right under it: %v", order)
	}
	view := ansi.Strip(m.backlogView(120, 30))
	if !strings.Contains(view, "└ FD-011") || !strings.Contains(view, "└ FD-012") {
		t.Fatalf("goal cards render as children:\n%s", view)
	}
	if !strings.Contains(view, "1/2 done-when · 1/2 cards · 1240/4000") {
		t.Fatalf("the goal row carries its progress:\n%s", view)
	}
	if !strings.Contains(view, "└ FD-013") || !strings.Contains(view, "⊘ dropped") {
		t.Fatalf("a card the goal dropped stays listed, marked dropped and out of the card count:\n%s", view)
	}
	// folding from a child moves the selection to the goal
	m.sel = 3
	m.toggleGoalFold()
	if m.sel != 1 || m.goalOpen[domain.FeatureID("GL-010")] {
		t.Fatalf("folding from a child selects the goal: sel=%d open=%v", m.sel, m.goalOpen)
	}
	// a plain card has nothing to fold
	m.sel = 0
	if m.toggleGoalFold() {
		t.Fatal("a card outside any goal has nothing to fold")
	}
}

func TestGoalStageActions(t *testing.T) {
	run := stageActions(nextInput{stage: domain.StageImplement, kind: domain.KindGoal})
	if len(run) != 2 || run[0].id != "goalstop" || run[1].id != "goalpage" {
		t.Fatalf("a running goal offers stop and its page: %+v", run)
	}
	ready := stageActions(nextInput{stage: domain.StageVerify, kind: domain.KindGoal, attn: attnGate, verdict: verdictPass})
	var ids []string
	for _, a := range ready {
		ids = append(ids, a.id)
	}
	if strings.Join(ids, ",") != "advance,bounce,goalreverse,handoff,goalpage" {
		t.Fatalf("a goal ready for you offers land, send back, reverse, hand off and its page: %v", ids)
	}
}

func TestGoalWithoutAPlanSaysSo(t *testing.T) {
	g := goalRow(10, "export works offline", domain.StagePlan)
	g.Goal = &engine.GoalReport{ID: g.F.ID, Stage: domain.StagePlan, Budget: engine.GoalReportBudget{Envelope: 4000, Total: 12}}
	tag := ansi.Strip(goalRowTag(theme.New(theme.GummiDark()), g, false))
	if tag != "no plan yet · 12/4000" {
		t.Fatalf("a goal with nothing agreed reads as unplanned, not empty: %q", tag)
	}
}

func TestGoalPlanGateNamesTheLead(t *testing.T) {
	approve := false
	for _, a := range stageActions(nextInput{stage: domain.StagePlan, kind: domain.KindGoal, attn: attnGate}) {
		if a.id != "advance" {
			continue
		}
		approve = true
		if strings.Contains(a.why, "implementer") || !strings.Contains(a.why, "lead") {
			t.Fatalf("approving a goal's plan hands it to its lead, not an implementer: %q", a.why)
		}
	}
	if !approve {
		t.Fatal("a goal's plan gate offers approve")
	}
	for stage, want := range map[domain.Stage]string{domain.StagePlan: "Done when", domain.StageImplement: "Cards", domain.StageVerify: "Verification plan"} {
		if got := currentSpecSection(domain.KindGoal, stage); got != want {
			t.Errorf("a goal at %s pins its doc's %q section, got %q", stage, want, got)
		}
	}
}

func TestGoalPageRendersTheHandOver(t *testing.T) {
	gp := &goalPageView{
		goal: domain.Feature{ID: "GL-010", Kind: domain.KindGoal, Title: "export works offline"},
		report: engine.GoalReport{
			ID: "GL-010", Title: "export works offline", Stage: domain.StageVerify, Ready: true, Partial: "1 card(s) dropped", Lanes: 2,
			DoneWhen:  []engine.DoneWhenStatus{{ID: "DW-1", Says: "cache exists", Status: engine.DoneWhenMet, Evidence: "check passed"}, {ID: "DW-2", Says: "docs", Status: engine.DoneWhenNotMet, Evidence: "every card serving it was dropped"}},
			Cards:     []engine.GoalReportCard{{ID: "FD-011", Title: "local cache", State: "landed", Commit: "abcdef1234", Subject: "feat: local cache"}, {ID: "FD-012", Title: "docs", State: "dropped", Reason: "not needed"}},
			Decisions: []engine.GoalLogLine{{Ref: "D-1", Detail: "json by default", Alternative: "table"}},
			Declined:  []engine.GoalLogLine{{Card: "FD-011", Finding: "rename dir", Detail: "fine as is"}},
			Found:     []engine.GoalLogLine{{Card: "BG-020", Detail: "crash on empty input"}},
			TryIt:     "run export --offline",
		},
		log: []state.GoalEntry{{At: fixedTime, GoalPayload: state.GoalPayload{Action: state.GoalLanded, Card: "FD-011", Detail: "abcdef1 feat: local cache"}}},
	}
	out := ansi.Strip(strings.Join(goalPageLines(theme.New(theme.GummiDark()), gp, 100), "\n"))
	for _, want := range []string{
		"ready for you", "partial: 1 card(s) dropped", "1 of 2 done-when met",
		"DW-1 cache exists", "every card serving it was dropped",
		"FD-011 local cache", "landed as abcdef1", "not needed",
		"D-1 json by default", "rename dir", "BG-020", "run export --offline", "landed FD-011",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("goal page lacks %q:\n%s", want, out)
		}
	}
}

// goalWorkspace wires a shell to a real workspace and a fake-backed
// engine, and puts a goal through its plan gate so its cards exist.
func goalWorkspace(t *testing.T) (*Shell, *engine.Engine, *state.Store, domain.Feature) {
	t.Helper()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	git := func(a ...string) {
		t.Helper()
		if out, err := exec.CommandContext(context.Background(), "git", append([]string{"-C", root}, a...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", a, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.name", "t")
	git("config", "user.email", "t@e.invalid")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-q", "-m", "init")
	ws, err := state.Init(root, root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	wt, err := worktree.NewManager(context.Background(), root, root, store)
	if err != nil {
		t.Fatal(err)
	}
	pool := worktree.WrapSingle(wt)
	ag := agent.NewFake("")
	ag.Responder = func(opts agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{{Kind: agent.EventMessage, Text: "working"}, {Kind: agent.EventIdle}}
	}
	eng := engine.New(engine.Config{Agents: singleAgent(ag), Store: store, Pool: pool, Workspace: ws, Model: "fake-model"})
	t.Cleanup(func() { eng.Close() })

	ctx := context.Background()
	id, _ := domain.NewID(domain.KindGoal, 1)
	g := domain.Feature{ID: id, Num: 1, Kind: domain.KindGoal, Title: "export works offline", Slug: "export-works-offline",
		Stage: domain.StagePlan, Budget: domain.Budget{Envelope: 4000}, CreatedAt: fixedTime, UpdatedAt: fixedTime}
	if err := store.CreateFeature(ctx, &g); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ws.SeqFile(), []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Create(ctx, &g); err != nil {
		t.Fatal(err)
	}
	doc := "# goal\n\n## Objective\n\nOffline export.\n\n## Done when\n\n```gummi-done-when\n- id: DW-1\n  says: cache exists\n  check: test -f cache.txt\n```\n\n" +
		"## Limits\n\n\n## Budget\n\n```gummi-goal\nlanes: 1\n```\n\n## Cards\n\n```gummi-cards\n- title: local cache\n  serves: [DW-1]\n```\n\n" +
		"## Notes\n\n\n## Try it\n\n\n## Review\n\n\n## Verification plan\n\nchecks\n\n## Report\n\n\n"
	p := filepath.Join(root, g.ArtifactPath())
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	if res, err := eng.Advance(ctx, g.ID, "user"); err != nil || res.Status != engine.StatusAdvanced {
		t.Fatalf("goal plan gate: %v %v %q", res.Status, err, res.Reason)
	}
	m := NewShell(theme.GummiDark(), "v0-test")
	m.now = func() time.Time { return fixedTime }
	m.Attach(store, pool, ws)
	m.AttachEngine(eng)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = pump(t, model.(*Shell), m.loadRows)
	return m, eng, store, g
}

func TestBoardConductsAGoal(t *testing.T) {
	m, eng, store, g := goalWorkspace(t)
	ctx := context.Background()
	cards, _ := store.ListGoalCards(ctx, g.ID)
	if len(cards) != 1 {
		t.Fatalf("cards = %+v", cards)
	}
	// the goal asks to be conducted; the tick starts its card
	m = pump(t, m, m.handleEngineEvent(engine.Event{Feature: g.ID, Stage: domain.StageImplement, Kind: engine.EventGoal}))
	c, _ := store.GetFeature(ctx, cards[0].ID)
	if c.Stage != domain.StagePlan {
		t.Fatalf("the board starts the goal's card: stage %s", c.Stage)
	}
	deadline := time.After(testWaitTimeout)
	for eng.Get(c.ID) == nil {
		select {
		case <-deadline:
			t.Fatal("the goal card never got a session")
		case <-time.After(10 * time.Millisecond):
		}
	}

	// a goal card's stop never reaches the inbox; it wakes the goal
	m.raiseEscalation(c.ID, "plan critique cap")
	if _, queued := m.inbox.get(c.ID); queued {
		t.Fatal("a goal card's escalation must not reach the inbox")
	}
	if !m.goalTickQueue[g.ID] {
		t.Fatal("a goal card's stop wakes its goal")
	}
	marks, _ := store.LatestCardMarks(ctx, c.ID)
	if r, _ := marks.ParkReason(); r != state.ParkReasonGaveUp {
		t.Fatalf("the stop is still recorded where the goal reads it: %q", r)
	}

	// a typed line on the running goal is a note for its lead
	m.rows = nil
	m = pump(t, m, m.loadRows)
	for i, r := range m.rows {
		if r.F.ID == g.ID {
			m.sel = i
		}
	}
	gr, _ := m.selected()
	if gr.Goal == nil {
		t.Fatal("a goal row carries its report")
	}
	m = pump(t, m, m.submitThreadLine(gr, "also check Windows paths"))
	log, _ := store.GoalLog(ctx, g.ID)
	var noted bool
	for _, en := range log {
		if en.Action == state.GoalNote && en.Detail == "also check Windows paths" {
			noted = true
		}
	}
	if !noted {
		t.Fatalf("the line should be a goal note: %+v", log)
	}
}
