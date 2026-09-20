package engine

// Experiments, seen from the engine. internal/experiment makes one run and
// says what it proved; this file decides what a run is ABOUT — the heads of
// a goal's trees — starts one in a process of its own, and reads back what
// the runs a goal has made say about the heads it has now.
//
// The files are the record. A run directory holds the job, the result as
// it is written, a log per phase and the evidence, so nothing here keeps a
// run in memory: a gummi that restarts, or a second gummi on the same
// workspace, finds every run where the runner left it.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/morphis/gummi/internal/atomicfile"
	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/experiment"
	"github.com/morphis/gummi/internal/goalpolicy"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/substrate"
	"github.com/morphis/gummi/internal/verify"
	"github.com/morphis/gummi/internal/worktree"
)

// Purposes a goal makes a run for.
const (
	// PurposeVerify: the goal's work has settled, and an item it serves
	// has no evidence about the heads the goal has now.
	PurposeVerify = "verify"
	// PurposeIntegration: cards have landed since the last run; the goal is
	// finding out early rather than at the end.
	PurposeIntegration = "integration"
	// PurposeNegativeControl: the trunk, which the experiment must fail on.
	PurposeNegativeControl = "negative-control"
	// PurposeBisect: one landing of several, to find which broke what was
	// green.
	PurposeBisect = "bisect"
	// PurposeCard prefixes the purpose of a run made for one card, on the
	// goal's heads with that card's branch in place of the goal's.
	PurposeCard = "card"
)

// ExperimentSpawner starts the process that makes a prepared run. The
// default spawns `gummi __experiment`; tests run it in-process.
type ExperimentSpawner func(dir string) error

// SetExperimentSpawner replaces how runs are started.
func (e *Engine) SetExperimentSpawner(fn ExperimentSpawner) { e.spawnExperiment = fn }

