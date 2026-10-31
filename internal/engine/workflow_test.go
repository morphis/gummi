package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
)

const wfTestSpec = "# Workflow test\n\n## Problem\n\nA file is missing.\n\n## Approach\n\nWrite it.\n\n" +
	"## Done when\n\n```gummi-done-when\n- id: DW-1\n  says: out.txt exists\n  check: test -f out.txt\n" +
	"- id: DW-2\n  says: out.txt reads well\n  judge: true\n```\n\n## Limits\n\nNothing else.\n\n## Notes\n\nnone\n"

var wfStepRe = regexp.MustCompile(`You are step (\S+) of workflow run`)

// wfReply is what a step's turn answers: its final message, and what the
// turn spends.
type wfReply struct {
	text    string
	credits float64
}

// wfFake answers a workflow's steps by their id: each step's handler is
// given the working directory, the turn's text and how many times that
// step id has run. A turn on a step's card that is not its brief (a steer,
// a carry-on, a person's message) goes to the handler under "<id>#". Every
// other turn is the session's.
type wfFake struct {
	*agent.Fake
	mu       sync.Mutex
	calls    map[string]int
	cardStep map[string]string
	steps    map[string]func(dir, msg string, n int) wfReply
	sessionT []string
	stepT    map[string][]string
}

func newWFFake() *wfFake {
	wf := &wfFake{
		Fake: agent.NewFake(""), calls: map[string]int{}, cardStep: map[string]string{},
		steps: map[string]func(string, string, int) wfReply{}, stepT: map[string][]string{},
	}
	wf.Caps = agent.Capabilities{ClientTools: true, UsageEvents: true, Interrupt: true}
	wf.Responder = func(opts agent.SessionOpts, msg string) []agent.Event {
		wf.mu.Lock()
		id := ""
		if m := wfStepRe.FindStringSubmatch(msg); m != nil {
			id = m[1][strings.LastIndex(m[1], "/")+1:]
			wf.cardStep[opts.FeatureID] = id
		} else if s, ok := wf.cardStep[opts.FeatureID]; ok {
			id = s + "#"
		}
		if id == "" {
			wf.sessionT = append(wf.sessionT, msg)
			wf.mu.Unlock()
			return []agent.Event{
				{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 1, Model: "m"}},
				{Kind: agent.EventMessage, Text: "ok"},
				{Kind: agent.EventIdle},
			}
		}
		wf.calls[id]++
		n := wf.calls[id]
		wf.stepT[id] = append(wf.stepT[id], msg)
		fn := wf.steps[id]
		wf.mu.Unlock()
		r := wfReply{text: "done", credits: 2}
		if fn != nil {
			r = fn(opts.WorkDir, msg, n)
		}
		return []agent.Event{
			{Kind: agent.EventUsage, Usage: agent.Usage{Credits: r.credits, Model: "m"}},
			{Kind: agent.EventMessage, Text: r.text},
			{Kind: agent.EventIdle},
		}
	}
	return wf
}

func (wf *wfFake) on(id string, fn func(dir, msg string, n int) wfReply) {
	wf.mu.Lock()
	wf.steps[id] = fn
	wf.mu.Unlock()
}

func (wf *wfFake) count(id string) int {
	wf.mu.Lock()
	defer wf.mu.Unlock()
	return wf.calls[id]
}

func (wf *wfFake) sessionTurns() []string {
	wf.mu.Lock()
	defer wf.mu.Unlock()
	return append([]string(nil), wf.sessionT...)
}

// gate holds a step's turn until it is opened; the test's cleanup opens
// it before the engine closes.
type gate struct {
	ch   chan struct{}
	once sync.Once
}

func newGate(t *testing.T) *gate {
	g := &gate{ch: make(chan struct{})}
	t.Cleanup(g.open)
	return g
}

func (g *gate) open() { g.once.Do(func() { close(g.ch) }) }

func wfEngine(t *testing.T, wf *wfFake) (*Engine, *state.Store, domain.Feature, *FreeformSession) {
	t.Helper()
	return wfEngineWith(t, wf, singleAgent(wf))
}

// wfEngineWith is wfEngine with the backends the engine knows.
func wfEngineWith(t *testing.T, wf *wfFake, agents map[string]agent.Agent) (*Engine, *state.Store, domain.Feature, *FreeformSession) {
	t.Helper()
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: agents, Store: store, Worktrees: wt, Workspace: ws, Model: "m", Persist: true})
	t.Cleanup(func() { e.Close() })
	f := freeformCard(1, "make the file")
	f.Budget.Envelope = 2000
	createFeature(t, store, f)
	ff, err := e.OpenFreeform(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if err := ff.Send(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	waitFreeformIdle(t, ff)
	ctx := context.Background()
	if _, _, err := e.StartSessionSpec(ctx, f, "Workflow test", "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.WriteSessionSpec(ctx, f, 1, wfTestSpec, "user"); err != nil {
		t.Fatal(err)
	}
	return e, store, f, ff
}

// tickRuns ticks the workspace's runs until cond holds of the session's
// run n.
func tickRuns(t *testing.T, e *Engine, session domain.FeatureID, n int, what string, cond func(*domain.Run) bool) *domain.Run {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(testWaitTimeout)
	for time.Now().Before(deadline) {
		if _, err := e.WorkflowTick(ctx); err != nil {
			t.Fatal(err)
		}
		if r, _ := e.cfg.Store.Run(ctx, session, n); r != nil && cond(r) {
			return r
		}
		time.Sleep(10 * time.Millisecond)
	}
	r, _ := e.cfg.Store.Run(ctx, session, n)
	steps, _ := e.cfg.Store.RunSteps(ctx, session, n)
	t.Fatalf("never %s: run %+v\nsteps %+v", what, r, steps)
	return nil
}

func ended(r *domain.Run) bool { return r.State.Ended() }

// pendingID is the id of run n's open decision, which an answer names.
func pendingID(t *testing.T, e *Engine, session domain.FeatureID, n int) string {
	t.Helper()
	r, err := e.cfg.Store.Run(context.Background(), session, n)
	if err != nil || r == nil || r.Pending == nil {
		t.Fatalf("run %d has no open decision: %+v %v", n, r, err)
	}
	return r.Pending.ID
}

func pendingOf(kind domain.RunDecisionKind) func(*domain.Run) bool {
	return func(r *domain.Run) bool { return r.Pending != nil && r.Pending.Kind == kind }
}

// propose proposes recipe and allows it.
func proposeAndAllow(t *testing.T, e *Engine, session domain.FeatureID, req WorkflowRequest) *domain.Run {
	t.Helper()
	ctx := context.Background()
	run, err := e.WorkflowPropose(ctx, session, req)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != domain.RunProposed || run.Pending == nil || run.Pending.Kind != domain.RunDecideProposal {
		t.Fatalf("proposal %+v", run)
	}
	if err := e.WorkflowAnswer(ctx, session, run.N, run.Pending.ID, WorkflowAllow); err != nil {
		t.Fatal(err)
	}
	return run
}

func stepPaths(t *testing.T, e *Engine, session domain.FeatureID, n int) map[string]domain.RunStep {
	t.Helper()
	steps, err := e.cfg.Store.RunSteps(context.Background(), session, n)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]domain.RunStep{}
	for _, s := range steps {
		out[s.Path] = s
	}
	return out
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(testWaitTimeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("never %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

var artifactRe = regexp.MustCompile(`(/\S+review\.json)`)

// TestADeliverRunIteratesUntilApproved: the bundled deliver recipe, end to
// end. The build loop goes round again on a CHANGES verdict and ends on
// APPROVED; the validation's MET: lines judge the judge item and the check
// the checked one; the run ends done with its tree validated, its changes
// left uncommitted, and its report in the spec, the run's directory and the
// session's thread.
func TestADeliverRunIteratesUntilApproved(t *testing.T) {
	wf := newWFFake()
	wf.on("implement", func(dir, _ string, n int) wfReply {
		_ = os.WriteFile(filepath.Join(dir, "out.txt"), []byte(fmt.Sprintf("take %d\n", n)), 0o600)
		return wfReply{text: "wrote out.txt\nDECISION: plain text", credits: 2}
	})
	wf.on("verdict", func(_, msg string, n int) wfReply {
		verdict := "CHANGES"
		if n > 1 {
			verdict = "APPROVED"
		}
		if m := artifactRe.FindStringSubmatch(msg); m != nil {
			_ = os.WriteFile(m[1], []byte(`{"verdict": "`+verdict+`", "findings": []}`), 0o600)
		}
		return wfReply{text: "merged: " + verdict, credits: 1}
	})
	wf.on("validate", func(string, string, int) wfReply {
		return wfReply{text: "Read it.\nMET: DW-1\nMET: DW-2", credits: 1}
	})
	e, store, f, ff := wfEngine(t, wf)
	ctx := context.Background()

	if hold := e.RunHolds(ctx, f.ID); hold != "" {
		t.Fatalf("a session with no run is held: %s", hold)
	}
	run := proposeAndAllow(t, e, f.ID, WorkflowRequest{Recipe: "deliver"})
	if hold := e.RunHolds(ctx, f.ID); hold == "" {
		t.Fatal("an open run does not hold its session")
	}
	// a session turn during the run begins with the note
	if err := ff.Send(ctx, "how is it going"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the session heard the note", func() bool {
		for _, m := range wf.sessionTurns() {
			if strings.Contains(m, "workflow run 1 is changing this session's tree") && strings.Contains(m, "how is it going") {
				return true
			}
		}
		return false
	})

	r := tickRuns(t, e, f.ID, run.N, "ended", ended)
	if r.State != domain.RunDone {
		t.Fatalf("run ended %s (%s):\n%s", r.State, r.Reason, r.Report)
	}
	paths := stepPaths(t, e, f.ID, run.N)
	for _, p := range []string{
		"design", "build/1/implement", "build/1/reviews/review-a", "build/1/reviews/review-b",
		"build/1/verdict", "build/2/implement", "build/2/verdict", "validate",
	} {
		if s, ok := paths[p]; !ok || s.State != domain.StepDone {
			t.Errorf("step %s: %+v", p, s)
		}
	}
	if _, ok := paths["build/3/implement"]; ok {
		t.Error("the loop ran past its APPROVED verdict")
	}
	if r.Until["build/1"].Met || !r.Until["build/2"].Met {
		t.Errorf("untils %+v", r.Until)
	}
	if r.EndTree == "" || !domain.ResultsMet(r.Results) {
		t.Errorf("end %q results %+v", r.EndTree, r.Results)
	}
	want := map[string]domain.ItemResult{
		"DW-1": {Item: "DW-1", Checked: true, Verdict: domain.ItemMet},
		"DW-2": {Item: "DW-2", Verdict: domain.ItemMet},
	}
	for _, res := range r.Results {
		if w := want[res.Item]; res.Checked != w.Checked || res.Verdict != w.Verdict {
			t.Errorf("result %+v, want %+v", res, w)
		}
	}
	for _, want := range []string{"✓ DW-1", "✓ DW-2", "out.txt", "build/2/implement: plain text"} {
		if !strings.Contains(r.Report, want) {
			t.Errorf("the report lacks %q:\n%s", want, r.Report)
		}
	}
	// a step's prompt had its templates filled
	if p := paths["build/1/reviews/review-a"].Prompt; !strings.Contains(p, filepath.Join(e.RunDir(f.ID, 1), "diffs")) || strings.Contains(p, "{{") {
		t.Errorf("review-a's prompt:\n%s", p)
	}
	// the work is in the tree, uncommitted
	sess, _ := store.GetFeature(ctx, f.ID)
	tree := e.treeOf(ctx, sess)
	if st := gitOut(t, tree, "status", "--porcelain"); !strings.Contains(st, "out.txt") {
		t.Errorf("out.txt is not left uncommitted: %q", st)
	}
	// the report: the spec, the run's directory, the thread
	if text, _ := e.SessionSpec(f, 1); !strings.Contains(text, "## Report") || !strings.Contains(text, "✓ DW-1") {
		t.Errorf("the spec has no report:\n%s", text)
	}
	if _, err := os.Stat(filepath.Join(e.RunDir(f.ID, 1), "report.md")); err != nil {
		t.Error(err)
	}
	if _, err := os.Stat(filepath.Join(e.RunDir(f.ID, 1), "outputs", "build", "2", "verdict.md")); err != nil {
		t.Error(err)
	}
	waitUntil(t, "the session got the report", func() bool {
		for _, m := range ff.Snapshot().Transcript {
			if m.Author == AuthorUser && m.By == state.ActorWorkflow && strings.Contains(m.Content, "Workflow run 1 has reported") {
				return true
			}
		}
		return false
	})
	if hold := e.RunHolds(ctx, f.ID); hold != "" {
		t.Errorf("an ended run still holds: %s", hold)
	}
	// the step cards are hidden children of the session, working in its tree
	card, _ := store.GetFeature(ctx, paths["design"].Card)
	if !card.IsChild() || card.ParentID != f.ID {
		t.Errorf("design's card %+v", card)
	}
	if ff := e.Freeform(card.ID); ff == nil || ff.WorkDir() != tree {
		t.Errorf("design worked outside the session's tree")
	}
}

func parallelRecipe(join string) string {
	return `name: par
steps:
  - id: p
    parallel:
      join: ` + join + `
      steps:
        - {id: a, role: scout, prompt: "look at a"}
        - {id: b, role: scout, prompt: "look at b"}
  - id: after
    role: scribe
    prompt: "sum up {{a.output}}"
`
}

// TestAParallelSettlesUnderItsJoin: all fails on one failed branch and the
// run ends partial; allSettled carries on past one; any settles on the
// first success and stops the branch still running.
func TestAParallelSettlesUnderItsJoin(t *testing.T) {
	t.Run("all", func(t *testing.T) {
		wf := newWFFake()
		wf.on("b", func(string, string, int) wfReply { return wfReply{text: "no luck\nFAILED: nothing to see", credits: 1} })
		e, _, f, _ := wfEngine(t, wf)
		run := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: parallelRecipe("all")})
		r := tickRuns(t, e, f.ID, run.N, "ended", ended)
		if r.State != domain.RunPartial {
			t.Fatalf("ended %s: %s", r.State, r.Reason)
		}
		if _, ran := stepPaths(t, e, f.ID, run.N)["after"]; ran {
			t.Error("the step after a failed parallel ran")
		}
	})
	t.Run("allSettled", func(t *testing.T) {
		wf := newWFFake()
		wf.on("b", func(string, string, int) wfReply { return wfReply{text: "FAILED: nothing to see", credits: 1} })
		e, _, f, _ := wfEngine(t, wf)
		run := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: parallelRecipe("allSettled")})
		r := tickRuns(t, e, f.ID, run.N, "ended", ended)
		paths := stepPaths(t, e, f.ID, run.N)
		if paths["p/b"].State != domain.StepFailed || paths["after"].State != domain.StepDone {
			t.Fatalf("steps %+v", paths)
		}
		// out.txt is missing, so the checked item fails the run
		if r.State != domain.RunPartial || domain.ResultsMet(r.Results) {
			t.Fatalf("ended %s results %+v", r.State, r.Results)
		}
	})
	t.Run("any", func(t *testing.T) {
		wf := newWFFake()
		g := newGate(t)
		wf.on("b", func(string, string, int) wfReply {
			<-g.ch
			return wfReply{text: "late", credits: 1}
		})
		e, _, f, _ := wfEngine(t, wf)
		run := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: parallelRecipe("any")})
		tickRuns(t, e, f.ID, run.N, "started after", func(*domain.Run) bool {
			_, ok := stepPaths(t, e, f.ID, run.N)["after"]
			return ok
		})
		g.open()
		tickRuns(t, e, f.ID, run.N, "ended", ended)
		paths := stepPaths(t, e, f.ID, run.N)
		if paths["p/a"].State != domain.StepDone || paths["p/b"].State != domain.StepStopped {
			t.Fatalf("a %s b %s (%s)", paths["p/a"].State, paths["p/b"].State, paths["p/b"].Reason)
		}
	})
}

