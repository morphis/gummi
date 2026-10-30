// Package workflowpolicy decides what a running workflow run does next
// (DESIGN §25.7). It is pure: given a snapshot of a run — its recipe, the
// step instances it has started, its money and the results the engine fed
// back — Decide returns the actions to take, in order, and never touches
// git, the store or an agent. Engine.WorkflowTick builds the snapshot and
// executes them.
//
// A run's position is read from its step instances alone: a step at a
// path (build/2/implement is iteration 2 of build) that is done lets the
// next entry start; a parallel's branches start together and the block
// settles under its join; a repeat whose iteration has ended needs its
// until judged, which the engine computes (the done-when checks, an
// artifact's verdict, the last output) and feeds back, and then iterates,
// ends the loop or applies onMax.
//
// Money (§24.7a): one pool, the session's envelope. A run estimates
// nothing and binds to that budget alone: it goes on until the pool is
// spent, then pauses for a top-up and never drops work — and never holds
// back, behind the top-up, what costs nothing.
//
// The rules, in the order Decide applies them:
//
//  1. Only a running run is conducted, but a stop winds down a paused one
//     too: interrupt the live steps, then end the run stopped.
//  2. A spent pool pauses the run for a top-up when a step would start or
//     a step stays live; what costs no money — interrupts, an until to
//     judge, a question to the person — still goes out first. With nothing
//     left to spend on, the run goes on settling, judging and asking, and
//     ends.
//  3. A parallel whose join is decided settles, stopping branches it no
//     longer needs; a repeat whose iteration has ended has its until
//     judged, then iterates, ends, or applies onMax. An until that could
//     not be judged goes to the person, never counted as not met.
//  4. The next ready step starts: the next entry in order, or every branch
//     of a parallel.
//  5. When nothing is left, the run ends: done, or partial on a failed step
//     outside a parallel, a failed parallel or an aborted loop.
package workflowpolicy

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/morphis/gummi/internal/domain"
)

// Step is one step instance in a snapshot.
type Step struct {
	// Path is where the instance sits: build/2/implement, reviews/review-a.
	Path  string
	State domain.StepState
	Spent float64
	// Watch is a watch row's memory (DESIGN §28.2); nil on every other.
	Watch *domain.WatchState
}

// UntilResult is the engine's judgement of a repeat's until after one
// iteration. Unjudged marks a condition gummi could not judge, which is
// never counted as not met: the person decides (AskUnjudged).
type UntilResult struct {
	Met      bool
	Unjudged bool
	Detail   string
}

// OnMaxAnswer is the person's answer to a repeat that reached its max
// without its condition, under onMax: pause — or to one whose condition
// could not be judged.
type OnMaxAnswer string

const (
	// OnMaxMore runs one more iteration.
	OnMaxMore OnMaxAnswer = "more"
	// OnMaxGoOn goes on to the next entry.
	OnMaxGoOn OnMaxAnswer = "continue"
	// OnMaxStop stops the run.
	OnMaxStop OnMaxAnswer = "stop"
)

// Snapshot is a run as the engine reads it from the store.
type Snapshot struct {
	State  domain.RunState
	Recipe domain.Recipe
	// Steps is every step instance the run has, in any order.
	Steps []Step
	// Until holds the judged untils, keyed by iteration path (build/2).
	Until map[string]UntilResult
	// OnMax holds the person's onMax answers, keyed by the iteration path
	// that reached the max or could not be judged.
	OnMax map[string]OnMaxAnswer
	// Bounded marks a pool with a ceiling: a session with an envelope. Left
	// is what it has left, the run's own spend taken out. Meaningless when
	// not Bounded.
	Bounded bool
	Left    float64
	// Stopping marks a workflow_stop or a person's stop.
	Stopping bool
	// Now is the engine's clock, the only one Decide reads: a watch's quiet
	// runs against it.
	Now time.Time
}

// Kind names an action.
type Kind int