// spawnDetached starts the runner in a session of its own, so it outlives
// this process and holds the substrate lease itself (cmd/gummi/experiment.go
// says why both matter).
func spawnDetached(dir string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	logf, err := os.OpenFile(filepath.Join(dir, "runner.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := exec.Command(self, "__experiment", "--dir", dir) //nolint:gosec // our own binary, a directory we made
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }() // reap it if we are still here when it ends
	return nil
}

// goalInputs names the state a run of def would be about: for each input
// repository, the commit it has out and the repository that commit is in.
// A repository the goal has a tree in contributes that tree's head; one it
// never touched contributes its own checkout's, because the experiment
// deploys it either way and evidence that did not say which commit would be
// evidence about nothing in particular.
func (e *Engine) goalInputs(ctx context.Context, goal domain.Feature, def config.Experiment) (heads, roots map[string]string, err error) {
	goalTrees, err := e.goalTrees(ctx, goal)
	if err != nil {
		return nil, nil, err
	}
	byRepo := map[string]worktree.GoalTree{}
	for _, t := range goalTrees {
		if t.Exists() {
			byRepo[t.Repo] = t
		}
	}
	repos := def.Inputs
	if len(repos) == 0 {
		for r := range byRepo {
			repos = append(repos, r)
		}
		sort.Strings(repos)
	}
	heads, roots = map[string]string{}, map[string]string{}
	for _, repo := range repos {
		root, ok := e.pool.RootForName(repo)
		if !ok {
			return nil, nil, fmt.Errorf("the experiment's input %q is not a repository this workspace manages", repo)
		}
		dir := root
		if t, ok := byRepo[repo]; ok {
			dir = t.Dir
		}
		sha, herr := worktree.HeadOf(ctx, dir)
		if herr != nil {
			return nil, nil, fmt.Errorf("reading the head of %s: %w", dir, herr)
		}
		heads[repo], roots[repo] = sha, root
	}
	return heads, roots, nil
}

// trunkInputs is goalInputs for the trunk: every input at its repository's
// own checkout. It is what a negative control runs on.
func (e *Engine) trunkInputs(ctx context.Context, goal domain.Feature, def config.Experiment) (heads, roots map[string]string, err error) {
	_, roots, err = e.goalInputs(ctx, goal, def)
	if err != nil {
		return nil, nil, err
	}
	heads = map[string]string{}
	for repo, root := range roots {
		sha, herr := worktree.HeadOf(ctx, root)
		if herr != nil {
			return nil, nil, herr
		}
		heads[repo] = sha
	}
	return heads, roots, nil
}

// snapshotInputs checks each head out, detached, under the run directory.
// A run deploys from these and never from a goal tree: a goal tree is what
// cards land on, and a landing under a running deploy would make the run
// evidence about a commit it only half deployed.
func snapshotInputs(ctx context.Context, runDir string, heads, roots map[string]string) (map[string]string, error) {
	trees := map[string]string{}
	for repo, sha := range heads {
		name := repo
		if name == "" {
			name = "home"
		}
		dir := filepath.Join(runDir, "trees", name)
		if err := os.MkdirAll(filepath.Dir(dir), 0o750); err != nil {
			return nil, err
		}
		if err := worktree.AddDetached(ctx, roots[repo], dir, sha); err != nil {
			for r, d := range trees {
				worktree.RemoveDetached(ctx, roots[r], d)
			}
			return nil, err
		}
		trees[repo] = dir
	}
	return trees, nil
}

// reapExperimentTrees removes the snapshots of runs that are over. The
// evidence stays; the checkouts were only ever somewhere to deploy from,
// and on a repository of any size they are most of what a run weighs.
func (e *Engine) reapExperimentTrees(ctx context.Context, runs []experiment.Result) {
	for _, r := range runs {
		if r.State == experiment.StateRunning {
			continue
		}
		if _, err := os.Stat(filepath.Join(r.Dir, "trees")); err != nil {
			continue
		}
		job, err := experiment.LoadJob(r.Dir)
		if err != nil {
			continue
		}
		for repo, dir := range job.Trees {
			if root := job.Roots[repo]; root != "" && strings.HasPrefix(dir, r.Dir) {
				worktree.RemoveDetached(ctx, root, dir)
			}
		}
		_ = os.RemoveAll(filepath.Join(r.Dir, "trees"))
	}
}

// ExperimentRuns lists every run made for owner, oldest first.
func (e *Engine) ExperimentRuns(owner domain.FeatureID) []experiment.Result {
	return experiment.List(e.cfg.Workspace.EvidenceDir(owner))
}

// ExperimentStart describes a run to make.
type ExperimentStart struct {
	Name    string
	Purpose string
	// Control runs the experiment's positive control first.
	Control bool
	// Trunk runs on the trunk instead of the goal's trees, expecting the
	// experiment to fail there.
	Trunk bool
	// Heads overrides what the run is about: a bisect step, or a card's
	// own branch in place of the goal's. Every repository it names must be
	// an input of the experiment.
	Heads map[string]string
	// Card is the card a run is for, when it is for one.
	Card domain.FeatureID
}


// goalWanted is the assertions this goal's done-when items cite for one
// experiment: what its runs are actually judged on.
//
// An item proved by the experiment that names no assertions is about the
// whole run — so one such item makes the whole run wanted, and the run is
// judged exactly as it always was. Only a goal that has said, item by
// item, which assertions it is about gets judged on those.
func (e *Engine) goalWanted(goal domain.Feature, name string) []string {
	path := e.artifactFile(&goal)
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	items, _, err := spec.ParseDoneWhen(string(raw))
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, it := range items {
		if it.Experiment != name {
			continue
		}
		if len(it.Assertions) == 0 {
			return nil // an item about the whole run makes the run whole
		}
		for _, a := range it.Assertions {
			if !seen[a] {
				seen[a] = true
				out = append(out, a)
			}
		}
	}
	return out
}

// StartExperiment prepares a run for goal and starts its runner. It
// returns as soon as the runner is started: the run is read back from its
// directory (ExperimentRuns) by whoever ticks next.
func (e *Engine) StartExperiment(ctx context.Context, goal domain.Feature, st ExperimentStart) (experiment.Result, error) {
	cfg, err := e.layeredConfig()
	if err != nil {
		return experiment.Result{}, err
	}
	def, ok := cfg.Experiments[st.Name]
	if !ok {
		return experiment.Result{}, fmt.Errorf("no experiment %q is configured", st.Name)
	}
	// One run at a time per goal (§17.9). The conductor's own guard is the
	// snapshot it ticked on, and a tick that began before the previous
	// run's record was written sees no run in flight and orders another —
	// which then spends its whole existence being refused the lease it was
	// made to take. The directory is the record, so it is what decides.
	for _, r := range e.ExperimentRuns(goal.ID) {
		if r.Experiment == st.Name && r.State == experiment.StateRunning {
			return experiment.Result{}, fmt.Errorf("a run of %s is already in flight for %s (%s)", st.Name, goal.ID, r.ID)
		}
	}
	var heads, roots map[string]string
	if st.Trunk {
		heads, roots, err = e.trunkInputs(ctx, goal, def)
	} else {
		heads, roots, err = e.goalInputs(ctx, goal, def)
	}
	if err != nil {
		return experiment.Result{}, err
	}
	for repo, sha := range st.Heads {
		if _, ok := heads[repo]; !ok {
			return experiment.Result{}, fmt.Errorf("%q is not an input of %s", repo, st.Name)
		}
		heads[repo] = sha
	}
	now := e.now()
	job := experiment.Job{
		ID: experiment.NewID(now), Experiment: st.Name, Owner: string(goal.ID), Purpose: st.Purpose,
		Heads: heads, Roots: roots, Control: st.Control, ExpectFail: st.Trunk,
		Def: def, Substrate: cfg.Substrates[def.Substrate],
		Wanted: e.goalWanted(goal, st.Name),
		Root:   e.cfg.Workspace.Root, StateDir: e.cfg.Workspace.StateDir(),
	}
	if st.Card != "" {
		job.Purpose = PurposeCard + " " + string(st.Card)
	}
	job.Dir = filepath.Join(e.cfg.Workspace.EvidenceDir(goal.ID), job.ID)
	if job.Trees, err = snapshotInputs(ctx, job.Dir, heads, roots); err != nil {
		return experiment.Result{}, err
	}
	if err := experiment.Prepare(job); err != nil {
		return experiment.Result{}, err
	}
	// The run exists from this moment, before its runner has written a
	// word: a tick that lands in between must see a run in flight, not an
	// absence it would answer by starting a second one.
	res := experiment.Result{
		ID: job.ID, Experiment: job.Experiment, Substrate: def.Substrate, Owner: job.Owner, Purpose: job.Purpose,
		Heads: heads, ExpectFail: job.ExpectFail, State: experiment.StateRunning, PID: os.Getpid(),
		Started: now.UTC(), Heartbeat: now.UTC(), Dir: job.Dir,
	}
	if err := writeExperimentResult(res); err != nil {
		return res, err
	}
	spawn := e.spawnExperiment
	if spawn == nil {
		spawn = spawnDetached
	}
	if err := spawn(job.Dir); err != nil {
		res.State, res.Outcome, res.Ended = experiment.StateDone, experiment.NotRun, e.now().UTC()
		res.Reason = "its runner could not be started: " + err.Error()
		_ = writeExperimentResult(res)
		return res, err
	}
	e.forgetSubstrate(def.Substrate)
	return res, nil
}

func writeExperimentResult(r experiment.Result) error {
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(r.Dir, "result.json"), raw, 0o600)
}