const loopRecipe = `name: loop
steps:
  - id: build
    repeat:
      max: 1
      until: { contains: ALL GOOD }
      onMax: pause
      steps:
        - { id: implement, role: implementer, prompt: "make it" }
`

// TestARepeatAtItsMaxAsksThePerson: onMax pause puts the loop to the
// person, and one more iteration is theirs to give.
func TestARepeatAtItsMaxAsksThePerson(t *testing.T) {
	wf := newWFFake()
	wf.on("implement", func(dir, _ string, n int) wfReply {
		if n == 1 {
			return wfReply{text: "not yet", credits: 1}
		}
		_ = os.WriteFile(filepath.Join(dir, "out.txt"), []byte("ok\n"), 0o600)
		return wfReply{text: "ALL GOOD", credits: 1}
	})
	e, _, f, _ := wfEngine(t, wf)
	ctx := context.Background()
	run := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: loopRecipe})
	r := tickRuns(t, e, f.ID, run.N, "asked about the max", pendingOf(domain.RunDecideMax))
	if r.Pending.Step != "build/1" || len(r.Pending.Options) != 3 {
		t.Fatalf("decision %+v", r.Pending)
	}
	// ticking again asks nothing new
	id := r.Pending.ID
	if _, err := e.WorkflowTick(ctx); err != nil {
		t.Fatal(err)
	}
	if again, _ := e.cfg.Store.Run(ctx, f.ID, run.N); again.Pending == nil || again.Pending.ID != id {
		t.Fatalf("the decision was raised again: %+v", again.Pending)
	}
	// the person's own words are no answer: they go to the session, and
	// the question stays open, the same one
	before := len(wf.sessionTurns())
	if err := e.WorkflowAnswer(ctx, f.ID, run.N, pendingID(t, e, f.ID, run.N), "why does it keep failing?"); err != nil {
		t.Fatalf("words to an open question: %v", err)
	}
	waitUntil(t, "the session heard the person", func() bool { return len(wf.sessionTurns()) > before })
	if turns := wf.sessionTurns(); !strings.Contains(turns[len(turns)-1], "says: why does it keep failing?") ||
		!strings.Contains(turns[len(turns)-1], "do not answer it for them") {
		t.Fatalf("the session was told %q", turns[len(turns)-1])
	}
	if still, _ := e.cfg.Store.Run(ctx, f.ID, run.N); still.Pending == nil || still.Pending.ID != id || len(still.OnMax) != 0 {
		t.Fatalf("words closed the question: %+v %v", still.Pending, still.OnMax)
	}
	if err := e.WorkflowAnswer(ctx, f.ID, run.N, pendingID(t, e, f.ID, run.N), WorkflowOneMore); err != nil {
		t.Fatal(err)
	}
	r = tickRuns(t, e, f.ID, run.N, "ended", ended)
	if r.State != domain.RunDone || wf.count("implement") != 2 {
		t.Fatalf("ended %s after %d iterations: %s", r.State, wf.count("implement"), r.Reason)
	}
}

// TestAStepIsNeverHeldOverWhatItSpends: a run estimates nothing, so no
// step is a runaway and nothing warns of a forecast. A step that spends
// far past the envelope its recipe still names — ignored — runs on while
// the session's pool has money; only a person's stop ends it.
func TestAStepIsNeverHeldOverWhatItSpends(t *testing.T) {
	wf := newWFFake()
	e, _, f, _ := wfEngine(t, wf)
	// the step spends past its cap, then holds its turn open
	wf.Responder = func(base func(agent.SessionOpts, string) []agent.Event) func(agent.SessionOpts, string) []agent.Event {
		return func(opts agent.SessionOpts, msg string) []agent.Event {
			if !strings.Contains(msg, "You are step big") {
				return base(opts, msg)
			}
			wf.mu.Lock()
			wf.calls["big"]++
			wf.mu.Unlock()
			return []agent.Event{
				{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 40, Model: "m"}},
				{Kind: agent.EventTextDelta, Text: "working"},
			}
		}
	}(wf.Responder)
	ctx := context.Background()
	run := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: "name: big\nsteps:\n  - { id: big, role: scout, envelope: 10, prompt: look }\n"})
	tickRuns(t, e, f.ID, run.N, "the step has spent past ten times its old envelope", func(*domain.Run) bool {
		s, ok := stepPaths(t, e, f.ID, run.N)["big"]
		return ok && s.Spent >= 40
	})
	for range 5 {
		if _, err := e.WorkflowTick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	r, _ := e.cfg.Store.Run(ctx, f.ID, run.N)
	if r.Pending != nil || r.State != domain.RunRunning {
		t.Fatalf("a step's spend was held against it: %s, %+v", r.State, r.Pending)
	}
	if s := stepPaths(t, e, f.ID, run.N)["big"]; s.State != domain.StepRunning {
		t.Fatalf("step %+v", s)
	}
	if err := e.WorkflowStop(ctx, f.ID, "enough"); err != nil {
		t.Fatal(err)
	}
	r = tickRuns(t, e, f.ID, run.N, "ended", ended)
	if r.State != domain.RunStopped {
		t.Fatalf("ended %s", r.State)
	}
}

