package engine

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// oneStep is a recipe of one implementer step, id one.
const oneStep = "name: one\nsteps:\n  - { id: one, role: implementer, prompt: build }\n"

// TestAParkedRunAsksToBeResumed: a run running under a gummi that is gone
// is parked when this one comes back, and it asks its person — resume it,
// or stop it — rather than sitting paused with nobody told. Resumed, it is
// this process's to conduct and runs to its end.
func TestAParkedRunAsksToBeResumed(t *testing.T) {
	wf := newWFFake()
	e, store, f, _ := wfEngine(t, wf)
	ctx := context.Background()
	run := &domain.Run{
		Session: f.ID, RecipeName: "x", State: domain.RunRunning, Spec: 1, SpecRev: 1, StartedAt: time.Now(),
		Recipe: domain.Recipe{Name: "x", Steps: []domain.Entry{{Step: &domain.Step{ID: "one", Role: domain.RoleScout, Prompt: "look"}}}},
	}
	if err := store.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	// updated after this engine booted, as a run another gummi left is
	res, err := e.WorkflowTick(ctx)
	if err != nil || !reflect.DeepEqual(res.Actions, []string{"park"}) {
		t.Fatalf("tick %+v %v", res, err)
	}
	r, _ := store.Run(ctx, f.ID, run.N)
	if r.State != domain.RunPaused || r.Pending == nil || r.Pending.Kind != domain.RunDecideResume ||
		!reflect.DeepEqual(r.Pending.Options, []string{WorkflowResumeRun, WorkflowStopRun}) {
		t.Fatalf("parked run %s, decision %+v", r.State, r.Pending)
	}
	if got, d := e.WorkflowDecision(ctx, f.ID); got == nil || d.Kind != domain.RunDecideResume {
		t.Fatalf("the session's decision is %+v", d)
	}
	if err := e.WorkflowAnswer(ctx, f.ID, run.N, r.Pending.ID, WorkflowResumeRun); err != nil {
		t.Fatal(err)
	}
	if r = tickRuns(t, e, f.ID, run.N, "ended", ended); r.State == domain.RunStopped || wf.count("one") != 1 {
		t.Fatalf("resumed run ended %s, its step ran %d", r.State, wf.count("one"))
	}
}

