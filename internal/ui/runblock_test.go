package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/threadfold"
	"github.com/morphis/gummi/internal/webapi"
)

var tuiRunAt = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

// tuiDeliver is the bundled deliver's shape: a design step, a build loop
// (an implementer, two reviews side by side, a verdict), a validation.
func tuiDeliver() domain.Recipe {
	return domain.Recipe{Name: "deliver", Steps: []domain.Entry{
		{Step: &domain.Step{ID: "design", Role: domain.RoleContractor}},
		{Repeat: &domain.Repeat{ID: "build", Max: 3, Steps: []domain.Entry{
			{Step: &domain.Step{ID: "implement", Role: domain.RoleImplementer}},
			{Parallel: &domain.Parallel{ID: "reviews", Join: domain.JoinAllSettled, Steps: []domain.Step{
				{ID: "review-a", Role: domain.RoleReviewer}, {ID: "review-b", Role: domain.RoleReviewer},
			}}},
			{Step: &domain.Step{ID: "verdict", Role: domain.RoleReviewer}},
		}}},
		{Step: &domain.Step{ID: "validate", Role: domain.RoleTester}},
	}}
}

func tuiStep(path string, role domain.WorkflowRole, st domain.StepState, m int, spent float64, out string) domain.RunStep {
	id := path[strings.LastIndex(path, "/")+1:]
	return domain.RunStep{
		Session: "FF-001", Run: 2, Path: path, StepID: id, Card: domain.FeatureID("FF-1" + strings.ReplaceAll(path, "/", "")[:2]),
		Role: role, State: st, Spent: spent, Output: out, StartedAt: tuiRunAt.Add(time.Duration(m) * time.Minute),
	}
}

// tuiRunningRun is run 2 in build's second iteration, its implementer
// asking the person a question.
func tuiRunningRun() threadfold.RunInput {
	return threadfold.RunInput{
		Run: domain.Run{
			Session: "FF-001", N: 2, RecipeName: "deliver", Recipe: tuiDeliver(), Spec: 2, SpecRev: 2,
			State: domain.RunRunning, StartTree: "aaaa", CreatedAt: tuiRunAt, StartedAt: tuiRunAt,
			Until: map[string]domain.UntilJudgement{"build/1": {Met: false, Detail: "CHANGES · 2 findings"}},
		},
		SpecTitle: "Resume pre-migration sessions",
		Files:     []string{"internal/state/migrate_turns.go", "internal/engine/migrate_test.go"},
		Steps: []domain.RunStep{
			tuiStep("design", domain.RoleContractor, domain.StepDone, 0, 40, "Design for the spec\n\nwrote design.md"),
			tuiStep("build/1/implement", domain.RoleImplementer, domain.StepDone, 5, 80, "implemented it"),
			tuiStep("build/1/reviews/review-a", domain.RoleReviewer, domain.StepDone, 9, 30, "findings"),
			tuiStep("build/1/reviews/review-b", domain.RoleReviewer, domain.StepDone, 9, 25, "findings"),
			tuiStep("build/1/verdict", domain.RoleReviewer, domain.StepDone, 12, 35, "CHANGES"),
			tuiStep("build/2/implement", domain.RoleImplementer, domain.StepAsking, 14, 0, ""),
		},
		Live: map[string]threadfold.StepLive{"build/2/implement": {
			Spent: 100, Action: "Edit  internal/state/migrate_turns.go",
			Ask: &engine.Ask{Question: "Resume a pruned v40 session, or refuse it?", Options: []engine.AskOption{
				{Label: "Refuse with a sentence"}, {Label: "Resume with what's left (recommended)", Detail: "the log is ordered by event id"},
			}},
		}},
	}
}