// TestASpentPoolPausesTheRunAndATopUpCarriesItOn: a run spends from its
// session's envelope. When that runs out the run pauses, its work kept,
// and puts a top-up to the person; the top-up carries it on.
func TestASpentPoolPausesTheRunAndATopUpCarriesItOn(t *testing.T) {
	wf := newWFFake()
	wf.on("first", func(dir, _ string, _ int) wfReply {
		_ = os.WriteFile(filepath.Join(dir, "out.txt"), []byte("ok\n"), 0o600)
		return wfReply{text: "wrote it", credits: 200}
	})
	e, store, f, _ := wfEngine(t, wf)
	ctx := context.Background()
	if err := e.RaiseEnvelope(ctx, f.ID, 150); err != nil {
		t.Fatal(err)
	}
	run := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: "name: two\nsteps:\n" +
		"  - { id: first, role: implementer, envelope: 20, prompt: write }\n  - { id: second, role: scout, envelope: 20, prompt: read }\n"})
	r := tickRuns(t, e, f.ID, run.N, "paused for money", func(r *domain.Run) bool {
		return r.State == domain.RunPaused && r.Pending != nil && r.Pending.Kind == domain.RunDecideBudget
	})
	if !r.OutOfBudget || r.Pending.Amount < 150 || !reflect.DeepEqual(r.Pending.Options, []string{WorkflowTopUp, WorkflowStopRun}) {
		t.Fatalf("paused run %+v, deciding %+v", r, r.Pending)
	}
	if _, ran := stepPaths(t, e, f.ID, run.N)["second"]; ran {
		t.Fatal("a step started on a spent pool")
	}
	sess, _ := store.GetFeature(ctx, f.ID)
	if left, _ := e.sessionPoolLeft(ctx, sess); left > 0 {
		t.Fatalf("the session has %.0f left after its run overspent it", left)
	}
	if err := e.WorkflowAnswer(ctx, f.ID, run.N, pendingID(t, e, f.ID, run.N), WorkflowTopUp); err != nil {
		t.Fatal(err)
	}
	r = tickRuns(t, e, f.ID, run.N, "ended", ended)
	if r.State != domain.RunDone {
		t.Fatalf("ended %s: %s", r.State, r.Reason)
	}
	if sess, _ = store.GetFeature(ctx, f.ID); sess.Budget.Envelope <= 150 {
		t.Errorf("the envelope is still %d", sess.Budget.Envelope)
	}
}

// TestStoppingARunRevertsNothing: a stop interrupts the running step and
// ends the run stopped, its changes left in the tree; Revert is refused
// while the step runs.
func TestStoppingARunRevertsNothing(t *testing.T) {
	wf := newWFFake()
	g := newGate(t)
	wf.on("slow", func(dir, _ string, _ int) wfReply {
		_ = os.WriteFile(filepath.Join(dir, "half.txt"), []byte("half\n"), 0o600)
		<-g.ch
		return wfReply{text: "late", credits: 1}
	})
	e, store, f, _ := wfEngine(t, wf)
	ctx := context.Background()
	run := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: "name: slow\nsteps:\n  - { id: slow, role: implementer, prompt: write }\n"})
	sess, _ := store.GetFeature(ctx, f.ID)
	tree := e.treeOf(ctx, sess)
	tickRuns(t, e, f.ID, run.N, "wrote half", func(*domain.Run) bool {
		_, err := os.Stat(filepath.Join(tree, "half.txt"))
		return err == nil
	})
	if _, err := e.WorkflowRevert(ctx, f.ID, run.N, "any"); err == nil || !strings.Contains(err.Error(), "running") {
		t.Fatalf("revert while a step runs: %v", err)
	}
	if err := e.WorkflowStop(ctx, f.ID, "changed my mind"); err != nil {
		t.Fatal(err)
	}
	r := tickRuns(t, e, f.ID, run.N, "ended", ended)
	g.open()
	if r.State != domain.RunStopped || !strings.Contains(r.Reason, "changed my mind") {
		t.Fatalf("ended %s: %s", r.State, r.Reason)
	}
	if s := stepPaths(t, e, f.ID, run.N)["slow"]; s.State != domain.StepStopped {
		t.Fatalf("step %+v", s)
	}
	if _, err := os.Stat(filepath.Join(tree, "half.txt")); err != nil {
		t.Fatal("the stop reverted the step's work")
	}
}

// TestRevertRunPutsTheTreeBack: Revert run restores what the run changed
// and deletes what it created.
func TestRevertRunPutsTheTreeBack(t *testing.T) {
	wf := newWFFake()
	wf.on("edit", func(dir, _ string, _ int) wfReply {
		_ = os.WriteFile(filepath.Join(dir, "README.md"), []byte("changed\n"), 0o600)
		_ = os.MkdirAll(filepath.Join(dir, "new"), 0o700)
		_ = os.WriteFile(filepath.Join(dir, "new", "file.txt"), []byte("new\n"), 0o600)
		return wfReply{text: "edited", credits: 1}
	})
	e, store, f, _ := wfEngine(t, wf)
	ctx := context.Background()
	run := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: "name: edit\nsteps:\n  - { id: edit, role: implementer, prompt: edit }\n"})
	tickRuns(t, e, f.ID, run.N, "ended", ended)
	pre, err := e.WorkflowRevertPreview(ctx, f.ID, run.N)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(pre.Paths, ",") != "README.md,new/file.txt" || pre.Note == "" {
		t.Fatalf("preview %+v", pre)
	}
	paths, err := e.WorkflowRevert(ctx, f.ID, run.N, pre.Tree)
	if err != nil || len(paths) != 2 {
		t.Fatalf("revert %v %v", paths, err)
	}
	sess, _ := store.GetFeature(ctx, f.ID)
	tree := e.treeOf(ctx, sess)
	if raw, _ := os.ReadFile(filepath.Join(tree, "README.md")); string(raw) != "x\n" {
		t.Errorf("README.md reads %q", raw)
	}
	if _, err := os.Stat(filepath.Join(tree, "new")); !os.IsNotExist(err) {
		t.Errorf("the created file is still there: %v", err)
	}
	if r, _ := store.Run(ctx, f.ID, run.N); r == nil || r.RevertedTo != domain.RevertedToStart {
		t.Errorf("the revert is not recorded on the run: %+v", r)
	}
}

// TestARunThatMeetsItsSpecMarksItMet: a run whose checked items all pass
// leaves its spec met, read from the run's results; a later run that
// leaves an item unmet makes it partial again.
func TestARunThatMeetsItsSpecMarksItMet(t *testing.T) {
	wf := newWFFake()
	wf.on("make", func(dir, _ string, _ int) wfReply {
		_ = os.WriteFile(filepath.Join(dir, "out.txt"), []byte("ok\n"), 0o600)
		return wfReply{text: "made\nMET: DW-2", credits: 1}
	})
	wf.on("unmake", func(dir, _ string, _ int) wfReply {
		_ = os.Remove(filepath.Join(dir, "out.txt"))
		return wfReply{text: "unmade", credits: 1}
	})
	e, store, f, _ := wfEngine(t, wf)
	ctx := context.Background()
	run := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: "name: make\nsteps:\n  - { id: make, role: implementer, prompt: make }\n"})
	r := tickRuns(t, e, f.ID, run.N, "ended", ended)
	if r.State != domain.RunDone || !domain.ResultsMet(r.Results) {
		t.Fatalf("ended %s results %+v", r.State, r.Results)
	}
	if strings.Contains(r.Report, "Validated") {
		t.Errorf("the report still names a validated tree:\n%s", r.Report)
	}
	if specs, _ := store.SessionSpecs(ctx, f.ID); specs[0].State != domain.SpecMet {
		t.Fatalf("spec 1 after a run that met it: %s", specs[0].State)
	}
	run = proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: "name: unmake\nsteps:\n  - { id: unmake, role: implementer, prompt: unmake }\n"})
	if r = tickRuns(t, e, f.ID, run.N, "ended", ended); r.State != domain.RunPartial {
		t.Fatalf("ended %s", r.State)
	}
	if specs, _ := store.SessionSpecs(ctx, f.ID); specs[0].State != domain.SpecPartial {
		t.Fatalf("spec 1 after a run that left it unmet: %s", specs[0].State)
	}
}

// TestARunAllowedWhileAnotherIsOpenWaitsQueued: the queue is one deep, and
// a queued run starts when the open one ends.
func TestARunAllowedWhileAnotherIsOpenWaitsQueued(t *testing.T) {
	wf := newWFFake()
	g := newGate(t)
	wf.on("slow", func(string, string, int) wfReply {
		<-g.ch
		return wfReply{text: "done", credits: 1}
	})
	e, _, f, _ := wfEngine(t, wf)
	ctx := context.Background()
	first := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: "name: slow\nsteps:\n  - { id: slow, role: scout, prompt: wait }\n"})
	tickRuns(t, e, f.ID, first.N, "started its step", func(*domain.Run) bool { return wf.count("slow") > 0 })
	second := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: "name: quick\nsteps:\n  - { id: quick, role: scout, prompt: look }\n"})
	// the spec is frozen as the person read it, when the run was proposed;
	// no baseline is taken while the first run's steps write the tree
	queued, _ := e.cfg.Store.Run(ctx, f.ID, second.N)
	if queued.State != domain.RunQueued || queued.SpecRev == 0 || queued.Baseline != nil {
		t.Fatalf("second run %s, revision %d, baseline %+v", queued.State, queued.SpecRev, queued.Baseline)
	}
	// an edit while it waits does not move what it is judged against
	if _, err := e.WriteSessionSpec(ctx, f, 1, wfTestSpec+"\nedited while queued\n", "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.WorkflowPropose(ctx, f.ID, WorkflowRequest{Recipe: "investigate"}); err == nil || !strings.Contains(err.Error(), "queued") {
		t.Fatalf("a third run behind the queue: %v", err)
	}
	g.open()
	tickRuns(t, e, f.ID, first.N, "ended", ended)
	r := tickRuns(t, e, f.ID, second.N, "ended", ended)
	if r.SpecRev != queued.SpecRev || wf.count("quick") != 1 || r.Baseline == nil {
		t.Fatalf("the queued run %+v", r)
	}
}

// TestAlwaysAllowAllowsEveryLaterRun: "Always allow" carries no amount. It
// allows this run and every later one of the session without asking,
// however large its recipe: the session's budget is the only cap.
func TestAlwaysAllowAllowsEveryLaterRun(t *testing.T) {
	wf := newWFFake()
	e, store, f, _ := wfEngine(t, wf)
	ctx := context.Background()
	recipe := "name: one\nsteps:\n  - { id: one, role: scout, envelope: 40, prompt: look }\n"
	run, err := e.WorkflowPropose(ctx, f.ID, WorkflowRequest{RecipeYAML: recipe})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(run.Pending.Options, []string{WorkflowAllow, WorkflowAlways, WorkflowDeny}) {
		t.Fatalf("a proposal offers %q", run.Pending.Options)
	}
	if err := e.WorkflowAnswer(ctx, f.ID, run.N, pendingID(t, e, f.ID, run.N), WorkflowAlways); err != nil {
		t.Fatal(err)
	}
	if always, _ := store.WorkflowAlwaysAllow(ctx, f.ID); !always {
		t.Fatal("always allow was not kept")
	}
	tickRuns(t, e, f.ID, run.N, "ended", ended)
	again, err := e.WorkflowPropose(ctx, f.ID, WorkflowRequest{RecipeYAML: recipe})
	if err != nil {
		t.Fatal(err)
	}
	if again.State != domain.RunRunning || again.Pending != nil {
		t.Fatalf("a pre-authorised run asked: %+v", again)
	}
	tickRuns(t, e, f.ID, again.N, "ended", ended)
	big, err := e.WorkflowPropose(ctx, f.ID, WorkflowRequest{RecipeYAML: "name: big\nsteps:\n" +
		"  - id: loop\n    repeat:\n      max: 50\n      until: checks\n      steps: [{ id: one, role: implementer, envelope: 4000, prompt: build }]\n"})
	if err != nil {
		t.Fatal(err)
	}
	if big.State != domain.RunRunning || big.Pending != nil {
		t.Fatalf("a large run asked: %+v", big)
	}
	if err := e.WorkflowStop(ctx, f.ID, "enough"); err != nil {
		t.Fatal(err)
	}
	tickRuns(t, e, f.ID, big.N, "ended", ended)
}

