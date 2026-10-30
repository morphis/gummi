package workflowpolicy

// Property tests of Decide, driven by hegel (https://hegel.dev). Each draws
// a recipe Check accepts — steps, parallels under every join, nested
// repeats under every onMax — and conducts it tick by tick against a
// simulated engine that does with each action what Engine.WorkflowTick
// does: Start mints a running instance, Interrupt stops one, NeedUntil is
// judged met, not met or unjudged, a question waits for the person's
// answer, Pause parks the run until a top-up, End ends it. Between ticks
// the world moves: a live step finishes, fails or asks, money is spent, a
// question is answered, the pool is topped up, the run is stopped. Each
// test states one rule of the package doc (or decision 28's three) over
// every tick of every such run.

import (
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"hegel.dev/go/hegel"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/workflowrecipe"
)

const (
	propCases  = 2000
	propEvents = 60  // world events before the run is drained
	propTicks  = 400 // ticks a drained run may take to end
)

func props(t *testing.T, check func(ht *hegel.T, s *sim, before Snapshot, acts []Action)) {
	t.Helper()
	hegel.Test(t, func(ht *hegel.T) {
		newSim(ht).drive(func(before Snapshot, acts []Action, s *sim) {
			check(ht, s, before, acts)
		})
	}, hegel.WithTestCases(propCases), hegel.WithDatabase(""))
}

// recipeGen draws a recipe Check accepts. Ids are numbered, so a run past
// nine of them holds ids that are prefixes of one another (s1, s10).
type recipeGen struct {
	tc hegel.TestCase
	n  int
}

func (g *recipeGen) id(prefix string) string {
	g.n++
	return prefix + strconv.Itoa(g.n)
}

func (g *recipeGen) step() domain.Step {
	return domain.Step{ID: g.id("s"), Role: domain.RoleImplementer, Prompt: "work"}
}

func (g *recipeGen) entries(depth int) []domain.Entry {
	var out []domain.Entry
	n := hegel.Draw(g.tc, hegel.Integers(1, 3))
	for range n {
		kinds := 3
		if depth == 0 {
			kinds = 2 // a recipe's nesting is bounded here, not in what is drawn per entry
		}
		kind := hegel.Draw(g.tc, hegel.Integers(0, kinds-1))
		switch kind {
		case 0:
			s := g.step()
			out = append(out, domain.Entry{Step: &s})
		case 1:
			join := hegel.Draw(g.tc, hegel.SampledFrom([]domain.Join{domain.JoinAll, domain.JoinAllSettled, domain.JoinAny}))
			branches := hegel.Draw(g.tc, hegel.Integers(1, 3))
			p := domain.Parallel{ID: g.id("p"), Join: join}
			for range branches {
				p.Steps = append(p.Steps, g.step())
			}
			out = append(out, domain.Entry{Parallel: &p})
		case 2:
			maxIter := hegel.Draw(g.tc, hegel.Integers(1, 3))
			onMax := hegel.Draw(g.tc, hegel.SampledFrom([]domain.OnMax{"", domain.OnMaxAbort, domain.OnMaxContinue, domain.OnMaxPause}))
			r := domain.Repeat{ID: g.id("r"), Max: maxIter, OnMax: onMax, Until: domain.Until{Checks: true}}
			r.Steps = g.entries(depth - 1)
			out = append(out, domain.Entry{Repeat: &r})
		}
	}
	return out
}

// sim is the engine around Decide.
type sim struct {
	tc     *hegel.T
	in     Snapshot
	asks   map[string]Kind // questions put to the person, not yet answered
	ended  *Action
	kinds  map[string]string // recipe id → step, parallel or repeat
	repeat map[string]domain.Repeat
}