// GoalExperiment is one experiment a goal's done-when items name, and what
// the goal's runs of it say about the heads the goal has now.
type GoalExperiment struct {
	Name      string
	Substrate string
	Items     []string // the done-when ids it proves
	// Problem is set when the experiment cannot be run at all — it is not
	// configured, or an input is not a repository here.
	Problem string
	// Heads is what a run made now would be about.
	Heads map[string]string
	// Running is the run in flight, if any.
	Running *experiment.Result
	// Evidence is the newest conclusive run about Heads, nil when there is
	// none — the heads moved, or nothing conclusive has run on them yet.
	Evidence *experiment.Result
	// Inconclusive counts the runs about Heads since the last conclusive
	// one (and since a person was last here) that judged nothing, and
	// LastReason is the newest of them saying why.
	Inconclusive int
	LastReason   string
	// ControlFailed reports that the newest such run failed the rig's own
	// control.
	ControlFailed bool
	Runs          []experiment.Result

	// Trunk is the newest conclusive run on the trunk — the negative
	// control — nil when none has been made.
	Trunk *experiment.Result
	// Green is the frontier: every assertion that has held in some
	// conclusive run of the goal's heads, at any point. It should only
	// grow. Regressed is what is in it and does not hold in Evidence.
	Green     []string
	Regressed []string
	// WholeRegressed reports a regression of a run that names no
	// assertions: it passed before and fails now.
	WholeRegressed bool
	// LandedSince lists the cards landed since the newest conclusive run.
	LandedSince []domain.FeatureID
	// Suspects are the landings between the run where what regressed last
	// held and the one where it did not, oldest first; BisectNext indexes
	// the one to try next (-1: none), Culprit is the one found, and
	// BisectStuck reports verdicts that contradict each other.
	Suspects    []goalLanding
	BisectNext  int
	Culprit     domain.FeatureID
	BisectStuck bool
	// base is the run what regressed last held in.
	base *experiment.Result
	// knownAt is when the newest fact about the regression arrived.
	knownAt time.Time
}