// TestFeedbackOnAProposalReachesTheSession: the person's own words close
// the proposal and go to the session agent as a turn.
func TestFeedbackOnAProposalReachesTheSession(t *testing.T) {
	wf := newWFFake()
	e, _, f, _ := wfEngine(t, wf)
	ctx := context.Background()
	run, err := e.WorkflowPropose(ctx, f.ID, WorkflowRequest{Recipe: "deliver"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"design · contractor", "build ↺ up to 3", "DW-1: test -f out.txt", "spends from the session's budget"} {
		if !strings.Contains(run.Pending.Question, want) {
			t.Errorf("the proposal does not show %q:\n%s", want, run.Pending.Question)
		}
	}
	for _, not := range []string{"stimate", "~"} {
		if strings.Contains(run.Pending.Question, not) {
			t.Errorf("the proposal still estimates (%q):\n%s", not, run.Pending.Question)
		}
	}
	if err := e.WorkflowAnswer(ctx, f.ID, run.N, pendingID(t, e, f.ID, run.N), "use investigate first"); err != nil {
		t.Fatal(err)
	}
	r, _ := e.cfg.Store.Run(ctx, f.ID, run.N)
	if r.State != domain.RunDenied || r.Feedback != "use investigate first" || r.Pending != nil {
		t.Fatalf("run %+v", r)
	}
	waitUntil(t, "the session heard the feedback", func() bool {
		for _, m := range wf.sessionTurns() {
			if strings.Contains(m, "use investigate first") {
				return true
			}
		}
		return false
	})
	// the session may propose again
	if _, err := e.WorkflowPropose(ctx, f.ID, WorkflowRequest{Recipe: "investigate"}); err != nil {
		t.Fatal(err)
	}
}

// TestAPersonTalksToAStepAfterItEnded: a message to an ended step resumes
// its conversation; the step stays done and nothing after it re-runs.
func TestAPersonTalksToAStepAfterItEnded(t *testing.T) {
	wf := newWFFake()
	wf.on("one#", func(dir, _ string, _ int) wfReply {
		_ = os.WriteFile(filepath.Join(dir, "after.txt"), []byte("later\n"), 0o600)
		return wfReply{text: "added after.txt", credits: 1}
	})
	e, _, f, _ := wfEngine(t, wf)
	ctx := context.Background()
	run := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: "name: one\nsteps:\n  - { id: one, role: implementer, prompt: do }\n  - { id: two, role: scout, prompt: look }\n"})
	tickRuns(t, e, f.ID, run.N, "ended", ended)
	one := stepPaths(t, e, f.ID, run.N)["one"]
	if err := e.StepSend(ctx, one.Card, "also add after.txt"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the step answered", func() bool { return wf.count("one#") == 1 })
	ff := e.Freeform(one.Card)
	waitFreeformIdle(t, ff)
	_, paths, err := e.RunDiff(ctx, f.ID, run.N)
	if err != nil || !strings.Contains(strings.Join(paths, ","), "after.txt") {
		t.Fatalf("the run's diff %v %v", paths, err)
	}
	steps := stepPaths(t, e, f.ID, run.N)
	if steps["one"].State != domain.StepDone || steps["one"].Output != "done" || wf.count("two") != 1 {
		t.Fatalf("the follow-up re-ran something: %+v, two ran %d", steps["one"], wf.count("two"))
	}
	if err := e.StepSend(ctx, f.ID, "hi"); err == nil {
		t.Fatal("the session's own card was taken for a step's")
	}
}

// TestAPlannerReplacesTheRemainingSteps: a planner's gummi-workflow block
// goes to the person as a revised plan; allowed, its steps replace the
// run's remaining ones.
func TestAPlannerReplacesTheRemainingSteps(t *testing.T) {
	wf := newWFFake()
	wf.on("plan", func(string, string, int) wfReply {
		return wfReply{text: "Here is the plan.\n\n```gummi-workflow\n- id: fresh\n  role: scribe\n  prompt: write it\n```\n", credits: 1}
	})
	e, _, f, _ := wfEngine(t, wf)
	ctx := context.Background()
	run := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: "name: planned\nsteps:\n" +
		"  - { id: plan, role: planner, prompt: plan it }\n  - { id: stale, role: scribe, prompt: old }\n"})
	r := tickRuns(t, e, f.ID, run.N, "asked about the plan", pendingOf(domain.RunDecidePlan))
	if r.Pending.Step != "plan" || !strings.Contains(r.Pending.RecipeYAML, "fresh") {
		t.Fatalf("decision %+v", r.Pending)
	}
	// the person reads what the plan drops, and the checks still in force
	if q := r.Pending.Question; !strings.Contains(q, "What they replace, which would not run:\n- stale") ||
		!strings.Contains(q, "still judged against spec S1") || !strings.Contains(q, "DW-1: test -f out.txt") {
		t.Fatalf("the question:\n%s", q)
	}
	if p, err := e.WorkflowProposal(ctx, f.ID, run.N); err != nil || len(p.Replaces) != 1 || p.Replaces[0].ID != "stale" ||
		len(p.Steps) != 1 || p.Steps[0].ID != "fresh" {
		t.Fatalf("the plan's card %+v %v", p, err)
	}
	if s := stepPaths(t, e, f.ID, run.N)["plan"]; s.State != domain.StepAsking {
		t.Fatalf("the planner %+v", s)
	}
	if err := e.WorkflowAnswer(ctx, f.ID, run.N, pendingID(t, e, f.ID, run.N), WorkflowAllowPlan); err != nil {
		t.Fatal(err)
	}
	r = tickRuns(t, e, f.ID, run.N, "ended", ended)
	paths := stepPaths(t, e, f.ID, run.N)
	if _, ran := paths["stale"]; ran || paths["fresh"].State != domain.StepDone || paths["plan"].State != domain.StepDone {
		t.Fatalf("steps %+v", paths)
	}
	if len(r.Recipe.Steps) != 2 || r.Recipe.Steps[1].ID() != "fresh" || !strings.Contains(r.RecipeYAML, "fresh") {
		t.Fatalf("the run's recipe %+v", r.Recipe)
	}
}

// TestARevisedPlanAddsNoPRStep: a planner's block that adds a pr: or a
// watch: step goes back to the planner, and the plan the person allowed runs on:
// what a run does to the PR is what the person allowed at the proposal.
func TestARevisedPlanAddsNoPRStep(t *testing.T) {
	for name, block := range map[string]string{
		"pr":    "- id: open\n  pr: { act: create }\n",
		"watch": "- id: shepherd\n  watch:\n    until: merged\n    on:\n      comment: { goto: plan }\n",
	} {
		t.Run(name, func(t *testing.T) {
			wf := newWFFake()
			wf.on("plan", func(string, string, int) wfReply {
				return wfReply{text: "Here is the plan.\n\n```gummi-workflow\n" + block + "```\n", credits: 1}
			})
			e, _, f, _ := wfEngine(t, wf)
			run := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: "name: planned\nsteps:\n" +
				"  - { id: plan, role: planner, prompt: plan it }\n  - { id: stale, role: scribe, prompt: old }\n"})
			tickRuns(t, e, f.ID, run.N, "ended", ended)
			paths := stepPaths(t, e, f.ID, run.N)
			if _, ran := paths["open"]; ran {
				t.Fatalf("the revised pr step ran: %+v", paths)
			}
			if _, ran := paths["shepherd"]; ran {
				t.Fatalf("the revised watch ran: %+v", paths)
			}
			if paths["stale"].State != domain.StepDone {
				t.Fatalf("the plan the person allowed did not run on: %+v", paths)
			}
			wf.mu.Lock()
			told := strings.Join(wf.stepT["plan#"], "\n")
			wf.mu.Unlock()
			if !strings.Contains(told, "adds no command, dispatch, pr or watch step") {
				t.Fatal("the planner was not told why its block was refused")
			}
		})
	}
}

// TestAQuitParksTheRun: a run that was running when gummi stopped is
// parked by the next engine's first tick, and resumes only when a person
// resumes it.
func TestAQuitParksTheRun(t *testing.T) {
	wf := newWFFake()
	e, store, f, _ := wfEngine(t, wf)
	ctx := context.Background()
	run := &domain.Run{
		Session: f.ID, RecipeName: "x", State: domain.RunRunning, Spec: 1, SpecRev: 1,
		Recipe: domain.Recipe{Name: "x", Steps: []domain.Entry{{Step: &domain.Step{ID: "one", Role: domain.RoleScout, Prompt: "look"}}}},
	}
	if err := store.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	e.wfRT().boot = time.Now().Add(time.Hour)
	res, err := e.WorkflowTick(ctx)
	if err != nil || len(res.Actions) != 1 || res.Actions[0] != "park" {
		t.Fatalf("tick %+v %v", res, err)
	}
	r, _ := store.Run(ctx, f.ID, run.N)
	if r.State != domain.RunPaused {
		t.Fatalf("run %s", r.State)
	}
	if _, err := e.WorkflowTick(ctx); err != nil {
		t.Fatal(err)
	}
	if steps := stepPaths(t, e, f.ID, run.N); len(steps) != 0 {
		t.Fatalf("a parked run started steps: %+v", steps)
	}
}

// TestTheSessionProposesThroughItsTool: workflow_run is a session's tool,
// and what it proposes waits on the person.
func TestTheSessionProposesThroughItsTool(t *testing.T) {
	wf := newWFFake()
	e, _, f, ff := wfEngine(t, wf)
	wf.Responder = func(base func(agent.SessionOpts, string) []agent.Event) func(agent.SessionOpts, string) []agent.Event {
		return func(opts agent.SessionOpts, msg string) []agent.Event {
			if msg != "run deliver" {
				return base(opts, msg)
			}
			return []agent.Event{
				toolCall("w1", workflowRunToolName, map[string]any{"recipe": "deliver"}),
				{Kind: agent.EventMessage, Text: "proposed"},
				{Kind: agent.EventIdle},
			}
		}
	}(wf.Responder)
	ctx := context.Background()
	if err := ff.Send(ctx, "run deliver"); err != nil {
		t.Fatal(err)
	}
	waitFreeformIdle(t, ff)
	r := tickRuns(t, e, f.ID, 1, "proposed", pendingOf(domain.RunDecideProposal))
	if r.RecipeName != "deliver" || r.Spec != 1 || r.State != domain.RunProposed {
		t.Fatalf("run %+v", r)
	}
	if run, d := e.WorkflowDecision(ctx, f.ID); run == nil || d == nil || d.ID != r.Pending.ID {
		t.Fatalf("decision %+v", d)
	}
}