func newSim(ht *hegel.T) *sim {
	g := &recipeGen{tc: ht}
	r := domain.Recipe{Name: "prop", Steps: g.entries(2)}
	if problems, _ := workflowrecipe.Check(r); len(problems) > 0 {
		ht.Fatalf("the generator drew a recipe Check refuses: %q", problems)
	}
	bounded := hegel.Draw(ht, hegel.Booleans())
	left := hegel.Draw(ht, hegel.Integers(0, 6))
	s := &sim{
		tc: ht,
		in: Snapshot{
			State: domain.RunRunning, Recipe: r,
			Until: map[string]UntilResult{}, OnMax: map[string]OnMaxAnswer{},
			Bounded: bounded,
			Left:    float64(left),
		},
		asks: map[string]Kind{}, kinds: map[string]string{}, repeat: map[string]domain.Repeat{},
	}
	var index func(es []domain.Entry)
	index = func(es []domain.Entry) {
		for _, e := range es {
			switch {
			case e.Step != nil:
				s.kinds[e.Step.ID] = "step"
			case e.Parallel != nil:
				s.kinds[e.Parallel.ID] = "parallel"
				for _, b := range e.Parallel.Steps {
					s.kinds[b.ID] = "step"
				}
			case e.Repeat != nil:
				s.kinds[e.Repeat.ID] = "repeat"
				s.repeat[e.Repeat.ID] = *e.Repeat
				index(e.Repeat.Steps)
			}
		}
	}
	index(r.Steps)
	ht.Note(fmt.Sprintf("recipe: %s; bounded=%v left=%v", recipeString(r.Steps), s.in.Bounded, s.in.Left))
	return s
}

func recipeString(es []domain.Entry) string {
	var parts []string
	for _, e := range es {
		switch {
		case e.Step != nil:
			parts = append(parts, e.Step.ID)
		case e.Parallel != nil:
			var ids []string
			for _, b := range e.Parallel.Steps {
				ids = append(ids, b.ID)
			}
			parts = append(parts, fmt.Sprintf("%s{%s %s}", e.Parallel.ID, e.Parallel.Join, strings.Join(ids, " ")))
		case e.Repeat != nil:
			parts = append(parts, fmt.Sprintf("%s[max %d %s](%s)", e.Repeat.ID, e.Repeat.Max, e.Repeat.OnMax.Or(), recipeString(e.Repeat.Steps)))
		}
	}
	return strings.Join(parts, " ")
}

func clone(in Snapshot) Snapshot {
	out := in
	out.Steps = slices.Clone(in.Steps)
	out.Until = map[string]UntilResult{}
	for k, v := range in.Until {
		out.Until[k] = v
	}
	out.OnMax = map[string]OnMaxAnswer{}
	for k, v := range in.OnMax {
		out.OnMax[k] = v
	}
	return out
}

func (s *sim) at(path string) int {
	return slices.IndexFunc(s.in.Steps, func(st Step) bool { return st.Path == path })
}

func (s *sim) live() []string {
	var out []string
	for _, st := range s.in.Steps {
		if st.State.Live() {
			out = append(out, st.Path)
		}
	}
	return out
}

// drive conducts the run: a tick, the hook, then one world event, until
// the run ends. After propEvents events the world only helps the run to
// its end — steps succeed, untils are met, questions are answered
// continue, the pool is topped up — and the run must end within propTicks.
func (s *sim) drive(hook func(before Snapshot, acts []Action, s *sim)) {
	for tick := 0; s.ended == nil; tick++ {
		drain := tick >= propEvents
		if tick >= propEvents+propTicks {
			s.tc.Fatalf("the run did not end: state %s, steps %v, asks %v", s.in.State, s.in.Steps, s.asks)
		}
		before := clone(s.in)
		acts := Decide(before)
		s.apply(acts, drain)
		hook(before, acts, s)
		if s.ended == nil {
			s.event(drain)
		}
	}
}

func (s *sim) apply(acts []Action, drain bool) {
	for _, a := range acts {
		s.tc.Note("  " + a.String())
		switch a.Kind {
		case Start:
			if i := s.at(a.Path); i >= 0 {
				s.in.Steps[i].State = domain.StepRunning
			} else {
				s.in.Steps = append(s.in.Steps, Step{Path: a.Path, State: domain.StepRunning})
			}
		case Interrupt:
			if i := s.at(a.Path); i >= 0 {
				s.in.Steps[i].State = domain.StepStopped
			}
		case NeedUntil:
			u := UntilResult{Met: true}
			if !drain {
				// mostly not met, so loops reach their max; sometimes unjudged
				switch {
				case s.chance(0.15):
					u = UntilResult{Unjudged: true, Detail: "no checks"}
				case !s.chance(0.35):
					u = UntilResult{}
				}
			}
			s.in.Until[a.Path] = u
		case AskOnMax, AskUnjudged:
			s.asks[a.Path] = a.Kind
		case Pause:
			s.in.State = domain.RunPaused
		case End:
			end := a
			s.ended = &end
			s.in.State = a.State
		}
	}
}