// TestARunsClockLeavesItsPausesOut: a run's elapsed time is the time it
// worked: its clock stops while it is paused and runs again when it
// resumes.
func TestARunsClockLeavesItsPausesOut(t *testing.T) {
	wf := newWFFake()
	g := newGate(t)
	wf.on("one", func(string, string, int) wfReply {
		<-g.ch
		return wfReply{text: "done", credits: 1}
	})
	e, store, f, _ := wfEngine(t, wf)
	ctx := context.Background()
	run := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: oneStep})
	r, _ := store.Run(ctx, f.ID, run.N)
	if r.ResumedAt.IsZero() || r.Active != 0 {
		t.Fatalf("a started run's clock: resumed %v, active %s", r.ResumedAt, r.Active)
	}
	time.Sleep(20 * time.Millisecond)
	if err := e.WorkflowPause(ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	r, _ = store.Run(ctx, f.ID, run.N)
	worked := r.Active
	if !r.ResumedAt.IsZero() || worked < 20*time.Millisecond {
		t.Fatalf("a paused run's clock: resumed %v, active %s", r.ResumedAt, worked)
	}
	time.Sleep(50 * time.Millisecond)
	if _, err := e.WorkflowTick(ctx); err != nil {
		t.Fatal(err)
	}
	if r, _ = store.Run(ctx, f.ID, run.N); r.Active != worked {
		t.Fatalf("the clock ran while the run was paused: %s → %s", worked, r.Active)
	}
	if err := e.WorkflowResume(ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	g.open()
	r = tickRuns(t, e, f.ID, run.N, "ended", ended)
	if !r.ResumedAt.IsZero() || r.Active < worked || r.Active >= r.EndedAt.Sub(r.StartedAt) {
		t.Fatalf("an ended run worked %s of %s", r.Active, r.EndedAt.Sub(r.StartedAt))
	}
}

// noChecksSpec is the test spec with its one checked item judged instead.
var noChecksSpec = strings.Replace(wfTestSpec, "check: test -f out.txt", "judge: true", 1)

// TestAnUntilChecksOnASpecWithoutChecksAsks: a loop that runs until the
// checks pass, on a spec with none, cannot judge itself done: its
// proposal warns of it, and after an iteration it asks the person rather
// than counting the loop met.
func TestAnUntilChecksOnASpecWithoutChecksAsks(t *testing.T) {
	wf := newWFFake()
	e, _, f, _ := wfEngine(t, wf)
	ctx := context.Background()
	if _, err := e.WriteSessionSpec(ctx, f, 1, noChecksSpec, "user"); err != nil {
		t.Fatal(err)
	}
	run, err := e.WorkflowPropose(ctx, f.ID, WorkflowRequest{Recipe: "fix-until-green"})
	if err != nil {
		t.Fatal(err)
	}
	if !containsWord(run.Pending.Warnings, "cannot judge itself done") {
		t.Fatalf("warnings %q", run.Pending.Warnings)
	}
	if err := e.WorkflowAnswer(ctx, f.ID, run.N, run.Pending.ID, WorkflowAllow); err != nil {
		t.Fatal(err)
	}
	r := tickRuns(t, e, f.ID, run.N, "asked", pendingOf(domain.RunDecideUnjudged))
	if u := r.Until["fix/1"]; u.Met || !u.Unjudged || !strings.Contains(u.Detail, "no done-when checks") {
		t.Fatalf("until %+v", u)
	}
}

// TestAProposalWarnsOfARecipeThatWritesNothing: checks no step of the
// recipe can make pass are said on the proposal: the investigate recipe
// writes no code, so a check failing now fails at its end too.
func TestAProposalWarnsOfARecipeThatWritesNothing(t *testing.T) {
	wf := newWFFake()
	e, _, f, _ := wfEngine(t, wf)
	run, err := e.WorkflowPropose(context.Background(), f.ID, WorkflowRequest{Recipe: "investigate"})
	if err != nil {
		t.Fatal(err)
	}
	if !containsWord(run.Pending.Warnings, "no step of this recipe writes code") {
		t.Fatalf("warnings %q", run.Pending.Warnings)
	}
	if !strings.Contains(run.Pending.Question, "no step of this recipe writes code") {
		t.Fatalf("the question does not say it:\n%s", run.Pending.Question)
	}
	// a recipe with an implementer is not warned of
	other, err := e.WorkflowPropose(context.Background(), f.ID, WorkflowRequest{RecipeYAML: oneStep})
	if err == nil && containsWord(other.Pending.Warnings, "writes code") {
		t.Fatalf("an implementer's recipe was warned of: %q", other.Pending.Warnings)
	}
}

func containsWord(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestARevertPutsBackWhatTheRunStaged: a step that stages its work (or a
// session that stages it after) leaves nothing staged for the next commit
// to bring back once the run is reverted; and a running run is refused a
// revert.
func TestARevertPutsBackWhatTheRunStaged(t *testing.T) {
	wf := newWFFake()
	g := newGate(t)
	wf.on("one", func(dir, _ string, _ int) wfReply {
		<-g.ch
		_ = os.WriteFile(filepath.Join(dir, "made.txt"), []byte("made\n"), 0o600)
		_ = os.WriteFile(filepath.Join(dir, "README.md"), []byte("changed\n"), 0o600)
		_ = exec.Command("git", "-C", dir, "add", "-A").Run()
		return wfReply{text: "made it", credits: 1}
	})
	e, store, f, _ := wfEngine(t, wf)
	ctx := context.Background()
	run := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: oneStep})
	tickRuns(t, e, f.ID, run.N, "started its step", func(*domain.Run) bool { return wf.count("one") > 0 })
	if _, err := e.WorkflowRevert(ctx, f.ID, run.N, "x"); err == nil || !strings.Contains(err.Error(), "running") {
		t.Fatalf("a running run was reverted: %v", err)
	}
	g.open()
	r := tickRuns(t, e, f.ID, run.N, "ended", ended)
	if r.StartIndex == "" {
		t.Fatal("the run recorded no index")
	}
	sess, _ := store.GetFeature(ctx, f.ID)
	tree := e.treeOf(ctx, sess)
	if st := gitIn(t, tree, "status", "--porcelain"); !strings.Contains(st, "A  made.txt") {
		t.Fatalf("the step staged nothing: %q", st)
	}
	pre, err := e.WorkflowRevertPreview(ctx, f.ID, run.N)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pre.Note, "staged as it was then") {
		t.Fatalf("note %q", pre.Note)
	}
	if _, err := e.WorkflowRevert(ctx, f.ID, run.N, pre.Tree); err != nil {
		t.Fatal(err)
	}
	if st := gitIn(t, tree, "status", "--porcelain", "--untracked-files=no"); strings.Contains(st, "made.txt") || strings.Contains(st, "README.md") {
		t.Fatalf("the revert left the run's work staged: %q", st)
	}
}