// tuiEndedRun is run 1, done: its checks all passed, so it validated.
func tuiEndedRun() threadfold.RunInput {
	steps := []domain.RunStep{
		tuiStep("design", domain.RoleContractor, domain.StepDone, 0, 40, "Design for the spec"),
		tuiStep("build/1/implement", domain.RoleImplementer, domain.StepDone, 5, 80, "implemented it"),
		tuiStep("build/1/verdict", domain.RoleReviewer, domain.StepDone, 12, 35, "APPROVED"),
		tuiStep("validate", domain.RoleReviewer, domain.StepDone, 20, 20, "MET: DW-1\nchecks pass"),
	}
	for i := range steps {
		steps[i].Run = 1
	}
	// the validating reviewer changed a file it was told to leave alone
	steps[3].Changed = []string{"internal/state/turns_test.go"}
	return threadfold.RunInput{
		Run: domain.Run{
			Session: "FF-001", N: 1, RecipeName: "deliver", Recipe: tuiDeliver(), Spec: 1, SpecRev: 4,
			State: domain.RunDone, StartTree: "aaaa", EndTree: "bbbbbbbbbb",
			CreatedAt: tuiRunAt, StartedAt: tuiRunAt, EndedAt: tuiRunAt.Add(72 * time.Minute),
			Until:   map[string]domain.UntilJudgement{"build/1": {Met: true, Detail: "APPROVED"}},
			Results: []domain.ItemResult{{Item: "DW-1", Verdict: domain.ItemMet, Checked: true}, {Item: "DW-2", Verdict: domain.ItemUnjudged}},
			Commits: []string{"abc1234 feat(state): store every turn"}, Report: "## Report\n\nDone.",
		},
		SpecTitle: "Persist turns",
		Files:     []string{"internal/state/turns.go"},
		Steps:     steps,
	}
}

// tuiReviewsRun is run 2 in build's first iteration, its two reviews at
// work side by side.
func tuiReviewsRun() threadfold.RunInput {
	return threadfold.RunInput{
		Run: domain.Run{
			Session: "FF-001", N: 2, RecipeName: "deliver", Recipe: tuiDeliver(), Spec: 2, SpecRev: 2,
			State: domain.RunRunning, StartTree: "aaaa", CreatedAt: tuiRunAt, StartedAt: tuiRunAt,
		},
		SpecTitle: "Resume pre-migration sessions",
		Files:     []string{"internal/state/migrate_turns.go"},
		Steps: []domain.RunStep{
			tuiStep("design", domain.RoleContractor, domain.StepDone, 0, 40, "Design for the spec"),
			tuiStep("build/1/implement", domain.RoleImplementer, domain.StepDone, 5, 80, "implemented it"),
			tuiStep("build/1/reviews/review-a", domain.RoleReviewer, domain.StepRunning, 9, 0, ""),
			tuiStep("build/1/reviews/review-b", domain.RoleReviewer, domain.StepRunning, 9, 0, ""),
		},
		Live: map[string]threadfold.StepLive{
			"build/1/reviews/review-a": {Busy: true, Spent: 12, Action: "Bash  go test ./internal/state/..."},
			"build/1/reviews/review-b": {Busy: true, Spent: 9, Action: "Read  internal/state/migrate_turns.go"},
		},
	}
}