// event moves the world once.
func (s *sim) event(drain bool) {
	type ev struct {
		name string
		do   func()
	}
	var evs []ev
	live := s.live()
	if len(live) > 0 && s.in.State == domain.RunRunning {
		evs = append(evs, ev{"settle a step", func() {
			path := live[0]
			if !drain {
				path = hegel.Draw(s.tc, hegel.SampledFrom(live))
			}
			i := s.at(path)
			state := domain.StepDone
			if !drain {
				// mostly done, so runs get deep; sometimes failed, asking, or back to running
				switch {
				case s.chance(0.08):
					state = domain.StepFailed
				case s.chance(0.05):
					state = domain.StepAsking
				case s.chance(0.05):
					state = domain.StepRunning
				}
			}
			spent := 0.0
			if !drain {
				spent = float64(hegel.Draw(s.tc, hegel.Integers(0, 2)))
			}
			s.in.Steps[i].State = state
			s.in.Steps[i].Spent += spent
			s.in.Left -= spent
			s.tc.Note(fmt.Sprintf("world: %s → %s, spent %v", path, state, spent))
		}})
	}
	if len(s.asks) > 0 {
		evs = append(evs, ev{"answer", func() {
			paths := make([]string, 0, len(s.asks))
			for p := range s.asks {
				paths = append(paths, p)
			}
			slices.Sort(paths)
			path, answer := paths[0], OnMaxGoOn
			if !drain {
				path = hegel.Draw(s.tc, hegel.SampledFrom(paths))
				answer = hegel.Draw(s.tc, hegel.SampledFrom([]OnMaxAnswer{OnMaxMore, OnMaxGoOn, OnMaxStop}))
			}
			delete(s.asks, path)
			s.in.OnMax[path] = answer
			s.tc.Note(fmt.Sprintf("world: the person answers %s at %s", answer, path))
		}})
	}
	if s.in.State == domain.RunPaused {
		evs = append(evs, ev{"top up", func() {
			more := hegel.Draw(s.tc, hegel.Integers(1, 5))
			s.in.Left += float64(more)
			s.in.State = domain.RunRunning
			s.tc.Note(fmt.Sprintf("world: top-up, left %v", s.in.Left))
		}})
	}
	if drain {
		if len(evs) > 0 {
			evs[0].do()
		}
		return
	}
	// a stop, or a tick where nothing happens, is rare, so a run gets deep
	// before it is stopped; otherwise one of the events the run can take
	if !s.in.Stopping && s.chance(0.02) {
		s.in.Stopping = true
		s.tc.Note("world: stop")
		return
	}
	if len(evs) == 0 || s.chance(0.03) {
		return
	}
	e := hegel.Draw(s.tc, hegel.SampledFrom(evs))
	e.do()
}

// chance is true with probability p.
func (s *sim) chance(p float64) bool {
	return hegel.Draw(s.tc, hegel.WeightedBooleans(p))
}

func renderAll(as []Action) string {
	var out []string
	for _, a := range as {
		out = append(out, a.String())
	}
	return strings.Join(out, "; ")
}

// segments splits a start's path into the repeat iterations it sits in:
// for r1/2/p3/s4, the iteration prefix r1/2 of repeat r1 at 2.
type iteration struct {
	prefix, repeat string
	n              int
}

func (s *sim) iterations(path string) []iteration {
	segs := strings.Split(path, "/")
	var out []iteration
	for i := 0; i+1 < len(segs); i++ {
		if s.kinds[segs[i]] != "repeat" {
			continue
		}
		n, err := strconv.Atoi(segs[i+1])
		if err != nil {
			continue
		}
		out = append(out, iteration{prefix: strings.Join(segs[:i], "/"), repeat: segs[i], n: n})
	}
	return out
}

// TestPropertyASpentPoolStartsNothing: on a spent pool nothing starts, and
// a tick that leaves a step live pauses the run, so nothing spends on.
func TestPropertyASpentPoolStartsNothing(t *testing.T) {
	props(t, func(ht *hegel.T, s *sim, in Snapshot, acts []Action) {
		if in.State != domain.RunRunning || in.Stopping || !in.Bounded || in.Left > 0 {
			return
		}
		if slices.ContainsFunc(acts, func(a Action) bool { return a.Kind == Start }) {
			ht.Fatalf("left %v: a step starts: %s", in.Left, renderAll(acts))
		}
		if live := s.live(); len(live) > 0 && s.in.State != domain.RunPaused {
			ht.Fatalf("left %v: %v stay live and the run did not pause: %s", in.Left, live, renderAll(acts))
		}
	})
}