const (
	// Start starts the step StepID at Path: snapshot the tree, fill its
	// prompt, mint its card and send it.
	Start Kind = iota
	// Interrupt stops a live step at its next tool boundary, marking it
	// stopped.
	Interrupt
	// NeedUntil asks the engine to judge the until of the repeat Step at
	// iteration Path and feed the result back as Snapshot.Until[Path].
	NeedUntil
	// AskOnMax puts a repeat that reached its max to the person: one more
	// iteration, continue, or stop. The answer comes back as
	// Snapshot.OnMax[Path].
	AskOnMax
	// AskUnjudged puts a repeat whose until could not be judged after the
	// iteration at Path to the person: one more iteration, continue, or
	// stop, answered as Snapshot.OnMax[Path]. Reason says why.
	AskUnjudged
	// Pause parks the run for a top-up: the pool is spent.
	Pause
	// End ends the run in State: run the end checks, write the report and
	// send it to the session.
	End
	// StartPass starts pass Pass of the watch at Path for Event; Key names
	// the event and Seen are the feedback ids the pass takes (DESIGN §28.3).
	StartPass
	// PushPass pushes the session's branch once pass Pass of the watch at
	// Path has run its steps.
	PushPass
	// AskWatch puts the watch at Path's question Ask (keyed Key) to the
	// person.
	AskWatch
	// ParkWatch parks the run for a person's Resume before the pass a
	// restart held.
	ParkWatch
	// EndWatch settles the watch at Path in Settle (done or failed).
	EndWatch
)

func (k Kind) String() string {
	return [...]string{
		"start", "interrupt", "need-until", "ask-on-max", "ask-unjudged", "pause", "end",
		"start-pass", "push-pass", "ask-watch", "park-watch", "end-watch",
	}[k]
}

// WatchAsk is which question a watch puts to the person (DESIGN §28.4).
type WatchAsk string

const (
	AskMax        WatchAsk = "max"        // overall or an event's own max
	AskSame       WatchAsk = "same"       // the same event on the same head after a pass
	AskUnchanged  WatchAsk = "unchanged"  // a pass changed nothing and its event stands
	AskFailedPass WatchAsk = "failed"     // a pass's step failed
	AskPush       WatchAsk = "push"       // the push after a pass failed
	AskUnreadable WatchAsk = "unreadable" // the trust filter could not be read
	AskReadError  WatchAsk = "read"       // the PR could not be read
	AskQuiet      WatchAsk = "quiet"      // nothing happened for the recipe's quiet
)

// The person's answers to a watch's question.
const (
	WatchMore  = "more"  // one more pass
	WatchWait  = "wait"  // keep waiting without acting
	WatchAgain = "again" // read or push again
	WatchStop  = "stop"
)

// Action is one thing for the engine to do.
type Action struct {
	Kind Kind
	// Path is the step instance's path, or for NeedUntil, AskOnMax and
	// AskUnjudged the iteration's.
	Path string
	// Step is the recipe id of the step to start, or of the repeat whose
	// until or max is in question.
	Step   string
	Reason string
	// State is End's: done, partial or stopped.
	State domain.RunState
	// A watch's actions: the event, the pass, its key and question, the
	// feedback ids a pass takes and the state EndWatch settles in.
	Event  domain.WatchEvent
	Pass   int
	Key    string
	Ask    WatchAsk
	Seen   []string
	Settle domain.StepState
}

func (a Action) String() string {
	s := a.Kind.String()
	if a.Path != "" {
		s += " " + a.Path
	}
	if a.Kind == End {
		s += " " + string(a.State)
	}
	if a.Reason != "" {
		s += ": " + a.Reason
	}
	return s
}

// outcome is how far a part of the recipe has got.
type outcome int

const (
	pending outcome = iota // under way, or waiting on something
	ok                     // finished; what follows may start
	failed                 // ends the run partial
	stopped                // ends the run stopped (a person's onMax answer)
)

type decider struct {
	in     Snapshot
	byPath map[string]Step
	settle []Action // rule 3
	starts []Action // rule 4
	// acts are a watch's own actions, which cost nothing and so never pause
	// a spent pool.
	acts []Action
}

func newDecider(in Snapshot) *decider {
	d := &decider{in: in, byPath: map[string]Step{}}
	for _, s := range in.Steps {
		d.byPath[s.Path] = s
	}
	return d
}

