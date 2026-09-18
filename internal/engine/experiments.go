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

	"github.com/morphis/gummi/internal/atomicfile"
	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/experiment"
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
// repository, the checkout to deploy from and the commit it has out. A
// repository the goal has a tree in contributes that tree; one it never
// touched contributes its own checkout, because the experiment deploys it
// either way and evidence that did not say which commit would be evidence
// about nothing in particular.
func (e *Engine) goalInputs(ctx context.Context, goal domain.Feature, def config.Experiment) (heads, trees map[string]string, err error) {
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
	heads, trees = map[string]string{}, map[string]string{}
	for _, repo := range repos {
		dir := ""
		if t, ok := byRepo[repo]; ok {
			dir = t.Dir
		} else if root, ok := e.pool.RootForName(repo); ok {
			dir = root
		} else {
			return nil, nil, fmt.Errorf("the experiment's input %q is not a repository this workspace manages", repo)
		}
		sha, herr := worktree.HeadOf(ctx, dir)
		if herr != nil {
			return nil, nil, fmt.Errorf("reading the head of %s: %w", dir, herr)
		}
		heads[repo], trees[repo] = sha, dir
	}
	return heads, trees, nil
}

// trunkInputs is goalInputs for the trunk: every input at its repository's
// own checkout. It is what a negative control runs on.
func (e *Engine) trunkInputs(ctx context.Context, goal domain.Feature, def config.Experiment) (heads, trees map[string]string, err error) {
	about, _, err := e.goalInputs(ctx, goal, def)
	if err != nil {
		return nil, nil, err
	}
	heads, trees = map[string]string{}, map[string]string{}
	for repo := range about {
		root, ok := e.pool.RootForName(repo)
		if !ok {
			return nil, nil, fmt.Errorf("the experiment's input %q is not a repository this workspace manages", repo)
		}
		sha, herr := worktree.HeadOf(ctx, root)
		if herr != nil {
			return nil, nil, herr
		}
		heads[repo], trees[repo] = sha, root
	}
	return heads, trees, nil
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
	// Heads and Trees override what the run is about (a bisect step). Both
	// or neither.
	Heads, Trees map[string]string
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
	heads, trees := st.Heads, st.Trees
	switch {
	case heads != nil:
	case st.Trunk:
		heads, trees, err = e.trunkInputs(ctx, goal, def)
	default:
		heads, trees, err = e.goalInputs(ctx, goal, def)
	}
	if err != nil {
		return experiment.Result{}, err
	}
	now := e.now()
	job := experiment.Job{
		ID: experiment.NewID(now), Experiment: st.Name, Owner: string(goal.ID), Purpose: st.Purpose,
		Heads: heads, Trees: trees, Control: st.Control, ExpectFail: st.Trunk,
		Def: def, Substrate: cfg.Substrates[def.Substrate],
		Root: e.cfg.Workspace.Root, StateDir: e.cfg.Workspace.StateDir(),
	}
	job.Dir = filepath.Join(e.cfg.Workspace.EvidenceDir(goal.ID), job.ID)
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
}

// goalExperiments reads the experiments goal's items name against its
// runs. since bounds the inconclusive count: runs that ended before it
// were seen by a person who chose to carry on.
func (e *Engine) goalExperiments(ctx context.Context, goal domain.Feature, items []domain.DoneWhen, since int64) []GoalExperiment {
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
				continue // a control is about the trunk, not about the goal
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
	for _, r := range experimentCheckResults(items, e.goalExperiments(ctx, goal, items, 0)) {
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
		var failed []string
		by := map[string]experiment.Assertion{}
		for _, a := range r.Assertions {
			by[a.ID] = a
		}
		for _, id := range assertions {
			if a, ok := by[id]; ok && !a.OK {
				failed = append(failed, strings.TrimSpace(id+" "+a.Detail))
			}
		}
		if len(failed) > 0 {
			b.WriteString(" — not held: " + strings.Join(failed, "; "))
		}
	} else if r.Outcome == experiment.Fail {
		b.WriteString(" — " + r.Reason)
	}
	b.WriteString(" — evidence in " + r.Dir)
	return b.String()
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