// TestPropertyASpentPoolHoldsBackNothingFree: on a spent pool, everything
// that costs no money — an interrupt, an until to judge, a question to the
// person — goes out as it would with money left.
func TestPropertyASpentPoolHoldsBackNothingFree(t *testing.T) {
	props(t, func(ht *hegel.T, _ *sim, in Snapshot, acts []Action) {
		if in.State != domain.RunRunning || in.Stopping || !in.Bounded || in.Left > 0 {
			return
		}
		free := clone(in)
		free.Bounded = false
		for _, a := range Decide(free) {
			if a.Kind != Start && a.Kind != End && !slices.ContainsFunc(acts, func(b Action) bool { return reflect.DeepEqual(a, b) }) {
				ht.Fatalf("left %v: held back %s: got %s", in.Left, a, renderAll(acts))
			}
		}
	})
}

// TestPropertyAStepStartsOnce: Start mints an instance that is missing or
// waiting, never one that is live or settled, and never twice in a tick.
func TestPropertyAStepStartsOnce(t *testing.T) {
	props(t, func(ht *hegel.T, _ *sim, in Snapshot, acts []Action) {
		seen := map[string]bool{}
		for _, a := range acts {
			if a.Kind != Start {
				continue
			}
			if seen[a.Path] {
				ht.Fatalf("%s started twice in one tick: %s", a.Path, renderAll(acts))
			}
			seen[a.Path] = true
			for _, st := range in.Steps {
				if st.Path == a.Path && st.State != domain.StepWaiting {
					ht.Fatalf("%s started while %s", a.Path, st.State)
				}
			}
		}
	})
}

// TestPropertyAStartNamesARecipeStep: every path started is a step of the
// recipe at its place — a step, a parallel's branch, or a step inside a
// repeat's numbered iteration — and Step is its id.
func TestPropertyAStartNamesARecipeStep(t *testing.T) {
	var valid func(es []domain.Entry, segs []string) string
	valid = func(es []domain.Entry, segs []string) string {
		for _, e := range es {
			switch {
			case e.Step != nil && len(segs) == 1 && segs[0] == e.Step.ID:
				return e.Step.ID
			case e.Parallel != nil && len(segs) == 2 && segs[0] == e.Parallel.ID:
				for _, b := range e.Parallel.Steps {
					if b.ID == segs[1] {
						return b.ID
					}
				}
			case e.Repeat != nil && len(segs) >= 3 && segs[0] == e.Repeat.ID:
				if n, err := strconv.Atoi(segs[1]); err == nil && n >= 1 {
					return valid(e.Repeat.Steps, segs[2:])
				}
			}
		}
		return ""
	}
	props(t, func(ht *hegel.T, _ *sim, in Snapshot, acts []Action) {
		for _, a := range acts {
			if a.Kind == Start {
				if id := valid(in.Recipe.Steps, strings.Split(a.Path, "/")); id == "" || id != a.Step {
					ht.Fatalf("start %s (step %q) is no step of the recipe", a.Path, a.Step)
				}
			}
		}
	})
}

// TestPropertyStepsRunOneAfterAnother: what is live is one step, or
// branches of one parallel — a later entry never runs beside an earlier one.
func TestPropertyStepsRunOneAfterAnother(t *testing.T) {
	props(t, func(ht *hegel.T, s *sim, _ Snapshot, acts []Action) {
		live := s.live()
		if len(live) < 2 {
			return
		}
		parent := func(p string) string { return p[:max(strings.LastIndex(p, "/"), 0)] }
		first := parent(live[0])
		segs := strings.Split(first, "/")
		if s.kinds[segs[len(segs)-1]] != "parallel" {
			ht.Fatalf("%d steps live outside a parallel: %v after %s", len(live), live, renderAll(acts))
		}
		for _, p := range live[1:] {
			if parent(p) != first {
				ht.Fatalf("steps of two places live together: %v after %s", live, renderAll(acts))
			}
		}
	})
}