// TestARevertPreviewNamesWhatItUndoesSince: reverting a run also undoes
// the runs after it and the commits made since it started; its preview
// says so.
func TestARevertPreviewNamesWhatItUndoesSince(t *testing.T) {
	wf := newWFFake()
	wf.on("one", writesOut)
	e, store, f, _ := wfEngine(t, wf)
	ctx := context.Background()
	first := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: oneStep})
	tickRuns(t, e, f.ID, first.N, "ended", ended)
	sess, _ := store.GetFeature(ctx, f.ID)
	tree := e.treeOf(ctx, sess)
	gitIn(t, tree, "add", "-A")
	gitIn(t, tree, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "keep it")
	second := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: strings.Replace(oneStep, "build", "more", 1)})
	tickRuns(t, e, f.ID, second.N, "ended", ended)
	pre, err := e.WorkflowRevertPreview(ctx, f.ID, first.N)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pre.Note, "run 2, which ran after it") || !strings.Contains(pre.Note, "1 commit made since it started") {
		t.Fatalf("note %q", pre.Note)
	}
	if late, _ := e.WorkflowRevertPreview(ctx, f.ID, second.N); strings.Contains(late.Note, "ran after it") || strings.Contains(late.Note, "commit") {
		t.Fatalf("the newest run's note %q", late.Note)
	}
}

// TestARunEndsWhenItsTreeCannotBeRead: a tree gummi cannot snapshot still
// ends its run, partial and saying why, rather than leaving it open for
// good, holding the session's landing and retried on every tick.
func TestARunEndsWhenItsTreeCannotBeRead(t *testing.T) {
	wf := newWFFake()
	var gitFile string
	wf.on("one", func(dir, _ string, _ int) wfReply {
		// the checkout's .git points nowhere now: git cannot read the
		// tree, nor walk up past it to another repository
		gitFile = filepath.Join(dir, ".git")
		_ = os.RemoveAll(gitFile + ".away")
		_ = os.Rename(gitFile, gitFile+".away")
		_ = os.WriteFile(gitFile, []byte("gitdir: /nonexistent/gummi-test\n"), 0o600)
		return wfReply{text: "done", credits: 1}
	})
	e, _, f, _ := wfEngine(t, wf)
	t.Cleanup(func() {
		if gitFile != "" {
			_ = os.Remove(gitFile)
			_ = os.Rename(gitFile+".away", gitFile)
		}
	})
	run := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: oneStep})
	r := tickRuns(t, e, f.ID, run.N, "ended", ended)
	if r.State != domain.RunPartial || !strings.Contains(r.Report, "could not read") {
		t.Fatalf("ended %s (%s):\n%s", r.State, r.Reason, r.Report)
	}
	if s := stepPaths(t, e, f.ID, run.N)["one"]; s.State != domain.StepFailed || !strings.Contains(s.Reason, "could not read the tree") {
		t.Fatalf("the step %+v", s)
	}
}

