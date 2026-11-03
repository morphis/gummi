package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/worktree"
)

const watchSpec = "# Watch\n\n## Problem\n\nWatch upstream.\n"

// childRecipe is the workspace recipe a dispatch in these tests names.
const childRecipe = "name: childwork\ntier: standard\nsteps:\n  - { id: work, role: implementer, prompt: \"do {{spec}}\" }\n"

// watchEngine is an engine over a pool with two named repositories, target and lib,
// and an agent card with a spec, an envelope and an open session.
func watchEngine(t *testing.T, wf *wfFake, envelope int) (*Engine, *state.Store, domain.Feature, *FreeformSession) {
	t.Helper()
	wsRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	init := func(root string) {
		t.Helper()
		gitOut(t, root, "init", "-q", "-b", "main")
		gitOut(t, root, "config", "user.name", "t")
		gitOut(t, root, "config", "user.email", "t@e.invalid")
		if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		gitOut(t, root, "add", ".")
		gitOut(t, root, "commit", "-q", "-m", "init")
	}
	init(wsRoot)
	target := filepath.Join(wsRoot, "git", "target")
	if err := os.MkdirAll(target, 0o750); err != nil {
		t.Fatal(err)
	}
	init(target)
	lib := filepath.Join(wsRoot, "git", "lib")
	if err := os.MkdirAll(lib, 0o750); err != nil {
		t.Fatal(err)
	}
	init(lib)
	ws, err := state.Init(wsRoot, wsRoot)
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	pool, err := worktree.NewPool(context.Background(), ws.Root, wsRoot,
		[]worktree.NamedRepo{{Name: "target", Root: target}, {Name: "lib", Root: lib}}, store, false)
	if err != nil {
		t.Fatal(err)
	}
	e := New(Config{Agents: singleAgent(wf), Store: store, Pool: pool, Workspace: ws, Model: "m", Persist: true})
	t.Cleanup(func() { e.Close() })
	dir := filepath.Join(wsRoot, ".gummi", "workflows")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "childwork.yaml"), []byte(childRecipe), 0o600); err != nil {
		t.Fatal(err)
	}

	f := mainCheckoutCard(1, "watch upstream")
	f.Home = true
	f.Budget.Envelope = envelope
	createFeature(t, store, f)
	ctx := context.Background()
	ff, err := e.OpenFreeform(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := ff.Send(ctx, "hi"); err != nil {
		t.Fatal(err)
	}
	waitFreeformIdle(t, ff)
	if _, _, err := e.StartSessionSpec(ctx, f, "Watch", "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.WriteSessionSpec(ctx, f, 1, watchSpec, "user"); err != nil {
		t.Fatal(err)
	}
	return e, store, f, ff
}

func runSchedule(id, agent string, recipe string) *domain.Schedule {
	return &domain.Schedule{
		ID: domain.ScheduleID(id), Name: id, Kind: domain.ScheduleRun, Target: domain.FeatureID(agent),
		Cron: "0 * * * *", Prompt: recipe, Spec: 1, CreatedAt: time.Now(),
	}
}