// Decide returns the actions for one tick, in execution order.
func Decide(in Snapshot) []Action {
	d := newDecider(in)
	live := d.live()

	if in.Stopping && (in.State == domain.RunRunning || in.State == domain.RunPaused) {
		var out []Action
		for _, s := range live {
			out = append(out, Action{Kind: Interrupt, Path: s.Path, Reason: "the run was stopped"})
		}
		if len(live) == 0 {
			out = append(out, Action{Kind: End, State: domain.RunStopped, Reason: "stopped"})
		}
		return out
	}
	if in.State != domain.RunRunning {
		return nil
	}

	res, why := d.seq("", in.Recipe.Steps)
	if in.Bounded && in.Left <= 0 {
		// a spent pool pauses whatever would spend more — a step to start,
		// or one that stays live — and holds back nothing that costs no
		// money: interrupts, untils to judge, questions and a watch's own
		// actions still go out, so a discarded branch does not spend the
		// top-up and the person is not asked for money to learn whether the
		// run is done. A watch only waiting spends nothing (DESIGN §28.2).
		interrupted := map[string]bool{}
		for _, a := range d.settle {
			if a.Kind == Interrupt {
				interrupted[a.Path] = true
			}
		}
		spends := len(d.starts) > 0
		for _, s := range live {
			spends = spends || (s.State != domain.StepWatching && !interrupted[s.Path])
		}
		if spends {
			out := append(append(d.settle, d.acts...), Action{Kind: Pause, Reason: fmt.Sprintf("the session's budget is spent (the run has spent %s)", domain.FormatDollars(Spent(in)))})
			return out
		}
	}
	var out []Action
	out = append(out, d.settle...)
	out = append(out, d.acts...)
	out = append(out, d.starts...)

	if res == pending || len(d.live()) > 0 {
		return out
	}
	switch res {
	case ok:
		out = append(out, Action{Kind: End, State: domain.RunDone})
	case failed:
		out = append(out, Action{Kind: End, State: domain.RunPartial, Reason: why})
	case stopped:
		out = append(out, Action{Kind: End, State: domain.RunStopped, Reason: why})
	}
	return out
}

// live lists the live step instances, in snapshot order.
func (d *decider) live() []Step {
	var out []Step
	for _, s := range d.in.Steps {
		if s.State.Live() {
			out = append(out, s)
		}
	}
	return out
}

// Spent is what a snapshot's steps have spent together.
func Spent(in Snapshot) float64 {
	t := 0.0
	for _, s := range in.Steps {
		t += s.Spent
	}
	return t
}

func join(prefix, id string) string {
	if prefix == "" {
		return id
	}
	return prefix + "/" + id
}

// seq walks a list of entries under prefix: the first that has not
// finished decides.
func (d *decider) seq(prefix string, es []domain.Entry) (outcome, string) {
	for _, e := range es {
		var res outcome
		var why string
		switch {
		case e.Step != nil && e.Step.Watch != nil:
			res, why = d.watch(prefix, *e.Step)
		case e.Step != nil:
			res, why = d.step(prefix, *e.Step)
		case e.Parallel != nil:
			res, why = d.parallel(prefix, *e.Parallel)
		case e.Repeat != nil:
			res, why = d.repeat(prefix, *e.Repeat)
		}
		if res != ok {
			return res, why
		}
	}
	return ok, ""
}

func (d *decider) start(path, id string) {
	d.starts = append(d.starts, Action{Kind: Start, Path: path, Step: id})
}

func (d *decider) step(prefix string, s domain.Step) (outcome, string) {
	path := join(prefix, s.ID)
	inst, has := d.byPath[path]
	switch {
	case !has || inst.State == domain.StepWaiting:
		d.start(path, s.ID)
		return pending, ""
	case inst.State == domain.StepDone:
		return ok, ""
	case inst.State == domain.StepFailed:
		return failed, "step " + path + " failed"
	case inst.State == domain.StepStopped:
		return failed, "step " + path + " was stopped"
	}
	return pending, ""
}

