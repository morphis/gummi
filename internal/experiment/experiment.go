// Package experiment runs an orchestrated live experiment on a substrate
// and says what it proved.
//
// A gummi-check is a command in a checkout, and its exit status is the
// whole of its answer. That cannot carry work whose correctness is only
// observable on infrastructure: the run is minutes to hours, it is not a
// property of one checkout, it needs the substrate to itself — and above
// all "it failed" has two meanings. A matrix that failed because routing
// had not converged and one that failed because the code is wrong exit the
// same, and a budgeted autonomous run that cannot tell them apart spends
// itself chasing the environment.
//
// So a run here has four outcomes, not two:
//
//	pass          the experiment held for these heads
//	fail          it did not, AND did not again on a freshly reset substrate
//	inconclusive  no opinion: the substrate could not be made ready, the rig
//	              failed its own control, a phase said EX_TEMPFAIL, the
//	              runner died — or a failure did not reproduce
//	not-run       it never started: the substrate was held, or would expire
//
// Three things make the split, all of them generic. Phase attribution:
// bringing the substrate up and proving the rig against its own reference
// (the control) are the environment's, never the work's. An explicit
// signal: any phase may exit 75. And reproduce-before-believing: a failure
// is a verdict only when a second attempt on a reset substrate fails in
// the same phase; one that passes second time is recorded as flaky and
// judged nothing.
//
// A run leaves a directory behind (job.json, result.json, a log per phase,
// and whatever the experiment wrote under evidence/). The files are the
// record: nothing about a run lives only in a process, so a gummi that
// restarts finds its runs where it left them.
package experiment

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/morphis/gummi/internal/atomicfile"
	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/substrate"
)

// Outcome is what a run proved.
type Outcome string

// The four outcomes.
const (
	Pass         Outcome = "pass"
	Fail         Outcome = "fail"
	Inconclusive Outcome = "inconclusive"
	NotRun       Outcome = "not-run"
)

// Conclusive reports that the outcome is a verdict on the inputs.
func (o Outcome) Conclusive() bool { return o == Pass || o == Fail }

// ExitTempFail is the exit status by which any phase says the run could
// not be judged (sysexits' EX_TEMPFAIL).
const ExitTempFail = 75

// Run states.
const (
	StateRunning = "running"
	StateDone    = "done"
)

// Job is one run to make. It carries its own copy of the experiment and
// substrate definitions: what a run did is decided when it starts, not by
// whatever the config says by the time someone reads the result.
type Job struct {
	ID         string            `json:"id"`
	Experiment string            `json:"experiment"`
	Owner      string            `json:"owner"`   // the goal or card the run is for
	Purpose    string            `json:"purpose"` // "integration", "verify", "control", "card FD-003", …
	Heads      map[string]string `json:"heads"`   // repo → commit the run is about
	Trees      map[string]string `json:"trees"`   // repo → checkout to deploy from
	// Roots is the repository each tree was snapshotted from, for whoever
	// removes the snapshots once the run is over.
	Roots map[string]string `json:"roots,omitempty"`
	// Wanted is the assertions the owner's done-when items actually cite,
	// and it is what this run is judged on. Empty means the whole run, so
	// a goal whose items are about everything the experiment asserts is
	// judged exactly as before.
	//
	// A goal is usually about part of a matrix — §17.12 recommends cutting
	// a programme into a sequence of goals, and then every goal but the
	// last is. Judging such a run on the whole matrix makes its experiment
	// unpassable by construction, which costs three things that are not
	// the work's fault: a reproduce attempt on every run, a lead turn for
	// a "failure" whose items are all met, and the negative control, which
	// is only ever triggered by heads that pass.
	Wanted []string `json:"wanted,omitempty"`
	// Control runs the experiment's positive control first.
	Control bool `json:"control,omitempty"`
	// ExpectFail marks a negative control: the inputs are the trunk, and
	// the experiment is supposed to fail on them. It changes nothing about
	// how the run goes, only how its outcome reads.
	ExpectFail bool `json:"expect_fail,omitempty"`

	Def       config.Experiment `json:"def"`
	Substrate config.Substrate  `json:"substrate"`
	Root      string            `json:"root"`      // where commands run
	StateDir  string            `json:"state_dir"` // where the substrate's lock lives
	Dir       string            `json:"dir"`       // the run directory
}