// TestTheRecipesToolShowsARecipeWhole: given a name, workflow_recipes
// answers with that recipe's YAML as written, so a session can adapt it;
// a name no recipe has is an error.
func TestTheRecipesToolShowsARecipeWhole(t *testing.T) {
	e, _, _, _ := wfEngine(t, newWFFake())
	out, err := e.workflowRecipesText("deliver")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "deliver (bundled):") || !strings.Contains(out, "until: { verdict: APPROVED, in: review }") {
		t.Fatalf("deliver's YAML:\n%s", out)
	}
	if list, _ := e.workflowRecipesText(""); !strings.Contains(list, "- fix-until-green (bundled): ") {
		t.Fatalf("the list:\n%s", list)
	}
	if _, err := e.workflowRecipesText("nope"); err == nil {
		t.Fatal("a name no recipe has answered")
	}
}

// TestAStepsQuestionWaitsOnThePerson: a step's ask_user is a question on
// the step's card; the step waits, asking, until the person answers.
func TestAStepsQuestionWaitsOnThePerson(t *testing.T) {
	wf := newWFFake()
	e, _, f, _ := wfEngine(t, wf)
	wf.Responder = func(base func(agent.SessionOpts, string) []agent.Event) func(agent.SessionOpts, string) []agent.Event {
		return func(opts agent.SessionOpts, msg string) []agent.Event {
			if !strings.Contains(msg, "You are step ask ") {
				return base(opts, msg)
			}
			wf.mu.Lock()
			wf.cardStep[opts.FeatureID] = "ask"
			wf.mu.Unlock()
			return []agent.Event{
				toolCall("q1", askToolName, map[string]any{"question": "Blue or green?", "options": []map[string]string{{"label": "blue"}, {"label": "green"}}}),
				{Kind: agent.EventIdle},
			}
		}
	}(wf.Responder)
	ctx := context.Background()
	run := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: "name: ask\nsteps:\n  - { id: ask, role: implementer, prompt: choose }\n"})
	tickRuns(t, e, f.ID, run.N, "asking", func(*domain.Run) bool {
		return stepPaths(t, e, f.ID, run.N)["ask"].State == domain.StepAsking
	})
	card := stepPaths(t, e, f.ID, run.N)["ask"].Card
	// an answer read off another question — one answered elsewhere, or
	// replaced since — is refused, naming what the step asks now
	if err := e.AnswerCall(ctx, card, "an-older-call", "green", state.ActorUser); err == nil ||
		!strings.Contains(err.Error(), "Blue or green?") {
		t.Fatalf("an answer to a question no longer open: %v", err)
	}
	call := e.Freeform(card).Snapshot().PendingAsk.CallID
	if err := e.AnswerCall(ctx, card, call, "blue", state.ActorUser); err != nil {
		t.Fatal(err)
	}
	r := tickRuns(t, e, f.ID, run.N, "ended", ended)
	if s := stepPaths(t, e, f.ID, run.N)["ask"]; s.State != domain.StepDone || wf.count("ask#") != 1 {
		t.Fatalf("ended %s; step %+v; answered turns %d", r.State, s, wf.count("ask#"))
	}
}