// A run's block, drawn as text from its fold: the head, the strip, a row
// per step with its role, glyph and spend, a folded iteration, the step's
// question under its row, the files and, once ended, its fold.
func TestRunBlockGolden(t *testing.T) {
	s := m0Styles()
	running := threadfold.FoldRun(tuiRunningRun())
	ended := threadfold.FoldRun(tuiEndedRun())
	proposed := threadfold.FoldRun(threadfold.RunInput{Run: domain.Run{
		Session: "FF-001", N: 3, RecipeName: "fix-until-green", Recipe: tuiDeliver(), Spec: 2, State: domain.RunProposed, CreatedAt: tuiRunAt,
	}, SpecTitle: "Resume pre-migration sessions"})
	var b strings.Builder
	draw := func(label string, lines []string) {
		b.WriteString("== " + label + "\n")
		for _, l := range lines {
			b.WriteString(strings.TrimRight(ansi.Strip(l), " ") + "\n")
		}
	}
	draw("running, its asking step selected", runBlockLines(s, running, "row:build/2/implement", false, false, "14m", "", 110))
	draw("running, unfocused", runBlockLines(s, running, "", false, true, "14m", "", 110))
	draw("ended whole, its steps folded, head selected", runBlockLines(s, ended, "head", false, false, "1h12m", "", 110))
	draw("ended whole, its folded steps selected", runBlockLines(s, ended, "row:"+runStepsRow, false, false, "1h12m", "", 110))
	draw("ended whole, its steps unfolded", runBlockLines(s, ended, "", true, true, "1h12m", "", 110))
	// an ended run no longer the latest news folds to one line (§25.9)
	draw("ended, folded to its line", runFoldedLines(s, ended, false, 110))
	draw("ended, folded to its line, selected", runFoldedLines(s, ended, true, 110))
	for _, st := range []domain.RunState{domain.RunPartial, domain.RunStopped} {
		in := tuiEndedRun()
		in.Run.State = st
		draw("ended "+string(st)+", folded to its line", runFoldedLines(s, threadfold.FoldRun(in), false, 110))
	}
	draw("proposed", runBlockLines(s, proposed, "", false, true, "", "", 110))
	draw("narrow", runBlockLines(s, running, "", false, true, "14m", "", 60))
	draw("two reviews at work side by side", runBlockLines(s, threadfold.FoldRun(tuiReviewsRun()), "", false, true, "16m", "⠹", 110))
	// a proposal's baseline (§25.5): still running, then each checked item
	// before the run, one that proves nothing marked
	checking := threadfold.FoldRun(threadfold.RunInput{Run: domain.Run{
		Session: "FF-001", N: 4, Recipe: domain.Recipe{Name: "one"}, Spec: 2, State: domain.RunProposed, CreatedAt: tuiRunAt,
		Baseline: &domain.RunBaseline{Running: true},
	}})
	draw("proposed, its baseline running", runBlockLines(s, checking, "", false, true, "", "", 110))
	checked := threadfold.FoldRun(threadfold.RunInput{Run: domain.Run{
		Session: "FF-001", N: 4, Recipe: domain.Recipe{Name: "one"}, Spec: 2, State: domain.RunProposed, CreatedAt: tuiRunAt,
		Baseline: &domain.RunBaseline{Tree: "aaaa", Results: []domain.ItemResult{
			{Item: "DW-1", Checked: true, Verdict: domain.ItemUnmet},
			{Item: "DW-2", Checked: true, Verdict: domain.ItemMet},
			{Item: "DW-3", Checked: true, Verdict: domain.ItemUnjudged},
		}},
	}})
	draw("proposed, its baseline found two items that prove nothing", runBlockLines(s, checked, "", false, true, "", "", 110))
	before := tuiEndedRun()
	before.Run.Results = []domain.ItemResult{
		{Item: "DW-1", Verdict: domain.ItemMet, Checked: true, Before: domain.ItemUnmet},
		{Item: "DW-2", Verdict: domain.ItemMet, Checked: true, Before: domain.ItemMet},
		{Item: "DW-3", Verdict: domain.ItemUnjudged},
	}
	draw("ended, before and after", runBlockLines(s, threadfold.FoldRun(before), "", false, true, "1h12m", "", 110))
	// gummi at work between steps, a person's always-allow, the run's
	// artifacts, a paused run's steps and a queued run's withdrawal
	ending := tuiRunningRun()
	ending.Phase, ending.AlwaysAllowed = "ending", true
	ending.Artifacts = []threadfold.RunArtifact{{Name: "design", File: "design.md", Exists: true}, {Name: "review", File: "review.json"}}
	draw("running its end checks, always allowed, head selected", runBlockLines(s, threadfold.FoldRun(ending), "head", false, false, "14m", "", 110))
	paused := tuiRunningRun()
	paused.Run.State, paused.Run.Reason, paused.Run.Pending = domain.RunPaused, "parked: gummi stopped while it ran", nil
	draw("parked", runBlockLines(s, threadfold.FoldRun(paused), "", false, false, "14m", "", 110))
	queued := threadfold.FoldRun(threadfold.RunInput{Run: domain.Run{
		Session: "FF-001", N: 5, Recipe: domain.Recipe{Name: "one"}, Spec: 2, State: domain.RunQueued, CreatedAt: tuiRunAt,
	}})
	draw("queued, head selected", runBlockLines(s, queued, "head", false, false, "", "", 110))
	golden.RequireEqual(t, []byte(b.String()))
}