// goalLanding is one card's landing on the goal branch, from the goal log.
type goalLanding struct {
	Card domain.FeatureID
	Repo string
	SHA  string
	At   time.Time
}

// headsAfter is the goal's heads as they were just after suspect i landed:
// the base run's, moved by each landing up to it. Every landing is one
// commit on its repository's goal branch, which is what makes a goal's
// history bisectable at all.
func (x GoalExperiment) headsAfter(i int) map[string]string {
	heads := map[string]string{}
	if x.base != nil {
		for repo, sha := range x.base.Heads {
			heads[repo] = sha
		}
	}
	for j := 0; j <= i && j < len(x.Suspects); j++ {
		if _, input := heads[x.Suspects[j].Repo]; input {
			heads[x.Suspects[j].Repo] = x.Suspects[j].SHA
		}
	}
	return heads
}

func sameHeads(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// readFrontier fills in what the goal's runs say over time: what has ever
// held, what no longer does, and how far a bisect of the landings in
// between has got.
func (x *GoalExperiment) readFrontier(landings []goalLanding) {
	x.BisectNext = -1
	green := map[string]bool{}
	var newest *experiment.Result
	for i := range x.Runs {
		r := &x.Runs[i]
		if r.ExpectFail || !r.Outcome.Conclusive() || strings.HasPrefix(r.Purpose, PurposeCard+" ") {
			continue
		}
		for _, a := range r.Assertions {
			if a.OK {
				green[a.ID] = true
			}
		}
		if r.Purpose != PurposeBisect {
			newest = r
		}
	}
	for id := range green {
		x.Green = append(x.Green, id)
	}
	sort.Strings(x.Green)
	for _, l := range landings {
		if newest == nil || l.At.After(newest.Started) {
			x.LandedSince = append(x.LandedSince, l.Card)
		}
	}

	ev := x.Evidence
	if ev == nil || ev.Outcome != experiment.Fail {
		return
	}
	for _, a := range ev.Assertions {
		if !a.OK && green[a.ID] {
			x.Regressed = append(x.Regressed, a.ID)
		}
	}
	held := func(r *experiment.Result) (bool, bool) { return r.Holds(x.Regressed) }
	if len(ev.Assertions) == 0 {
		held = func(r *experiment.Result) (bool, bool) { return r.Holds(nil) }
	} else if len(x.Regressed) == 0 {
		return
	}
	// the newest earlier run in which all of it held
	for i := range x.Runs {
		r := &x.Runs[i]
		if r.ExpectFail || r.ID == ev.ID || !r.Started.Before(ev.Started) || strings.HasPrefix(r.Purpose, PurposeCard+" ") {
			continue
		}
		if h, ok := held(r); ok && h {
			x.base = r
		}
	}
	if x.base == nil {
		x.Regressed = nil // it never held as a whole: nothing regressed
		return
	}
	x.WholeRegressed = len(ev.Assertions) == 0
	x.knownAt = ev.Ended
	for _, l := range landings {
		if !l.At.After(x.base.Started) || l.At.After(ev.Started) {
			continue
		}
		// A landing in a repository the experiment does not deploy cannot
		// have changed what the run saw, and it is not a candidate. Left
		// in, it is worse than noise: headsAfter leaves the head tuple
		// unmoved across it, so it shares a tuple with the landing before
		// it, one run answers for both, and the bisect can name a card
		// that could not have done it.
		if _, input := x.Heads[l.Repo]; !input {
			continue
		}
		x.Suspects = append(x.Suspects, l)
	}
	verdicts := map[int]bool{}
	for i := range x.Suspects {
		want := x.headsAfter(i)
		for j := range x.Runs {
			r := &x.Runs[j]
			if r.ExpectFail || !sameHeads(r.Heads, want) {
				continue
			}
			if h, ok := held(r); ok {
				verdicts[i] = h
				if r.Ended.After(x.knownAt) {
					x.knownAt = r.Ended
				}
			}
		}
	}
	next, culprit := goalpolicy.Bisect(len(x.Suspects), verdicts)
	switch {
	case culprit >= 0:
		x.Culprit = x.Suspects[culprit].Card
	case next >= 0:
		x.BisectNext = next
	default:
		x.BisectStuck = len(x.Suspects) > 0
	}
}

// readLiveProof reads how far a live card's proof has got: the runs made
// for it, about the commit its branch has now. A card sent back and
// verified again has a new head, and what was proven about the old one
// says nothing about it.
func (e *Engine) readLiveProof(ctx context.Context, gc *GoalCard, runs []experiment.Result, since time.Time) {
	m, err := e.pool.ManagerFor(ctx, &gc.Feature)
	if err != nil {
		return
	}
	head, err := m.Head(ctx, &gc.Feature)
	if err != nil {
		return
	}
	gc.LiveHead = head
	purpose := PurposeCard + " " + string(gc.Feature.ID)
	inconclusive := 0
	for _, r := range runs {
		if r.Purpose != purpose || r.Heads[gc.Feature.Repo] != head {
			continue
		}
		switch {
		case r.State == experiment.StateRunning:
			gc.LiveProof = goalpolicy.LiveRunning
			return
		case r.Outcome == experiment.Pass:
			gc.LiveProof, gc.LiveWhy = goalpolicy.LivePassed, ""
			inconclusive = 0
		case r.Outcome == experiment.Fail:
			gc.LiveProof, gc.LiveWhy = goalpolicy.LiveFailed, describeEvidence(r, nil)
			inconclusive = 0
		case r.Ended.After(since):
			inconclusive++
			if gc.LiveProof == goalpolicy.LiveNone && inconclusive >= goalpolicy.MaxInconclusive {
				gc.LiveProof, gc.LiveWhy = goalpolicy.LiveGaveUp, r.Reason
			}
		}
	}
}

// trunkGaveUp reports that the goal has tried its negative control as often
// as it tries anything that judges nothing, and stops asking: a pass handed
// over without one says so (the item's evidence carries no trunk line)
// rather than the goal spending the runs it holds back on a rig that will
// not answer.
func trunkGaveUp(x GoalExperiment) bool {
	n := 0
	for _, r := range x.Runs {
		if r.ExpectFail && r.State != experiment.StateRunning && !r.Outcome.Conclusive() {
			n++
		}
	}
	return n >= goalpolicy.MaxInconclusive
}

// regressionWhy is the sentence a lead is woken with.
func (x GoalExperiment) regressionWhy() string {
	what := strings.Join(x.Regressed, ", ")
	if x.WholeRegressed {
		what = "the run as a whole"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "a regression in %s: %s held in run %s and does not in run %s, on the goal's current heads. ", x.Name, what, x.base.ID, x.Evidence.ID)
	switch {
	case x.Culprit != "":
		fmt.Fprintf(&b, "Bisecting the %d landings between them puts it on %s: it held just before that card landed and not just after. "+
			"Send %s's work back as a new card that fixes it (card_create), citing the evidence in %s.", len(x.Suspects), x.Culprit, x.Culprit, x.Evidence.Dir)
	case x.BisectStuck:
		b.WriteString("Bisecting the landings between them gave verdicts that contradict each other — it held after a landing it had failed before — so no single card can be blamed and the rig itself may be flaky. ")
		fmt.Fprintf(&b, "The landings in question: %s. Evidence in %s.", landingIDs(x.Suspects), x.Evidence.Dir)
	default:
		fmt.Fprintf(&b, "The landings between them: %s. There was no substrate budget to narrow it further. Evidence in %s.", landingIDs(x.Suspects), x.Evidence.Dir)
	}
	return b.String()
}

func landingIDs(ls []goalLanding) string {
	ids := make([]string, 0, len(ls))
	for _, l := range ls {
		ids = append(ids, string(l.Card))
	}
	return strings.Join(ids, ", ")
}

// goalExperiments reads the experiments goal's items name against its
// runs. since bounds the inconclusive count: runs that ended before it
// were seen by a person who chose to carry on.
func (e *Engine) goalExperiments(ctx context.Context, goal domain.Feature, items []domain.DoneWhen, since int64, landings []goalLanding) []GoalExperiment {
	byName := map[string]*GoalExperiment{}
	var order []string
	for _, it := range items {
		name := strings.TrimSpace(it.Experiment)
		if name == "" {
			continue
		}
		if byName[name] == nil {
			byName[name] = &GoalExperiment{Name: name}
			order = append(order, name)
		}
		byName[name].Items = append(byName[name].Items, it.ID)
	}
	if len(order) == 0 {
		return nil
	}
	cfg, cfgErr := e.layeredConfig()
	runs := e.ExperimentRuns(goal.ID)
	out := make([]GoalExperiment, 0, len(order))
	for _, name := range order {
		x := *byName[name]
		def, ok := cfg.Experiments[name]
		switch {
		case cfgErr != nil:
			x.Problem = cfgErr.Error()
		case !ok:
			x.Problem = fmt.Sprintf("no experiment %q is configured", name)
		default:
			x.Substrate = def.Substrate
			heads, _, err := e.goalInputs(ctx, goal, def)
			if err != nil {
				x.Problem = err.Error()
			}
			x.Heads = heads
		}
		for i := range runs {
			r := runs[i]
			if r.Experiment != name {
				continue
			}
			x.Runs = append(x.Runs, r)
			if r.ExpectFail {
				// a control is about the trunk, not about the goal
				if r.Outcome.Conclusive() {
					x.Trunk = &runs[i]
				}
				continue
			}
			if r.State == experiment.StateRunning {
				x.Running = &runs[i]
				continue
			}
			if !r.About(x.Heads) {
				continue
			}
			switch {
			case r.Outcome.Conclusive():
				x.Evidence, x.Inconclusive, x.LastReason, x.ControlFailed = &runs[i], 0, "", false
			case r.Ended.UnixNano() > since:
				x.Inconclusive++
				x.LastReason, x.ControlFailed = r.Reason, r.ControlFailed
			}
		}
		x.readFrontier(landings)
		out = append(out, x)
	}
	return out
}

// experimentCheckResults turns a goal's experiment items into check
// results, so that everything downstream of a goal's checks — the verify
// kickoff, the verdict floor, the hand-over — treats an item proved by an
// experiment exactly as it treats one proved by a command.
func experimentCheckResults(items []domain.DoneWhen, xs []GoalExperiment) []goalItemResult {
	byName := map[string]GoalExperiment{}
	for _, x := range xs {
		byName[x.Name] = x
	}
	var out []goalItemResult
	for _, it := range items {
		if strings.TrimSpace(it.Experiment) == "" {
			continue
		}
		x := byName[it.Experiment]
		res := goalItemResult{Item: it, Experiment: x}
		switch {
		case x.Problem != "":
			res.Detail = x.Problem
		case x.Evidence == nil:
			res.Detail = "no conclusive run of " + x.Name + " is about the goal's current heads"
			if x.LastReason != "" {
				res.Detail += " — the newest judged nothing: " + x.LastReason
			}
		default:
			held, ok := x.Evidence.Holds(it.Assertions)
			res.Run, res.Known, res.Held = x.Evidence, ok, held
			res.Detail = describeEvidence(*x.Evidence, it.Assertions)
			if held && x.Trunk != nil {
				// honest over optimistic: a pass is worth what the same run
				// says about the trunk
				if onTrunk, tok := x.Trunk.Holds(it.Assertions); tok && onTrunk {
					res.Detail += " — NOTE: run " + x.Trunk.ID + " shows this holding on the trunk too, so it is not this goal's work that made it true"
				} else if tok {
					res.Detail += " — and run " + x.Trunk.ID + " shows it NOT holding on the trunk"
				}
			}
			if !ok {
				res.Detail = "run " + x.Evidence.ID + " passed without reporting " + strings.Join(it.Assertions, ", ")
			}
		}
		out = append(out, res)
	}
	return out
}

// experimentCmdPrefix marks a check result that stands for an experiment
// item. It is never run: nothing that starts with it is a command.
const experimentCmdPrefix = "experiment: "

// goalExperimentResults reads a goal's experiment items against its runs
// and returns them as check results, named as a commanded item's check is.
func (e *Engine) goalExperimentResults(ctx context.Context, goal domain.Feature, doc string) []verify.Result {
	items, _, err := spec.ParseDoneWhen(doc)
	if err != nil {
		return nil
	}
	var out []verify.Result
	for _, r := range experimentCheckResults(items, e.goalExperiments(ctx, goal, items, 0, nil)) {
		res := verify.Result{Name: r.Item.CheckName(), Cmd: experimentCmdPrefix + r.Item.Experiment, Output: r.Detail}
		switch {
		case !r.Known:
			res.Status = verify.StatusNotRun
		case r.Held:
			res.Status, res.OK = verify.StatusPass, true
		default:
			res.Status, res.ExitCode = verify.StatusFail, 1
		}
		out = append(out, res)
	}
	return out
}

// experimentEvidence is what a recorded check result keeps of an
// experiment item's evidence, "" for an ordinary command's.
func experimentEvidence(r verify.Result) string {
	if strings.HasPrefix(r.Cmd, experimentCmdPrefix) {
		return r.Output
	}
	return ""
}

// goalItemResult is one experiment item against the evidence.
type goalItemResult struct {
	Item       domain.DoneWhen
	Experiment GoalExperiment
	Run        *experiment.Result
	// Known is false when no run has an opinion on the item; Held is what
	// the opinion is.
	Known, Held bool
	Detail      string
}

// describeEvidence is the sentence a hand-over shows for an item: what
// held, and where the evidence is.
func describeEvidence(r experiment.Result, assertions []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "run %s: %s", r.ID, r.Outcome)
	if ok, total := r.Passed(); total > 0 {
		fmt.Fprintf(&b, ", %d of %d assertions held", ok, total)
	}
	if len(assertions) > 0 {
		var failed, silent []string
		by := map[string]experiment.Assertion{}
		for _, a := range r.Assertions {
			by[a.ID] = a
		}
		for _, id := range assertions {
			a, reported := by[id]
			switch {
			case !reported:
				silent = append(silent, id)
			case !a.OK:
				failed = append(failed, strings.TrimSpace(id+" "+a.Detail))
			}
		}
		if len(failed) > 0 {
			b.WriteString(" — not held: " + strings.Join(failed, "; "))
		}
		// An assertion the run never mentioned is not an assertion that
		// failed, and the difference is the whole difference between work
		// that is not finished and an item nothing can observe. Without
		// this the two read identically — the same sentence, word for word
		// — so nobody, lead or owner, has anything to notice until the
		// hand-over says "not met" about a statement no run could ever
		// have proved.
		if len(silent) > 0 && len(r.Assertions) > 0 {
			b.WriteString(" — NOT REPORTED by the run at all: " + strings.Join(silent, ", ") +
				" (the experiment does not observe them; the item cannot hold as written)")
		}
	} else if r.Outcome == experiment.Fail {
		b.WriteString(" — " + r.Reason)
	}
	b.WriteString(" — evidence in " + evidencePath(r.Dir))
	return b.String()
}