// A command step runs with no model: its output is the step's and feeds
// the next, an exit that is not declared fails it, and what an earlier
// step printed never adds to the command it is filled into.
func TestACommandStepFeedsTheNextStep(t *testing.T) {
	wf := newWFFake()
	e, _, f, _ := wfEngine(t, wf)
	ctx := context.Background()
	run := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: "name: gate\nsteps:\n" +
		"  - { id: check, run: \"echo v1.2; echo noise >&2\" }\n" +
		"  - { id: keep, run: \"echo {{check.output}} > got.txt\" }\n" +
		"  - { id: use, role: scribe, prompt: \"read {{check.output}}\" }\n"})
	r := tickRuns(t, e, f.ID, run.N, "ended", ended)
	steps := stepPaths(t, e, f.ID, run.N)
	if r.State != domain.RunPartial && r.State != domain.RunDone {
		t.Fatalf("run %s: %s", r.State, r.Reason)
	}
	if c := steps["check"]; c.State != domain.StepDone || c.Output != "v1.2" || c.Card != "" || c.Spent != 0 {
		t.Errorf("check = %+v", c)
	}
	if wf.count("check") != 0 {
		t.Error("a command step sent a turn")
	}
	if !strings.Contains(strings.Join(wf.stepT["use"], "\n"), "v1.2") {
		t.Errorf("the next step never read the output: %q", wf.stepT["use"])
	}
	// what a step printed is one shell word, never more commands
	run2 := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: "name: gate\nsteps:\n" +
		"  - { id: check, run: \"echo 'x; touch pwned'\" }\n" +
		"  - { id: keep, run: \"echo {{check.output}} > got.txt\" }\n"})
	tickRuns(t, e, f.ID, run2.N, "ended", ended)
	dir, _ := e.sessionTree(ctx, f.ID)
	if got, _ := os.ReadFile(filepath.Join(dir, "got.txt")); strings.TrimSpace(string(got)) != "x; touch pwned" {
		t.Errorf("got.txt = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "pwned")); err == nil {
		t.Error("a step's output ran as a command")
	}

	// any other exit fails the step, with the output's tail for the reason
	run3 := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: "name: gate\nsteps:\n" +
		"  - { id: check, run: \"echo oops >&2; exit 3\", quietExit: 1 }\n" +
		"  - { id: after, run: \"echo never\" }\n"})
	tickRuns(t, e, f.ID, run3.N, "ended", ended)
	steps = stepPaths(t, e, f.ID, run3.N)
	if c := steps["check"]; c.State != domain.StepFailed || !strings.Contains(c.Reason, "exited 3") || !strings.Contains(c.Reason, "oops") {
		t.Errorf("failed check = %+v", c)
	}
	if _, ran := steps["after"]; ran {
		t.Error("a step ran after a failed one")
	}
}

// A gate's quiet exit leaves nothing: no run, no steps, no directory, no
// turn and no note — only no-change on the schedule that started it.
func TestAQuietGateLeavesNoRun(t *testing.T) {
	wf := newWFFake()
	e, store, f, ff := watchEngine(t, wf, 2000)
	ctx := context.Background()
	recipe := "name: watch\ntier: cheap\nsteps:\n  - { id: check, run: \"exit 1\", quietExit: 1 }\n" +
		"  - { id: next, role: scribe, prompt: report }\n"
	if err := store.CreateSchedule(ctx, runSchedule("watch", string(f.ID), recipe)); err != nil {
		t.Fatal(err)
	}
	turns := len(wf.sessionTurns())
	activity := len(ff.Session().Snapshot().Transcript)
	fire, err := e.RunSchedule(ctx, "watch", time.Now())
	if err != nil || fire.Outcome.Status != domain.ScheduleOK || fire.Outcome.Detail != "run 1 started" {
		t.Fatalf("fire = %+v %v", fire, err)
	}
	waitUntil(t, "no-change", func() bool {
		_, _ = e.WorkflowTick(ctx)
		sc, _ := store.Schedule(ctx, "watch")
		return sc.LastStatus == domain.ScheduleNoChange
	})
	if runs, _ := store.SessionRuns(ctx, f.ID); len(runs) != 0 {
		t.Errorf("runs left: %+v", runs)
	}
	if steps, _ := store.RunSteps(ctx, f.ID, 1); len(steps) != 0 {
		t.Errorf("steps left: %+v", steps)
	}
	if _, err := os.Stat(e.RunDir(f.ID, 1)); err == nil {
		t.Error("the run's directory is left")
	}
	if wf.count("next") != 0 {
		t.Error("a step after the gate ran")
	}
	time.Sleep(100 * time.Millisecond)
	if got := len(wf.sessionTurns()); got != turns {
		t.Errorf("the agent was sent %d turn(s)", got-turns)
	}
	for _, m := range ff.Session().Snapshot().Transcript[activity:] {
		if strings.Contains(m.Content, "run 1") || strings.Contains(m.Content, "workflow") {
			t.Errorf("the thread holds a note: %q", m.Content)
		}
	}
	if sc, _ := store.Schedule(ctx, "watch"); sc.LastDetail == "" || sc.LastRun.IsZero() {
		t.Errorf("the schedule's row: %+v", sc)
	}
}