// TestPropertyARepeatStaysWithinItsLimit: a repeat starts iteration n only
// while n is within its max, plus one for every "more" the person answered
// on an earlier iteration.
func TestPropertyARepeatStaysWithinItsLimit(t *testing.T) {
	props(t, func(ht *hegel.T, s *sim, in Snapshot, acts []Action) {
		for _, a := range acts {
			if a.Kind != Start {
				continue
			}
			for _, it := range s.iterations(a.Path) {
				limit := max(s.repeat[it.repeat].Max, 1)
				for k := 1; k < it.n; k++ {
					if in.OnMax[domain.IterationPath(it.prefix, it.repeat, k)] == OnMaxMore {
						limit++
					}
				}
				if it.n > limit {
					ht.Fatalf("start %s: iteration %d of %s past its limit %d", a.Path, it.n, it.repeat, limit)
				}
			}
		}
	})
}

// TestPropertyALoopIteratesOnlyPastAnUnmetUntil: iteration n+1 starts only
// once iteration n's until was judged not met — or could not be judged and
// the person asked for one more.
func TestPropertyALoopIteratesOnlyPastAnUnmetUntil(t *testing.T) {
	props(t, func(ht *hegel.T, s *sim, in Snapshot, acts []Action) {
		for _, a := range acts {
			if a.Kind != Start {
				continue
			}
			for _, it := range s.iterations(a.Path) {
				if it.n == 1 {
					continue
				}
				prev := domain.IterationPath(it.prefix, it.repeat, it.n-1)
				u, judged := in.Until[prev]
				switch {
				case !judged:
					ht.Fatalf("start %s before %s's until was judged", a.Path, prev)
				case u.Met:
					ht.Fatalf("start %s after %s's until was met", a.Path, prev)
				case u.Unjudged && in.OnMax[prev] != OnMaxMore:
					ht.Fatalf("start %s after %s could not be judged, and the person said %q", a.Path, prev, in.OnMax[prev])
				}
			}
		}
	})
}

// TestPropertyAnOpenQuestionHoldsTheRun: a question put to the person
// holds the run — nothing starts beside it, and the run does not end done
// or partial while it is unanswered.
func TestPropertyAnOpenQuestionHoldsTheRun(t *testing.T) {
	props(t, func(ht *hegel.T, s *sim, in Snapshot, acts []Action) {
		asks := slices.ContainsFunc(acts, func(a Action) bool { return a.Kind == AskOnMax || a.Kind == AskUnjudged })
		for _, a := range acts {
			if asks && a.Kind == Start {
				ht.Fatalf("a step starts beside a question: %s", renderAll(acts))
			}
			if a.Kind == End && a.State != domain.RunStopped && len(s.asks) > 0 {
				ht.Fatalf("the run ended %s with a question unanswered (%v): %s", a.State, s.asks, renderAll(acts))
			}
		}
	})
}

// TestPropertyAStopWindsDown: a stopped run interrupts every live step and
// starts nothing; with none live it ends stopped.
func TestPropertyAStopWindsDown(t *testing.T) {
	props(t, func(ht *hegel.T, _ *sim, in Snapshot, acts []Action) {
		if !in.Stopping || (in.State != domain.RunRunning && in.State != domain.RunPaused) {
			return
		}
		var live, interrupted []string
		for _, st := range in.Steps {
			if st.State.Live() {
				live = append(live, st.Path)
			}
		}
		for _, a := range acts {
			switch a.Kind {
			case Interrupt:
				interrupted = append(interrupted, a.Path)
			case End:
				if len(live) > 0 || a.State != domain.RunStopped {
					ht.Fatalf("a stop with %v live: %s", live, renderAll(acts))
				}
			default:
				ht.Fatalf("a stop did %s", a)
			}
		}
		if !slices.Equal(live, interrupted) || (len(live) == 0 && len(acts) != 1) {
			ht.Fatalf("a stop with %v live: %s", live, renderAll(acts))
		}
	})
}

// TestPropertyTheRunEndsLastAndOnce: End is the tick's last action, and
// comes only with nothing live and nothing starting.
func TestPropertyTheRunEndsLastAndOnce(t *testing.T) {
	props(t, func(ht *hegel.T, s *sim, _ Snapshot, acts []Action) {
		for i, a := range acts {
			if a.Kind != End {
				continue
			}
			if i != len(acts)-1 {
				ht.Fatalf("End is not last: %s", renderAll(acts))
			}
			if live := s.live(); len(live) > 0 {
				ht.Fatalf("the run ended with %v live: %s", live, renderAll(acts))
			}
			if slices.ContainsFunc(acts, func(a Action) bool { return a.Kind == Start }) {
				ht.Fatalf("the run ended and started a step in one tick: %s", renderAll(acts))
			}
		}
	})
}