// evidencePath is a run directory as a reader can hold it: from the
// workspace down. The absolute path is a container temp directory that can
// be a hundred characters of nothing, and at eighty columns it spends four
// wrapped lines of every item's evidence line saying where the workspace
// is — which the reader already knows, being in it.
func evidencePath(dir string) string {
	const mark = "/.gummi/evidence/"
	if i := strings.Index(dir, mark); i >= 0 {
		return dir[i+1:]
	}
	return dir
}

// goalExperimentProblem is the plan gate's question about experiment
// items: is each one's experiment something this workspace can run. An item
// proved by an experiment nobody configured is an item nothing can check,
// which is the thing the gate exists to refuse.
func (e *Engine) goalExperimentProblem(items []domain.DoneWhen) string {
	var cfg config.Config
	loaded := false
	for _, it := range items {
		name := strings.TrimSpace(it.Experiment)
		if name == "" {
			continue
		}
		if !loaded {
			var err error
			if cfg, err = e.layeredConfig(); err != nil {
				return "the config cannot be read, so " + it.ID + "'s experiment cannot be resolved: " + err.Error()
			}
			loaded = true
		}
		def, ok := cfg.Experiments[name]
		if !ok {
			var names []string
			for n := range cfg.Experiments {
				names = append(names, n)
			}
			sort.Strings(names)
			if len(names) == 0 {
				return fmt.Sprintf("%s is proved by the experiment %q, and this workspace configures none — an operator adds it under `experiments:` in .gummi/config.yaml", it.ID, name)
			}
			return fmt.Sprintf("%s is proved by the experiment %q, which is not configured — use one of %s", it.ID, name, strings.Join(names, ", "))
		}
		for _, repo := range def.Inputs {
			if e.pool != nil && !e.pool.Known(repo) {
				return fmt.Sprintf("%s's experiment %q takes the repository %q as an input, which is not configured", it.ID, name, repo)
			}
		}
	}
	return ""
}