// TestTheTickCarriesOnPastASessionInTrouble: one session's run that cannot
// start (its tree is gone) is said in the tick's error, and every other
// session's run is still ticked to its end.
func TestTheTickCarriesOnPastASessionInTrouble(t *testing.T) {
	wf := newWFFake()
	e, store, f, _ := wfEngine(t, wf)
	ctx := context.Background()
	other := freeformCard(2, "a second session")
	other.Budget.Envelope = 2000
	createFeature(t, store, other)
	off, err := e.OpenFreeform(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	if err := off.Send(ctx, "hi"); err != nil {
		t.Fatal(err)
	}
	waitFreeformIdle(t, off)
	if _, _, err := e.StartSessionSpec(ctx, other, "Second", "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.WriteSessionSpec(ctx, other, 1, wfTestSpec, "user"); err != nil {
		t.Fatal(err)
	}
	// the first session's run, queued with nothing open, cannot start: its
	// tree is gone
	stuck := &domain.Run{
		Session: f.ID, Spec: 1, SpecRev: 1, State: domain.RunQueued, RecipeName: "one",
		Recipe: domain.Recipe{Name: "one", Steps: []domain.Entry{{Step: &domain.Step{ID: "one", Role: domain.RoleImplementer, Prompt: "x"}}}},
	}
	if err := store.CreateRun(ctx, stuck); err != nil {
		t.Fatal(err)
	}
	sess, _ := store.GetFeature(ctx, f.ID)
	tree := e.treeOf(ctx, sess)
	gitFile := filepath.Join(tree, ".git")
	if err := os.Rename(gitFile, gitFile+".away"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gitFile, []byte("gitdir: /nonexistent/gummi-test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Remove(gitFile)
		_ = os.Rename(gitFile+".away", gitFile)
	})

	run := proposeAndAllow(t, e, other.ID, WorkflowRequest{RecipeYAML: oneStep})
	deadline := time.Now().Add(testWaitTimeout)
	var tickErr error
	for time.Now().Before(deadline) {
		if _, err := e.WorkflowTick(ctx); err != nil {
			tickErr = err
		}
		if r, _ := store.Run(ctx, other.ID, run.N); r != nil && r.State.Ended() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if r, _ := store.Run(ctx, other.ID, run.N); !r.State.Ended() {
		t.Fatalf("the other session's run stood still: %s", r.State)
	}
	if tickErr == nil || !strings.Contains(tickErr.Error(), string(f.ID)) {
		t.Fatalf("the trouble was not said: %v", tickErr)
	}
}

// TestAQueuedRunWaitsForTheSessionsTurn: a queued run does not start while
// its session is mid-turn — reading the report of the run before it,
// committing — in the tree it would write; it starts when the turn ends.
func TestAQueuedRunWaitsForTheSessionsTurn(t *testing.T) {
	wf := newWFFake()
	hold := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-hold:
		default:
			close(hold)
		}
	})
	base := wf.Responder
	wf.Responder = func(opts agent.SessionOpts, msg string) []agent.Event {
		if strings.Contains(msg, "hold the turn") {
			<-hold
		}
		return base(opts, msg)
	}
	e, store, f, ff := wfEngine(t, wf)
	ctx := context.Background()
	run := &domain.Run{
		Session: f.ID, Spec: 1, SpecRev: 1, State: domain.RunQueued, RecipeName: "one",
		Recipe: domain.Recipe{Name: "one", Steps: []domain.Entry{{Step: &domain.Step{ID: "one", Role: domain.RoleImplementer, Prompt: "x"}}}},
	}
	if err := store.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := ff.Send(ctx, "hold the turn"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the session is mid-turn", func() bool { return e.freeformBusy(f.ID) })
	for range 3 {
		if _, err := e.WorkflowTick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if r, _ := store.Run(ctx, f.ID, run.N); r.State != domain.RunQueued {
		t.Fatalf("the queued run started under the session's turn: %s", r.State)
	}
	close(hold)
	waitFreeformIdle(t, ff)
	tickRuns(t, e, f.ID, run.N, "started", func(r *domain.Run) bool { return r.State != domain.RunQueued })
}

// TestTheOpenRunsQuestionComesFirst: a run paused on its budget holds the
// tree, and its question is the session's before a newer proposal's.
func TestTheOpenRunsQuestionComesFirst(t *testing.T) {
	wf := newWFFake()
	e, store, f, _ := wfEngine(t, wf)
	ctx := context.Background()
	open := &domain.Run{
		Session: f.ID, Spec: 1, SpecRev: 1, State: domain.RunPaused, OutOfBudget: true,
		Pending: &domain.RunDecision{ID: "b", Kind: domain.RunDecideBudget, Question: "top up?"},
	}
	newer := &domain.Run{
		Session: f.ID, Spec: 1, SpecRev: 1, State: domain.RunProposed,
		Pending: &domain.RunDecision{ID: "p", Kind: domain.RunDecideProposal, Question: "allow?"},
	}
	for _, r := range []*domain.Run{open, newer} {
		if err := store.CreateRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if r, d := e.WorkflowDecision(ctx, f.ID); r == nil || r.N != open.N || d.Kind != domain.RunDecideBudget {
		t.Fatalf("the session's decision is run %+v: %+v", r, d)
	}
}

// TestAPersonWithdrawsAQueuedRunAndAsksAgain: a run allowed and queued
// behind the open one is a person's to withdraw; an open run is not
// withdrawn but stopped. A person's always-allow is theirs to take back,
// and the next proposal asks again.
func TestAPersonWithdrawsAQueuedRunAndAsksAgain(t *testing.T) {
	wf := newWFFake()
	g := newGate(t)
	wf.on("slow", func(string, string, int) wfReply {
		<-g.ch
		return wfReply{text: "done", credits: 1}
	})
	e, _, f, _ := wfEngine(t, wf)
	ctx := context.Background()
	first, err := e.WorkflowPropose(ctx, f.ID, WorkflowRequest{RecipeYAML: "name: slow\nsteps:\n  - { id: slow, role: scout, prompt: wait }\n"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.WorkflowAnswer(ctx, f.ID, first.N, first.Pending.ID, WorkflowAlways); err != nil {
		t.Fatal(err)
	}
	if !e.WorkflowAlwaysAllows(ctx, f.ID) {
		t.Fatal("always allow did not stick")
	}
	queued, err := e.WorkflowPropose(ctx, f.ID, WorkflowRequest{RecipeYAML: oneStep})
	if err != nil || queued.State != domain.RunQueued {
		t.Fatalf("second run %+v %v", queued, err)
	}
	if err := e.WorkflowWithdraw(ctx, f.ID, first.N, ""); err == nil {
		t.Fatal("an open run was withdrawn")
	}
	if err := e.WorkflowWithdraw(ctx, f.ID, queued.N, ""); err != nil {
		t.Fatal(err)
	}
	if r, _ := e.cfg.Store.Run(ctx, f.ID, queued.N); r.State != domain.RunDenied || !strings.Contains(r.Reason, "withdrawn") {
		t.Fatalf("withdrawn run %s (%s)", r.State, r.Reason)
	}
	if err := e.WorkflowAskAgain(ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	if e.WorkflowAlwaysAllows(ctx, f.ID) {
		t.Fatal("asking again did not take always-allow back")
	}
	again, err := e.WorkflowPropose(ctx, f.ID, WorkflowRequest{RecipeYAML: oneStep})
	if err != nil || again.State != domain.RunProposed {
		t.Fatalf("after asking again a proposal is %+v %v", again, err)
	}
}

// TestALeftoverVerdictIsNotReadAsTheNewOne: a verdict step that only prints
// its verdict, in a later iteration, has it captured from its output: the
// last iteration's review.json, still on disk, is not read as its answer.
func TestALeftoverVerdictIsNotReadAsTheNewOne(t *testing.T) {
	wf := newWFFake()
	wf.on("implement", writesOut)
	var review string
	wf.on("verdict", func(_, _ string, n int) wfReply {
		if n == 1 {
			_ = os.WriteFile(review, []byte(`{"verdict": "CHANGES", "findings": ["x"]}`), 0o600)
			return wfReply{text: "changes asked", credits: 1}
		}
		return wfReply{text: "```json\n{\"verdict\": \"APPROVED\", \"findings\": []}\n```", credits: 1}
	})
	e, _, f, _ := wfEngine(t, wf)
	review = filepath.Join(e.RunDir(f.ID, 1), "review.json")
	run := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: verdictLoop})
	r := tickRuns(t, e, f.ID, run.N, "ended", ended)
	if !r.Until["build/2"].Met || wf.count("implement") != 2 {
		t.Fatalf("until %+v after %d iterations (%s)", r.Until, wf.count("implement"), r.State)
	}
}

// TestALaterRoundIsToldWhatTheLastOneFound: the implementer of a loop's
// second round is briefed with why the first did not end it and the
// review it left, inline; the first round's brief carries neither.
func TestALaterRoundIsToldWhatTheLastOneFound(t *testing.T) {
	wf := newWFFake()
	var mu sync.Mutex
	var briefs []string
	wf.on("implement", func(dir, msg string, n int) wfReply {
		mu.Lock()
		briefs = append(briefs, msg)
		mu.Unlock()
		return writesOut(dir, msg, n)
	})
	var review string
	wf.on("verdict", func(_, _ string, n int) wfReply {
		v := `{"verdict": "CHANGES", "findings": ["the empty case panics"]}`
		if n > 1 {
			v = `{"verdict": "APPROVED", "findings": []}`
		}
		_ = os.WriteFile(review, []byte(v), 0o600)
		return wfReply{text: "judged", credits: 1}
	})
	e, _, f, _ := wfEngine(t, wf)
	review = filepath.Join(e.RunDir(f.ID, 1), "review.json")
	run := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: verdictLoop})
	tickRuns(t, e, f.ID, run.N, "ended", ended)
	mu.Lock()
	defer mu.Unlock()
	if len(briefs) != 2 {
		t.Fatalf("%d implement briefs", len(briefs))
	}
	if strings.Contains(briefs[0], "did not end it") {
		t.Fatalf("the first round was told of a round before it:\n%s", briefs[0])
	}
	for _, want := range []string{"round 2 of the loop build", `verdict "CHANGES"`, "Address what it found first", "the empty case panics"} {
		if !strings.Contains(briefs[1], want) {
			t.Fatalf("the second round's brief lacks %q:\n%s", want, briefs[1])
		}
	}
}

// TestAStepThatLosesTheRaceToASpentPoolPauses: a step whose start finds
// the pool spent — a sibling spent it between the tick and the start —
// pauses the run for a top-up rather than failing; it starts again when
// the run carries on.
func TestAStepThatLosesTheRaceToASpentPoolPauses(t *testing.T) {
	wf := newWFFake()
	e, store, f, _ := wfEngine(t, wf)
	ctx := context.Background()
	run := &domain.Run{
		Session: f.ID, Spec: 1, SpecRev: 1, State: domain.RunRunning, StartedAt: time.Now(), RecipeName: "one",
		Recipe: domain.Recipe{Name: "one", Steps: []domain.Entry{{Step: &domain.Step{ID: "one", Role: domain.RoleImplementer, Prompt: "x"}}}},
	}
	if err := store.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	e.wfRT().conduct(f.ID, run.N)
	if err := store.CreateRunStep(ctx, &domain.RunStep{
		Session: f.ID, Run: run.N, Path: "one", StepID: "one",
		Role: domain.RoleImplementer, State: domain.StepRunning, StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	sess, _ := store.GetFeature(ctx, f.ID)
	sess.Budget.Envelope = 1 // the session's own turn spent it
	if err := store.UpdateFeature(ctx, &sess); err != nil {
		t.Fatal(err)
	}
	if err := e.startStep(ctx, f.ID, run.N, "one"); !errors.Is(err, errStepPoolSpent) {
		t.Fatalf("start on a spent pool: %v", err)
	}
	e.startStepAsync(f.ID, run.N, "one")
	waitUntil(t, "the run paused", func() bool {
		r, _ := store.Run(ctx, f.ID, run.N)
		return r.State == domain.RunPaused && r.OutOfBudget
	})
	if s := stepPaths(t, e, f.ID, run.N)["one"]; s.State != domain.StepRunning || s.Prompt != "" {
		t.Fatalf("the step %+v", s)
	}
}

// TestASpecEditReachesTheRunAsAPointer: an edit of the spec an open run is
// judged against reaches its steps as a steer naming where the edited
// spec is, not the whole spec copied into every step again.
func TestASpecEditReachesTheRunAsAPointer(t *testing.T) {
	wf := newWFFake()
	g := newGate(t)
	wf.on("one", func(string, string, int) wfReply {
		<-g.ch
		return wfReply{text: "done", credits: 1}
	})
	e, store, f, _ := wfEngine(t, wf)
	ctx := context.Background()
	run := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: oneStep})
	tickRuns(t, e, f.ID, run.N, "started its step", func(*domain.Run) bool { return wf.count("one") > 0 })
	edited := wfTestSpec + "\nA new line of the spec.\n"
	if _, err := e.WriteSessionSpec(ctx, f, 1, edited, "user"); err != nil {
		t.Fatal(err)
	}
	r, _ := store.Run(ctx, f.ID, run.N)
	if len(r.Steers) != 1 || strings.Contains(r.Steers[0], "A new line of the spec.") || !strings.Contains(r.Steers[0], "Read it at ") {
		t.Fatalf("steers %q", r.Steers)
	}
	path := r.Steers[0][strings.Index(r.Steers[0], "Read it at ")+len("Read it at "):]
	if raw, err := os.ReadFile(path); err != nil || string(raw) != edited {
		t.Fatalf("the edited spec at %s: %q %v", path, raw, err)
	}
}

// TestRunPhaseSaysWhatGummiDoesBetweenSteps: a run's phase reads what
// gummi has in flight on it.
func TestRunPhaseSaysWhatGummiDoesBetweenSteps(t *testing.T) {
	e := New(Config{})
	rt := e.wfRT()
	if got := e.RunPhase("FF-1", 1); got != "" {
		t.Fatalf("idle phase %q", got)
	}
	for _, c := range []struct{ key, want string }{
		{baselineKey("FF-1", 1), "baseline"},
		{"until/FF-1/1/build/2", "until"},
		{"end/FF-1/1", "ending"},
	} {
		rt.claim(c.key)
		if got := e.RunPhase("FF-1", 1); got != c.want {
			t.Errorf("with %s claimed the phase is %q, want %q", c.key, got, c.want)
		}
	}
	if got := e.RunPhase("FF-1", 2); got != "" {
		t.Errorf("another run's phase %q", got)
	}
}

// TestAFollowUpWaitsWhileTheRunWritesTheTree: a person's follow-up to an
// ended step may change the tree, which the run's step at work is
// writing: it is refused until that step is done, and taken after.
func TestAFollowUpWaitsWhileTheRunWritesTheTree(t *testing.T) {
	wf := newWFFake()
	g := newGate(t)
	wf.on("two", func(string, string, int) wfReply {
		<-g.ch
		return wfReply{text: "done", credits: 1}
	})
	e, _, f, _ := wfEngine(t, wf)
	ctx := context.Background()
	run := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: "name: two\nsteps:\n" +
		"  - { id: one, role: implementer, prompt: first }\n  - { id: two, role: implementer, prompt: second }\n"})
	tickRuns(t, e, f.ID, run.N, "the second step at work", func(*domain.Run) bool { return wf.count("two") > 0 })
	one := stepPaths(t, e, f.ID, run.N)["one"].Card
	if err := e.StepSend(ctx, one, "and one more thing"); err == nil || !strings.Contains(err.Error(), "step two of run 1 is at work") {
		t.Fatalf("a follow-up under a step at work: %v", err)
	}
	g.open()
	tickRuns(t, e, f.ID, run.N, "ended", ended)
	if err := e.StepSend(ctx, one, "and one more thing"); err != nil {
		t.Fatalf("a follow-up once the run has ended: %v", err)
	}
}