// TestPropertyTheEndStateSaysHowTheRunWent: done means no step failed or
// was stopped outside a parallel and nobody stopped the run; stopped means
// a stop or a person's "stop"; partial means a step failed or was stopped,
// or a loop aborted.
func TestPropertyTheEndStateSaysHowTheRunWent(t *testing.T) {
	props(t, func(ht *hegel.T, s *sim, in Snapshot, acts []Action) {
		if len(acts) == 0 || acts[len(acts)-1].Kind != End {
			return
		}
		end := acts[len(acts)-1]
		personStopped := slices.Contains(slicesValues(in.OnMax), OnMaxStop)
		var bad []string
		for _, st := range in.Steps {
			if st.State == domain.StepFailed || st.State == domain.StepStopped {
				bad = append(bad, st.Path)
			}
		}
		badOutsideParallel := slices.ContainsFunc(bad, func(p string) bool {
			segs := strings.Split(p, "/")
			return len(segs) < 2 || s.kinds[segs[len(segs)-2]] != "parallel"
		})
		aborts := slices.ContainsFunc(mapsValues(s.repeat), func(r domain.Repeat) bool { return r.OnMax.Or() == domain.OnMaxAbort })
		switch end.State {
		case domain.RunDone:
			if in.Stopping || personStopped || badOutsideParallel {
				ht.Fatalf("ended done: stopping=%v, person stopped=%v, failed %v", in.Stopping, personStopped, bad)
			}
		case domain.RunStopped:
			if !in.Stopping && !personStopped {
				ht.Fatalf("ended stopped with no stop: %s", end)
			}
		case domain.RunPartial:
			if len(bad) == 0 && !aborts {
				ht.Fatalf("ended partial with nothing failed and no loop to abort: %s", end)
			}
		default:
			ht.Fatalf("ended %q", end.State)
		}
	})
}