// TestAProposalReadsAsItsCard: the proposal the faces draw (DESIGN §25.5)
// carries the recipe's step tree with each step's role and model, the spec
// and the revision it would freeze, every done-when check command, what
// the session's pool has left, and whether the tree is dirty; the "Always
// allow" answer names no amount and allows the run.
func TestAProposalReadsAsItsCard(t *testing.T) {
	wf := newWFFake()
	e, _, f, ff := wfEngine(t, wf)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(ff.WorkDir(), "scratch.txt"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run, err := e.WorkflowPropose(ctx, f.ID, WorkflowRequest{Recipe: "deliver"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := e.WorkflowProposal(ctx, f.ID, run.N)
	if err != nil {
		t.Fatal(err)
	}
	if p.Recipe != "deliver" || p.Spec != 1 || p.SpecTitle != "Workflow test" || p.SpecRev == 0 {
		t.Fatalf("head %+v", p)
	}
	if !p.Dirty || !p.Bounded || p.PoolLeft <= 0 {
		t.Fatalf("money or tree %+v", p)
	}
	if len(p.Checks) != 1 || p.Checks[0].Item != "DW-1" || p.Checks[0].Cmd != "test -f out.txt" {
		t.Fatalf("checks %+v", p.Checks)
	}
	kinds := map[string]ProposalStep{}
	for _, s := range p.Steps {
		kinds[s.ID] = s
	}
	if b := kinds["build"]; b.Kind != "repeat" || b.Max != 3 || b.Depth != 0 {
		t.Fatalf("build %+v", b)
	}
	if r := kinds["review-a"]; r.Kind != "step" || r.Role != domain.RoleReviewer || r.Slot != "" || r.Model == "" || r.Depth != 2 {
		t.Fatalf("review-a %+v", r)
	}
	if r := kinds["review-b"]; r.Slot != "reviewer-alt" {
		t.Fatalf("review-b %+v", r)
	}
	// each step names what it runs on, and its prompt's first paragraph
	// whole, not its first wrapped line
	if im := kinds["implement"]; !strings.Contains(im.Model, "m on ") ||
		im.Prompt != "Implement {{artifacts.design}} for the spec below. If {{artifacts.review}} exists, address its findings first. "+
			"Run the whole test suite of every package you changed, and keep it passing." {
		t.Fatalf("implement %+v", im)
	}
	always := run.Pending.Options[1]
	if always != WorkflowAlways {
		t.Fatalf("always allow reads %q", always)
	}
	if err := e.WorkflowAnswer(ctx, f.ID, run.N, pendingID(t, e, f.ID, run.N), always); err != nil {
		t.Fatal(err)
	}
	if r, _ := e.cfg.Store.Run(ctx, f.ID, run.N); r.State != domain.RunRunning {
		t.Fatalf("always allow left the run %s", r.State)
	}
	if _, err := e.WorkflowProposal(ctx, f.ID, run.N); err == nil {
		t.Fatal("an allowed run still reads as a proposal")
	}
}

// TestAProcessConductsOnlyTheRunsItHolds: a tick conducts a session's run
// only while this process holds the session's card lock. A run another
// gummi drives — whose lock it holds — is neither parked nor ticked here;
// a run this process conducts keeps its session's lock held until it ends.
func TestAProcessConductsOnlyTheRunsItHolds(t *testing.T) {
	wf := newWFFake()
	e, store, f, _ := wfEngine(t, wf)
	ctx := context.Background()
	other := freeformCard(2, "driven elsewhere")
	createFeature(t, store, other)
	ws := e.cfg.Workspace
	e.UseCardLocks(state.NewCardLocks(ws))
	recipe := domain.Recipe{Name: "x", Steps: []domain.Entry{{Step: &domain.Step{ID: "one", Role: domain.RoleScout, Prompt: "look"}}}}
	for _, s := range []domain.FeatureID{f.ID, other.ID} {
		if err := store.CreateRun(ctx, &domain.Run{Session: s, RecipeName: "x", State: domain.RunRunning, Spec: 1, SpecRev: 1, Recipe: recipe}); err != nil {
			t.Fatal(err)
		}
	}
	// another gummi drives the other session
	release, err := state.AcquireLock(ws.CardLockFile(other.ID))
	if err != nil {
		t.Fatal(err)
	}
	e.wfRT().boot = time.Now().Add(time.Hour)
	if _, err := e.WorkflowTick(ctx); err != nil {
		t.Fatal(err)
	}
	if r, _ := store.Run(ctx, other.ID, 1); r.State != domain.RunRunning {
		t.Fatalf("a run another gummi drives was conducted here: %s (%s)", r.State, r.Reason)
	}
	if r, _ := store.Run(ctx, f.ID, 1); r.State != domain.RunPaused {
		t.Fatalf("the run this process holds was not conducted: %s", r.State)
	}
	// the open run keeps its session's lock held here
	if rel, err := state.AcquireLock(ws.CardLockFile(f.ID)); err == nil {
		rel()
		t.Fatal("another gummi could take the card whose run this process conducts")
	}
	release()
	if _, err := e.WorkflowTick(ctx); err != nil {
		t.Fatal(err)
	}
	if r, _ := store.Run(ctx, other.ID, 1); r.State != domain.RunPaused {
		t.Fatalf("once the other gummi let go, the run was not taken up: %s", r.State)
	}
	// a run that ends lets its session's lock go
	if err := e.WorkflowStop(ctx, f.ID, "done with it"); err != nil {
		t.Fatal(err)
	}
	tickRuns(t, e, f.ID, 1, "ended", ended)
	waitUntil(t, "the ended run's session lock let go", func() bool {
		if _, err := e.WorkflowTick(ctx); err != nil {
			t.Fatal(err)
		}
		rel, err := state.AcquireLock(ws.CardLockFile(f.ID))
		if err != nil {
			return false
		}
		rel()
		return true
	})
}

// TestARevertNeverDeletesWhatWasOnDiskAtTheStart: an ignored file on disk
// when a run starts is not the run's. A step that edits .gitignore so the
// file is no longer ignored does not make it look created: its contents
// stay out of the run's diff, and Revert run leaves it where it was.
func TestARevertNeverDeletesWhatWasOnDiskAtTheStart(t *testing.T) {
	wf := newWFFake()
	wf.on("edit", func(dir, _ string, _ int) wfReply {
		_ = os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("# nothing ignored\n"), 0o600)
		_ = os.WriteFile(filepath.Join(dir, "out.txt"), []byte("ok\n"), 0o600)
		return wfReply{text: "edited", credits: 1}
	})
	e, store, f, _ := wfEngine(t, wf)
	ctx := context.Background()
	sess, _ := store.GetFeature(ctx, f.ID)
	tree := e.treeOf(ctx, sess)
	if err := os.WriteFile(filepath.Join(tree, ".gitignore"), []byte(".env\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitOut(t, tree, "add", ".gitignore")
	gitOut(t, tree, "commit", "-q", "-m", "ignore .env")
	if err := os.WriteFile(filepath.Join(tree, ".env"), []byte("TOKEN=s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: "name: edit\nsteps:\n  - { id: edit, role: implementer, prompt: edit }\n"})
	tickRuns(t, e, f.ID, run.N, "ended", ended)
	patch, paths, err := e.RunDiff(ctx, f.ID, run.N)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(paths, ",") != ".gitignore,out.txt" {
		t.Errorf("the run's paths %q", paths)
	}
	if strings.Contains(patch, "s3cret") {
		t.Errorf("an ignored file's contents are in the run's diff:\n%s", patch)
	}
	pre, err := e.WorkflowRevertPreview(ctx, f.ID, run.N)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.WorkflowRevert(ctx, f.ID, run.N, pre.Tree); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(filepath.Join(tree, ".env")); err != nil || string(raw) != "TOKEN=s3cret\n" {
		t.Fatalf("Revert run took .env, which was on disk before the run: %q %v", raw, err)
	}
	if _, err := os.Stat(filepath.Join(tree, "out.txt")); !os.IsNotExist(err) {
		t.Errorf("the file the run created is still there: %v", err)
	}
}

// TestARevertIsOfWhatItsPreviewShowed: a revert is confirmed against its
// preview's tree, so a tree that changed since — the session or a person
// wrote a file after the confirm went up — is refused, not reverted
// unseen; and a revert waits while the session's own agent, or a step's
// agent answering a follow-up after its run ended, works in the tree.
func TestARevertIsOfWhatItsPreviewShowed(t *testing.T) {
	wf := newWFFake()
	wf.on("edit", func(dir, _ string, _ int) wfReply {
		_ = os.WriteFile(filepath.Join(dir, "out.txt"), []byte("ok\n"), 0o600)
		return wfReply{text: "edited", credits: 1}
	})
	g := newGate(t)
	wf.on("edit#", func(string, string, int) wfReply {
		<-g.ch
		return wfReply{text: "also done", credits: 1}
	})
	e, store, f, ff := wfEngine(t, wf)
	ctx := context.Background()
	run := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: "name: edit\nsteps:\n  - { id: edit, role: implementer, prompt: edit }\n"})
	tickRuns(t, e, f.ID, run.N, "ended", ended)
	sess, _ := store.GetFeature(ctx, f.ID)
	tree := e.treeOf(ctx, sess)
	pre, err := e.WorkflowRevertPreview(ctx, f.ID, run.N)
	if err != nil || pre.Tree == "" || strings.Join(pre.Paths, ",") != "out.txt" {
		t.Fatalf("preview %+v %v", pre, err)
	}
	// the tree moves after the preview
	if err := os.WriteFile(filepath.Join(tree, "later.txt"), []byte("later\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.WorkflowRevert(ctx, f.ID, run.N, pre.Tree); err == nil || !strings.Contains(err.Error(), "changed since") {
		t.Fatalf("a revert against a stale preview: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tree, "later.txt")); err != nil {
		t.Fatalf("the refused revert touched the tree: %v", err)
	}
	if _, err := e.WorkflowRevert(ctx, f.ID, run.N, ""); err == nil {
		t.Fatal("a revert with no preview went through")
	}
	// a follow-up to the ended step is working in the tree
	card := stepPaths(t, e, f.ID, run.N)["edit"].Card
	if err := e.StepSend(ctx, card, "also tidy"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the step is answering", func() bool { return e.freeformBusy(card) })
	pre, _ = e.WorkflowRevertPreview(ctx, f.ID, run.N)
	if _, err := e.WorkflowRevert(ctx, f.ID, run.N, pre.Tree); err == nil || !strings.Contains(err.Error(), "step edit") {
		t.Fatalf("a revert while a step answers a follow-up: %v", err)
	}
	g.open()
	waitUntil(t, "the step answered", func() bool { return !e.freeformBusy(card) })
	// the session's own agent is mid-turn
	sg := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-sg:
		default:
			close(sg)
		}
	})
	wf.Responder = func(base func(agent.SessionOpts, string) []agent.Event) func(agent.SessionOpts, string) []agent.Event {
		return func(opts agent.SessionOpts, msg string) []agent.Event {
			if strings.Contains(msg, "hold on") {
				<-sg
			}
			return base(opts, msg)
		}
	}(wf.Responder)
	if err := ff.Send(ctx, "hold on"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the session is mid-turn", ff.Busy)
	pre, _ = e.WorkflowRevertPreview(ctx, f.ID, run.N)
	if _, err := e.WorkflowRevert(ctx, f.ID, run.N, pre.Tree); err == nil || !strings.Contains(err.Error(), "session's agent") {
		t.Fatalf("a revert while the session's agent works: %v", err)
	}
	close(sg)
	waitFreeformIdle(t, ff)
	pre, _ = e.WorkflowRevertPreview(ctx, f.ID, run.N)
	if paths, err := e.WorkflowRevert(ctx, f.ID, run.N, pre.Tree); err != nil || len(paths) == 0 {
		t.Fatalf("the revert against a fresh preview: %v %v", paths, err)
	}
}

// TestTheEnvelopeIsACeilingAcrossARunsSteps: steps side by side share
// their session's pool. Each one's own cap is what the pool had when it
// spawned, so the pool is held across them as they spend: when it is
// spent the run pauses at once and every step's backend stops, and no
// step's cap is ever more than nothing on a spent pool.
func TestTheEnvelopeIsACeilingAcrossARunsSteps(t *testing.T) {
	wf := newWFFake()
	e, store, f, _ := wfEngine(t, wf)
	wf.Responder = func(base func(agent.SessionOpts, string) []agent.Event) func(agent.SessionOpts, string) []agent.Event {
		return func(opts agent.SessionOpts, msg string) []agent.Event {
			if !strings.Contains(msg, "You are step p/") {
				return base(opts, msg)
			}
			// spends most of the pool, and holds its turn open
			return []agent.Event{
				{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 60, Model: "m"}},
				{Kind: agent.EventTextDelta, Text: "working"},
			}
		}
	}(wf.Responder)
	ctx := context.Background()
	if err := e.RaiseEnvelope(ctx, f.ID, 100); err != nil {
		t.Fatal(err)
	}
	run := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: "name: two\nsteps:\n  - id: p\n    parallel:\n      join: all\n      steps:\n" +
		"        - { id: a, role: scout, envelope: 20, prompt: look }\n        - { id: b, role: scout, envelope: 20, prompt: look }\n"})
	r := tickRuns(t, e, f.ID, run.N, "paused for money", func(r *domain.Run) bool {
		return r.State == domain.RunPaused && r.OutOfBudget
	})
	if r.Pending == nil || r.Pending.Kind != domain.RunDecideBudget {
		t.Fatalf("the paused run asks %+v", r.Pending)
	}
	steps := stepPaths(t, e, f.ID, run.N)
	waitUntil(t, "every step's backend stopped", func() bool {
		for _, s := range steps {
			if s.Card == "" {
				return false
			}
			if ff := e.Freeform(s.Card); ff != nil && ff.Session().Live() {
				return false
			}
		}
		return true
	})
	for _, s := range steps {
		card, _ := store.GetFeature(ctx, s.Card)
		if b := e.stepBudget(ctx, card); b != 0 {
			t.Errorf("step %s's cap on a spent pool is %.0f, not nothing", s.Path, b)
		}
		if ff := e.Freeform(card.ID); ff == nil {
			t.Errorf("step %s has no session", s.Path)
		} else if _, err := ff.ensureBackend(ctx); err == nil || !strings.Contains(err.Error(), "budget is spent") {
			t.Errorf("step %s's backend started again on a spent pool: %v", s.Path, err)
		}
	}
}

// TestARunsSpendCountsAgainstTheLiveSessionsCap: a session's backend took
// its cap from what the envelope had left when it spawned. A run that
// spent from the same envelope since leaves that cap too high, so the
// session's own turn must still stop when the envelope, runs and all, is
// spent.
func TestARunsSpendCountsAgainstTheLiveSessionsCap(t *testing.T) {
	wf := newWFFake()
	wf.on("one", func(_, _ string, _ int) wfReply { return wfReply{text: "looked", credits: 1990} })
	wf.Responder = func(base func(agent.SessionOpts, string) []agent.Event) func(agent.SessionOpts, string) []agent.Event {
		return func(opts agent.SessionOpts, msg string) []agent.Event {
			if msg != "again" {
				return base(opts, msg)
			}
			return []agent.Event{
				{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 10, Model: "m"}},
				{Kind: agent.EventMessage, Text: "ok"},
				{Kind: agent.EventIdle},
			}
		}
	}(wf.Responder)
	e, _, f, ff := wfEngine(t, wf)
	run := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: "name: one\nsteps:\n  - { id: one, role: scout, prompt: look }\n"})
	tickRuns(t, e, f.ID, run.N, "ended", ended)
	// the run's report reaches the session as a turn of its own (1 credit)
	waitUntil(t, "the report reached the session", func() bool {
		wf.mu.Lock()
		defer wf.mu.Unlock()
		return len(wf.sessionT) >= 2
	})
	waitFreeformIdle(t, ff)
	if ff.Session().isExhausted() {
		t.Fatal("the session is exhausted before the envelope is spent")
	}
	if err := ff.Send(context.Background(), "again"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the session stopped on its spent envelope", ff.Session().isExhausted)
}

// TestAStepStartedIntoAStopOrPauseIsNotBriefed: a step's backend takes a
// while to come up, and the run may be stopped or paused meanwhile. The
// step's brief goes only to a step still live in a running run: stopped,
// it never runs its turn; paused, it is briefed when the run is resumed.
func TestAStepStartedIntoAStopOrPauseIsNotBriefed(t *testing.T) {
	const recipe = "name: one\nsteps:\n  - { id: one, role: scout, prompt: look }\n"
	for _, tc := range []struct {
		name string
		act  func(e *Engine, session domain.FeatureID) error
	}{
		{"stop", func(e *Engine, s domain.FeatureID) error {
			return e.WorkflowStop(context.Background(), s, "changed my mind")
		}},
		{"pause", func(e *Engine, s domain.FeatureID) error { return e.WorkflowPause(context.Background(), s) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wf := newWFFake()
			e, _, f, _ := wfEngine(t, wf)
			ctx := context.Background()
			run := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: recipe})
			// the run moves while the step's backend is coming up
			var once sync.Once
			wf.OnNewSession = func(opts agent.SessionOpts) {
				if opts.FeatureID == string(f.ID) {
					return
				}
				once.Do(func() {
					if err := tc.act(e, f.ID); err != nil {
						t.Error(err)
					}
				})
			}
			tickRuns(t, e, f.ID, run.N, "started the step", func(*domain.Run) bool {
				s, ok := stepPaths(t, e, f.ID, run.N)["one"]
				return ok && s.Card != "" && !e.wfRT().isBusy(stepKey(f.ID, run.N, "one"))
			})
			if tc.name == "stop" {
				r := tickRuns(t, e, f.ID, run.N, "ended", ended)
				if r.State != domain.RunStopped || wf.count("one") != 0 {
					t.Fatalf("ended %s; the step ran %d turns", r.State, wf.count("one"))
				}
				return
			}
			if wf.count("one") != 0 {
				t.Fatalf("a step of a paused run ran its turn")
			}
			if s := stepPaths(t, e, f.ID, run.N)["one"]; s.State != domain.StepRunning || s.Prompt != "" {
				t.Fatalf("the unbriefed step %+v", s)
			}
			if err := e.WorkflowResume(ctx, f.ID); err != nil {
				t.Fatal(err)
			}
			r := tickRuns(t, e, f.ID, run.N, "ended", ended)
			if s := stepPaths(t, e, f.ID, run.N)["one"]; s.State != domain.StepDone || wf.count("one") != 1 {
				t.Fatalf("resumed, the run ended %s, its step %s and briefed %d times", r.State, s.State, wf.count("one"))
			}
		})
	}
}