// The run's notes in its session's transcript ("workflow: run 1
// started") are its block's to say, in both faces; a block stands among
// the turns where its run was proposed.
func TestRunNotesLeaveTheTranscriptAndTheBlockStandsWhereProposed(t *testing.T) {
	s := m0Styles()
	at := func(m int) time.Time { return tuiRunAt.Add(time.Duration(m) * time.Minute) }
	snap := engine.Snapshot{Transcript: []engine.Message{
		{Author: engine.AuthorUser, Content: "make the file", At: at(0)},
		{Author: engine.AuthorAssistant, Content: "proposing a run", At: at(1)},
		{Author: engine.AuthorTool, Content: engine.RunNotePrefix + "run 1 (deliver) proposed on spec 1 · estimated 300", At: at(2)},
		{Author: engine.AuthorTool, Content: engine.RunNotePrefix + "run 1 allowed and started", At: at(3)},
		{Author: engine.AuthorUser, Content: "thanks", At: at(5)},
	}}
	block := timedBlock{at: at(2), lines: []string{"RUN-BLOCK"}}
	got := ansi.Strip(strings.Join(transcriptLinesWith(s, snap, 100, true, []timedBlock{block}), "\n"))
	if strings.Contains(got, "workflow: run 1") {
		t.Errorf("the run's notes are drawn in the terminal's transcript:\n%s", got)
	}
	iProp, iBlock, iThanks := strings.Index(got, "proposing a run"), strings.Index(got, "RUN-BLOCK"), strings.Index(got, "thanks")
	if iProp < 0 || iBlock < iProp || iThanks < iBlock {
		t.Errorf("the block does not stand where its run was proposed:\n%s", got)
	}
	turns, _, _, _ := webTranscript(snap)
	for _, tr := range turns {
		if tr.Tool != nil && strings.Contains(tr.Tool.Label, "workflow: run") {
			t.Errorf("the web's transcript draws a run's note: %+v", tr.Tool)
		}
	}
	if len(turns) != 3 {
		t.Errorf("the web's transcript has %d turns, want the three that are not notes", len(turns))
	}
}

// terminalRun is a session whose one-step run (webRunRecipe) has ended,
// on a headless board: its step wrote out.txt, and every later message is
// answered with reply's words when it has some, else "wrote out.txt".
func terminalRun(t *testing.T, reply func(msg string) string) (*Bridge, *engine.Engine, domain.Feature, *engine.FreeformSession) {
	t.Helper()
	now := time.Now()
	f := domain.Feature{
		ID: "FF-001", Num: 1, Kind: domain.KindFreeform, Title: "Out file", Slug: "out-file",
		Stage: domain.StageOpen, BranchScheme: domain.BranchSchemeKind, CreatedAt: now, UpdatedAt: now,
		Budget: domain.Budget{Envelope: 5000},
	}
	fake := agent.NewFake("ok")
	fake.Caps = agent.Capabilities{ClientTools: true, UsageEvents: true, Interrupt: true}
	fake.Responder = func(opts agent.SessionOpts, msg string) []agent.Event {
		text := "wrote out.txt"
		if strings.Contains(msg, "of workflow run") {
			_ = os.WriteFile(filepath.Join(opts.WorkDir, "out.txt"), []byte("hi\n"), 0o600)
		} else if reply != nil && reply(msg) != "" {
			text = reply(msg)
		}
		return []agent.Event{
			{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 2, Model: "m"}},
			{Kind: agent.EventMessage, Text: text},
			{Kind: agent.EventIdle},
		}
	}
	b, _, eng, _ := headlessBoardWith(t, fake, []domain.Feature{f}, nil)
	waitBoard(t, b, func(bd webapi.Board) bool { return len(bd.Rows) == 1 })
	ctx := context.Background()
	ff, err := eng.OpenFreeform(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := ff.Send(ctx, "hi"); err != nil {
		t.Fatal(err)
	}
	waitUntilTrue(t, "the session's first turn", func() bool { return !ff.Busy() && len(ff.Snapshot().Transcript) > 1 })
	if _, _, err := eng.StartSessionSpec(ctx, f, "Out file", "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.WriteSessionSpec(ctx, f, 1, webRunSpec, "user"); err != nil {
		t.Fatal(err)
	}
	run, err := eng.WorkflowPropose(ctx, f.ID, engine.WorkflowRequest{RecipeYAML: webRunRecipe})
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.WorkflowAnswer(ctx, f.ID, run.N, run.Pending.ID, engine.WorkflowAllow); err != nil {
		t.Fatal(err)
	}
	waitUntilTrue(t, "the run to end", func() bool {
		_, _ = eng.WorkflowTick(ctx)
		r, _ := eng.RunSteps(ctx, f.ID, run.N)
		var got *domain.Run
		_ = b.Do(ctx, func(m *Shell) tea.Cmd { got, _ = m.store.Run(ctx, f.ID, run.N); return nil })
		// past the run's end phase too: nothing is reverted while it is ending
		return len(r) == 1 && got != nil && got.State.Ended() && eng.RunPhase(f.ID, run.N) == ""
	})
	reloadRows(t, b)
	return b, eng, f, ff
}

