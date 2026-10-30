package workflowpolicy

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/workflowrecipe"
)

func recipe(t *testing.T, name string) domain.Recipe {
	t.Helper()
	f, err := workflowrecipe.Load(name, "")
	if err != nil {
		t.Fatal(err)
	}
	return f.Recipe
}

func parse(t *testing.T, y string) domain.Recipe {
	t.Helper()
	r, err := workflowrecipe.Parse([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	if problems, _ := workflowrecipe.Check(r); len(problems) > 0 {
		t.Fatalf("test recipe fails Check: %q", problems)
	}
	return r
}

// steps builds instances from "path=state" pairs; spend and estimates are
// left at zero.
func steps(pairs ...string) []Step {
	var out []Step
	for _, p := range pairs {
		path, state, _ := strings.Cut(p, "=")
		out = append(out, Step{Path: path, State: domain.StepState(state)})
	}
	return out
}

// render is the actions as strings, for comparing.
func render(as []Action) []string {
	out := []string{}
	for _, a := range as {
		s := a.Kind.String()
		if a.Path != "" {
			s += " " + a.Path
		}
		if a.Kind == End {
			s += " " + string(a.State)
		}
		out = append(out, s)
	}
	return out
}

func TestDecideConductsDeliver(t *testing.T) {
	deliver := recipe(t, "deliver")
	approved := map[string]UntilResult{"build/1": {Met: false}, "build/2": {Met: true}}
	for _, tc := range []struct {
		name  string
		steps []Step
		until map[string]UntilResult
		want  []string
	}{
		{"fresh run starts design", nil, nil, []string{"start design"}},
		{"a waiting row starts", steps("design=waiting"), nil, []string{"start design"}},
		{"design running", steps("design=running"), nil, []string{}},
		{"design asking", steps("design=asking"), nil, []string{}},
		{"design done starts the first iteration", steps("design=done"), nil, []string{"start build/1/implement"}},
		{
			"implement done starts both reviews",
			steps("design=done", "build/1/implement=done"), nil,
			[]string{"start build/1/reviews/review-a", "start build/1/reviews/review-b"},
		},
		{
			"one review done, one running: wait",
			steps("design=done", "build/1/implement=done", "build/1/reviews/review-a=done", "build/1/reviews/review-b=running"), nil,
			[]string{},
		},
		{
			"allSettled goes on past one failed review",
			steps("design=done", "build/1/implement=done", "build/1/reviews/review-a=failed", "build/1/reviews/review-b=done"), nil,
			[]string{"start build/1/verdict"},
		},
		{
			"allSettled fails when every review failed",
			steps("design=done", "build/1/implement=done", "build/1/reviews/review-a=failed", "build/1/reviews/review-b=stopped"), nil,
			[]string{"end partial"},
		},
		{
			"an ended iteration needs its until",
			steps("design=done", "build/1/implement=done", "build/1/reviews/review-a=done", "build/1/reviews/review-b=done", "build/1/verdict=done"), nil,
			[]string{"need-until build/1"},
		},
		{
			"an unmet until iterates",
			steps("design=done", "build/1/implement=done", "build/1/reviews/review-a=done", "build/1/reviews/review-b=done", "build/1/verdict=done"), approved,
			[]string{"start build/2/implement"},
		},
		{
			"the second iteration's reviews",
			steps("design=done", "build/1/implement=done", "build/1/reviews/review-a=done", "build/1/reviews/review-b=done", "build/1/verdict=done", "build/2/implement=done"), approved,
			[]string{"start build/2/reviews/review-a", "start build/2/reviews/review-b"},
		},
		{
			"a met until ends the loop",
			steps("design=done", "build/1/implement=done", "build/1/reviews/review-a=done", "build/1/reviews/review-b=done", "build/1/verdict=done",
				"build/2/implement=done", "build/2/reviews/review-a=done", "build/2/reviews/review-b=done", "build/2/verdict=done"), approved,
			[]string{"start validate"},
		},
		{
			"validate done ends the run",
			steps("design=done", "build/1/implement=done", "build/1/reviews/review-a=done", "build/1/reviews/review-b=done", "build/1/verdict=done",
				"build/2/implement=done", "build/2/reviews/review-a=done", "build/2/reviews/review-b=done", "build/2/verdict=done", "validate=done"), approved,
			[]string{"end done"},
		},
		{"a failed step ends the run partial", steps("design=failed"), nil, []string{"end partial"}},
		{"a stopped step ends the run partial", steps("design=stopped"), nil, []string{"end partial"}},
		{
			"a failed implement ends the run partial",
			steps("design=done", "build/1/implement=failed"), nil,
			[]string{"end partial"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := render(Decide(Snapshot{State: domain.RunRunning, Recipe: deliver, Steps: tc.steps, Until: tc.until}))
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("actions = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDecideAppliesOnMax(t *testing.T) {
	deliver := recipe(t, "deliver")
	var all []string
	unmet := map[string]UntilResult{}
	all = append(all, "design=done")
	for _, k := range []string{"1", "2", "3"} {
		all = append(all, "build/"+k+"/implement=done", "build/"+k+"/reviews/review-a=done", "build/"+k+"/reviews/review-b=done", "build/"+k+"/verdict=done")
		unmet["build/"+k] = UntilResult{}
	}
	snap := func(onMax domain.OnMax, answers map[string]OnMaxAnswer, extra ...string) Snapshot {
		r := deliver
		rp := *r.Steps[1].Repeat
		rp.OnMax = onMax
		r.Steps = append([]domain.Entry{}, r.Steps...)
		r.Steps[1] = domain.Entry{Repeat: &rp}
		return Snapshot{State: domain.RunRunning, Recipe: r, Steps: steps(append(append([]string{}, all...), extra...)...), Until: unmet, OnMax: answers}
	}
	for _, tc := range []struct {
		name string
		in   Snapshot
		want []string
	}{
		{"pause asks the person", snap(domain.OnMaxPause, nil), []string{"ask-on-max build/3"}},
		{"an empty onMax pauses", snap("", nil), []string{"ask-on-max build/3"}},
		{"abort ends partial", snap(domain.OnMaxAbort, nil), []string{"end partial"}},
		{"continue goes on", snap(domain.OnMaxContinue, nil), []string{"start validate"}},
		{"answered continue goes on", snap(domain.OnMaxPause, map[string]OnMaxAnswer{"build/3": OnMaxGoOn}), []string{"start validate"}},
		{"answered stop ends stopped", snap(domain.OnMaxPause, map[string]OnMaxAnswer{"build/3": OnMaxStop}), []string{"end stopped"}},
		{"answered more runs a fourth", snap(domain.OnMaxPause, map[string]OnMaxAnswer{"build/3": OnMaxMore}), []string{"start build/4/implement"}},
		{
			"the fourth ending unmet asks again",
			snap(domain.OnMaxPause, map[string]OnMaxAnswer{"build/3": OnMaxMore},
				"build/4/implement=done", "build/4/reviews/review-a=done", "build/4/reviews/review-b=done", "build/4/verdict=done"),
			[]string{"need-until build/4"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "the fourth ending unmet asks again" {
				got := render(Decide(tc.in))
				if !reflect.DeepEqual(got, tc.want) {
					t.Fatalf("actions = %q, want %q", got, tc.want)
				}
				tc.in.Until = map[string]UntilResult{"build/1": {}, "build/2": {}, "build/3": {}, "build/4": {}}
				tc.want = []string{"ask-on-max build/4"}
			}
			got := render(Decide(tc.in))
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("actions = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDecideSettlesEachJoin(t *testing.T) {
	r := func(join string) domain.Recipe {
		return parse(t, `
steps:
  - id: p
    parallel:
      join: `+join+`
      steps:
        - {id: a, role: reviewer, prompt: x}
        - {id: b, role: reviewer, prompt: x}
        - {id: c, role: reviewer, prompt: x}
  - {id: after, role: scribe, prompt: x}
`)
	}
	for _, tc := range []struct {
		name, join string
		steps      []Step
		want       []string
	}{
		{"all starts every branch", "all", nil, []string{"start p/a", "start p/b", "start p/c"}},
		{"all starts only the missing", "all", steps("p/a=running"), []string{"start p/b", "start p/c"}},
		{"all waits", "all", steps("p/a=done", "p/b=running", "p/c=asking"), []string{}},
		{"all goes on", "all", steps("p/a=done", "p/b=done", "p/c=done"), []string{"start after"}},
		{"all stops the rest on a failure", "all", steps("p/a=failed", "p/b=running", "p/c=asking"), []string{"interrupt p/b", "interrupt p/c"}},
		{"all fails once the rest stopped", "all", steps("p/a=failed", "p/b=stopped", "p/c=done"), []string{"end partial"}},
		{"allSettled waits on a live branch", "allSettled", steps("p/a=failed", "p/b=running", "p/c=done"), []string{}},
		{"allSettled goes on with one success", "allSettled", steps("p/a=failed", "p/b=failed", "p/c=done"), []string{"start after"}},
		{"allSettled fails when all fail", "allSettled", steps("p/a=failed", "p/b=failed", "p/c=stopped"), []string{"end partial"}},
		{"any waits", "any", steps("p/a=failed", "p/b=running", "p/c=running"), []string{}},
		{"any stops the rest on a success", "any", steps("p/a=done", "p/b=running", "p/c=asking"), []string{"interrupt p/b", "interrupt p/c"}},
		{"any goes on once the rest stopped", "any", steps("p/a=done", "p/b=stopped", "p/c=failed"), []string{"start after"}},
		{"any fails when none succeeds", "any", steps("p/a=failed", "p/b=failed", "p/c=stopped"), []string{"end partial"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := render(Decide(Snapshot{State: domain.RunRunning, Recipe: r(tc.join), Steps: tc.steps}))
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("actions = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDecideRunsNestedRepeats(t *testing.T) {
	r := parse(t, `
steps:
  - id: outer
    repeat:
      max: 2
      until: checks
      onMax: abort
      steps:
        - id: inner
          repeat:
            max: 2
            until: {contains: OK}
            onMax: continue
            steps:
              - {id: try, role: implementer, prompt: x}
        - {id: test, role: tester, prompt: x}
`)
	for _, tc := range []struct {
		name  string
		steps []Step
		until map[string]UntilResult
		want  []string
	}{
		{"starts the innermost", nil, nil, []string{"start outer/1/inner/1/try"}},
		{"inner needs its until", steps("outer/1/inner/1/try=done"), nil, []string{"need-until outer/1/inner/1"}},
		{"inner iterates", steps("outer/1/inner/1/try=done"), map[string]UntilResult{"outer/1/inner/1": {}}, []string{"start outer/1/inner/2/try"}},
		{
			"inner continues at its max",
			steps("outer/1/inner/1/try=done", "outer/1/inner/2/try=done"),
			map[string]UntilResult{"outer/1/inner/1": {}, "outer/1/inner/2": {}},
			[]string{"start outer/1/test"},
		},
		{
			"outer needs its until",
			steps("outer/1/inner/1/try=done", "outer/1/test=done"),
			map[string]UntilResult{"outer/1/inner/1": {Met: true}},
			[]string{"need-until outer/1"},
		},
		{
			"outer iterates, and the inner loop starts over",
			steps("outer/1/inner/1/try=done", "outer/1/test=done"),
			map[string]UntilResult{"outer/1/inner/1": {Met: true}, "outer/1": {}},
			[]string{"start outer/2/inner/1/try"},
		},
		{
			"outer aborts at its max",
			steps("outer/1/inner/1/try=done", "outer/1/test=done", "outer/2/inner/1/try=done", "outer/2/test=done"),
			map[string]UntilResult{"outer/1/inner/1": {Met: true}, "outer/1": {}, "outer/2/inner/1": {Met: true}, "outer/2": {}},
			[]string{"end partial"},
		},
		{
			"outer met ends the run",
			steps("outer/1/inner/1/try=done", "outer/1/test=done"),
			map[string]UntilResult{"outer/1/inner/1": {Met: true}, "outer/1": {Met: true}},
			[]string{"end done"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := render(Decide(Snapshot{State: domain.RunRunning, Recipe: r, Steps: tc.steps, Until: tc.until}))
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("actions = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDecideConductsOnlyARunningRun(t *testing.T) {
	deliver := recipe(t, "deliver")
	for _, s := range []domain.RunState{domain.RunProposed, domain.RunQueued, domain.RunPaused, domain.RunDone, domain.RunPartial, domain.RunStopped, domain.RunDenied} {
		if got := Decide(Snapshot{State: s, Recipe: deliver}); len(got) != 0 {
			t.Errorf("%s: %v", s, got)
		}
	}
}

func TestDecideWindsDownWhenStopped(t *testing.T) {
	deliver := recipe(t, "deliver")
	for _, tc := range []struct {
		name  string
		state domain.RunState
		steps []Step
		want  []string
	}{
		{
			"interrupts the live steps", domain.RunRunning, steps("design=done", "build/1/implement=done", "build/1/reviews/review-a=running", "build/1/reviews/review-b=asking"),
			[]string{"interrupt build/1/reviews/review-a", "interrupt build/1/reviews/review-b"},
		},
		{"ends once none is live", domain.RunRunning, steps("design=done", "build/1/implement=stopped"), []string{"end stopped"}},
		{"ends before anything started", domain.RunRunning, nil, []string{"end stopped"}},
		{"winds down a paused run", domain.RunPaused, steps("design=running"), []string{"interrupt design"}},
		{"leaves a proposed run to the engine", domain.RunProposed, nil, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := render(Decide(Snapshot{State: tc.state, Recipe: deliver, Steps: tc.steps, Stopping: true, Bounded: true, Left: 0}))
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("actions = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDecidePausesOnASpentPool(t *testing.T) {
	deliver := recipe(t, "deliver")
	got := Decide(Snapshot{
		State: domain.RunRunning, Recipe: deliver, Bounded: true, Left: 0,
		Steps: []Step{{Path: "design", State: domain.StepDone, Spent: 60}, {Path: "build/1/implement", State: domain.StepRunning, Spent: 40}},
	})
	// the reason names the run's spend in dollars, as everything beside it does
	if len(got) != 1 || got[0].Kind != Pause || !strings.Contains(got[0].Reason, "spent $1.00") {
		t.Fatalf("actions = %v", got)
	}
	// an unbounded pool never pauses
	got = Decide(Snapshot{State: domain.RunRunning, Recipe: deliver, Bounded: false, Left: 0})
	if !reflect.DeepEqual(render(got), []string{"start design"}) {
		t.Fatalf("unbounded = %v", got)
	}
}

// TestASpentPoolEndsARunWithNothingLeft: a run whose every step has
// settled ends on a spent pool, rather than pausing for a top-up its end
// checks and report would never spend; one with a step still to start
// pauses.
func TestASpentPoolEndsARunWithNothingLeft(t *testing.T) {
	r := parse(t, "name: two\nsteps:\n  - {id: a, role: implementer, prompt: x}\n  - {id: b, role: reviewer, prompt: x}\n")
	done := Decide(Snapshot{State: domain.RunRunning, Recipe: r, Bounded: true, Left: -3, Steps: steps("a=done", "b=done")})
	if !reflect.DeepEqual(render(done), []string{"end done"}) {
		t.Fatalf("all settled on a spent pool = %v", render(done))
	}
	failed := Decide(Snapshot{State: domain.RunRunning, Recipe: r, Bounded: true, Left: 0, Steps: steps("a=failed")})
	if !reflect.DeepEqual(render(failed), []string{"end partial"}) {
		t.Fatalf("a failed step on a spent pool = %v", render(failed))
	}
	more := Decide(Snapshot{State: domain.RunRunning, Recipe: r, Bounded: true, Left: 0, Steps: steps("a=done")})
	if !reflect.DeepEqual(render(more), []string{"pause"}) {
		t.Fatalf("a step left to start on a spent pool = %v", render(more))
	}
}

// TestASpentPoolHoldsBackNothingFree: a spent pool pauses what would
// spend, never what costs nothing. An `any` join that has its success
// interrupts the branch it no longer needs rather than leaving it to spend
// the top-up, and a loop's until is judged rather than the person being
// asked for money to learn whether the run is done.
func TestASpentPoolHoldsBackNothingFree(t *testing.T) {
	anyJoin := parse(t, `name: any
steps:
  - id: race
    parallel:
      join: any
      steps:
        - {id: a, role: implementer, prompt: x}
        - {id: b, role: implementer, prompt: x}
  - {id: after, role: reviewer, prompt: x}
`)
	got := Decide(Snapshot{State: domain.RunRunning, Recipe: anyJoin, Bounded: true, Left: 0, Steps: steps("race/a=done", "race/b=running")})
	if !reflect.DeepEqual(render(got), []string{"interrupt race/b"}) {
		t.Fatalf("a settled any join on a spent pool = %v", render(got))
	}
	// with the losing branch stopped, the next step would spend: pause
	got = Decide(Snapshot{State: domain.RunRunning, Recipe: anyJoin, Bounded: true, Left: 0, Steps: steps("race/a=done", "race/b=stopped")})
	if !reflect.DeepEqual(render(got), []string{"pause"}) {
		t.Fatalf("a step left to start on a spent pool = %v", render(got))
	}

	loop := parse(t, `name: loop
steps:
  - id: fix
    repeat:
      max: 1
      until: checks
      steps:
        - {id: work, role: implementer, prompt: x}
`)
	got = Decide(Snapshot{State: domain.RunRunning, Recipe: loop, Bounded: true, Left: 0, Steps: steps("fix/1/work=done")})
	if !reflect.DeepEqual(render(got), []string{"need-until fix/1"}) {
		t.Fatalf("an iteration ended on a spent pool = %v", render(got))
	}
	met := Decide(Snapshot{
		State: domain.RunRunning, Recipe: loop, Bounded: true, Left: 0, Steps: steps("fix/1/work=done"),
		Until: map[string]UntilResult{"fix/1": {Met: true}},
	})
	if !reflect.DeepEqual(render(met), []string{"end done"}) {
		t.Fatalf("a met loop on a spent pool = %v", render(met))
	}
	unmet := Decide(Snapshot{
		State: domain.RunRunning, Recipe: loop, Bounded: true, Left: 0, Steps: steps("fix/1/work=done"),
		Until: map[string]UntilResult{"fix/1": {}},
	})
	if !reflect.DeepEqual(render(unmet), []string{"ask-on-max fix/1"}) {
		t.Fatalf("a loop at its max on a spent pool = %v", render(unmet))
	}
}

// TestARunEstimatesNothing: a run binds to the session's budget alone. A
// step that has spent any amount, however far past what anyone would have
// guessed, is neither flagged nor warned about while the pool has money
// left: the run goes on until the pool is spent, and then pauses.
func TestARunEstimatesNothing(t *testing.T) {
	deliver := recipe(t, "deliver")
	for _, left := range []float64{0.5, 50, 5000} {
		in := Snapshot{
			State: domain.RunRunning, Recipe: deliver, Bounded: true, Left: left,
			Steps: []Step{{Path: "design", State: domain.StepRunning, Spent: 100000}},
		}
		if got := Decide(in); len(got) != 0 {
			t.Fatalf("with %v left, a live step spending freely draws %v", left, got)
		}
	}
	in := Snapshot{
		State: domain.RunRunning, Recipe: deliver, Bounded: true, Left: 0,
		Steps: []Step{{Path: "design", State: domain.StepRunning, Spent: 30}},
	}
	if got := render(Decide(in)); !reflect.DeepEqual(got, []string{"pause"}) {
		t.Fatalf("a spent pool = %q, want a pause alone", got)
	}
	if got := Spent(Snapshot{Steps: []Step{{Spent: 1.5}, {Spent: 2}}}); got != 3.5 {
		t.Errorf("Spent = %v", got)
	}
}

func TestActionString(t *testing.T) {
	a := Action{Kind: End, State: domain.RunPartial, Reason: "step x failed"}
	if got := a.String(); got != "end partial: step x failed" {
		t.Errorf("String = %q", got)
	}
	if got := (Action{Kind: Pause}).String(); got != "pause" {
		t.Errorf("String = %q", got)
	}
}

// TestAnUnjudgedUntilGoesToThePerson: a repeat whose condition could not
// be judged — its verdict's artifact was never written — is never counted
// as not met. The run asks the person, and their answer decides: one more
// iteration (which does not count against the max), continue, or stop.
func TestAnUnjudgedUntilGoesToThePerson(t *testing.T) {
	deliver := recipe(t, "deliver")
	iteration := func(n int) []Step {
		p := fmt.Sprintf("build/%d/", n)
		return []Step{
			{Path: p + "implement", State: domain.StepDone},
			{Path: p + "reviews/review-a", State: domain.StepDone},
			{Path: p + "reviews/review-b", State: domain.StepDone},
			{Path: p + "verdict", State: domain.StepDone},
		}
	}
	steps := append([]Step{{Path: "design", State: domain.StepDone}}, iteration(1)...)
	never := UntilResult{Unjudged: true, Detail: "the loop can't judge: review (review.json) was never written"}
	in := Snapshot{State: domain.RunRunning, Recipe: deliver, Steps: steps, Until: map[string]UntilResult{"build/1": never}}

	got := Decide(in)
	if !reflect.DeepEqual(render(got), []string{"ask-unjudged build/1"}) || got[0].Reason != never.Detail {
		t.Fatalf("an unjudged iteration = %v, want the person asked", got)
	}
	for answer, want := range map[OnMaxAnswer][]string{
		OnMaxMore: {"start build/2/implement"},
		OnMaxGoOn: {"start validate"},
		OnMaxStop: {"end stopped"},
	} {
		in.OnMax = map[string]OnMaxAnswer{"build/1": answer}
		if got := render(Decide(in)); !reflect.DeepEqual(got, want) {
			t.Errorf("answered %s: %q, want %q", answer, got, want)
		}
	}

	// at the max, one more still runs: the unjudged iteration is not
	// counted against it, and onMax is not what is asked
	steps = append(append(steps, iteration(2)...), iteration(3)...)
	in = Snapshot{State: domain.RunRunning, Recipe: deliver, Steps: steps, Until: map[string]UntilResult{
		"build/1": {Detail: "CHANGES"}, "build/2": {Detail: "CHANGES"}, "build/3": never,
	}}
	if got := render(Decide(in)); !reflect.DeepEqual(got, []string{"ask-unjudged build/3"}) {
		t.Fatalf("unjudged at the max = %q", got)
	}
	in.OnMax = map[string]OnMaxAnswer{"build/3": OnMaxMore}
	if got := render(Decide(in)); !reflect.DeepEqual(got, []string{"start build/4/implement"}) {
		t.Fatalf("one more at the max = %q", got)
	}
}