// A run schedule starts an allowed run on its agent from the frozen
// recipe, skips while that run is open, and refuses a card that is no
// agent.
func TestARunScheduleStartsARunOnItsAgent(t *testing.T) {
	wf := newWFFake()
	gateOpen := newGate(t)
	wf.on("hold", func(string, string, int) wfReply {
		<-gateOpen.ch
		return wfReply{text: "done"}
	})
	e, store, f, _ := watchEngine(t, wf, 2000)
	ctx := context.Background()
	recipe := "name: watch\ntier: cheap\nsteps:\n  - { id: check, run: \"echo go\" }\n  - { id: hold, role: scribe, prompt: wait }\n"
	if err := store.CreateSchedule(ctx, runSchedule("watch", string(f.ID), recipe)); err != nil {
		t.Fatal(err)
	}
	fire, err := e.RunSchedule(ctx, "watch", time.Now())
	if err != nil || fire.Outcome.Status != domain.ScheduleOK {
		t.Fatalf("fire = %+v %v", fire, err)
	}
	run, _ := store.Run(ctx, f.ID, 1)
	if run == nil || run.Schedule != "watch" || run.State != domain.RunRunning || run.Pending != nil ||
		run.RecipeYAML != recipe || run.Spec != 1 {
		t.Fatalf("run = %+v", run)
	}
	tickRuns(t, e, f.ID, 1, "holding", func(r *domain.Run) bool { return stepPaths(t, e, f.ID, 1)["hold"].Card != "" })
	if steps := stepPaths(t, e, f.ID, 1); steps["check"].State != domain.StepDone {
		t.Errorf("check = %+v", steps["check"])
	}
	fire, err = e.RunSchedule(ctx, "watch", time.Now())
	if err != nil || fire.Outcome.Status != domain.ScheduleSkippedBusy {
		t.Fatalf("a fire over an open run = %+v %v", fire, err)
	}
	gateOpen.open()
	tickRuns(t, e, f.ID, 1, "ended", ended)

	// a card that is no agent turns the schedule off
	other := freeformCard(7, "not an agent")
	createFeature(t, store, other)
	bad := runSchedule("bad", string(other.ID), recipe)
	if err := store.CreateSchedule(ctx, bad); err != nil {
		t.Fatal(err)
	}
	fire, err = e.RunSchedule(ctx, "bad", time.Now())
	if err != nil || fire.Outcome.Status != domain.ScheduleDisabledTargetClosed || !fire.Outcome.Disable {
		t.Fatalf("a run schedule on a session = %+v %v", fire, err)
	}
}

// dispatchRecipe is a gate and a dispatch of the childwork recipe into target.
// dispatchRecipe's envelope is in credits, written as the dollars a
// recipe takes.
func dispatchRecipe(envelope int) string {
	return "name: watch\ntier: cheap\nsteps:\n  - { id: check, run: \"echo v2\" }\n" +
		"  - id: bump\n    dispatch:\n      repo: target\n      title: \"Bump to {{check.output}}\"\n      envelope: " + domain.DollarsInput(envelope) + "\n" +
		"      recipe: childwork\n      spec: |\n        Bump to {{check.output}}.\n"
}