// TestAMessageToAPausedRunsStepIsRefused: a paused run's steps have no
// backend, and a message would start one under the person's pause.
func TestAMessageToAPausedRunsStepIsRefused(t *testing.T) {
	wf := newWFFake()
	g := newGate(t)
	wf.on("slow", func(string, string, int) wfReply {
		<-g.ch
		return wfReply{text: "done", credits: 1}
	})
	e, _, f, _ := wfEngine(t, wf)
	ctx := context.Background()
	run := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: "name: slow\nsteps:\n  - { id: slow, role: scout, prompt: wait }\n"})
	tickRuns(t, e, f.ID, run.N, "started its step", func(*domain.Run) bool { return wf.count("slow") > 0 })
	if err := e.WorkflowPause(ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	card := stepPaths(t, e, f.ID, run.N)["slow"].Card
	waitUntil(t, "the step's backend stopped", func() bool { return !e.freeformBusy(card) })
	if err := e.StepSend(ctx, card, "how far along?"); err == nil || !strings.Contains(err.Error(), "resume it") {
		t.Fatalf("a message to a paused run's step: %v", err)
	}
	if wf.count("slow#") != 0 || e.freeformBusy(card) {
		t.Fatal("the message started the step's backend under the pause")
	}
}

// TestAnAnswerIsToTheDecisionItWasGivenTo: a run raises one decision after
// another, so an answer names the one it answers. An answer read off a
// decision that has since been settled and replaced — here the proposal's,
// once the loop asks about its max — is refused with what the run asks
// now, and leaves that decision standing.
func TestAnAnswerIsToTheDecisionItWasGivenTo(t *testing.T) {
	wf := newWFFake()
	e, _, f, _ := wfEngine(t, wf)
	ctx := context.Background()
	run, err := e.WorkflowPropose(ctx, f.ID, WorkflowRequest{RecipeYAML: loopRecipe})
	if err != nil {
		t.Fatal(err)
	}
	proposal := run.Pending.ID
	if err := e.WorkflowAnswer(ctx, f.ID, run.N, "", WorkflowAllow); err == nil {
		t.Fatal("an answer naming no decision was taken")
	}
	if err := e.WorkflowAnswer(ctx, f.ID, run.N, proposal, WorkflowAllow); err != nil {
		t.Fatal(err)
	}
	r := tickRuns(t, e, f.ID, run.N, "asked about the max", pendingOf(domain.RunDecideMax))
	// the proposal's "Continue"-shaped answer, given late, is not the max's
	err = e.WorkflowAnswer(ctx, f.ID, run.N, proposal, WorkflowContinue)
	if err == nil || !strings.Contains(err.Error(), "now asks") {
		t.Fatalf("a stale answer: %v", err)
	}
	if again, _ := e.cfg.Store.Run(ctx, f.ID, run.N); again.Pending == nil || again.Pending.ID != r.Pending.ID {
		t.Fatalf("the stale answer moved the decision: %+v", again.Pending)
	}
	if err := e.WorkflowAnswer(ctx, f.ID, run.N, r.Pending.ID, WorkflowStopRun); err != nil {
		t.Fatal(err)
	}
}

// TestASessionIsNotHandedOffUnderItsRun: a hand-off closes the session,
// and a session is not closed under its open run, as it is not under its
// agent mid-turn: the refusal says so, and the card stays open with its
// run. Once the run has ended the hand-off goes through, withdrawing a
// proposal nobody answered.
func TestASessionIsNotHandedOffUnderItsRun(t *testing.T) {
	wf := newWFFake()
	g := newGate(t)
	wf.on("slow", func(string, string, int) wfReply {
		<-g.ch
		return wfReply{text: "done", credits: 1}
	})
	e, store, f, _ := wfEngine(t, wf)
	ctx := context.Background()
	sess, _ := store.GetFeature(ctx, f.ID)
	tree := e.treeOf(ctx, sess)
	if err := os.WriteFile(filepath.Join(tree, "kept.txt"), []byte("kept\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitOut(t, tree, "add", "kept.txt")
	gitOut(t, tree, "commit", "-q", "-m", "keep a file")
	run := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: "name: slow\nsteps:\n  - { id: slow, role: scout, prompt: wait }\n"})
	tickRuns(t, e, f.ID, run.N, "started its step", func(*domain.Run) bool { return wf.count("slow") > 0 })
	if _, err := e.HandOff(ctx, f.ID, "user"); err == nil || !strings.Contains(err.Error(), "workflow run 1") ||
		!strings.Contains(err.Error(), "mid-turn") {
		t.Fatalf("a hand-off under an open run: %v", err)
	}
	if cur, _ := store.GetFeature(ctx, f.ID); cur.Stage == domain.StageDone || cur.HandedOff() {
		t.Fatalf("the refused hand-off closed the session: %+v", cur)
	}
	g.open()
	tickRuns(t, e, f.ID, run.N, "ended", ended)
	later, err := e.WorkflowPropose(ctx, f.ID, WorkflowRequest{Recipe: "investigate"})
	if err != nil || later.State != domain.RunProposed {
		t.Fatalf("a later proposal %+v %v", later, err)
	}
	res, err := e.HandOff(ctx, f.ID, "user")
	if err != nil || res.Status != HandOffClosed {
		t.Fatalf("the hand-off once the run ended: %+v %v", res, err)
	}
	if r, _ := store.Run(ctx, f.ID, later.N); r.State != domain.RunDenied || r.Pending != nil {
		t.Fatalf("the proposal outlived its session: %+v", r)
	}
}

// TestAnAmountReadsWithItsUnit: an amount is credits, bare or spelt
// "300cr" or "300 credits" (the form the headless driver asks for, where a
// bare number picks an option), or dollars as $N.NN.
func TestAnAmountReadsWithItsUnit(t *testing.T) {
	for in, want := range map[string]int{"3": 300, "2.5": 250, "300cr": 300, "300 cr": 300, "300 credits": 300, "$3": 300} {
		if got, err := parseCredits(in); err != nil || got != want {
			t.Errorf("parseCredits(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	if _, err := parseCredits("lots"); err == nil {
		t.Error("an amount with no number was read")
	}
}

// TestAReadingStepHasAShellAndIsFlaggedWhenItChangesFiles: a scout or a
// reviewer is spawned like any step — never ReadOnly, which is a research
// session's allowlist and would keep a reviewer from running the tests —
// even on a backend that could strip its write tools. Its brief tells it
// to change no files. One that changes files anyway is recorded and named
// in the run's report: evidence, never a refusal.
func TestAReadingStepHasAShellAndIsFlaggedWhenItChangesFiles(t *testing.T) {
	wf := newWFFake()
	wf.Caps.ReadOnlyEnforce = true
	var mu sync.Mutex
	readOnly := map[string]bool{}
	wf.Responder = func(base func(agent.SessionOpts, string) []agent.Event) func(agent.SessionOpts, string) []agent.Event {
		return func(opts agent.SessionOpts, msg string) []agent.Event {
			if m := wfStepRe.FindStringSubmatch(msg); m != nil {
				mu.Lock()
				readOnly[m[1]] = opts.ReadOnly
				mu.Unlock()
			}
			return base(opts, msg)
		}
	}(wf.Responder)
	wf.on("look", func(dir, _ string, _ int) wfReply {
		_ = os.WriteFile(filepath.Join(dir, "stray.txt"), []byte("oops\n"), 0o600)
		return wfReply{text: "mapped it", credits: 2}
	})
	wf.on("check", func(string, string, int) wfReply { return wfReply{text: "ran the tests: all pass", credits: 2} })
	e, _, f, _ := wfEngine(t, wf)
	run := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: "name: read\nsteps:\n" +
		"  - { id: look, role: scout, prompt: map it }\n  - { id: check, role: reviewer, prompt: run the tests }\n"})
	r := tickRuns(t, e, f.ID, run.N, "ended", ended)

	mu.Lock()
	for _, id := range []string{"look", "check"} {
		if ro, ran := readOnly[id]; !ran || ro {
			t.Errorf("step %s: ran %v, spawned ReadOnly %v", id, ran, ro)
		}
	}
	mu.Unlock()
	steps := stepPaths(t, e, f.ID, run.N)
	wf.mu.Lock()
	brief := wf.stepT["check"][0]
	wf.mu.Unlock()
	if !strings.Contains(brief, "full shell") || !strings.Contains(brief, "Do not change files") {
		t.Errorf("a reading step's brief: %s", brief)
	}
	if got := steps["look"].Changed; len(got) != 1 || got[0] != "stray.txt" {
		t.Fatalf("the scout's changes = %q", got)
	}
	if got := steps["check"].Changed; got != nil {
		t.Fatalf("a reviewer that changed nothing is flagged: %q", got)
	}
	if steps["look"].State != domain.StepDone || r.State == domain.RunPartial && strings.Contains(r.Reason, "look") {
		t.Fatalf("the flag held the step: %+v, run %s (%s)", steps["look"], r.State, r.Reason)
	}
	if !strings.Contains(r.Report, "Reading steps that changed files") || !strings.Contains(r.Report, "look (scout) changed stray.txt") {
		t.Fatalf("the report does not name the scout's change:\n%s", r.Report)
	}
}

// verdictLoop is a repeat whose until reads a verdict from an artifact
// its last step declares.
const verdictLoop = "name: loop\nsteps:\n  - id: build\n    repeat:\n      max: 2\n      until: { verdict: APPROVED, in: review }\n" +
	"      steps:\n        - { id: implement, role: implementer, prompt: build }\n" +
	"        - { id: verdict, role: reviewer, prompt: judge, artifacts: { review: review.json } }\n" +
	"  - { id: after, role: scribe, prompt: wrap up }\n"

// writesOut is an implementer that makes the spec's check pass.
func writesOut(dir, _ string, _ int) wfReply {
	_ = os.WriteFile(filepath.Join(dir, "out.txt"), []byte("ok\n"), 0o600)
	return wfReply{text: "wrote it", credits: 1}
}