// goalExperimentNames lists the experiments a goal's items are proved by.
func (e *Engine) goalExperimentNames(goal domain.Feature) []string {
	path := e.artifactFile(&goal)
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	items, _, err := spec.ParseDoneWhen(string(raw))
	if err != nil {
		return nil
	}
	return experimentNames(items)
}

// experimentNames lists the experiments items name, in the order they first
// appear.
func experimentNames(items []domain.DoneWhen) []string {
	var names []string
	seen := map[string]bool{}
	for _, it := range items {
		if n := strings.TrimSpace(it.Experiment); n != "" && !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	return names
}

// describeHeads spells a head tuple for a log line: repo@short-sha, the
// unnamed default repository as "home".
func describeHeads(heads map[string]string) string {
	var parts []string
	for repo, sha := range heads {
		if repo == "" {
			repo = "home"
		}
		parts = append(parts, repo+"@"+shortSHA(sha))
	}
	sort.Strings(parts)
	return strings.Join(parts, " ")
}

// substrateHeld reports that someone has name right now. Asking is free —
// a held substrate is never probed.
func (e *Engine) substrateHeld(ctx context.Context, name string) bool {
	m, err := e.Substrates()
	if err != nil || !m.Has(name) {
		return false
	}
	st, err := e.substrateStatus(ctx, m, name)
	return err == nil && st.State == substrate.Held
}

// needsControl reports whether a run of name for owner should prove the
// rig first: when no run of it for this owner has passed its control yet.
// Once is what it takes to know the rig can judge; after that a run that
// judges nothing is caught by the inconclusive count instead, and paying
// for the control on every run would double what evidence costs.
func (e *Engine) needsControl(owner domain.FeatureID, name string) bool {
	for _, r := range e.ExperimentRuns(owner) {
		if r.Experiment != name {
			continue
		}
		for _, ph := range r.Phases {
			if ph.Name == "control" && ph.OK {
				return false
			}
		}
	}
	return true
}