// A dispatch mints a session in its named repo with its envelope, records
// it, writes the spec and starts the frozen recipe's run already allowed.
func TestADispatchMintsASessionInItsRepo(t *testing.T) {
	wf := newWFFake()
	e, store, f, ff := watchEngine(t, wf, 2000)
	ctx := context.Background()
	run := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: dispatchRecipe(30), Spec: 1})
	tickRuns(t, e, f.ID, run.N, "ended", ended)

	ds, err := store.Dispatched(ctx, f.ID)
	if err != nil || len(ds) != 1 {
		t.Fatalf("dispatches %+v %v", ds, err)
	}
	d := ds[0]
	child, err := store.GetFeature(ctx, d.Card)
	if err != nil {
		t.Fatal(err)
	}
	if child.Repo != "target" || child.Budget.Envelope != 30 || d.Envelope != 30 || d.Run != run.N || d.Path != "bump" ||
		child.Title != "Bump to v2" || child.Home || child.IsChild() {
		t.Errorf("child %+v, dispatch %+v", child, d)
	}
	steps := stepPaths(t, e, f.ID, run.N)
	if s := steps["bump"]; s.State != domain.StepDone || s.Output != "dispatched "+string(child.ID) || s.Card != "" {
		t.Errorf("bump = %+v", s)
	}
	// The child's run may already have ended and written its report below
	// the spec the dispatch wrote; only what the dispatch wrote is checked.
	text, ok := e.SessionSpec(child, 1)
	if written, _, _ := strings.Cut(text, "\n## Report\n"); !ok || written != "Bump to v2.\n" {
		t.Errorf("the child's spec 1 = %q %v", text, ok)
	}
	cr := tickRuns(t, e, child.ID, 1, "the child's run ended", ended)
	if cr.RecipeName != "childwork" || cr.RecipeYAML != childRecipe || cr.Pending != nil || cr.Schedule != "" {
		t.Errorf("the child's run = %+v", cr)
	}
	if wf.count("work") != 1 {
		t.Errorf("the child's step ran %d times", wf.count("work"))
	}
	// the agent's thread says so
	lines := ""
	for range 200 {
		lines = ""
		for _, m := range ff.Session().Snapshot().Transcript {
			lines += m.Content + "\n"
		}
		if strings.Contains(lines, DispatchNotePrefix+"session "+string(child.ID)) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(lines, DispatchNotePrefix+"run 1 dispatched session "+string(child.ID)) ||
		!strings.Contains(lines, DispatchNotePrefix+"session "+string(child.ID)) {
		t.Errorf("the agent's thread:\n%s", lines)
	}
	// the child's envelope is held against the agent's
	agentCard, _ := store.GetFeature(ctx, f.ID)
	if held := e.dispatchedHeld(ctx, f.ID); held < 30 {
		t.Errorf("held %.0f", held)
	}
	if left, _ := e.sessionPoolLeft(ctx, agentCard); left > 2000-30 {
		t.Errorf("the agent has %.0f left with a child of 30 open", left)
	}
	// a dispatch step that restarts finds its dispatch and mints nothing more
	if err := e.startStep(ctx, f.ID, run.N, "bump"); err != nil {
		t.Logf("restart: %v", err)
	}
	if ds, _ := store.Dispatched(ctx, f.ID); len(ds) != 1 {
		t.Errorf("a restarted dispatch minted again: %+v", ds)
	}
}

// A dispatch the agent's pool cannot cover pauses the run for a top-up and
// mints nothing.
func TestADispatchTheAgentCannotCoverPauses(t *testing.T) {
	wf := newWFFake()
	e, store, f, _ := watchEngine(t, wf, 4)
	ctx := context.Background()
	run := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: dispatchRecipe(30), Spec: 1})
	r := tickRuns(t, e, f.ID, run.N, "paused for money", func(r *domain.Run) bool {
		return r.State == domain.RunPaused && r.Pending != nil && r.Pending.Kind == domain.RunDecideBudget
	})
	if !r.OutOfBudget || !strings.Contains(r.Reason, "dispatch to target") {
		t.Errorf("run %+v", r)
	}
	if ds, _ := store.Dispatched(ctx, f.ID); len(ds) != 0 {
		t.Errorf("a dispatch was minted: %+v", ds)
	}
	if st := stepPaths(t, e, f.ID, run.N)["bump"]; st.State.Settled() {
		t.Errorf("the paused step settled: %+v", st)
	}
	cards, _ := store.ListFeatures(ctx)
	for _, c := range cards {
		if c.Repo == "target" {
			t.Errorf("a card was minted in target: %+v", c)
		}
	}
}