// Phase is one command a run executed.
type Phase struct {
	Name    string  `json:"name"`
	Attempt int     `json:"attempt"`
	Exit    int     `json:"exit"`
	OK      bool    `json:"ok"`
	Seconds float64 `json:"seconds"`
	Log     string  `json:"log,omitempty"` // path relative to the run directory
	Tail    string  `json:"tail,omitempty"`
}

// Assertion is one named thing the experiment checked.
type Assertion struct {
	ID     string `json:"id"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// Result is a run's record, written as it goes.
type Result struct {
	ID         string            `json:"id"`
	Experiment string            `json:"experiment"`
	Substrate  string            `json:"substrate"`
	Owner      string            `json:"owner"`
	Purpose    string            `json:"purpose"`
	Heads      map[string]string `json:"heads"`
	ExpectFail bool              `json:"expect_fail,omitempty"`

	State   string  `json:"state"`
	Outcome Outcome `json:"outcome,omitempty"`
	// Reason is the sentence that says why the outcome is what it is.
	Reason string `json:"reason,omitempty"`
	// FailedPhase names the phase a fail (or a failure that did not
	// reproduce) happened in.
	FailedPhase string `json:"failed_phase,omitempty"`
	// Scoped says the run passed on what the owner asked about while
	// something else in the experiment did not hold. It is a real pass and
	// a narrower one, and a reader is told which.
	Scoped bool `json:"scoped,omitempty"`
	// Flaky: an attempt failed and the next passed. The run judged nothing,
	// and the rate of these is what says whether the rig can be believed.
	Flaky bool `json:"flaky,omitempty"`
	// ControlFailed: the rig failed its own reference. No amount of work on
	// the inputs can change that.
	ControlFailed bool `json:"control_failed,omitempty"`
	// ControlProved is how many assertions the positive control reported
	// holding, when one ran. Zero with ControlRan true is the shape F-2
	// found in the trial harness: a control that asserts nothing cannot
	// fail, so it proves nothing, and until this was recorded a run backed
	// by such a control read exactly like one backed by a real rig.
	ControlRan    bool `json:"control_ran,omitempty"`
	ControlProved int  `json:"control_proved,omitempty"`

	Phases     []Phase     `json:"phases,omitempty"`
	Assertions []Assertion `json:"assertions,omitempty"`
	// Ops lists what was done to the substrate to make it ready.
	Ops []string `json:"substrate_ops,omitempty"`
	// Retaken says an owner declared this run's evidence stale: the
	// substrate it ran on was wrong in a way the run could not see, and
	// the verdict it reached is about that rather than about the code.
	// The run stays where it is — the directory is the record, and what
	// it cost was still spent — but it is no longer evidence about
	// anything, so the goal takes the run again.
	Retaken bool `json:"retaken,omitempty"`
	// PhaseGroup is the process group of the phase command running right
	// now, and PhaseGroupStart identifies its leader against pid reuse.
	// They are what lets a reader of this record kill a phase whose runner
	// died under it: the parent-death signal reaches the shell and nothing
	// below it, and a deploy still working the substrate after its lease
	// has gone is the one thing exclusivity is for.
	PhaseGroup      int    `json:"phase_group,omitempty"`
	PhaseGroupStart uint64 `json:"phase_group_start,omitempty"`

	PID       int       `json:"pid"`
	Started   time.Time `json:"started"`
	Ended     time.Time `json:"ended,omitzero"`
	Heartbeat time.Time `json:"heartbeat,omitzero"`
	// Seconds is how long the run held the substrate.
	Seconds float64 `json:"seconds"`
	Dir     string  `json:"dir"`
}

// Passed counts the assertions that held.
func (r Result) Passed() (ok, total int) {
	for _, a := range r.Assertions {
		if a.OK {
			ok++
		}
	}
	return ok, len(r.Assertions)
}

// Holds reports whether the run proves an item about the named assertions
// (all of them when none are named). ok is false when the run has no
// opinion: it was not conclusive, or it never reported an assertion the
// item is about.
func (r Result) Holds(assertions []string) (held, ok bool) {
	if !r.Outcome.Conclusive() {
		return false, false
	}
	if len(assertions) == 0 {
		return r.Outcome == Pass, true
	}
	by := map[string]bool{}
	for _, a := range r.Assertions {
		by[a.ID] = a.OK
	}
	for _, id := range assertions {
		v, reported := by[id]
		if !reported {
			// a passing run that never mentioned it proves nothing about it;
			// a failing one may simply not have got that far
			return false, r.Outcome == Fail
		}
		if !v {
			return false, true
		}
	}
	return true, true
}

// About reports whether the run's evidence is about heads: every input it
// ran on is still at the commit it ran on.
func (r Result) About(heads map[string]string) bool {
	if len(r.Heads) == 0 {
		return false
	}
	for repo, sha := range r.Heads {
		if heads[repo] != sha {
			return false
		}
	}
	return true
}

const (
	jobFile     = "job.json"
	resultFile  = "result.json"
	retakenFile = "retaken"
)

// NewID mints a run id that sorts by when it was made, to the nanosecond:
// a goal reads its runs in order, and two made in the same second are the
// ordinary case in a test and not unheard of outside one.
func NewID(now time.Time) string {
	now = now.UTC()
	return now.Format("20060102T150405") + fmt.Sprintf("-%09d", now.Nanosecond())
}

// Prepare writes the job into its run directory, ready for Execute — in
// this process or another.
func Prepare(job Job) error {
	if err := os.MkdirAll(filepath.Join(job.Dir, "evidence"), 0o750); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(job.Dir, jobFile), raw, 0o600)
}

// LoadJob reads a prepared job back.
func LoadJob(dir string) (Job, error) {
	var j Job
	raw, err := os.ReadFile(filepath.Join(dir, jobFile))
	if err != nil {
		return j, err
	}
	if err := json.Unmarshal(raw, &j); err != nil {
		return j, err
	}
	j.Dir = dir
	return j, nil
}

// Load reads a run's record. A run that says it is running but whose
// runner is gone is reported as what it is: inconclusive, because a run
// nobody finished judged nothing.
func Load(dir string) (Result, error) {
	var r Result
	raw, err := os.ReadFile(filepath.Join(dir, resultFile))
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return r, err
	}
	r.Dir = dir
	if r.State == StateRunning && !alive(r.PID) {
		// The run is over and what it left behind is still working the
		// substrate: the parent-death signal took the phase's shell and
		// nothing under it. Reading the run and killing its remains are
		// one fact, so they happen in one place — and the next holder of
		// the substrate reads runs before it takes one.
		if substrate.KillGroup(r.PhaseGroup, r.PhaseGroupStart) {
			r.Reason = "its runner stopped before the run finished, and the phase it left running was killed"
		} else {
			r.Reason = "its runner stopped before the run finished"
		}
		r.State, r.Outcome = StateDone, Inconclusive
		r.PhaseGroup, r.PhaseGroupStart = 0, 0
		if r.Ended.IsZero() {
			r.Ended = r.Heartbeat
		}
	}
	return r, nil
}

func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// List reads every run under root (one directory per run), oldest first.
// Retake marks a run's evidence stale. It returns the runs it marked.
//
// Only an owner can know this: a run that failed because the substrate
// was steadily wrong reproduces faithfully and is recorded as a verdict
// on the work, and nothing in the run can tell otherwise. Fixing the
// substrate does not change the heads, so without this the goal would
// never take the run again and would hand over on evidence nobody
// believes.
func Retake(root, experimentName string) ([]string, error) {
	var marked []string
	for _, r := range List(root) {
		if r.Retaken || (experimentName != "" && r.Experiment != experimentName) {
			continue
		}
		if !r.Outcome.Conclusive() {
			continue // it was never evidence
		}
		note := fmt.Sprintf("retaken: the owner declared this run's evidence stale at %s\n",
			time.Now().UTC().Format(time.RFC3339))
		if err := os.WriteFile(filepath.Join(r.Dir, retakenFile), []byte(note), 0o600); err != nil {
			return marked, err
		}
		marked = append(marked, r.ID)
	}
	return marked, nil
}

func List(root string) []Result {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []Result
	for _, en := range entries {
		if !en.IsDir() {
			continue
		}
		if r, err := Load(filepath.Join(root, en.Name())); err == nil {
			if _, statErr := os.Stat(filepath.Join(root, en.Name(), retakenFile)); statErr == nil {
				r.Retaken = true
			}
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Started.Equal(out[j].Started) {
			return out[i].Started.Before(out[j].Started)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// runner carries one Execute.
type runner struct {
	job Job
	res Result
	now func() time.Time
}

func (r *runner) save() {
	r.res.Heartbeat = r.now().UTC()
	if raw, err := json.MarshalIndent(r.res, "", "  "); err == nil {
		_ = atomicfile.Write(filepath.Join(r.job.Dir, resultFile), raw, 0o600)
	}
}

// justProvisioned reports whether making the substrate ready included a
// provision that worked — in which case its lifetime is already as long as
// this substrate's lifetimes get.
func justProvisioned(ops []substrate.Op) bool {
	for _, op := range ops {
		if op.Kind == "provision" && op.OK {
			return true
		}
	}
	return false
}

func (r *runner) end(o Outcome, reason string) Result {
	r.res.State, r.res.Outcome, r.res.Reason = StateDone, o, reason
	r.res.Ended = r.now().UTC()
	r.save()
	return r.res
}

// Execute makes the run and returns its record. It takes the substrate for
// the whole of it and lets go when it returns — or when the process dies,
// which is why the process that runs an experiment is the one that holds
// the lease.
func Execute(ctx context.Context, job Job) Result {
	r := &runner{job: job, now: time.Now}
	r.res = Result{
		ID: job.ID, Experiment: job.Experiment, Substrate: job.Def.Substrate, Owner: job.Owner,
		Purpose: job.Purpose, Heads: job.Heads, ExpectFail: job.ExpectFail,
		State: StateRunning, PID: os.Getpid(), Started: r.now().UTC(), Dir: job.Dir,
	}
	r.save()

	mgr := substrate.New(job.StateDir, job.Root, map[string]config.Substrate{job.Def.Substrate: job.Substrate})
	lease, err := mgr.Acquire(job.Def.Substrate, substrate.Holder{Who: job.Owner, Purpose: job.Purpose + " of " + job.Experiment})
	if err != nil {
		return r.end(NotRun, err.Error())
	}
	held := r.now()
	defer func() {
		lease.Release()
	}()
	finish := func(o Outcome, reason string) Result {
		r.res.Seconds = r.now().Sub(held).Seconds()
		return r.end(o, reason)
	}

	// 1. a substrate worth running on
	ops, st, err := lease.EnsureReady(ctx, r.logFile(0, "substrate"))
	r.noteOps(ops)
	if err != nil {
		// err already says it — "<name> is absent: the substrate could not
		// be made ready" — so a prefix repeats the sentence back at the
		// reader instead of framing it.
		return finish(Inconclusive, err.Error())
	}
	if !st.Fits(job.Def.Longest()) {
		// Renewing is the slowest thing gummi asks of a substrate, so it is
		// asked for only when it can change the answer. A substrate whose
		// whole lifetime is shorter than one run's worst case never fits,
		// however freshly it is provisioned — and when EnsureReady has just
		// provisioned it on the way in, a second provision is measuring the
		// same lifetime twice. The run goes ahead either way (a worst case
		// is not a forecast); what it must not do is pay for that twice.
		if !justProvisioned(ops) {
			op, could := lease.Provision(ctx, r.logFile(0, "substrate"))
			if !could {
				return finish(NotRun, fmt.Sprintf("%s expires at %s, before a run of up to %s could finish, and there is no provision command to renew it",
					job.Def.Substrate, st.ExpiresAt.Format(time.RFC3339), job.Def.Longest()))
			}
			r.noteOps([]substrate.Op{op})
			if st = lease.Status(ctx); st.State != substrate.Ready {
				return finish(Inconclusive, "the substrate was renewed before its expiry and did not come back ready")
			}
		}
	}

	// 2. attempts: a failure is believed only when it happens twice
	var first *Phase
	for attempt := 1; attempt <= 2; attempt++ {
		if op, did := lease.Reset(ctx, r.logFile(attempt, "reset")); did {
			r.noteOps([]substrate.Op{op})
			if !op.OK {
				return finish(Inconclusive, "the substrate could not be reset to its known state")
			}
		}
		if job.Control && strings.TrimSpace(job.Def.Control) != "" && attempt == 1 {
			if ph := r.phase(ctx, attempt, "control", job.Def.Control); !ph.OK {
				r.res.ControlFailed = true
				return finish(Inconclusive, "the rig failed its own control, so it can judge nothing: "+lastLine(ph.Tail))
			}
			// What the control asserted is as much the question as whether
			// it passed: a control that reported nothing held has not shown
			// the rig can turn anything green.
			r.res.ControlRan = true
			r.res.ControlProved = len(readAssertions(filepath.Join(r.job.Dir, "evidence", "results.ndjson")))
			if op, did := lease.Reset(ctx, r.logFile(attempt, "reset")); did {
				// the control leaves the rig's own reference topology on
				// the substrate, so putting it back costs a reset cycle
				// like any other and is charged like one
				r.noteOps([]substrate.Op{op})
				if !op.OK {
					return finish(Inconclusive, "the substrate could not be reset after its control")
				}
			}
		}
		failed, tempfail := r.attempt(ctx, attempt)
		r.collect(ctx, attempt)
		switch {
		case tempfail != nil:
			return finish(Inconclusive, fmt.Sprintf("%s said the run could not be judged (exit %d): %s", tempfail.Name, ExitTempFail, lastLine(tempfail.Tail)))
		case failed == nil && first == nil:
			return finish(Pass, r.passReason())
		case failed == nil:
			r.res.Flaky, r.res.FailedPhase = true, first.Name
			return finish(Inconclusive, fmt.Sprintf("%s failed once and passed on a reset substrate — flaky, so it judged nothing", first.Name))
		case failed.Name == "run" && r.wantedHeld():
			// Everything this goal is about held. What did not hold is
			// outside what anyone agreed it would do, so there is nothing
			// here to reproduce, escalate, or call a failure.
			r.res.Scoped = true
			return finish(Pass, r.scopedReason())
		case first == nil:
			first = failed
			if ctx.Err() != nil {
				return finish(Inconclusive, "the run was stopped")
			}
		case failed.Name != first.Name:
			r.res.Flaky, r.res.FailedPhase = true, first.Name
			return finish(Inconclusive, fmt.Sprintf("it failed in %s and then in %s — two different failures are not one verdict", first.Name, failed.Name))
		default:
			r.res.FailedPhase = failed.Name
			return finish(Fail, fmt.Sprintf("%s failed twice, the second time on a reset substrate (exit %d): %s", failed.Name, failed.Exit, lastLine(failed.Tail)))
		}
	}
	return finish(Inconclusive, "the run ended without a verdict")
}

// attempt runs the experiment's phases once. It returns the phase that
// failed, or the phase that said the run could not be judged.
func (r *runner) attempt(ctx context.Context, n int) (failed, tempfail *Phase) {
	for _, p := range r.job.Def.Phases() {
		ph := r.phase(ctx, n, p[0], p[1])
		if ph.Exit == ExitTempFail {
			return nil, &ph
		}
		if !ph.OK {
			return &ph, nil
		}
	}
	return nil, nil
}

func (r *runner) collect(ctx context.Context, attempt int) {
	r.res.Assertions = readAssertions(filepath.Join(r.job.Dir, "evidence", "results.ndjson"))
	if strings.TrimSpace(r.job.Def.Collect) != "" {
		r.phase(ctx, attempt, "collect", r.job.Def.Collect)
	}
	r.save()
}

// wantedHeld reports that every assertion the owner cited was reported by
// this run and held. An assertion nobody reported is not held: a run that
// never mentioned it proves nothing about it.
func (r *runner) wantedHeld() bool {
	if len(r.job.Wanted) == 0 {
		return false
	}
	by := make(map[string]bool, len(r.res.Assertions))
	for _, a := range r.res.Assertions {
		by[a.ID] = a.OK
	}
	for _, id := range r.job.Wanted {
		if ok, reported := by[id]; !reported || !ok {
			return false
		}
	}
	return true
}

func (r *runner) scopedReason() string {
	ok, total := r.res.Passed()
	return fmt.Sprintf("every assertion this goal is about held (%d of them); %d of %d overall, and what did not hold is outside what it agreed to do",
		len(r.job.Wanted), ok, total)
}

func (r *runner) passReason() string {
	if ok, total := r.res.Passed(); total > 0 {
		return fmt.Sprintf("%d of %d assertions held", ok, total)
	}
	return "every phase passed"
}

func (r *runner) noteOps(ops []substrate.Op) {
	for _, op := range ops {
		word := "ok"
		if !op.OK {
			word = "failed"
		}
		r.res.Ops = append(r.res.Ops, fmt.Sprintf("%s %s (%s)", op.Kind, word, op.Took.Round(time.Second)))
	}
	r.save()
}

func (r *runner) logFile(attempt int, name string) *os.File {
	dir := filepath.Join(r.job.Dir, "log")
	_ = os.MkdirAll(dir, 0o750)
	f, err := os.OpenFile(filepath.Join(dir, fmt.Sprintf("%d-%s.log", attempt, name)), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil
	}
	return f
}

func (r *runner) phase(ctx context.Context, attempt int, name, cmd string) Phase {
	start := r.now()
	ph := Phase{Name: name, Attempt: attempt, Log: filepath.Join("log", fmt.Sprintf("%d-%s.log", attempt, name))}
	var out string
	var err error
	note := func(pgid int, gstart uint64) {
		r.res.PhaseGroup, r.res.PhaseGroupStart = pgid, gstart
		r.save()
	}
	if f := r.logFile(attempt, name); f != nil {
		out, ph.Exit, err = substrate.RunShell(ctx, r.job.Root, cmd, r.job.Def.PhaseTimeout(), r.env(attempt), f, note)
		_ = f.Close()
	} else {
		out, ph.Exit, err = substrate.RunShell(ctx, r.job.Root, cmd, r.job.Def.PhaseTimeout(), r.env(attempt), nil, note)
	}
	r.res.PhaseGroup, r.res.PhaseGroupStart = 0, 0
	ph.OK = err == nil && ph.Exit == 0
	ph.Seconds = r.now().Sub(start).Seconds()
	ph.Tail = tailLines(out, 12)
	if err != nil && ph.Tail == "" {
		ph.Tail = err.Error()
	}
	r.res.Phases = append(r.res.Phases, ph)
	r.save()
	return ph
}

func (r *runner) env(attempt int) []string {
	env := []string{
		"GUMMI_SUBSTRATE=" + r.job.Def.Substrate,
		"GUMMI_EXPERIMENT=" + r.job.Experiment,
		"GUMMI_RUN=" + r.job.ID,
		"GUMMI_PURPOSE=" + r.job.Purpose,
		"GUMMI_EVIDENCE=" + filepath.Join(r.job.Dir, "evidence"),
		fmt.Sprintf("GUMMI_ATTEMPT=%d", attempt),
	}
	for repo, dir := range r.job.Trees {
		env = append(env, "GUMMI_TREE_"+envName(repo)+"="+dir)
	}
	for repo, sha := range r.job.Heads {
		env = append(env, "GUMMI_HEAD_"+envName(repo)+"="+sha)
	}
	sort.Strings(env)
	return env
}

// envName spells a repository name as an environment variable suffix. The
// unnamed default repository is HOME — the goal's home tree.
func envName(repo string) string {
	if repo == "" {
		return "HOME"
	}
	var b strings.Builder
	for _, c := range strings.ToUpper(repo) {
		if (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			b.WriteRune(c)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

func readAssertions(path string) []Assertion {
	f, err := os.Open(path) //nolint:gosec // inside the run's own directory
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []Assertion
	at := map[string]int{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		var a Assertion
		if json.Unmarshal(sc.Bytes(), &a) != nil || strings.TrimSpace(a.ID) == "" {
			continue
		}
		// a second attempt appends to the same file: the newest word on an
		// assertion is the one that stands
		if i, seen := at[a.ID]; seen {
			out[i] = a
			continue
		}
		at[a.ID] = len(out)
		out = append(out, a)
	}
	return out
}

func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if s == "" {
		return "no output"
	}
	return s
}