// An ended run folds to one line once it is no longer the latest news —
// here, once the person says something to the session after it — and a
// person unfolds it whole with enter on its line, and folds it again with
// enter on its head. Their unfolding is kept until they leave the card.
func TestAnEndedRunFoldsAndUnfoldsInTheTerminal(t *testing.T) {
	b, _, f, ff := terminalRun(t, nil)
	do, view, key := shellDriver(t, b)
	thread := func() string { return view(func(m *Shell) string { return m.threadView(137, 46) }) }
	do(func(m *Shell) tea.Cmd { m.width, m.height = 140, 50; m.sel = 0; return m.openCard() })
	waitUntilTrue(t, "the run's block, whole", func() bool { return strings.Contains(thread(), "┌ workflow one · run 1") })

	// the person speaks after the run ended: it is no longer the latest news
	if err := ff.Send(context.Background(), "thanks"); err != nil {
		t.Fatal(err)
	}
	waitUntilTrue(t, "the session to answer", func() bool { return !ff.Busy() })
	folded := "✓ one · run 1 · spec 1 “Out file” · met 1/1"
	waitUntilTrue(t, "the run to fold to its line", func() bool {
		do(func(m *Shell) tea.Cmd { return m.loadRuns(f.ID) })
		return strings.Contains(thread(), folded)
	})
	th := thread()
	if strings.Contains(th, "┌ workflow one · run 1") || !strings.Contains(th, "1 file ▸") {
		t.Fatalf("the folded run still draws its block:\n%s", th)
	}
	// it stands where it was proposed: before the person's later line
	if i, j := strings.Index(th, folded), strings.Index(th, "thanks"); i < 0 || j < i {
		t.Errorf("the folded run does not stand before the line sent after it:\n%s", th)
	}

	// alt+w lands on its line; enter unfolds it whole, enter folds it again
	key("alt+w")
	if !strings.Contains(thread(), "▸ ✓ one · run 1") {
		t.Fatalf("the cursor is not on the folded run's line:\n%s", thread())
	}
	key("enter")
	if th := thread(); !strings.Contains(th, "▸ workflow one · run 1") || !strings.Contains(th, "1 step · write") || !strings.Contains(th, "enter folds it") {
		t.Fatalf("enter did not unfold the run whole:\n%s", th)
	}
	key("enter")
	if th := thread(); !strings.Contains(th, "▸ ✓ one · run 1") || strings.Contains(th, "1 step · write") {
		t.Fatalf("enter on the head did not fold the run again:\n%s", th)
	}
	// unfolded, then the card left and opened again: the rule's fold
	key("enter")
	key("esc")
	do(func(m *Shell) tea.Cmd { m.closeCard(); return m.openCard() })
	waitUntilTrue(t, "the rule's fold again", func() bool { return strings.Contains(thread(), "─ "+folded) })

	// the spec lists its runs, newest first; a row's digit jumps to the
	// run's block in the thread, unfolded whole
	key("alt+s")
	spec := func() string { return view(func(m *Shell) string { return m.mainView(140, 48) }) }
	waitUntilTrue(t, "the spec's runs", func() bool { return strings.Contains(spec(), "runs of S1") })
	if sp := spec(); !strings.Contains(sp, "1 ✓ one · run 1 · done · met 1/1") || !strings.Contains(sp, "ended ") {
		t.Fatalf("the spec does not list its run:\n%s", sp)
	}
	key("1")
	if surface := view(func(m *Shell) string { s, _ := m.activeSurface(); return s }); surface != "runs" {
		t.Fatalf("the spec's row put the keyboard on %q, not the run", surface)
	}
	if th := thread(); !strings.Contains(th, "▸ workflow one · run 1") || !strings.Contains(th, "1 step · write") {
		t.Fatalf("the row did not jump to the run unfolded:\n%s", th)
	}
}