// TestAReaderBesideAWriterIsNotFlaggedForItsWork: a reviewer at work
// beside an implementer in a parallel is not flagged for the files the
// implementer changed meanwhile; one it changed itself still is.
func TestAReaderBesideAWriterIsNotFlaggedForItsWork(t *testing.T) {
	wf := newWFFake()
	var e *Engine
	var f domain.Feature
	wf.on("write", func(dir, _ string, _ int) wfReply {
		_ = os.WriteFile(filepath.Join(dir, "theirs.txt"), []byte("the writer's\n"), 0o600)
		return wfReply{text: "wrote", credits: 1}
	})
	wf.on("read", func(dir, _ string, _ int) wfReply {
		// the reader's own change comes once the writer's step has ended,
		// so it is in neither the writer's snapshots nor its diff
		deadline := time.Now().Add(testWaitTimeout)
		for time.Now().Before(deadline) {
			if s, _ := e.cfg.Store.RunStep(context.Background(), f.ID, 1, "both/write"); s != nil && s.EndTree != "" {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		_ = os.WriteFile(filepath.Join(dir, "mine.txt"), []byte("the reader's\n"), 0o600)
		return wfReply{text: "read", credits: 1}
	})
	e, _, f, _ = wfEngine(t, wf)
	run := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: "name: side\nsteps:\n  - id: both\n    parallel:\n      join: allSettled\n" +
		"      steps:\n        - { id: write, role: implementer, prompt: w }\n        - { id: read, role: reviewer, prompt: r }\n"})
	tickRuns(t, e, f.ID, run.N, "ended", ended)
	if got := stepPaths(t, e, f.ID, run.N)["both/read"].Changed; len(got) != 1 || got[0] != "mine.txt" {
		t.Fatalf("the reader is flagged for %q", got)
	}
}