func slicesValues(m map[string]OnMaxAnswer) []OnMaxAnswer {
	out := make([]OnMaxAnswer, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

func mapsValues(m map[string]domain.Repeat) []domain.Repeat {
	out := make([]domain.Repeat, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

// TestPropertyARunningRunNeverStalls: a running run with nothing live
// always has something to do — start, settle, judge, ask, pause or end.
// (TestPropertyTheRunEnds… is drive's own: a drained run that does not
// end fails every property.)
func TestPropertyARunningRunNeverStalls(t *testing.T) {
	props(t, func(ht *hegel.T, _ *sim, in Snapshot, acts []Action) {
		if in.State != domain.RunRunning || len(acts) > 0 {
			return
		}
		for _, st := range in.Steps {
			if st.State.Live() {
				return
			}
		}
		ht.Fatalf("a running run with nothing live did nothing: steps %v, until %v, onMax %v", in.Steps, in.Until, in.OnMax)
	})
}

// TestPropertyStepOrderInTheSnapshotDoesNotMatter: the snapshot's Steps
// are "in any order"; shuffled, Decide returns the same actions.
func TestPropertyStepOrderInTheSnapshotDoesNotMatter(t *testing.T) {
	props(t, func(ht *hegel.T, _ *sim, in Snapshot, acts []Action) {
		shuffled := clone(in)
		for i := len(shuffled.Steps) - 1; i > 0; i-- {
			j := hegel.Draw(ht, hegel.Integers(0, i))
			shuffled.Steps[i], shuffled.Steps[j] = shuffled.Steps[j], shuffled.Steps[i]
		}
		a, b := render(acts), render(Decide(shuffled))
		slices.Sort(a)
		slices.Sort(b)
		if !slices.Equal(a, b) {
			ht.Fatalf("shuffling the steps changed the actions:\n  %v\n  %v", a, b)
		}
	})
}

// conducted is a snapshot Decide conducts in full: running, not stopping,
// money left.
func conducted(in Snapshot) bool {
	return in.State == domain.RunRunning && !in.Stopping && (!in.Bounded || in.Left > 0)
}

// joinCheck runs check on every parallel of the recipe under join, at
// every place it sits (each iteration of a repeat around it).
func joinCheck(s *sim, in Snapshot, join domain.Join, check func(base string, branches []string)) {
	places := map[string]bool{}
	for _, st := range in.Steps {
		segs := strings.Split(st.Path, "/")
		if len(segs) >= 2 && s.kinds[segs[len(segs)-2]] == "parallel" {
			places[strings.Join(segs[:len(segs)-1], "/")] = true
		}
	}
	var find func(es []domain.Entry, id string) *domain.Parallel
	find = func(es []domain.Entry, id string) *domain.Parallel {
		for _, e := range es {
			if e.Parallel != nil && e.Parallel.ID == id {
				return e.Parallel
			}
			if e.Repeat != nil {
				if p := find(e.Repeat.Steps, id); p != nil {
					return p
				}
			}
		}
		return nil
	}
	for base := range places {
		p := find(in.Recipe.Steps, base[strings.LastIndex(base, "/")+1:])
		if p == nil || p.Join != join {
			continue
		}
		var branches []string
		for _, b := range p.Steps {
			branches = append(branches, base+"/"+b.ID)
		}
		check(base, branches)
	}
}

func stateOf(in Snapshot, path string) (domain.StepState, bool) {
	for _, st := range in.Steps {
		if st.Path == path {
			return st.State, true
		}
	}
	return "", false
}

// TestPropertyAJoinAnySettlesOnTheFirstSuccess: once a branch of an `any`
// parallel is done, no branch of it starts and every live one is
// interrupted.
func TestPropertyAJoinAnySettlesOnTheFirstSuccess(t *testing.T) {
	props(t, func(ht *hegel.T, s *sim, in Snapshot, acts []Action) {
		if !conducted(in) {
			return
		}
		joinCheck(s, in, domain.JoinAny, func(base string, branches []string) {
			if !slices.ContainsFunc(branches, func(b string) bool { st, _ := stateOf(in, b); return st == domain.StepDone }) {
				return
			}
			for _, b := range branches {
				st, _ := stateOf(in, b)
				starts := slices.ContainsFunc(acts, func(a Action) bool { return a.Kind == Start && a.Path == b })
				interrupted := slices.ContainsFunc(acts, func(a Action) bool { return a.Kind == Interrupt && a.Path == b })
				if starts || (st.Live() && !interrupted) {
					ht.Fatalf("any %s has a branch done, yet %s (%s): %s", base, b, st, renderAll(acts))
				}
			}
		})
	})
}

// TestPropertyAJoinAllSettlesOnTheFirstFailure: once a branch of an `all`
// parallel failed or was stopped, no branch of it starts and every live
// one is interrupted.
func TestPropertyAJoinAllSettlesOnTheFirstFailure(t *testing.T) {
	props(t, func(ht *hegel.T, s *sim, in Snapshot, acts []Action) {
		if !conducted(in) {
			return
		}
		joinCheck(s, in, domain.JoinAll, func(base string, branches []string) {
			if !slices.ContainsFunc(branches, func(b string) bool {
				st, _ := stateOf(in, b)
				return st == domain.StepFailed || st == domain.StepStopped
			}) {
				return
			}
			for _, b := range branches {
				st, _ := stateOf(in, b)
				starts := slices.ContainsFunc(acts, func(a Action) bool { return a.Kind == Start && a.Path == b })
				interrupted := slices.ContainsFunc(acts, func(a Action) bool { return a.Kind == Interrupt && a.Path == b })
				if starts || (st.Live() && !interrupted) {
					ht.Fatalf("all %s has a branch failed, yet %s (%s): %s", base, b, st, renderAll(acts))
				}
			}
		})
	})
}

// TestPropertyAJoinAllSettledInterruptsNothing: an `allSettled` parallel
// waits for every branch; outside a stop, nothing of it is interrupted.
func TestPropertyAJoinAllSettledInterruptsNothing(t *testing.T) {
	props(t, func(ht *hegel.T, s *sim, in Snapshot, acts []Action) {
		if in.Stopping {
			return
		}
		joinCheck(s, in, domain.JoinAllSettled, func(base string, branches []string) {
			for _, a := range acts {
				if a.Kind == Interrupt && slices.Contains(branches, a.Path) {
					ht.Fatalf("allSettled %s interrupted %s: %s", base, a.Path, renderAll(acts))
				}
			}
		})
	})
}

// TestPropertyAnInterruptStopsALiveStep: Interrupt names a live instance,
// each at most once a tick.
func TestPropertyAnInterruptStopsALiveStep(t *testing.T) {
	props(t, func(ht *hegel.T, _ *sim, in Snapshot, acts []Action) {
		seen := map[string]bool{}
		for _, a := range acts {
			if a.Kind != Interrupt {
				continue
			}
			if st, _ := stateOf(in, a.Path); !st.Live() || seen[a.Path] {
				ht.Fatalf("interrupt %s (%s, seen %v): %s", a.Path, st, seen[a.Path], renderAll(acts))
			}
			seen[a.Path] = true
		}
	})
}

// TestPropertyAnAnsweredQuestionIsNotAskedAgain: once the person answered
// at an iteration, the run acts on the answer and never puts that
// iteration's question again.
func TestPropertyAnAnsweredQuestionIsNotAskedAgain(t *testing.T) {
	props(t, func(ht *hegel.T, _ *sim, in Snapshot, acts []Action) {
		for _, a := range acts {
			if (a.Kind == AskOnMax || a.Kind == AskUnjudged) && in.OnMax[a.Path] != "" {
				ht.Fatalf("%s asked again after the person answered %q", a, in.OnMax[a.Path])
			}
		}
	})
}

// TestPropertyAnUnjudgedUntilReachesThePerson: an iteration whose until
// could not be judged, unanswered, is put to the person whatever the
// repeat's onMax.
func TestPropertyAnUnjudgedUntilReachesThePerson(t *testing.T) {
	props(t, func(ht *hegel.T, _ *sim, in Snapshot, acts []Action) {
		if !conducted(in) {
			return
		}
		for iter, u := range in.Until {
			if u.Unjudged && !u.Met && in.OnMax[iter] == "" &&
				!slices.ContainsFunc(acts, func(a Action) bool { return a.Kind == AskUnjudged && a.Path == iter }) {
				ht.Fatalf("%s could not be judged and nobody was asked: %s", iter, renderAll(acts))
			}
		}
	})
}

// TestPropertyTheMaxIsAskedOnlyAtTheMax: AskOnMax comes only for a repeat
// under onMax pause, at its limit, its until judged and not met.
func TestPropertyTheMaxIsAskedOnlyAtTheMax(t *testing.T) {
	props(t, func(ht *hegel.T, s *sim, in Snapshot, acts []Action) {
		for _, a := range acts {
			if a.Kind != AskOnMax {
				continue
			}
			its := s.iterations(a.Path + "/x")
			it := its[len(its)-1]
			r := s.repeat[a.Step]
			limit := max(r.Max, 1)
			for k := 1; k <= it.n; k++ {
				if in.OnMax[domain.IterationPath(it.prefix, it.repeat, k)] == OnMaxMore {
					limit++
				}
			}
			u, judged := in.Until[a.Path]
			if r.OnMax.Or() != domain.OnMaxPause || it.n != limit || !judged || u.Met || u.Unjudged {
				ht.Fatalf("%s: onMax %s, iteration %d of limit %d, until %+v (judged %v)", a, r.OnMax.Or(), it.n, limit, u, judged)
			}
		}
	})
}

// TestPropertyOnlyARunningRunIsConducted: a run that is neither running
// nor being stopped gets no actions.
func TestPropertyOnlyARunningRunIsConducted(t *testing.T) {
	props(t, func(ht *hegel.T, _ *sim, in Snapshot, acts []Action) {
		for _, state := range []domain.RunState{domain.RunPaused, domain.RunDone, domain.RunPartial, domain.RunStopped, "proposed"} {
			other := clone(in)
			other.State, other.Stopping = state, false
			if got := Decide(other); len(got) > 0 {
				ht.Fatalf("a %s run got %s", state, renderAll(got))
			}
		}
	})
}

// TestPropertyMoneyLeftDoesNotChangeThePlan: with money left, a bounded
// pool decides exactly as an unbounded one — a run estimates nothing.
func TestPropertyMoneyLeftDoesNotChangeThePlan(t *testing.T) {
	props(t, func(ht *hegel.T, _ *sim, in Snapshot, acts []Action) {
		if !in.Bounded || in.Left <= 0 {
			return
		}
		free := clone(in)
		free.Bounded, free.Left = false, 0
		if a, b := renderAll(acts), renderAll(Decide(free)); a != b {
			ht.Fatalf("left %v changed the plan:\n  bounded:   %s\n  unbounded: %s", in.Left, a, b)
		}
	})
}

// TestPropertyDecideIsDeterministic: the same snapshot, the same actions.
func TestPropertyDecideIsDeterministic(t *testing.T) {
	props(t, func(ht *hegel.T, _ *sim, in Snapshot, acts []Action) {
		if a, b := renderAll(acts), renderAll(Decide(clone(in))); a != b {
			ht.Fatalf("two calls disagree:\n  %s\n  %s", a, b)
		}
	})
}