// shellDriver drives a headless board's Shell: do runs fn on it, view
// reads what fn draws with its styling stripped, and key presses a key.
func shellDriver(t *testing.T, b *Bridge) (do func(func(*Shell) tea.Cmd), view func(func(*Shell) string) string, key func(string)) {
	ctx := context.Background()
	do = func(fn func(m *Shell) tea.Cmd) {
		t.Helper()
		if err := b.Do(ctx, fn); err != nil {
			t.Fatal(err)
		}
	}
	view = func(fn func(m *Shell) string) string {
		var out string
		do(func(m *Shell) tea.Cmd { out = ansi.Strip(fn(m)); return nil })
		return out
	}
	key = func(k string) {
		do(func(m *Shell) tea.Cmd { return m.handleKey(keyPress(k)) })
	}
	return do, view, key
}

// A run in the terminal (DESIGN §25.9, §25.10, §25.6): the block stands in
// the session's thread; alt+w steps into it; enter opens the step with its
// crumb, transcript, own diff and brief; a line goes to the step's agent;
// v reverts the run behind a confirm that lists the paths.
func TestARunInTheTerminal(t *testing.T) {
	b, eng, _, ff := terminalRun(t, nil)
	do, view, key := shellDriver(t, b)
	// the card page opens on its thread, and its runs are read beside it
	do(func(m *Shell) tea.Cmd { m.width, m.height = 140, 50; m.sel = 0; return m.openCard() })
	waitUntilTrue(t, "the runs to be read", func() bool {
		return view(func(m *Shell) string { return m.threadView(137, 46) }) != "" &&
			strings.Contains(view(func(m *Shell) string { return m.threadView(137, 46) }), "workflow one · run 1")
	})
	th := view(func(m *Shell) string { return m.threadView(137, 46) })
	for _, want := range []string{"done", "✓ DW-1", "1 step · write", "1 file left changed in the tree: out.txt", "alt+w steps in"} {
		if !strings.Contains(th, want) {
			t.Errorf("the run's block does not say %q:\n%s", want, th)
		}
	}
	if strings.Contains(th, "workflow: run 1") {
		t.Errorf("the run's notes are drawn beside its block:\n%s", th)
	}
	if strings.Contains(th, "validated") || strings.Contains(th, "re-check") {
		t.Errorf("the thread still draws a validated mark:\n%s", th)
	}

	// the composer's table names the runs' chord
	keys := view(func(m *Shell) string {
		_, bs := m.activeSurface()
		var out []string
		for _, b := range bs {
			out = append(out, b.key)
		}
		return strings.Join(out, " ")
	})
	if !strings.Contains(keys, "alt+w") {
		t.Errorf("the card's key table does not name alt+w: %s", keys)
	}

	// alt+w steps in; enter on the ended run's head unfolds its steps,
	// and enter on the step opens it
	key("alt+w")
	if surface := view(func(m *Shell) string { s, _ := m.activeSurface(); return s }); surface != "runs" {
		t.Fatalf("alt+w put the keyboard on %q, not the runs", surface)
	}
	key("enter")
	key("down")
	key("enter")
	waitUntilTrue(t, "the step to open", func() bool {
		return strings.Contains(view(func(m *Shell) string { return m.mainView(140, 48) }), "Session › one run 1 › write")
	})
	sv := view(func(m *Shell) string { return m.mainView(140, 48) })
	if surface := view(func(m *Shell) string { s, _ := m.activeSurface(); return s }); surface != "step" {
		t.Errorf("the opened step's keys are %q's", surface)
	}
	for _, want := range []string{"implementer  write", "transcript  ·  diff d  ·  brief b", "‹ esc  Session › one run 1 › write", "wrote out.txt"} {
		if !strings.Contains(sv, want) {
			t.Errorf("the opened step does not say %q:\n%s", want, sv)
		}
	}
	key("d")
	waitUntilTrue(t, "the step's own diff", func() bool {
		return strings.Contains(view(func(m *Shell) string { return m.mainView(140, 48) }), "+hi")
	})
	key("b")
	if br := view(func(m *Shell) string { return m.mainView(140, 48) }); !strings.Contains(br, "Write out.txt.") {
		t.Errorf("the brief does not hold the prompt as filled in:\n%s", br)
	}
	// a line to the step's agent is a turn on its card
	key("/")
	do(func(m *Shell) tea.Cmd { m.stepv.input.SetValue("one more thing"); return nil })
	key("enter")
	waitUntilTrue(t, "the step's agent to hear the line", func() bool {
		s := eng.Freeform(domain.FeatureID(func() string {
			var c string
			do(func(m *Shell) tea.Cmd { c = string(m.stepv.card); return nil })
			return c
		}()))
		if s == nil {
			return false
		}
		for _, msg := range s.Snapshot().Transcript {
			if msg.Content == "one more thing" {
				return true
			}
		}
		return false
	})
	key("esc")
	key("esc")
	if surface := view(func(m *Shell) string { s, _ := m.activeSurface(); return s }); surface != "runs" {
		t.Fatalf("esc from the step went to %q, not back to the runs", surface)
	}

	// v reverts the run, behind a confirm that lists its paths
	// (a headless board keeps no dialog a web intent does not own, so the
	// confirm is read here rather than off the overlay)
	key("up")
	// f reads the run's whole diff into the diff view, saying whose it is
	var runDiff tea.Cmd
	do(func(m *Shell) tea.Cmd { runDiff = m.handleRunKey("f"); return nil })
	if runDiff == nil {
		t.Fatal("f read no diff")
	}
	if dl, ok := runDiff().(diffLoadedMsg); !ok || !strings.Contains(dl.diff, "+hi") || !strings.Contains(dl.drift, "run 1's own diff") {
		t.Fatalf("the run's diff: %+v", dl)
	}
	// r, w and a say why there is nothing to do on this run
	for k, want := range map[string]string{
		"r": "declares no artifacts", "w": "only a queued run is withdrawn", "a": "allowed one by one already",
	} {
		var said string
		do(func(m *Shell) tea.Cmd { m.handleRunKey(k); said = m.notice.text; return nil })
		if !strings.Contains(said, want) {
			t.Errorf("%s says %q, want %q", k, said, want)
		}
	}
	var preview tea.Cmd
	do(func(m *Shell) tea.Cmd { preview = m.handleRunKey("v"); return nil })
	if preview == nil {
		t.Fatal("v on the ended run read nothing to revert")
	}
	p, ok := preview().(revertPreviewMsg)
	if !ok || p.err != nil || len(p.paths) != 1 || p.paths[0] != "out.txt" {
		t.Fatalf("the revert's preview: %+v", p)
	}
	var revert tea.Cmd
	do(func(m *Shell) tea.Cmd {
		d := revertConfirm(p, m.revertRun)
		if !strings.Contains(d.detail, "out.txt") || !strings.Contains(d.detail, "undoes anything") || !strings.Contains(d.detail, "staged") {
			t.Errorf("the revert's confirm does not list the path and what it undoes: %q", d.detail)
		}
		revert = d.onConfirm()
		return nil
	})
	revert()
	waitUntilTrue(t, "the run's file to be put back", func() bool {
		_, err := os.Stat(filepath.Join(ff.WorkDir(), "out.txt"))
		return os.IsNotExist(err)
	})
}

// keyPress is the key press a key's name says.
func keyPress(k string) tea.KeyPressMsg {
	switch k {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	}
	if rest, ok := strings.CutPrefix(k, "alt+"); ok {
		return tea.KeyPressMsg{Code: []rune(rest)[0], Mod: tea.ModAlt}
	}
	r := []rune(k)[0]
	return tea.KeyPressMsg{Code: r, Text: k}
}