func (d *decider) parallel(prefix string, p domain.Parallel) (outcome, string) {
	base := join(prefix, p.ID)
	var done, bad, missing int
	var live []string
	var firstBad string
	for _, s := range p.Steps {
		path := join(base, s.ID)
		inst, has := d.byPath[path]
		switch {
		case !has || inst.State == domain.StepWaiting:
			missing++
		case inst.State == domain.StepDone:
			done++
		case inst.State.Settled():
			bad++
			if firstBad == "" {
				firstBad = path
			}
		default:
			live = append(live, path)
		}
	}
	startMissing := func() {
		for _, s := range p.Steps {
			path := join(base, s.ID)
			if inst, has := d.byPath[path]; !has || inst.State == domain.StepWaiting {
				d.start(path, s.ID)
			}
		}
	}
	interrupt := func(why string) {
		for _, path := range live {
			d.settle = append(d.settle, Action{Kind: Interrupt, Path: path, Reason: why})
		}
	}
	n := len(p.Steps)
	switch p.Join {
	case domain.JoinAll:
		if bad > 0 {
			interrupt(fmt.Sprintf("parallel %s settled: %s did not succeed", base, firstBad))
			if len(live) > 0 {
				return pending, ""
			}
			return failed, fmt.Sprintf("parallel %s failed: %s did not succeed", base, firstBad)
		}
		if done == n {
			return ok, ""
		}
	case domain.JoinAny:
		if done > 0 {
			interrupt(fmt.Sprintf("parallel %s settled: a branch succeeded", base))
			if len(live) > 0 {
				return pending, ""
			}
			return ok, ""
		}
		if bad == n {
			return failed, fmt.Sprintf("parallel %s failed: no branch succeeded", base)
		}
	default: // allSettled, and anything Check would have refused
		if missing == 0 && len(live) == 0 {
			if done == 0 {
				return failed, fmt.Sprintf("parallel %s failed: every branch failed", base)
			}
			return ok, ""
		}
	}
	startMissing()
	return pending, ""
}

// iterations is the highest iteration of the repeat at base that has a
// step instance, 0 when none has.
func (d *decider) iterations(base string) int {
	cur := 0
	for path := range d.byPath {
		rest, found := strings.CutPrefix(path, base+"/")
		if !found {
			continue
		}
		seg, _, _ := strings.Cut(rest, "/")
		if k, err := strconv.Atoi(seg); err == nil && k > cur {
			cur = k
		}
	}
	return cur
}

// limit is how many iterations the repeat may run: its max, and one more
// for every time the person asked for one.
func (d *decider) limit(prefix string, r domain.Repeat, upTo int) int {
	n := max(r.Max, 1)
	for k := 1; k <= upTo; k++ {
		if d.in.OnMax[domain.IterationPath(prefix, r.ID, k)] == OnMaxMore {
			n++
		}
	}
	return n
}

func (d *decider) repeat(prefix string, r domain.Repeat) (outcome, string) {
	base := join(prefix, r.ID)
	cur := d.iterations(base)
	if cur == 0 {
		return d.seq(domain.IterationPath(prefix, r.ID, 1), r.Steps)
	}
	iter := domain.IterationPath(prefix, r.ID, cur)
	if res, why := d.seq(iter, r.Steps); res != ok {
		return res, why
	}
	u, judged := d.in.Until[iter]
	if !judged {
		d.settle = append(d.settle, Action{Kind: NeedUntil, Path: iter, Step: r.ID})
		return pending, ""
	}
	switch {
	case u.Met:
		return ok, ""
	case u.Unjudged:
		// the loop cannot tell whether it is done: the person says, and one
		// more iteration does not count against its max (limit)
		switch d.in.OnMax[iter] {
		case OnMaxGoOn:
			return ok, ""
		case OnMaxStop:
			return stopped, fmt.Sprintf("repeat %s could not judge its condition (%s); the person stopped the run", base, u.Detail)
		case OnMaxMore:
		default:
			d.settle = append(d.settle, Action{Kind: AskUnjudged, Path: iter, Step: r.ID, Reason: u.Detail})
			return pending, ""
		}
	}
	if cur < d.limit(prefix, r, cur) {
		return d.seq(domain.IterationPath(prefix, r.ID, cur+1), r.Steps)
	}
	why := fmt.Sprintf("repeat %s ran %d times without its condition", base, cur)
	switch r.OnMax.Or() {
	case domain.OnMaxAbort:
		return failed, why
	case domain.OnMaxContinue:
		return ok, ""
	}
	switch d.in.OnMax[iter] {
	case OnMaxGoOn:
		return ok, ""
	case OnMaxStop:
		return stopped, why + "; the person stopped the run"
	}
	d.settle = append(d.settle, Action{Kind: AskOnMax, Path: iter, Step: r.ID, Reason: why})
	return pending, ""
}