// What a person allows is what runs: proposing a run, or a schedule,
// copies each dispatch's named recipe into the recipe; a later edit of the
// recipe changes nothing, and only an agent's recipe may dispatch.
func TestADispatchRecipeIsFrozenAtProposal(t *testing.T) {
	wf := newWFFake()
	e, store, f, ff := watchEngine(t, wf, 2000)
	ctx := context.Background()
	run, err := e.WorkflowPropose(ctx, f.ID, WorkflowRequest{RecipeYAML: dispatchRecipe(5), Spec: 1})
	if err != nil {
		t.Fatal(err)
	}
	var frozen string
	for _, d := range dispatchesOf(run.Recipe) {
		frozen = d.RecipeYAML
	}
	if frozen != childRecipe || !strings.Contains(run.RecipeYAML, "recipeYAML") {
		t.Fatalf("frozen = %q, run's text:\n%s", frozen, run.RecipeYAML)
	}
	q := run.Pending.Question
	for _, want := range []string{"check · run", "bump · dispatch → target", "check: echo v2", "in target", "childwork"} {
		if !strings.Contains(q, want) {
			t.Errorf("the proposal does not say %q:\n%s", want, q)
		}
	}
	// editing the workspace's recipe afterwards moves nothing
	path := filepath.Join(e.pool.Root(), ".gummi", "workflows", "childwork.yaml")
	if err := os.WriteFile(path, []byte(strings.Replace(childRecipe, "do ", "undo ", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Run(ctx, f.ID, run.N)
	for _, d := range dispatchesOf(got.Recipe) {
		if d.RecipeYAML != childRecipe {
			t.Errorf("the allowed recipe changed: %q", d.RecipeYAML)
		}
	}

	// a schedule's proposal is frozen the same way
	args, _ := json.Marshal(map[string]any{"name": "bumper", "preset": "1h", "recipe_yaml": dispatchRecipe(5)})
	if _, err := e.DispatchClientTool(ctx, ff.Session(), scheduleProposeToolName, args); err != nil {
		t.Fatal(err)
	}
	sc, err := store.Schedule(ctx, "bumper")
	if err != nil || !strings.Contains(sc.Prompt, "undo ") {
		t.Fatalf("schedule %+v %v", sc, err)
	}

	// a session that is not an agent cannot propose a dispatch
	pe, _, pf, _ := wfEngine(t, newWFFake())
	if _, err := pe.WorkflowPropose(ctx, pf.ID, WorkflowRequest{RecipeYAML: dispatchRecipe(5), Spec: 1}); err == nil ||
		!strings.Contains(err.Error(), "not an agent") {
		t.Errorf("a session's proposal of a dispatch = %v", err)
	}
}

// schedule_propose from an agent stores a run schedule that is off, and
// leaves a line in its thread; a session that is no agent is refused.
func TestScheduleProposeLeavesTheScheduleOff(t *testing.T) {
	wf := newWFFake()
	e, store, f, ff := watchEngine(t, wf, 2000)
	ctx := context.Background()
	args, _ := json.Marshal(map[string]any{"name": "Watch Foo", "cron": "5 * * * *", "recipe_yaml": dispatchRecipe(5), "spec": 1})
	out, err := e.DispatchClientTool(ctx, ff.Session(), scheduleProposeToolName, args)
	if err != nil || !strings.Contains(out, "off") || strings.Contains(out, "error") {
		t.Fatalf("answer %q %v", out, err)
	}
	sc, err := store.Schedule(ctx, "watch-foo")
	if err != nil {
		t.Fatal(err)
	}
	if sc.Kind != domain.ScheduleRun || sc.Enabled || sc.Target != f.ID || sc.Spec != 1 || sc.Cron != "5 * * * *" || !sc.NextRun.IsZero() {
		t.Errorf("schedule %+v", sc)
	}
	thread := ""
	for _, m := range ff.Session().Snapshot().Transcript {
		thread += m.Content + "\n"
	}
	if !strings.Contains(thread, DispatchNotePrefix+"proposed the schedule") {
		t.Errorf("no line in the thread:\n%s", thread)
	}
	// the same name again, a recipe that cannot run and a spec that is not there are refused
	for _, bad := range []map[string]any{
		{"name": "Watch Foo", "preset": "1h", "recipe_yaml": dispatchRecipe(5)},
		{"name": "two", "preset": "1h", "recipe_yaml": "name: x\nsteps:\n  - { id: a, run: x, role: scout }\n"},
		{"name": "three", "preset": "1h", "recipe_yaml": dispatchRecipe(5), "spec": 9},
		{"name": "four", "preset": "2x", "recipe_yaml": dispatchRecipe(5)},
	} {
		args, _ := json.Marshal(bad)
		if out, _ := e.DispatchClientTool(ctx, ff.Session(), scheduleProposeToolName, args); !strings.HasPrefix(out, "error: ") {
			t.Errorf("%v answered %q", bad, out)
		}
	}
	if rows, _ := store.ListSchedules(ctx); len(rows) != 1 {
		t.Errorf("%d schedules", len(rows))
	}

	// not an agent
	pe, pstore, pf, pff := wfEngine(t, newWFFake())
	_ = pf
	out, err = pe.DispatchClientTool(ctx, pff.Session(), scheduleProposeToolName, args)
	if err != nil || !strings.Contains(out, "not one") {
		t.Errorf("a session's answer %q %v", out, err)
	}
	if rows, _ := pstore.ListSchedules(ctx); len(rows) != 0 {
		t.Errorf("a session proposed a schedule: %+v", rows)
	}
}

// session_status and session_send answer only for sessions the agent
// dispatched.
func TestAnAgentDrivesOnlyItsOwnDispatches(t *testing.T) {
	wf := newWFFake()
	e, store, f, ff := watchEngine(t, wf, 2000)
	ctx := context.Background()
	mine := mainCheckoutCard(2, "my child")
	stranger := mainCheckoutCard(3, "someone else's")
	for _, c := range []domain.Feature{mine, stranger} {
		createFeature(t, store, c)
	}
	if err := store.RecordDispatch(ctx, state.Dispatch{Card: mine.ID, Agent: f.ID, Run: 1, Path: "bump", Envelope: 9}); err != nil {
		t.Fatal(err)
	}
	call := func(tool string, args map[string]any) string {
		raw, _ := json.Marshal(args)
		out, err := e.DispatchClientTool(ctx, ff.Session(), tool, raw)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if out := call(sessionStatusToolName, nil); !strings.Contains(out, string(mine.ID)) || strings.Contains(out, string(stranger.ID)) ||
		!strings.Contains(out, "$0.09") {
		t.Errorf("status = %q", out)
	}
	if out := call(sessionStatusToolName, map[string]any{"card": string(mine.ID)}); !strings.Contains(out, "my child") {
		t.Errorf("status of mine = %q", out)
	}
	if out := call(sessionStatusToolName, map[string]any{"card": string(stranger.ID)}); !strings.HasPrefix(out, "error: ") ||
		!strings.Contains(out, "not a session you dispatched") {
		t.Errorf("status of a stranger = %q", out)
	}
	before := len(wf.sessionTurns())
	if out := call(sessionSendToolName, map[string]any{"card": string(stranger.ID), "text": "hello"}); !strings.HasPrefix(out, "error: ") {
		t.Errorf("send to a stranger = %q", out)
	}
	if out := call(sessionSendToolName, map[string]any{"card": f.ID.Ref(), "text": "hello"}); !strings.HasPrefix(out, "error: ") {
		t.Errorf("send to itself = %q", out)
	}
	if got := len(wf.sessionTurns()); got != before {
		t.Fatalf("a turn went to a card that is not the agent's dispatch")
	}
	if out := call(sessionSendToolName, map[string]any{"card": string(mine.ID), "text": "please rebase"}); out != "sent to "+string(mine.ID) {
		t.Errorf("send to mine = %q", out)
	}
	waitUntil(t, "the turn arrives", func() bool {
		for _, m := range wf.sessionTurns() {
			if m == "please rebase" {
				return true
			}
		}
		return false
	})
	// another agent has dispatched nothing
	other := mainCheckoutCard(4, "other agent")
	other.Home = true
	createFeature(t, store, other)
	if ds, _ := store.Dispatched(ctx, other.ID); len(ds) != 0 {
		t.Errorf("%+v", ds)
	}
}

// An agent is told it may start work in a repository through a dispatch,
// and its repo_list names the repositories a dispatch may target; it
// attaches nothing.
func TestAnAgentLearnsWhereItMayDispatch(t *testing.T) {
	wf := newWFFake()
	var mu sync.Mutex
	var hints []string
	var tools []string
	wf.OnNewSession = func(opts agent.SessionOpts) {
		mu.Lock()
		defer mu.Unlock()
		hints = opts.SystemHints
		for _, td := range opts.Tools {
			tools = append(tools, td.Name)
		}
	}
	e, _, _, ff := watchEngine(t, wf, 2000)
	ctx := context.Background()

	mu.Lock()
	joined := strings.Join(hints, "\n")
	offered := strings.Join(tools, " ")
	mu.Unlock()
	for _, want := range []string{"you dispatch one", "workflow_run and a recipe holding the dispatch"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the hints do not say %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "is the person's to start as a card.") {
		t.Errorf("the hints still say only the person starts repository work:\n%s", joined)
	}
	if !strings.Contains(offered, repoListToolName) || strings.Contains(offered, repoAttachToolName) {
		t.Errorf("tools offered = %s, want repo_list and no repo_attach", offered)
	}

	out, err := e.DispatchClientTool(ctx, ff.Session(), repoListToolName, []byte(`{}`))
	if err != nil || !strings.Contains(out, "repositories a dispatch may name: lib, target") {
		t.Errorf("repo_list = %q %v", out, err)
	}
	if out, _ := e.DispatchClientTool(ctx, ff.Session(), repoAttachToolName, []byte(`{"repo":"lib"}`)); !strings.Contains(out, "not one") {
		t.Errorf("an agent attached a repository: %q", out)
	}
}
