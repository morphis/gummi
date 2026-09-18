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
	"github.com/morphis/gummi/internal/gatepolicy"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui/theme"
	"github.com/morphis/gummi/internal/verdict"
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
	g.Goal.DoneWhen = []engine.DoneWhenStatus{{ID: "DW-1"}, {ID: "DW-2"}}
	if tag := ansi.Strip(goalRowTag(theme.New(theme.GummiDark()), g, false)); tag != "2 done-when · cards made at approval · 12/4000" {
		t.Fatalf("a drafted plan's cards are not zero, they are not made yet: %q", tag)
	}
	run := goalRow(10, "export works offline", domain.StageImplement)
	if q := decisionQuestion(decisionKind(""), run, nextInput{stage: domain.StageImplement, kind: domain.KindGoal}); strings.Contains(q, "nothing is running") {
		t.Fatalf("a conducting goal is not idle: %q", q)
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
			DoneWhen:  []engine.DoneWhenStatus{{ID: "DW-1", Says: "cache exists", Status: engine.DoneWhenMet, Evidence: "check passed"}, {ID: "DW-2", Says: "docs", Status: engine.DoneWhenNotMet, Evidence: "every card serving it was dropped, and the check as written runs go run, which masks the exit code"}},
			Cards:     []engine.GoalReportCard{{ID: "FD-011", Title: "local cache", State: "landed", Commit: "abcdef1234", Subject: "feat: local cache"}, {ID: "FD-012", Title: "docs", State: "dropped", Reason: "not needed"}},
			Decisions: []engine.GoalLogLine{{Ref: "D-1", Detail: "json by default", Alternative: "table"}},
			Declined:  []engine.GoalLogLine{{Card: "FD-011", Finding: "rename dir", Detail: "fine as is"}},
			Found:     []engine.GoalLogLine{{Card: "BG-020", Detail: "crash on empty input"}},
			TryIt:     "run export --offline",
		},
		log: []state.GoalEntry{{At: fixedTime, GoalPayload: state.GoalPayload{Action: state.GoalLanded, Card: "FD-011", Detail: "abcdef1 feat: local cache"}}},
	}
	lines, cardAt := goalPageLines(theme.New(theme.GummiDark()), gp, 100)
	out := ansi.Strip(strings.Join(lines, "\n"))
	if len(cardAt) != len(gp.report.Cards) {
		t.Fatalf("every card is tagged with the line it landed on: %v", cardAt)
	}
	for i, at := range cardAt {
		if !strings.Contains(ansi.Strip(lines[at]), string(gp.report.Cards[i].ID)) {
			t.Fatalf("cardAt[%d]=%d points at %q", i, at, ansi.Strip(lines[at]))
		}
	}
	for _, want := range []string{
		"ready for you", "partial: 1 card(s) dropped", "1 of 2 done-when met",
		"DW-1 cache exists", "every card serving it was dropped",
		"FD-011 local cache", "landed as abcdef1", "not needed",
		"D-1 json by default", "not: table", "rename dir", "BG-020", "run export --offline", "landed FD-011",
		"masks the exit code", // long evidence wraps rather than clips
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

func TestGoalHandOverSaysWhatWasMet(t *testing.T) {
	g := &engine.GoalReport{DoneWhen: []engine.DoneWhenStatus{{ID: "DW-1", Status: engine.DoneWhenMet}, {ID: "DW-2", Status: engine.DoneWhenNotMet}}}
	in := nextInput{stage: domain.StageVerify, kind: domain.KindGoal, verdict: verdictPass, goal: g}
	if got := verifyStopped(in, "goal doc"); !strings.Contains(got, "1 of 2 done-when met") || strings.Contains(got, "Verify passed") {
		t.Fatalf("a goal's hand-over names what was met, not a plain pass: %q", got)
	}
	g.Partial = "1 card(s) dropped"
	if got := decisionQuestion(decisionVerify, featureRow{F: domain.Feature{Kind: domain.KindGoal}}, in); !strings.Contains(got, "partial: 1 card(s) dropped") {
		t.Fatalf("a partial goal says so at its decision: %q", got)
	}
}

func TestGoalMergeIsNotASquash(t *testing.T) {
	if got := mergeHelp(domain.KindGoal, "main"); strings.Contains(got, "squash") {
		t.Fatalf("a goal lands as a merge commit over its cards' commits: %q", got)
	}
	d := newCommitMsgDialog(domain.Feature{ID: "GL-001", Kind: domain.KindGoal, Slug: "x"}, func(string) tea.Cmd { return nil }, nil)
	if got := ansi.Strip(d.View(theme.New(theme.GummiDark()), 100, 30)); strings.Contains(got, "squash-merge") || !strings.Contains(got, "merge GL-001") {
		t.Fatalf("the goal's commit dialog names a merge:\n%s", got)
	}
}

// A goal card's verify pass wakes its goal, which tells a card ready to
// land from one stuck at a stop by the verified stamp: the stamp is
// written before the wake, never by a command racing the goal's tick.
func TestGoalCardVerifiedStampIsInPlace(t *testing.T) {
	m, _, store, g := goalWorkspace(t)
	ctx := context.Background()
	cards, _ := store.ListGoalCards(ctx, g.ID)
	m.stampVerified(cards[0].ID) // no command to run: the write is done
	c, _ := store.GetFeature(ctx, cards[0].ID)
	if c.VerifiedAt.IsZero() {
		t.Fatal("stampVerified writes the stamp in place")
	}
}

// offersAction reports whether the card inventory offers id.
func offersAction(acts []cardAction, id string) bool {
	for _, a := range acts {
		if a.id == id {
			return true
		}
	}
	return false
}

// TestAGoalsCardIsWatchedNotSteered: a card inside a running goal already
// has a driver — its goal's lead, which starts it, answers it and lands
// it (§17) — so the board watches it and withholds everything that would
// steer it, exactly as it does for a card another gummi process drives.
// The two cards that wear a goal id and are nobody's to conduct, one the
// goal dropped and one already done, keep every verb.
func TestAGoalsCardIsWatchedNotSteered(t *testing.T) {
	m := goalBoard()
	m.sel = 3 // FD-012: implement, inside GL-010, not dropped
	r, ok := m.selected()
	if !ok || !r.watchOnly() || r.watchDriver() != "GL-010's lead" {
		t.Fatalf("a conducted card names its driver: watchOnly=%v driver=%q", r.watchOnly(), r.watchDriver())
	}

	acts := cardActionsFor(nextInput{stage: domain.StageImplement, kind: domain.KindFeature}, r)
	for _, id := range []string{"advance", "bounce", "verify", "merge", "handoff", "envelope", "attach", "delete"} {
		if offersAction(acts, id) {
			t.Errorf("a conducted card still offers %q: %s", id, idsOf(acts))
		}
	}
	for _, id := range []string{"run", "spec", "diff", "ask"} {
		if !offersAction(acts, id) {
			t.Errorf("a conducted card should still offer %q: %s", id, idsOf(acts))
		}
	}
	for _, a := range acts {
		if a.id == "run" && a.label != "watch" {
			t.Errorf("enter on a conducted card watches, it does not run: %q — %q", a.label, a.why)
		}
	}

	// the key answers what the list offers: the two must stay in lockstep
	if cmd := m.boardVerb("m"); cmd != nil {
		t.Error("m on a conducted card must refuse rather than merge")
	}
	if !strings.Contains(m.notice.text, "GL-010's lead") || !m.notice.isErr {
		t.Errorf("the refusal names the driver: %q", m.notice.text)
	}

	// nothing typed into it reaches an agent: the lead is mid-turn on this
	// very card, and the goal's own thread is where a note lands
	if cmd := m.submitThreadLine(r, "try the other library"); cmd != nil {
		t.Error("a line typed into a conducted card must not be delivered")
	}
	if !strings.Contains(m.notice.text, "GL-010") {
		t.Errorf("the composer's refusal points at the goal: %q", m.notice.text)
	}
	if d := m.openDecision(r); d != nil {
		t.Errorf("a conducted card raises no decision here — its lead answers: %+v", d)
	}

	// a card the goal dropped is out of its hands, and a landed one is
	// nobody's to drive: both keep the full inventory
	for _, sel := range []int{2, 4} {
		m.sel = sel
		r, _ := m.selected()
		if r.watchOnly() {
			t.Errorf("%s is not conducted (dropped or done) yet reads as watch-only", r.F.ID)
		}
	}
}

// TestTheGoalPageOpensItsCards: the goal page's cards are a list with a
// cursor, enter opens the selected one to watch, and esc comes back to
// the page rather than dropping the reader on the board underneath it.
func TestTheGoalPageOpensItsCards(t *testing.T) {
	m := goalBoard()
	goal := m.rows[1]
	m.goalPage = &goalPageView{goal: goal.F, report: *goal.Goal}

	// j/k walk the cards; past the last one they go back to scrolling
	m.handleGoalPageKey("j")
	if m.goalPage.cursor != 1 || !m.goalPage.reveal {
		t.Fatalf("j selects the next card and asks to be revealed: cursor=%d reveal=%v", m.goalPage.cursor, m.goalPage.reveal)
	}
	m.handleGoalPageKey("j")
	m.handleGoalPageKey("j")
	if m.goalPage.cursor != 2 || m.goalPage.scroll != 1 {
		t.Fatalf("past the last card j scrolls the page: cursor=%d scroll=%d", m.goalPage.cursor, m.goalPage.scroll)
	}

	m.goalPage.cursor = 1 // FD-012, the one that is running
	m.handleGoalPageKey("enter")
	if !m.cardOpen || m.sel != 3 {
		t.Fatalf("enter opens the selected card's page: open=%v sel=%d", m.cardOpen, m.sel)
	}
	if m.goalPage != nil {
		t.Error("the card page replaces the goal page rather than hiding under it")
	}
	if m.goalReturn != goal.F.ID {
		t.Errorf("the card remembers the page it was entered from: %q", m.goalReturn)
	}
	if !m.goalOpen[goal.F.ID] {
		t.Error("the goal is unfolded behind, so the board agrees with the card in front of the reader")
	}

	// esc goes back to the goal page, not to the board
	cmd, handled := m.backlogKey("esc")
	if !handled || cmd == nil {
		t.Fatalf("esc on a watched card reopens its goal page: handled=%v cmd=%v", handled, cmd)
	}
	if m.cardOpen || m.goalReturn != "" || m.sel != 1 {
		t.Fatalf("esc leaves the card and selects the goal: open=%v return=%q sel=%d", m.cardOpen, m.goalReturn, m.sel)
	}
}

// TestAGoalsReviewEndsOnItsReportNotInYourInbox: this loop had no goal arm
// at all. A goal's own review asking for changes nothing can make burned
// the rework rounds against a conductor that had already finished and then
// raised an escalation — parking the goal in the inbox behind a picker
// with no row that answers one. gatepolicy now returns HandOver for it and
// this is the arm that carries it out: the reason is recorded as what made
// the result partial, the goal moves on to its verify, and nothing reaches
// the inbox.
func TestAGoalsReviewEndsOnItsReportNotInYourInbox(t *testing.T) {
	m, _, store, g := goalWorkspace(t) // a goal at its conducted implement stage
	ctx := context.Background()

	out := gatepolicy.Outcome{Action: gatepolicy.HandOver, Stage: domain.StageImplement, Reason: "goal-review-cap"}
	m = pump(t, m, m.goalReviewUnactionable(g.ID, out))

	got, err := store.GetFeature(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Goal.Partial == "" {
		t.Fatal("the review's ending is what makes the result partial")
	}
	if !strings.Contains(got.Goal.Partial, "could not make") {
		t.Fatalf("the reason names which ending it was: %q", got.Goal.Partial)
	}
	if got.Stage != domain.StageVerify {
		t.Fatalf("the goal goes on to its verify and its hand-over, got %s", got.Stage)
	}
	if _, queued := m.inbox.get(g.ID); queued {
		t.Fatal("a goal's review must never park it in the inbox — its one stop is the hand-over")
	}
}

// TestAGoalsReviewNeverParks guards the invariant the arm above exists to
// keep: whatever its review says, a goal at its review is handed over or
// reworked, never parked for a reader. Park is a card's ending.
func TestAGoalsReviewNeverParks(t *testing.T) {
	for _, v := range []verdict.Verdict{verdict.Pass, verdict.Changes, verdict.Unclear} {
		for _, settled := range []bool{false, true} {
			for _, round := range []int{0, 3} {
				out := gatepolicy.Decide(gatepolicy.Input{
					Stage: domain.StageImplement, Forward: domain.StageVerify, Kind: domain.KindGoal,
					Verdict: v, Corrective: round, CorrectiveMax: 3,
					WorkStage: domain.StageImplement, GoalSettled: settled,
				})
				if out.Action == gatepolicy.Park {
					t.Fatalf("a goal's review parked (verdict %v, settled %v, round %d): %s", v, settled, round, out.Reason)
				}
			}
		}
	}
}

// TestAStoppedGoalIsNotOfferedAStop: "stop the goal" promises that
// verified work lands and the rest is dropped — acts only a running
// conductor performs. The arm used to return it whatever the goal was
// doing, including to a goal whose conductor had already given up, where
// it was also the recommendation. A stopped goal gets its endings instead.
func TestAStoppedGoalIsNotOfferedAStop(t *testing.T) {
	ids := func(acts []nextAction) []string {
		var out []string
		for _, a := range acts {
			out = append(out, a.id)
		}
		return out
	}
	has := func(acts []nextAction, id string) bool {
		for _, a := range acts {
			if a.id == id {
				return true
			}
		}
		return false
	}

	conducting := nextInput{stage: domain.StageImplement, kind: domain.KindGoal}
	if got := nextActions(conducting); !has(got, "goalstop") {
		t.Fatalf("a conducting goal keeps its stop: %v", ids(got))
	}

	// a finished session of its own is what the conductor reads as busy
	// and refuses to tick behind — the goal is not conducting anything
	stopped := nextInput{stage: domain.StageImplement, kind: domain.KindGoal, sess: engine.StateDone}
	got := nextActions(stopped)
	if has(got, "goalstop") {
		t.Fatalf("a goal that is not conducting must not be offered a stop: %v", ids(got))
	}
	for _, want := range []string{"advance", "bounce", "goalpage"} {
		if !has(got, want) {
			t.Fatalf("a stopped goal is offered %q: %v", want, ids(got))
		}
	}
	if got[0].id != "advance" {
		t.Fatalf("the recommendation is the act that finishes it, got %q", got[0].id)
	}

	// and so is an attention item: the goal stopped for a reader
	if got := nextActions(nextInput{stage: domain.StageImplement, kind: domain.KindGoal, attn: attnGate}); has(got, "goalstop") {
		t.Fatalf("a goal stopped for you must not be offered a stop: %v", ids(got))
	}
}

// TestAGoalsReviewGateSaysWhoseAnswerItIs: the head over the picker used
// to be the card's ("implement critique asked for changes — choose what
// happens next"), inviting a decision whose rows a goal never carries.
func TestAGoalsReviewGateSaysWhoseAnswerItIs(t *testing.T) {
	r := featureRow{F: domain.Feature{ID: "GL-001", Kind: domain.KindGoal, Stage: domain.StageImplement}}
	in := nextInput{stage: domain.StageImplement, kind: domain.KindGoal, sess: engine.StateDone, verdict: verdictChanges, exited: true}
	got := decisionQuestion(decisionGate, r, in)
	if !strings.Contains(got, "its cards") {
		t.Fatalf("a goal's review gate names whose answer it was: %q", got)
	}
}