// TestARetryRunsTheFailedStepsAgain: a person retrying a run that ended
// partial runs its failed step again, keeping the earlier attempt (and
// what it spent) beside it and every step that finished as it was; the run
// ends anew. Only the newest run that ended, with nothing failed or open,
// is retried.
func TestARetryRunsTheFailedStepsAgain(t *testing.T) {
	wf := newWFFake()
	wf.on("one", writesOut)
	wf.on("two", func(_ string, _ string, n int) wfReply {
		if n == 1 {
			return wfReply{text: "could not\nFAILED: the tool was missing", credits: 3}
		}
		return wfReply{text: "done now", credits: 1}
	})
	e, store, f, _ := wfEngine(t, wf)
	ctx := context.Background()
	run := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: "name: two\nsteps:\n" +
		"  - { id: one, role: implementer, prompt: first }\n  - { id: two, role: implementer, prompt: second }\n"})
	if err := e.WorkflowRetry(ctx, f.ID, run.N); err == nil {
		t.Fatal("a running run was retried")
	}
	r := tickRuns(t, e, f.ID, run.N, "ended", ended)
	if r.State != domain.RunPartial {
		t.Fatalf("first ended %s", r.State)
	}
	spent := r.Spent
	if err := e.WorkflowRetry(ctx, f.ID, run.N); err != nil {
		t.Fatal(err)
	}
	if r, _ = store.Run(ctx, f.ID, run.N); r.State != domain.RunRunning || !r.EndedAt.IsZero() || len(r.Results) != 0 {
		t.Fatalf("the retried run %s, ended %v, results %v", r.State, r.EndedAt, r.Results)
	}
	r = tickRuns(t, e, f.ID, run.N, "ended again", ended)
	steps := stepPaths(t, e, f.ID, run.N)
	if r.State != domain.RunDone || steps["two"].State != domain.StepDone || steps["two@1"].State != domain.StepFailed ||
		wf.count("one") != 1 || wf.count("two") != 2 {
		t.Fatalf("after the retry: run %s, steps %+v, one ran %d, two %d", r.State, steps, wf.count("one"), wf.count("two"))
	}
	if r.Spent < spent+1 {
		t.Fatalf("the earlier attempt's spend went: %.0f after %.0f", r.Spent, spent)
	}
	if err := e.WorkflowRetry(ctx, f.ID, run.N); err == nil || !strings.Contains(err.Error(), "only a run that ended partial or stopped") {
		t.Fatalf("a run that ended done was retried: %v", err)
	}
}