// TestAStepMayWriteItsRunsArtifacts: the run's directory lies outside the
// session's checkout, so a step's backend is handed it to write
// (SessionOpts.WriteDirs), made before the backend spawns; the session's
// own backend is handed nothing. A verdict step that writes review.json
// there ends its loop.
func TestAStepMayWriteItsRunsArtifacts(t *testing.T) {
	wf := newWFFake()
	var mu sync.Mutex
	dirs := map[string][]string{}
	wf.Responder = func(base func(agent.SessionOpts, string) []agent.Event) func(agent.SessionOpts, string) []agent.Event {
		return func(opts agent.SessionOpts, msg string) []agent.Event {
			key := "session"
			if m := wfStepRe.FindStringSubmatch(msg); m != nil {
				key = m[1]
			}
			mu.Lock()
			dirs[key] = opts.WriteDirs
			mu.Unlock()
			return base(opts, msg)
		}
	}(wf.Responder)
	e, _, f, _ := wfEngine(t, wf)
	wf.on("implement", writesOut)
	wf.on("verdict", func(string, string, int) wfReply {
		mu.Lock()
		d := append([]string(nil), dirs["build/1/verdict"]...)
		mu.Unlock()
		if len(d) == 1 {
			_ = os.WriteFile(filepath.Join(d[0], "review.json"), []byte(`{"verdict":"APPROVED"}`), 0o600)
		}
		return wfReply{text: "approved", credits: 1}
	})
	run := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: verdictLoop})
	r := tickRuns(t, e, f.ID, run.N, "ended", ended)
	mu.Lock()
	defer mu.Unlock()
	if d := dirs["build/1/verdict"]; len(d) != 1 || d[0] != e.RunDir(f.ID, run.N) {
		t.Fatalf("the verdict step was handed %q to write, want its run's directory %s", d, e.RunDir(f.ID, run.N))
	}
	if d := dirs["session"]; d != nil {
		t.Fatalf("the session's own backend was handed %q", d)
	}
	if r.State != domain.RunDone || !r.Until["build/1"].Met || wf.count("implement") != 1 {
		t.Fatalf("ended %s after %d iterations, until %+v", r.State, wf.count("implement"), r.Until)
	}
}

// TestAMissingArtifactIsCapturedFromTheOutput: a step that declared an
// artifact and ended without writing it has it written from its output —
// the last JSON object for a .json — so the loop is judged on it, and the
// run's notes say gummi wrote it.
func TestAMissingArtifactIsCapturedFromTheOutput(t *testing.T) {
	wf := newWFFake()
	wf.on("implement", writesOut)
	wf.on("verdict", func(string, string, int) wfReply {
		return wfReply{text: "Merged the reviews:\n```json\n{\"verdict\": \"APPROVED\", \"findings\": []}\n```", credits: 1}
	})
	e, _, f, ff := wfEngine(t, wf)
	run := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: verdictLoop})
	r := tickRuns(t, e, f.ID, run.N, "ended", ended)
	if r.State != domain.RunDone || !r.Until["build/1"].Met || wf.count("implement") != 1 {
		t.Fatalf("ended %s after %d iterations, until %+v", r.State, wf.count("implement"), r.Until)
	}
	raw, err := os.ReadFile(filepath.Join(e.RunDir(f.ID, run.N), "review.json"))
	if err != nil || string(raw) != "{\"verdict\": \"APPROVED\", \"findings\": []}\n" {
		t.Fatalf("captured review.json = %q, %v", raw, err)
	}
	var said bool
	for _, l := range ff.Snapshot().Activity {
		said = said || strings.Contains(l, "gummi wrote it from the step's output")
	}
	if !said {
		t.Errorf("the run's notes do not say the artifact was captured: %q", ff.Snapshot().Activity)
	}
}

// TestALoopThatCannotJudgeAsksThePerson: when the artifact a repeat's
// verdict is read from was never written — nor held in the step's output
// — the iteration is not counted as not met. The run waits on the person:
// one more iteration, continue, or stop.
func TestALoopThatCannotJudgeAsksThePerson(t *testing.T) {
	wf := newWFFake()
	wf.on("verdict", func(string, string, int) wfReply { return wfReply{text: "Looks good to me.", credits: 1} })
	e, _, f, _ := wfEngine(t, wf)
	ctx := context.Background()
	run := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: verdictLoop})
	r := tickRuns(t, e, f.ID, run.N, "asked", pendingOf(domain.RunDecideUnjudged))
	if !strings.Contains(r.Pending.Question, "the loop can't judge: review (review.json) was never written") || r.Pending.Step != "build/1" ||
		!reflect.DeepEqual(r.Pending.Options, []string{WorkflowOneMore, WorkflowContinue, WorkflowStopRun}) {
		t.Fatalf("decision %+v", r.Pending)
	}
	if u := r.Until["build/1"]; u.Met || !u.Unjudged {
		t.Fatalf("until %+v", u)
	}
	// it waits: ticking starts nothing
	for range 5 {
		if _, err := e.WorkflowTick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if _, ran := stepPaths(t, e, f.ID, run.N)["build/2/implement"]; ran || wf.count("after") != 0 {
		t.Fatal("the run went on while it could not judge its loop")
	}
	if err := e.WorkflowAnswer(ctx, f.ID, run.N, pendingID(t, e, f.ID, run.N), WorkflowContinue); err != nil {
		t.Fatal(err)
	}
	r = tickRuns(t, e, f.ID, run.N, "ended", ended)
	if wf.count("implement") != 1 || wf.count("after") != 1 {
		t.Fatalf("ended %s: implement ran %d, after %d", r.State, wf.count("implement"), wf.count("after"))
	}
}

// emptyCheckSpec is the test spec with a done-when check that passes and
// runs no tests: a `go test -run` whose pattern matches nothing.
var emptyCheckSpec = strings.Replace(wfTestSpec, "check: test -f out.txt",
	`check: "printf 'testing: warning: no tests to run\\nok  x 0.01s [no tests to run]\\n' # go test ./x -run TestNope"`, 1)

// TestACheckThatRanNoTestsIsUnjudged: a done-when check that passed while
// running no tests proves nothing. At a run's end its item is unjudged
// (–), saying so, never ✓; the run ends partial and its spec is not met.
func TestACheckThatRanNoTestsIsUnjudged(t *testing.T) {
	wf := newWFFake()
	e, _, f, _ := wfEngine(t, wf)
	ctx := context.Background()
	if _, err := e.WriteSessionSpec(ctx, f, 1, emptyCheckSpec, "user"); err != nil {
		t.Fatal(err)
	}
	run := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: "name: one\nsteps:\n  - { id: one, role: implementer, prompt: build }\n"})
	r := tickRuns(t, e, f.ID, run.N, "ended", ended)
	if len(r.Results) == 0 || r.Results[0].Item != "DW-1" || r.Results[0].Verdict != domain.ItemUnjudged ||
		!r.Results[0].Checked || r.Results[0].Detail != "the check ran no tests" {
		t.Fatalf("DW-1 = %+v", r.Results)
	}
	if r.State != domain.RunPartial || domain.ResultsMet(r.Results) || r.Reason != "a done-when check ran no tests" {
		t.Fatalf("ended %s (%s), results %+v", r.State, r.Reason, r.Results)
	}
	if !strings.Contains(r.Report, "– DW-1 out.txt exists (check): ran no tests before the run, still runs no tests: the check ran no tests") {
		t.Fatalf("the report:\n%s", r.Report)
	}
	if specs, _ := e.cfg.Store.SessionSpecs(ctx, f.ID); specs[0].State == domain.SpecMet {
		t.Fatal("a check that ran no tests met the spec")
	}
}

// TestAnUntilChecksThatRanNoTestsAsks: a repeat's `until: checks` whose
// check ran no tests cannot judge the loop: the run asks the person
// rather than iterating to its max on a check that proves nothing.
func TestAnUntilChecksThatRanNoTestsAsks(t *testing.T) {
	wf := newWFFake()
	e, _, f, _ := wfEngine(t, wf)
	ctx := context.Background()
	if _, err := e.WriteSessionSpec(ctx, f, 1, emptyCheckSpec, "user"); err != nil {
		t.Fatal(err)
	}
	run := proposeAndAllow(t, e, f.ID, WorkflowRequest{Recipe: "fix-until-green"})
	r := tickRuns(t, e, f.ID, run.N, "asked", pendingOf(domain.RunDecideUnjudged))
	if !strings.Contains(r.Pending.Question, "the check of DW-1 ran no tests") || wf.count("implement") != 1 {
		t.Fatalf("after %d iterations, decision %+v", wf.count("implement"), r.Pending)
	}
	if err := e.WorkflowAnswer(ctx, f.ID, run.N, pendingID(t, e, f.ID, run.N), WorkflowStopRun); err != nil {
		t.Fatal(err)
	}
	if r = tickRuns(t, e, f.ID, run.N, "ended", ended); r.State != domain.RunStopped {
		t.Fatalf("ended %s", r.State)
	}
}

// A tick that starts steps tells the faces at once (DESIGN §25.9): both
// branches of a parallel read as running while their backends are still
// coming up, not only once each has.
func TestATickThatStartsStepsTellsTheFacesAtOnce(t *testing.T) {
	wf := newWFFake()
	g := newGate(t)
	var mu sync.Mutex
	first := ""
	wf.OnNewSession = func(opts agent.SessionOpts) {
		mu.Lock()
		if first == "" {
			first = opts.FeatureID
		}
		step := opts.FeatureID != first
		mu.Unlock()
		if step {
			<-g.ch // a step's backend is slow to come up
		}
	}
	e, store, f, _ := wfEngine(t, wf)
	t.Cleanup(g.open) // before the engine closes, which waits on the held backends
	ctx := context.Background()
	run := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: parallelRecipe("allSettled")})
	// what the proposal and the allowance said is already heard
	for quiet := false; !quiet; {
		select {
		case <-e.Events():
		case <-time.After(200 * time.Millisecond):
			quiet = true
		}
	}
	if _, err := e.WorkflowTick(ctx); err != nil {
		t.Fatal(err)
	}
	steps, _ := store.RunSteps(ctx, f.ID, run.N)
	if len(steps) != 2 || steps[0].State != domain.StepRunning || steps[1].State != domain.StepRunning {
		t.Fatalf("the tick did not start both branches: %+v", steps)
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-e.Events():
			if ev.Kind == EventWorkflow && ev.Feature == f.ID {
				g.open()
				return
			}
		case <-deadline:
			t.Fatal("the faces were not told the run's steps started until their backends came up")
		}
	}
}

// TestAProposalRefusesDoneWhenItCannotRead: a spec whose Done when holds
// items as a plain list, outside a gummi-done-when block, is refused at
// the proposal rather than run against no checks; written as a block, the
// same spec proposes.
func TestAProposalRefusesDoneWhenItCannotRead(t *testing.T) {
	e, _, f, _ := wfEngine(t, newWFFake())
	ctx := context.Background()
	prose, _, err := spec.ReplaceSection(wfTestSpec, spec.SessionSectionDoneWhen, "- DW-1: made.txt exists. check: `test -f made.txt`\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.WriteSessionSpec(ctx, f, 1, prose, "user"); err != nil {
		t.Fatal(err)
	}
	recipe := "name: one\nsteps:\n  - { id: one, role: scout, prompt: look }\n"
	if _, err := e.WorkflowPropose(ctx, f.ID, WorkflowRequest{RecipeYAML: recipe}); err == nil || !strings.Contains(err.Error(), "gummi-done-when") {
		t.Fatalf("a plain-list Done when proposed: %v", err)
	}
	if runs, _ := e.cfg.Store.SessionRuns(ctx, f.ID); len(runs) != 0 {
		t.Fatalf("the refusal left runs %+v", runs)
	}
	if _, err := e.WriteSessionSpec(ctx, f, 1, wfTestSpec, "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.WorkflowPropose(ctx, f.ID, WorkflowRequest{RecipeYAML: recipe}); err != nil {
		t.Fatalf("the block form was refused: %v", err)
	}
}