// TestARevertToAStepKeepsWhatTheStepsBeforeItDid: reverting a run to where
// one of its steps started puts back what that step and those after it
// changed, and keeps what the steps before it did.
func TestARevertToAStepKeepsWhatTheStepsBeforeItDid(t *testing.T) {
	wf := newWFFake()
	wf.on("one", func(dir, _ string, _ int) wfReply {
		_ = os.WriteFile(filepath.Join(dir, "first.txt"), []byte("first\n"), 0o600)
		return wfReply{text: "first", credits: 1}
	})
	wf.on("two", func(dir, _ string, _ int) wfReply {
		_ = os.WriteFile(filepath.Join(dir, "second.txt"), []byte("second\n"), 0o600)
		return wfReply{text: "second", credits: 1}
	})
	e, store, f, _ := wfEngine(t, wf)
	ctx := context.Background()
	run := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: "name: two\nsteps:\n" +
		"  - { id: one, role: implementer, prompt: first }\n  - { id: two, role: implementer, prompt: second }\n"})
	tickRuns(t, e, f.ID, run.N, "ended", ended)
	pre, err := e.WorkflowRevertPreviewTo(ctx, f.ID, run.N, "two")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(pre.Paths, ",") != "second.txt" || pre.Step != "two" || !strings.Contains(pre.Note, "before step two started") {
		t.Fatalf("preview %+v", pre)
	}
	if _, err := e.WorkflowRevertPreviewTo(ctx, f.ID, run.N, "three"); err == nil {
		t.Fatal("a step the run never had was reverted to")
	}
	if _, err := e.WorkflowRevertTo(ctx, f.ID, run.N, "two", pre.Tree); err != nil {
		t.Fatal(err)
	}
	sess, _ := store.GetFeature(ctx, f.ID)
	tree := e.treeOf(ctx, sess)
	if _, err := os.Stat(filepath.Join(tree, "second.txt")); !os.IsNotExist(err) {
		t.Errorf("the reverted step's file is still there: %v", err)
	}
	if raw, err := os.ReadFile(filepath.Join(tree, "first.txt")); err != nil || string(raw) != "first\n" {
		t.Errorf("the step before it lost its work: %q %v", raw, err)
	}
}
