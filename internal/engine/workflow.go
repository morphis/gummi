package engine

// A session's workflow runs (DESIGN §25): the session proposes a run of a
// recipe against one of its specs (workflow_run), the person allows it —
// or always allows the session's runs — and the run's steps work in
// the session's own tree, one after another or side by side, until the
// run ends with a report the session carries on from. Nothing is branched
// or landed: the run leaves its changes in the tree, and the session
// commits what it keeps.
//
// This file is the run's lifecycle: the session's tools, the person's
// answers to a run's decisions, pause, resume, stop and steer, the
// conductor's tick (workflowpolicy.Decide, executed) and a run's end. A
// step's own run is workflowstep.go; the tree, its snapshots, the end
// checks and Revert run are workflowtree.go.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/verify"
	"github.com/morphis/gummi/internal/workflowpolicy"
	"github.com/morphis/gummi/internal/workflowrecipe"
	"github.com/morphis/gummi/internal/worktree"
)

const (
	workflowRecipesToolName = "workflow_recipes"
	workflowRunToolName     = "workflow_run"
	workflowStatusToolName  = "workflow_status"
	workflowSteerToolName   = "workflow_steer"
	workflowStopToolName    = "workflow_stop"

	// The answers a run's decisions offer. A proposal's answer that is none
	// of its options is the person's feedback, delivered to the session.
	WorkflowAllow = "Allow"
	WorkflowDeny  = "Deny"
	// WorkflowAlways allows this run and every later one of the session
	// without asking: the session's budget is the only cap. An answer that
	// begins with it (an older "Always allow up to $5") reads the same.
	WorkflowAlways = "Always allow"
	// a revised plan's
	WorkflowAllowPlan = "Allow the revised plan"
	WorkflowKeepPlan  = "Keep the plan"
	// a repeat at its max's
	WorkflowOneMore  = "Run one more iteration"
	WorkflowContinue = "Continue"
	WorkflowStopRun  = "Stop the run"
	// a budget decision's, beside WorkflowStopRun
	WorkflowTopUp = "Top up the session"
	// a parked run's, beside WorkflowStopRun
	WorkflowResumeRun = "Resume the run"
)

// workflowRuntime is what this process knows about runs beyond the store:
// the work in flight (a step starting, an until being judged, a run
// ending), a lock per session that every write of a run is made under,
// and when this engine started, so a run that was running before a quit
// is parked rather than resumed.
type workflowRuntime struct {
	mu        sync.Mutex
	boot      time.Time
	busy      map[string]bool
	locks     map[domain.FeatureID]*sync.Mutex
	planTries map[string]int
	// held is this process's hold on the card lock of each session whose
	// run it conducts: kept while the session has a run open or queued,
	// so no other gummi conducts the same run meanwhile
	held map[domain.FeatureID]func()
	// conducted marks each run ("session/n") this process started or
	// resumed: a running run it did not was running under another gummi,
	// gone now, and is parked rather than ticked as if its steps still ran
	conducted map[string]bool
	// stamps is when each artifact a live step declares was last written
	// as the step started, by step key and file: one left as it was is
	// captured from the step's output, not read as the step's own
	stamps map[string]map[string]time.Time
	// diffs caches the diff between two settled snapshots: a run's or a
	// step's that has ended never changes, and reading it runs git
	diffs map[string]cachedDiff
	// unsent marks each session whose runs' reports this process keeps
	// from it (KeepReports): the report is written all the same
	unsent map[domain.FeatureID]bool
}

func newWorkflowRuntime() *workflowRuntime {
	return &workflowRuntime{
		boot: time.Now(), busy: map[string]bool{},
		locks: map[domain.FeatureID]*sync.Mutex{}, planTries: map[string]int{},
		held: map[domain.FeatureID]func(){}, conducted: map[string]bool{},
		stamps: map[string]map[string]time.Time{}, diffs: map[string]cachedDiff{},
		unsent: map[domain.FeatureID]bool{},
	}
}

// KeepReports stops this process sending session the reports of its runs
// as turns: a run's end is written into its spec, its run directory and
// the run itself, but the session is not woken to read it. `gummi run`
// asks it for a run it drives with no session turn — a triage whose
// report is the product. It lasts the life of the engine.
func (e *Engine) KeepReports(session domain.FeatureID) {
	rt := e.wfRT()
	rt.mu.Lock()
	rt.unsent[session] = true
	rt.mu.Unlock()
}

func (e *Engine) keepsReports(session domain.FeatureID) bool {
	rt := e.wfRT()
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.unsent[session]
}

func runKey(session domain.FeatureID, n int) string { return fmt.Sprintf("%s/%d", session, n) }

// conduct marks run n of session as this process's to conduct.
func (rt *workflowRuntime) conduct(session domain.FeatureID, n int) {
	rt.mu.Lock()
	rt.conducted[runKey(session, n)] = true
	rt.mu.Unlock()
}

// conducts reports whether this process started or resumed run n.
func (rt *workflowRuntime) conducts(session domain.FeatureID, n int) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.conducted[runKey(session, n)]
}

// RunPhase is what gummi itself is doing on run n, between its steps:
// "baseline" while it runs the done-when checks before the first step,
// "until" while it judges a repeat's condition, "ending" while it runs the
// end checks and writes the report; "" otherwise. A face says it, and
// offers no pause, stop or revert while the run is ending.
func (e *Engine) RunPhase(session domain.FeatureID, n int) string {
	rt := e.wfRT()
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.busy[fmt.Sprintf("end/%s/%d", session, n)] {
		return "ending"
	}
	prefix := fmt.Sprintf("until/%s/%d/", session, n)
	for k := range rt.busy {
		if strings.HasPrefix(k, prefix) {
			return "until"
		}
	}
	if rt.busy[baselineKey(session, n)] {
		return "baseline"
	}
	return ""
}

// clockStart starts run's clock, unless it runs already.
func (e *Engine) clockStart(run *domain.Run) {
	if run.ResumedAt.IsZero() {
		run.ResumedAt = e.now()
	}
}

// clockStop adds the time since run's clock started to what it has
// worked, and stops it: the run pauses, parks or ends.
func (e *Engine) clockStop(run *domain.Run) {
	if !run.ResumedAt.IsZero() {
		if d := e.now().Sub(run.ResumedAt); d > 0 {
			run.Active += d
		}
		run.ResumedAt = time.Time{}
	}
}

// holdRuns takes (or keeps) this process's hold on session's card lock,
// and reports whether this process may conduct the session's runs: false
// while another gummi holds the card — a headless run or resume of it, or
// a board — which conducts them instead.
func (e *Engine) holdRuns(session domain.FeatureID) bool {
	rt := e.wfRT()
	rt.mu.Lock()
	_, ok := rt.held[session]
	rt.mu.Unlock()
	if ok {
		return true
	}
	release, err := e.lockCard(session)
	if err != nil {
		return false
	}
	rt.mu.Lock()
	rt.held[session] = release
	rt.mu.Unlock()
	return true
}

// letGoRuns drops this process's hold on session's card lock, taken by
// holdRuns, once the session has no run left to conduct.
func (e *Engine) letGoRuns(session domain.FeatureID) {
	rt := e.wfRT()
	rt.mu.Lock()
	release := rt.held[session]
	delete(rt.held, session)
	rt.mu.Unlock()
	if release != nil {
		release()
	}
}

// letGoAllRuns drops every hold holdRuns took: the engine is closing.
func (e *Engine) letGoAllRuns() {
	rt := e.wfRT()
	rt.mu.Lock()
	held := rt.held
	rt.held = map[domain.FeatureID]func(){}
	rt.mu.Unlock()
	for _, release := range held {
		release()
	}
}

func (e *Engine) wfRT() *workflowRuntime { return e.workflows }

func (rt *workflowRuntime) claim(key string) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.busy[key] {
		return false
	}
	rt.busy[key] = true
	return true
}

func (rt *workflowRuntime) release(key string) {
	rt.mu.Lock()
	delete(rt.busy, key)
	rt.mu.Unlock()
}

func (rt *workflowRuntime) isBusy(key string) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.busy[key]
}

// planTry counts one more failed plan block from the planner at key.
func (rt *workflowRuntime) planTry(key string) int {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.planTries[key]++
	return rt.planTries[key]
}

// lockRuns takes the session's run lock: every read-modify-write of a run
// or its steps happens under it, so the tick, a step's end and a person's
// answer never write over one another.
func (e *Engine) lockRuns(session domain.FeatureID) func() {
	rt := e.wfRT()
	rt.mu.Lock()
	mu := rt.locks[session]
	if mu == nil {
		mu = &sync.Mutex{}
		rt.locks[session] = mu
	}
	rt.mu.Unlock()
	mu.Lock()
	return mu.Unlock
}

func (e *Engine) workflowEvent(session domain.FeatureID) {
	e.send(Event{Feature: session, Stage: domain.StageOpen, Kind: EventWorkflow})
}

// RunNotePrefix starts every line a run puts into its session's
// transcript (runNote). The run's own block in the thread says the same
// and more, so the faces leave these lines out (threadfold.IsRunNote).
const RunNotePrefix = "workflow: "

// runNote puts a line from a run into the session's thread.
func (e *Engine) runNote(session domain.FeatureID, line string) {
	ff := e.Freeform(session)
	if ff == nil {
		return
	}
	if sess := ff.Session(); sess != nil {
		sess.appendActivity(RunNotePrefix + line)
		e.persist(sess)
		e.send(Event{Feature: session, Stage: domain.StageOpen, Kind: EventUpdated})
	}
}

var errNoRun = errors.New("this session has no open workflow run")

// --- the session's tools ---------------------------------------------------

func workflowTools() []agent.ToolDef {
	return []agent.ToolDef{
		mkTool(workflowRecipesToolName, "List the workflow recipes this session may run: the workspace's own "+
			"(.gummi/workflows/) and gummi's bundled ones, each with its description. Given a name, returns "+
			"that recipe's YAML in full instead, to adapt into a recipe_yaml of your own.",
			map[string]any{"name": str("A recipe's name: return its YAML instead of the list.")}),
		mkTool(workflowRunToolName,
			"Propose a workflow run: a recipe of steps, each a fresh-context agent, run in this session's own tree "+
				"against one of its specs. The person allows it (or always allows this session's runs). Returns at "+
				"once; the run's report arrives later as a turn. Nothing is committed: the run leaves its changes in the "+
				"tree for you to read and commit. One run at a time; one more may wait queued behind it.",
			map[string]any{
				"recipe":      str("A recipe's name, as workflow_recipes lists them."),
				"recipe_yaml": str("A recipe written out in full, drafted for the work in hand, instead of a name."),
				"spec":        specArg,
				"inputs":      map[string]any{"type": "object", "description": "The recipe's inputs, by name.", "additionalProperties": map[string]any{"type": "string"}},
			}),
		mkTool(workflowStatusToolName, "The run's state, its steps, spend and open questions; an ended run's report.", map[string]any{}),
		mkTool(workflowSteerToolName, "Send a note to the open run: every running step gets it as a turn, and steps not yet started see it in their prompt.",
			map[string]any{"note": str("The note.")}, "note"),
		mkTool(workflowStopToolName, "Stop the open run: its running steps are interrupted, and nothing is reverted. Withdraws a run still proposed or queued.",
			map[string]any{"reason": str("Why.")}, "reason"),
	}
}

func (e *Engine) handleWorkflowTool(s *Session, tc *agent.ToolCall) {
	var a struct {
		Recipe     string            `json:"recipe"`
		RecipeYAML string            `json:"recipe_yaml"`
		Spec       json.RawMessage   `json:"spec"`
		Inputs     map[string]string `json:"inputs"`
		Note       string            `json:"note"`
		Reason     string            `json:"reason"`
		Name       string            `json:"name"`
	}
	_ = json.Unmarshal(tc.Args, &a)
	f := s.Feature
	if !isSession(f) {
		e.resolveNow(s, tc.ID, "workflows are a session's: this card cannot run one")
		return
	}
	ctx := context.Background()
	var out string
	var err error
	switch tc.Name {
	case workflowRecipesToolName:
		out, err = e.workflowRecipesText(a.Name)
	case workflowRunToolName:
		var n int
		if n, _, err = parseSpecArg(a.Spec); err == nil {
			var run *domain.Run
			run, err = e.WorkflowPropose(ctx, f.ID, WorkflowRequest{Recipe: a.Recipe, RecipeYAML: a.RecipeYAML, Spec: n, Inputs: a.Inputs})
			if err == nil {
				out = proposedText(run)
			}
		}
	case workflowStatusToolName:
		out, err = e.WorkflowStatusText(ctx, f.ID)
	case workflowSteerToolName:
		if err = e.WorkflowSteer(ctx, f.ID, a.Note, ActorAgent); err == nil {
			out = "the run's steps have the note"
		}
	case workflowStopToolName:
		if err = e.WorkflowStop(ctx, f.ID, a.Reason); err == nil {
			out = "the run is stopping"
		}
	}
	if err != nil {
		out = "error: " + err.Error()
	}
	e.resolveNow(s, tc.ID, out)
}

func proposedText(r *domain.Run) string {
	switch r.State {
	case domain.RunRunning:
		return fmt.Sprintf("run %d is always allowed and has started; its report will arrive as a turn. "+
			"Do not change files in the tree until then.", r.N)
	case domain.RunQueued:
		return fmt.Sprintf("run %d is always allowed and queued behind the open run; it starts when that one ends.", r.N)
	}
	return fmt.Sprintf("run %d proposed: the person decides whether it runs. Its report will arrive as a turn; carry on meanwhile.", r.N)
}

// WorkflowRecipe is a recipe as the session and the faces list it.
type WorkflowRecipe struct {
	Name        string
	Description string
	// Source is workspace or bundled.
	Source string
	// Err is why a workspace recipe would not parse.
	Err string
}

// WorkflowRecipes lists the recipes a session may run: the workspace's own
// and gummi's bundled ones, by name.
func (e *Engine) WorkflowRecipes() ([]WorkflowRecipe, error) {
	found, err := workflowrecipe.List(e.pool.Root())
	if err != nil {
		return nil, err
	}
	out := make([]WorkflowRecipe, 0, len(found))
	for _, f := range found {
		r := WorkflowRecipe{Name: f.Name, Description: f.Recipe.Description, Source: string(f.Source)}
		if f.Err != nil {
			r.Err = f.Err.Error()
		}
		out = append(out, r)
	}
	return out, nil
}

// workflowRecipesText is workflow_recipes' answer: the list, or with a
// name that recipe's YAML as written.
func (e *Engine) workflowRecipesText(name string) (string, error) {
	if name = strings.TrimSpace(name); name != "" {
		f, err := workflowrecipe.Load(name, e.pool.Root())
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s (%s):\n\n%s", f.Name, f.Source, strings.TrimRight(string(f.YAML), "\n")), nil
	}
	rs, err := e.WorkflowRecipes()
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, r := range rs {
		if r.Err != "" {
			fmt.Fprintf(&b, "- %s (%s): does not parse: %s\n", r.Name, r.Source, r.Err)
			continue
		}
		fmt.Fprintf(&b, "- %s (%s): %s\n", r.Name, r.Source, r.Description)
	}
	if b.Len() == 0 {
		return "no recipes", nil
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

const sessionWorkflowHint = `A session may also run a workflow (gummi's DESIGN §25): a recipe of steps, each a fresh-context agent, that work in this session's own tree against one of its specs. Decide at the start of each new piece of work, before your first edit, whether to do it here or to propose a run: a run proposed once you have done most of the work buys nothing. Propose one when the person asks (they may send /workflow); for a feature or change across several packages, a risky refactor, or work with tests and docs to write (deliver); for a question the person wants answered in a written report (investigate); for checks that already fail and must pass (fix-until-green). Do it here for a question, a small or local change, a fix whose cause you know, or work the person is still shaping with you. When gummi's triage names a recipe ahead of a request, follow it unless you find a reason not to, and say the reason. To propose: write the spec (the steps read only it, never this conversation), run its done-when checks so you know they fail, and call workflow_run with the recipe — by name (workflow_recipes lists them and shows one's YAML) or written out as recipe_yaml — saying in one sentence why a run beats doing it here. The person allows it. It returns at once; while the run is open do not touch files, and when its report arrives as a turn, read it and the diff the run left in the tree, run the checks, tell the person what it means, and commit what you keep. gummi commits nothing. workflow_status, workflow_steer and workflow_stop follow the open run.`

// --- proposing a run --------------------------------------------------------

// WorkflowRequest is a proposal: a recipe by name or written out, the spec
// it runs against (0: the session's only spec) and its inputs.
type WorkflowRequest struct {
	Recipe     string
	RecipeYAML string
	Spec       int
	Inputs     map[string]string
}

// WorkflowPropose proposes a run on session (§25.5). It is refused when
// the recipe fails Check, when the spec does not exist, or when a run is
// already proposed or queued. The recipe is copied into the run, and the
// proposal goes to the person as a decision — or, when the person always
// allows the session's runs, is allowed at once: queued behind an open
// run, or started. Nothing is estimated: the run spends from the session's
// budget until it is spent.
func (e *Engine) WorkflowPropose(ctx context.Context, session domain.FeatureID, req WorkflowRequest) (*domain.Run, error) {
	st := e.cfg.Store
	if st == nil {
		return nil, errors.New("workflows need the store")
	}
	sess, err := st.GetFeature(ctx, session)
	if err != nil {
		return nil, err
	}
	switch {
	case !isSession(sess):
		return nil, fmt.Errorf("%s cannot run a workflow: only a session can", session)
	case sess.Stage == domain.StageDone:
		return nil, fmt.Errorf("%s is closed", session)
	}
	recipe, name, text, err := e.loadRecipe(req)
	if err != nil {
		return nil, err
	}
	if len(dispatchesOf(recipe)) > 0 && !sess.Home {
		return nil, fmt.Errorf("%s is not an agent: only an agent's recipe dispatches sessions", session)
	}
	if err := e.checkPublishAllowance(ctx, sess, recipe); err != nil {
		return nil, err
	}
	changedHandlers, err := e.freezeHandlers(&recipe)
	if err != nil {
		return nil, err
	}
	if changed, err := e.freezeDispatches(&recipe); err != nil {
		return nil, err
	} else if changed || changedHandlers {
		b, err := workflowrecipe.Marshal(recipe)
		if err != nil {
			return nil, err
		}
		text = string(b)
	}
	problems, warnings := workflowrecipe.Check(recipe)
	if len(problems) > 0 {
		return nil, fmt.Errorf("the recipe cannot run:\n- %s", strings.Join(problems, "\n- "))
	}
	inputs, err := workflowrecipe.ResolveInputs(recipe, req.Inputs)
	if err != nil {
		return nil, err
	}
	if err := checkWatchTrust(recipe, inputs); err != nil {
		return nil, err
	}
	n, err := e.runSpec(ctx, sess, req.Spec)
	if err != nil {
		return nil, err
	}

	unlock := e.lockRuns(session)
	defer unlock()
	live, err := st.SessionRuns(ctx, session)
	if err != nil {
		return nil, err
	}
	open := false
	for _, r := range live {
		switch r.State {
		case domain.RunProposed:
			return nil, fmt.Errorf("run %d is still waiting on the person: one proposal at a time", r.N)
		case domain.RunQueued:
			return nil, fmt.Errorf("run %d is already queued behind the open run: the queue is one deep", r.N)
		case domain.RunRunning, domain.RunPaused:
			open = true
		}
	}
	// the spec is frozen as the person reads it: the run is judged against
	// the revision its proposal showed, whenever it starts
	rev, err := e.freezeSpec(ctx, sess, n)
	if err != nil {
		return nil, err
	}
	run := &domain.Run{
		Session: session, RecipeName: name, Recipe: recipe, RecipeYAML: text, Inputs: inputs,
		Spec: n, SpecRev: rev, State: domain.RunProposed, CreatedAt: e.now(),
	}
	if _, items := e.frozenSpec(ctx, run); true {
		warnings = append(warnings, proposalWarnings(recipe, items)...)
	}
	if err := st.CreateRun(ctx, run); err != nil {
		return nil, err
	}
	_, _ = e.writeRunFile(session, run.N, "recipe.yaml", text)
	if name == "" {
		// an inline recipe as the session gave it, which a person may save
		// (WorkflowSaveRecipe); recipe.yaml follows a revised plan
		_, _ = e.writeRunFile(session, run.N, proposedRecipeFile, text)
	}
	if always, _ := st.WorkflowAlwaysAllow(ctx, session); always {
		if err := e.allowRunLocked(ctx, run, "the person always allows this session's runs"); err != nil {
			return nil, err
		}
		e.workflowEvent(session)
		return run, nil
	}
	run.Pending = &domain.RunDecision{
		ID: e.runDecisionID(run), Kind: domain.RunDecideProposal,
		Question:   e.proposalQuestion(ctx, sess, run, warnings),
		Options:    []string{WorkflowAllow, WorkflowAlways, WorkflowDeny},
		RecipeYAML: text, Warnings: warnings,
	}
	if err := st.UpdateRun(ctx, run); err != nil {
		return nil, err
	}
	// the run's baseline: the spec's checks in the tree as it is, while the
	// proposal waits on the person; the call returns at once. Not while
	// another run's steps write the tree: its checks would read their work
	// half done, and its files land in their diffs. It is taken when the
	// run starts instead.
	if !open {
		if _, err := e.askBaselineLocked(ctx, run); err != nil {
			return nil, err
		}
	}
	e.runNote(session, fmt.Sprintf("run %d (%s) proposed on spec %d", run.N, recipeLabel(run), n))
	e.send(Event{Feature: session, Stage: domain.StageOpen, Kind: EventQuestion})
	e.workflowEvent(session)
	return run, nil
}

func recipeLabel(r *domain.Run) string {
	if r.RecipeName != "" {
		return r.RecipeName
	}
	if r.Recipe.Name != "" {
		return r.Recipe.Name + ", inline"
	}
	return "inline recipe"
}

// WorkflowCheck reads req's recipe and refuses it as WorkflowPropose
// would for the recipe alone — one that fails Check, an input it does not
// declare, a required one left out — with no session yet: `gummi run`
// asks it before it mints one.
func (e *Engine) WorkflowCheck(req WorkflowRequest) (domain.Recipe, error) {
	recipe, _, _, err := e.loadRecipe(req)
	if err != nil {
		return recipe, err
	}
	if problems, _ := workflowrecipe.Check(recipe); len(problems) > 0 {
		return recipe, fmt.Errorf("the recipe cannot run:\n- %s", strings.Join(problems, "\n- "))
	}
	_, err = workflowrecipe.ResolveInputs(recipe, req.Inputs)
	return recipe, err
}

// loadRecipe reads the request's recipe: its name, its text, and the
// recipe parsed from it.
func (e *Engine) loadRecipe(req WorkflowRequest) (domain.Recipe, string, string, error) {
	switch {
	case strings.TrimSpace(req.RecipeYAML) != "" && strings.TrimSpace(req.Recipe) != "":
		return domain.Recipe{}, "", "", errors.New("name a recipe or write one out, not both")
	case strings.TrimSpace(req.RecipeYAML) != "":
		r, err := workflowrecipe.Parse([]byte(req.RecipeYAML))
		if err != nil {
			return r, "", "", err
		}
		return r, "", req.RecipeYAML, nil
	case strings.TrimSpace(req.Recipe) != "":
		f, err := workflowrecipe.Load(strings.TrimSpace(req.Recipe), e.pool.Root())
		if err != nil {
			return domain.Recipe{}, "", "", err
		}
		return f.Recipe, f.Name, string(f.YAML), nil
	}
	return domain.Recipe{}, "", "", errors.New("name a recipe (workflow_recipes lists them) or write one out as recipe_yaml")
}

// runSpec resolves the spec a run is proposed against: the one named, or
// the session's only spec.
func (e *Engine) runSpec(ctx context.Context, sess domain.Feature, n int) (int, error) {
	specs, err := e.SessionSpecs(ctx, sess)
	if err != nil {
		return 0, err
	}
	switch {
	case len(specs) == 0:
		return 0, errors.New("this session has no spec: start one with spec_start, and give it done-when items the run is judged against")
	case n == 0 && len(specs) > 1:
		return 0, fmt.Errorf("this session has %d specs: name the one to run against with spec", len(specs))
	case n == 0:
		return specs[0].N, nil
	}
	for _, sp := range specs {
		if sp.N == n {
			return n, nil
		}
	}
	return 0, fmt.Errorf("this session has no spec %d; spec_list lists them", n)
}

func (e *Engine) runDecisionID(r *domain.Run) string {
	return fmt.Sprintf("run:%s:%d:%d", r.Session, r.N, e.now().UnixNano())
}

// proposalQuestion is what the person reads to allow a run (§25.5).
func (e *Engine) proposalQuestion(ctx context.Context, sess domain.Feature, run *domain.Run, warnings []string) string {
	var b strings.Builder
	title := ""
	if sp, ok, err := e.cfg.Store.SessionSpec(ctx, sess.ID, run.Spec); err == nil && ok {
		title = " " + strconv.Quote(sp.Title)
	}
	fmt.Fprintf(&b, "Run the %s workflow on spec S%d%s? It is judged against revision %d of the spec, frozen now: "+
		"a later edit reaches the run as a note.\n\nSteps:\n", recipeLabel(run), run.Spec, title, run.SpecRev)
	e.writeRecipeTree(&b, sess.Profile, run.Recipe.Steps, "")
	writeAllowance(&b, run.Recipe)
	e.writePRAllowance(ctx, &b, run)
	if len(warnings) > 0 {
		b.WriteString("\nWarnings:\n- " + strings.Join(warnings, "\n- ") + "\n")
	}
	if _, items := e.frozenSpec(ctx, run); len(items) > 0 {
		var checks []string
		for _, it := range items {
			if strings.TrimSpace(it.Check) != "" {
				checks = append(checks, it.ID+": "+it.Check)
			}
		}
		if len(checks) > 0 {
			b.WriteString("\nDone-when checks it will run in the tree:\n- " + strings.Join(checks, "\n- ") + "\n")
		}
	}
	if left, bounded := e.sessionPoolLeft(ctx, sess); bounded {
		fmt.Fprintf(&b, "\nIt spends from the session's budget, which has %s left, and pauses for a top-up if that runs out.\n",
			domain.FormatDollars(math.Max(0, left)))
	} else {
		b.WriteString("\nIt spends from the session, which has no envelope.\n")
	}
	if mgr, err := e.mgr(ctx, &sess); err == nil {
		if dirty, err := mgr.Dirty(ctx, &sess); err == nil && dirty {
			b.WriteString("The tree holds uncommitted work: the run's diff and a Revert run take it in too.\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// writeRecipeTree draws a recipe's steps, each with its role and the
// model it runs on under profile.
func (e *Engine) writeRecipeTree(b *strings.Builder, profile string, es []domain.Entry, indent string) {
	stepLine := func(s domain.Step, mark string) {
		switch {
		case s.Run != "":
			fmt.Fprintf(b, "%s- %s%s · run\n", indent, mark, s.ID)
			return
		case s.Dispatch != nil:
			fmt.Fprintf(b, "%s- %s%s · dispatch → %s\n", indent, mark, s.ID, s.Dispatch.Repo)
			return
		case s.PR != nil:
			fmt.Fprintf(b, "%s- %s%s · pr %s\n", indent, mark, s.ID, s.PR.Act)
			return
		case s.Watch != nil:
			fmt.Fprintf(b, "%s- %s%s · watch the PR until %s\n", indent, mark, s.ID, s.Watch.UntilOr())
			return
		}
		fmt.Fprintf(b, "%s- %s%s · %s · %s\n", indent, mark, s.ID, s.Role, e.stepModelWords(profile, s.Role, s.Slot))
	}
	for _, en := range es {
		switch {
		case en.Step != nil:
			stepLine(*en.Step, "")
		case en.Parallel != nil:
			fmt.Fprintf(b, "%s- %s ∥ %s\n", indent, en.Parallel.ID, en.Parallel.Join)
			for _, s := range en.Parallel.Steps {
				fmt.Fprintf(b, "  ")
				stepLine(s, "")
			}
		case en.Repeat != nil:
			r := en.Repeat
			fmt.Fprintf(b, "%s- %s ↺ up to %d, until %s (at the max: %s)\n", indent, r.ID, r.Max, untilText(r.Until), r.OnMax.Or())
			e.writeRecipeTree(b, profile, r.Steps, indent+"  ")
		}
	}
}

func untilText(u domain.Until) string {
	switch u.Kind() {
	case domain.UntilChecks:
		return "the done-when checks pass"
	case domain.UntilVerdict:
		return fmt.Sprintf("verdict %s in %s", u.Verdict, u.In)
	case domain.UntilContains:
		return fmt.Sprintf("the output contains %q", u.Contains)
	}
	return "?"
}

// allowRunLocked allows a run: queued behind the session's open run, or
// started. The caller holds the session's run lock.
func (e *Engine) allowRunLocked(ctx context.Context, run *domain.Run, why string) error {
	st := e.cfg.Store
	run.Pending = nil
	open, err := st.OpenRun(ctx, run.Session)
	if err != nil {
		return err
	}
	if open != nil && open.N != run.N {
		if err := st.QueueRun(ctx, run); err != nil {
			return err
		}
		e.runNote(run.Session, fmt.Sprintf("run %d allowed (%s): queued behind run %d", run.N, why, open.N))
		return nil
	}
	if err := e.startRunLocked(ctx, run); err != nil {
		return err
	}
	e.runNote(run.Session, fmt.Sprintf("run %d allowed (%s) and started", run.N, why))
	return nil
}

// freezeSpec is the revision of spec n a run is judged against: its latest,
// recorded first when the file has moved past it.
func (e *Engine) freezeSpec(ctx context.Context, sess domain.Feature, n int) (int, error) {
	st := e.cfg.Store
	text, ok := e.SessionSpec(sess, n)
	if !ok {
		return 0, fmt.Errorf("spec %d is gone", n)
	}
	if revs, _ := st.SpecRevisions(ctx, sess.ID, n); len(revs) > 0 {
		if last, ok, _ := st.SpecRevision(ctx, sess.ID, n, revs[0].Rev); ok && last.Content == text {
			return last.Rev, nil
		}
	}
	return e.recordSpecRevision(ctx, sess.ID, n, text, state.ActorWorkflow)
}

// proposalWarnings are what gummi itself sees wrong with running recipe on
// the done-when items: a loop whose until is the checks on a spec with
// none, which could never judge itself done; and checks on a recipe with
// no step that writes code, which cannot change what they find.
func proposalWarnings(recipe domain.Recipe, items []domain.DoneWhen) []string {
	var out []string
	checks := hasChecks(items)
	var loops func(es []domain.Entry) bool
	loops = func(es []domain.Entry) bool {
		for _, en := range es {
			if en.Repeat != nil && (en.Repeat.Until.Kind() == domain.UntilChecks || loops(en.Repeat.Steps)) {
				return true
			}
		}
		return false
	}
	if !checks && loops(recipe.Steps) {
		out = append(out, "a loop runs until the done-when checks pass, but the spec has no checks: it cannot judge itself done, and asks you after every iteration")
	}
	writes := false
	for _, s := range workflowrecipe.Steps(recipe) {
		if s.Role == domain.RoleImplementer || s.Role == domain.RoleTester {
			writes = true
		}
	}
	if checks && !writes {
		out = append(out, "no step of this recipe writes code (no implementer or tester): a checked item that fails now fails at the end too, and the run ends partial")
	}
	return out
}

// startRunLocked starts a run: its spec revision (frozen when it was
// proposed, or now for a run from before), a snapshot of the tree and of
// the index, and its clock. The tick starts its steps.
func (e *Engine) startRunLocked(ctx context.Context, run *domain.Run) error {
	st := e.cfg.Store
	sess, err := st.GetFeature(ctx, run.Session)
	if err != nil {
		return err
	}
	rev := run.SpecRev
	if rev == 0 {
		if rev, err = e.freezeSpec(ctx, sess, run.Spec); err != nil {
			return err
		}
	}
	dir, err := e.sessionTree(ctx, run.Session)
	if err != nil {
		return err
	}
	if err := e.recordRunIgnored(ctx, run.Session, run.N); err != nil {
		return fmt.Errorf("listing the tree's ignored files: %w", err)
	}
	tree, err := e.snapshotRun(ctx, run.Session, run.N)
	if err != nil {
		return fmt.Errorf("snapshotting the tree: %w", err)
	}
	head, _ := objectiveGit(ctx, dir, "rev-parse", "HEAD")
	index, _ := worktree.IndexTree(ctx, dir)
	run.SpecRev, run.StartTree, run.StartHead, run.StartIndex = rev, tree, head, index
	run.State, run.StartedAt, run.Pending, run.Reason = domain.RunRunning, e.now(), nil, ""
	run.Active, run.ResumedAt = 0, time.Time{}
	e.clockStart(run)
	e.wfRT().conduct(run.Session, run.N)
	return st.UpdateRun(ctx, run)
}

// --- decisions -------------------------------------------------------------

// raiseRunDecision puts d to the person, unless one is already open. The
// caller holds the run lock and writes the run.
func (e *Engine) raiseRunDecision(run *domain.Run, d domain.RunDecision) bool {
	if run.Pending != nil {
		return false
	}
	d.ID = e.runDecisionID(run)
	run.Pending = &d
	e.send(Event{Feature: run.Session, Stage: domain.StageOpen, Kind: EventQuestion})
	return true
}

// WorkflowDecision is the session's run with an open decision, if any:
// a proposal, a revised plan, a repeat at its max or one that cannot judge
// its condition, or the budget. A step's own ask_user is a question on the
// step's card.
func (e *Engine) WorkflowDecision(ctx context.Context, session domain.FeatureID) (*domain.Run, *domain.RunDecision) {
	if e.cfg.Store == nil {
		return nil, nil
	}
	runs, err := e.cfg.Store.SessionRuns(ctx, session)
	if err != nil {
		return nil, nil
	}
	// the open run's question first: a proposal behind it waits, while a
	// run paused on its budget or its loop holds the tree
	for i := len(runs) - 1; i >= 0; i-- {
		if r := runs[i]; r.Pending != nil && r.State.Open() {
			return &r, r.Pending
		}
	}
	for i := len(runs) - 1; i >= 0; i-- {
		if r := runs[i]; r.Pending != nil && r.State == domain.RunProposed {
			return &r, r.Pending
		}
	}
	return nil, nil
}

// WorkflowAnswer answers run n's open decision (§25.5, §25.7). answer is
// one of the decision's options; for a proposal it may also be the
// person's own words, which go to the session agent as a turn and close
// the proposal. A revised plan
// answered in other words goes back to the planner as a turn; a budget
// decision also takes an amount, the top-up: credits as "300cr" or
// "300 credits", or dollars as "$3" (parseTopUp).
//
// decision is the id of the decision the answer was given to
// (RunDecision.ID): a run raises one decision after another, and an
// answer read off one is refused once another stands in its place.
func (e *Engine) WorkflowAnswer(ctx context.Context, session domain.FeatureID, n int, decision, answer string) error {
	st := e.cfg.Store
	if st == nil {
		return errors.New("workflows need the store")
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return errors.New("empty answer")
	}
	if decision == "" {
		return fmt.Errorf("an answer to run %d names the decision it answers", n)
	}
	unlock := e.lockRuns(session)
	after, err := e.answerLocked(ctx, session, n, decision, answer)
	unlock()
	if err != nil {
		return err
	}
	if after != nil {
		if err := after(); err != nil {
			return err
		}
	}
	e.send(Event{Feature: session, Stage: domain.StageOpen, Kind: EventUpdated})
	e.workflowEvent(session)
	return nil
}

// answerLocked applies an answer under the run lock; what it returns runs
// once the lock is let go (a turn to send, a top-up that reads the run).
func (e *Engine) answerLocked(ctx context.Context, session domain.FeatureID, n int, decision, answer string) (func() error, error) {
	st := e.cfg.Store
	run, err := st.Run(ctx, session, n)
	if err != nil {
		return nil, err
	}
	if run == nil || run.Pending == nil {
		return nil, fmt.Errorf("run %d has no open decision: the one answered was settled already", n)
	}
	if run.Pending.ID != decision {
		return nil, fmt.Errorf("run %d no longer asks what was answered: it now asks %q — read it and answer that",
			n, firstLine(run.Pending.Question))
	}
	d := *run.Pending
	run.Pending = nil
	who := "the person"
	var after func() error
	switch d.Kind {
	case domain.RunDecideProposal:
		switch {
		case answer == WorkflowAllow:
			if err := e.allowRunLocked(ctx, run, who+" allowed it"); err != nil {
				return nil, err
			}
			return nil, nil
		case strings.HasPrefix(answer, WorkflowAlways):
			if err := st.SetWorkflowAlwaysAllow(ctx, session, true); err != nil {
				return nil, err
			}
			if err := e.allowRunLocked(ctx, run, who+" always allows this session's runs"); err != nil {
				return nil, err
			}
			return nil, nil
		case answer == WorkflowDeny:
			run.State, run.Reason = domain.RunDenied, who+" denied it"
			e.runNote(session, fmt.Sprintf("run %d denied", n))
		default:
			run.State, run.Feedback, run.Reason = domain.RunDenied, answer, who+" answered with feedback"
			e.runNote(session, fmt.Sprintf("run %d not run: the person answered with feedback", n))
			msg := fmt.Sprintf("About the workflow run you proposed (run %d, which did not run): %s", n, answer)
			after = func() error { return e.sendSessionTurn(ctx, session, msg) }
		}
	case domain.RunDecidePlan:
		inst, err := st.RunStep(ctx, session, n, d.Step)
		if err != nil || inst == nil {
			return nil, fmt.Errorf("step %s is gone", d.Step)
		}
		switch answer {
		case WorkflowAllowPlan, WorkflowAllow:
			if err := e.spliceLocked(ctx, run, d); err != nil {
				return nil, err
			}
			inst.State, inst.Reason, inst.EndedAt = domain.StepDone, "its revised plan was allowed", e.now()
		case WorkflowKeepPlan, WorkflowDeny:
			inst.State, inst.Reason, inst.EndedAt = domain.StepDone, "its revised plan was not allowed", e.now()
			e.runNote(session, fmt.Sprintf("run %d: the person kept the plan", n))
		default:
			// the person talks the plan over with the planner, which ends its
			// turn again with a revised block, or none
			inst.State, inst.Reason, inst.EndTree = domain.StepRunning, "", ""
			card := inst.Card
			after = func() error {
				e.sendStepTurn(card, "The person answered your revised plan: "+answer)
				return nil
			}
		}
		if err := st.UpdateRunStep(ctx, inst); err != nil {
			return nil, err
		}
	case domain.RunDecideMax, domain.RunDecideUnjudged:
		if run.OnMax == nil {
			run.OnMax = map[string]string{}
		}
		switch answer {
		case WorkflowOneMore:
			run.OnMax[d.Step] = string(workflowpolicy.OnMaxMore)
		case WorkflowContinue:
			run.OnMax[d.Step] = string(workflowpolicy.OnMaxGoOn)
		case WorkflowStopRun:
			run.OnMax[d.Step] = string(workflowpolicy.OnMaxStop)
		default:
			return e.chatAboutDecision(ctx, session, n, d, answer), nil
		}
		e.runNote(session, fmt.Sprintf("run %d: %s at %s", n, strings.ToLower(answer), d.Step))
	case domain.RunDecideWatch:
		applied, err := e.answerWatchLocked(ctx, run, d, answer)
		if err != nil {
			return nil, err
		}
		if !applied {
			return e.chatAboutDecision(ctx, session, n, d, answer), nil
		}
	case domain.RunDecideBudget:
		switch answer {
		case WorkflowStopRun:
			run.Stopping, run.Reason = true, "stopped by the person over its budget"
		default:
			amount := d.Amount
			if answer != WorkflowTopUp {
				if amount, err = parseCredits(answer); err != nil {
					return e.chatAboutDecision(ctx, session, n, d, answer), nil
				}
			}
			e.runNote(session, fmt.Sprintf("run %d: the person topped the session up by %s", n, domain.FormatDollars(float64(amount))))
			after = func() error { return e.topUpForRun(ctx, session, amount) }
		}
	case domain.RunDecideResume:
		switch answer {
		case WorkflowResumeRun, "Resume":
			if err := e.resumeLocked(ctx, run); err != nil {
				return nil, err
			}
			return nil, nil
		case WorkflowStopRun:
			run.Stopping, run.Reason = true, "stopped by the person after gummi came back"
		default:
			return e.chatAboutDecision(ctx, session, n, d, answer), nil
		}
	}
	if err := st.UpdateRun(ctx, run); err != nil {
		return nil, err
	}
	return after, nil
}

// chatAboutDecision is a person's own words to a run's question that are
// none of its answers: they go to the session agent as a turn, and the
// question stays open for the person to answer once they have talked it
// over. Nothing of the run is written.
func (e *Engine) chatAboutDecision(ctx context.Context, session domain.FeatureID, n int, d domain.RunDecision, words string) func() error {
	msg := fmt.Sprintf("The person, while workflow run %d waits on them (%s), says: %s\n\n"+
		"The run's question stays open, and only the person answers it: talk it over, but do not answer it for them.",
		n, firstLine(d.Question), words)
	return func() error { return e.sendSessionTurn(ctx, session, msg) }
}

// parseCredits reads an amount: credits as "300cr" or "300 credits", or
// dollars as "$3", "3.50" or a bare number — what every face shows money
// in.
func parseCredits(s string) (int, error) {
	s = strings.TrimSpace(s)
	dollars := true
	for _, unit := range []string{"credits", "cr"} {
		if t, ok := strings.CutSuffix(s, unit); ok {
			s, dollars = strings.TrimSpace(t), false
			break
		}
	}
	v, err := strconv.ParseFloat(strings.TrimPrefix(s, "$"), 64)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("%q is not an amount: give dollars as $N.NN, or credits as 300cr", s)
	}
	if dollars {
		v /= domain.CreditsToDollars
	}
	return int(math.Ceil(v)), nil
}

// spliceLocked makes a planner's revised plan the run's: its steps replace
// the run's remaining entries.
func (e *Engine) spliceLocked(ctx context.Context, run *domain.Run, d domain.RunDecision) error {
	entries, err := workflowrecipe.ParseEntries([]byte(d.RecipeYAML))
	if err != nil {
		return err
	}
	steps, err := e.cfg.Store.RunSteps(ctx, run.Session, run.N)
	if err != nil {
		return err
	}
	r, problems, _ := workflowrecipe.CheckReplacement(run.Recipe, startedEntries(run.Recipe, steps), entries)
	if len(problems) > 0 {
		return fmt.Errorf("the revised plan no longer passes the check:\n- %s", strings.Join(problems, "\n- "))
	}
	text, err := workflowrecipe.Marshal(r)
	if err != nil {
		return err
	}
	run.Recipe, run.RecipeYAML = r, string(text)
	_, _ = e.writeRunFile(run.Session, run.N, "recipe.yaml", run.RecipeYAML)
	e.runNote(run.Session, fmt.Sprintf("run %d: the person allowed %s's revised plan", run.N, d.Step))
	return nil
}

// sendSessionTurn sends the session agent a turn, as whoever ctx names.
func (e *Engine) sendSessionTurn(ctx context.Context, session domain.FeatureID, msg string) error {
	sess, err := e.cfg.Store.GetFeature(ctx, session)
	if err != nil || sess.Stage == domain.StageDone {
		return err
	}
	ff, err := e.OpenFreeform(ctx, sess)
	if err != nil {
		return err
	}
	return ff.SendTurn(ctx, msg, nil)
}

// topUpForRun raises the session's envelope by credits: a person's top-up
// answering a run's budget decision.
func (e *Engine) topUpForRun(ctx context.Context, session domain.FeatureID, credits int) error {
	sess, err := e.cfg.Store.GetFeature(ctx, session)
	if err != nil {
		return err
	}
	if sess.Budget.Envelope <= 0 {
		e.workflowEnvelopeRaised(ctx, session)
		return nil
	}
	return e.RaiseEnvelope(ctx, session, sess.Budget.Envelope+max(credits, 1))
}

// workflowEnvelopeRaised is the session's envelope going up: a run paused
// because the money ran out carries on once the pool has some again.
func (e *Engine) workflowEnvelopeRaised(ctx context.Context, session domain.FeatureID) {
	if e.cfg.Store == nil {
		return
	}
	unlock := e.lockRuns(session)
	defer unlock()
	run, err := e.cfg.Store.OpenRun(ctx, session)
	if err != nil || run == nil {
		return
	}
	if run.Pending != nil && run.Pending.Kind == domain.RunDecideBudget {
		run.Pending = nil
	}
	if run.OutOfBudget && run.State == domain.RunPaused {
		sess, err := e.cfg.Store.GetFeature(ctx, session)
		if err != nil {
			return
		}
		if left, bounded := e.sessionPoolLeft(ctx, sess); !bounded || left > 0 {
			run.OutOfBudget, run.State, run.Reason = false, domain.RunRunning, ""
			e.clockStart(run)
			e.wfRT().conduct(session, run.N)
			e.carryOnRun(ctx, run)
			e.runNote(session, fmt.Sprintf("run %d: the session was topped up, and the run carries on", run.N))
		}
	}
	_ = e.cfg.Store.UpdateRun(ctx, run)
	e.workflowEvent(session)
}

// --- pause, resume, stop, steer --------------------------------------------

func (e *Engine) openRunLocked(ctx context.Context, session domain.FeatureID) (*domain.Run, error) {
	if e.cfg.Store == nil {
		return nil, errors.New("workflows need the store")
	}
	r, err := e.cfg.Store.OpenRun(ctx, session)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, errNoRun
	}
	return r, nil
}

// WorkflowPause parks the session's running run: nothing new starts, and
// its steps' backends stop, their work kept in the tree.
func (e *Engine) WorkflowPause(ctx context.Context, session domain.FeatureID) error {
	unlock := e.lockRuns(session)
	defer unlock()
	run, err := e.openRunLocked(ctx, session)
	if err != nil {
		return err
	}
	if run.State != domain.RunRunning {
		return fmt.Errorf("run %d is %s, not running", run.N, run.State)
	}
	return e.parkRunLocked(ctx, run, "paused by the person")
}

func (e *Engine) parkRunLocked(ctx context.Context, run *domain.Run, why string) error {
	run.State, run.Reason = domain.RunPaused, why
	e.clockStop(run)
	if err := e.cfg.Store.UpdateRun(ctx, run); err != nil {
		return err
	}
	e.stopRunSteps(ctx, run)
	e.runNote(run.Session, fmt.Sprintf("run %d %s", run.N, why))
	e.workflowEvent(run.Session)
	return nil
}

// WorkflowResume runs a paused run again. Only a person resumes one: a run
// parked by a quit never resumes itself (§25.7).
func (e *Engine) WorkflowResume(ctx context.Context, session domain.FeatureID) error {
	unlock := e.lockRuns(session)
	defer unlock()
	run, err := e.openRunLocked(ctx, session)
	if err != nil {
		return err
	}
	return e.resumeLocked(ctx, run)
}

// resumeLocked runs a paused run again; the caller holds the run lock.
func (e *Engine) resumeLocked(ctx context.Context, run *domain.Run) error {
	session := run.Session
	if run.State != domain.RunPaused {
		return fmt.Errorf("run %d is %s, not paused", run.N, run.State)
	}
	if run.OutOfBudget {
		sess, err := e.cfg.Store.GetFeature(ctx, session)
		if err != nil {
			return err
		}
		if left, bounded := e.sessionPoolLeft(ctx, sess); bounded && left <= 0 {
			return fmt.Errorf("run %d is paused because the session's budget is spent: raise the session's envelope to carry on", run.N)
		}
		run.OutOfBudget = false
		if run.Pending != nil && run.Pending.Kind == domain.RunDecideBudget {
			run.Pending = nil
		}
	}
	if run.Pending != nil && run.Pending.Kind == domain.RunDecideResume {
		run.Pending = nil
	}
	run.State, run.Reason = domain.RunRunning, ""
	e.clockStart(run)
	e.wfRT().conduct(session, run.N)
	if err := e.cfg.Store.UpdateRun(ctx, run); err != nil {
		return err
	}
	e.releaseHeld(ctx, run)
	e.carryOnRun(ctx, run)
	e.runNote(session, fmt.Sprintf("run %d resumed", run.N))
	e.workflowEvent(session)
	return nil
}

// WorkflowStop stops the session's open run: its running steps are
// interrupted at their next tool boundary and the run ends stopped,
// reverting nothing. With no run open it withdraws one still proposed or
// queued.
func (e *Engine) WorkflowStop(ctx context.Context, session domain.FeatureID, reason string) error {
	if e.cfg.Store == nil {
		return errors.New("workflows need the store")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "stopped"
	}
	unlock := e.lockRuns(session)
	defer unlock()
	run, err := e.openRunLocked(ctx, session)
	if errors.Is(err, errNoRun) {
		runs, err := e.cfg.Store.SessionRuns(ctx, session)
		if err != nil {
			return err
		}
		for i := len(runs) - 1; i >= 0; i-- {
			r := runs[i]
			if r.State != domain.RunProposed && r.State != domain.RunQueued {
				continue
			}
			r.State, r.Pending, r.Reason = domain.RunDenied, nil, "withdrawn: "+reason
			if err := e.cfg.Store.UpdateRun(ctx, &r); err != nil {
				return err
			}
			e.runNote(session, fmt.Sprintf("run %d withdrawn: %s", r.N, reason))
			e.workflowEvent(session)
			return nil
		}
		return errNoRun
	}
	if err != nil {
		return err
	}
	run.Stopping, run.Reason = true, reason
	if err := e.cfg.Store.UpdateRun(ctx, run); err != nil {
		return err
	}
	e.runNote(session, fmt.Sprintf("run %d stopping: %s", run.N, reason))
	e.workflowEvent(session)
	return nil
}

// WorkflowWithdraw withdraws run n of session while it is still proposed
// or queued: a person taking back a run they allowed, before it starts.
func (e *Engine) WorkflowWithdraw(ctx context.Context, session domain.FeatureID, n int, reason string) error {
	if e.cfg.Store == nil {
		return errors.New("workflows need the store")
	}
	unlock := e.lockRuns(session)
	defer unlock()
	r, err := e.cfg.Store.Run(ctx, session, n)
	if err != nil {
		return err
	}
	if r == nil || (r.State != domain.RunProposed && r.State != domain.RunQueued) {
		return fmt.Errorf("run %d is not waiting to start: only a proposed or queued run is withdrawn", n)
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "by the person"
	}
	r.State, r.Pending, r.Reason = domain.RunDenied, nil, "withdrawn "+reason
	if err := e.cfg.Store.UpdateRun(ctx, r); err != nil {
		return err
	}
	e.runNote(session, fmt.Sprintf("run %d withdrawn %s", n, reason))
	e.workflowEvent(session)
	return nil
}

// WorkflowRetry is a person retrying run n (domain.RetryRefusal says
// when they may): every step that failed or stopped runs again, its
// earlier attempt kept beside it (domain.RetriedPath) with its spend; a
// loop a person stopped asks again at its max; every step that finished
// stays as it was. The run is open again, judged against the same frozen
// spec, its diff still from where it first started. Only a person retries:
// no session tool does.
func (e *Engine) WorkflowRetry(ctx context.Context, session domain.FeatureID, n int) error {
	st := e.cfg.Store
	if st == nil {
		return errors.New("workflows need the store")
	}
	unlock := e.lockRuns(session)
	defer unlock()
	run, err := st.Run(ctx, session, n)
	if err != nil {
		return err
	}
	if run == nil {
		return fmt.Errorf("this session has no run %d", n)
	}
	runs, err := st.SessionRuns(ctx, session)
	if err != nil {
		return err
	}
	steps, err := st.RunSteps(ctx, session, n)
	if err != nil {
		return err
	}
	if why := domain.RetryRefusal(*run, steps, runs); why != "" {
		return errors.New(why)
	}
	taken := map[string]bool{}
	for _, s := range steps {
		taken[s.Path] = true
	}
	var again []string
	for _, s := range steps {
		if s.State != domain.StepFailed && s.State != domain.StepStopped {
			continue
		}
		k := 1
		for taken[domain.RetriedPath(s.Path, k)] {
			k++
		}
		to := domain.RetriedPath(s.Path, k)
		if err := st.RenameRunStep(ctx, session, n, s.Path, to); err != nil {
			return err
		}
		taken[to] = true
		again = append(again, s.Path)
	}
	for k, a := range run.OnMax {
		if a == string(workflowpolicy.OnMaxStop) {
			delete(run.OnMax, k)
			again = append(again, k)
		}
	}
	run.State, run.Reason, run.Stopping, run.Pending, run.OutOfBudget = domain.RunRunning, "", false, nil, false
	run.EndedAt, run.EndTree, run.Results, run.Report, run.Commits = time.Time{}, "", nil, "", nil
	e.clockStart(run)
	e.wfRT().conduct(session, n)
	if err := st.UpdateRun(ctx, run); err != nil {
		return err
	}
	e.runNote(session, fmt.Sprintf("run %d retried: %s again", n, strings.Join(again, ", ")))
	e.workflowEvent(session)
	return nil
}

// WorkflowAlwaysAllows reports whether a person always allows session's
// runs, so its proposals start without asking.
func (e *Engine) WorkflowAlwaysAllows(ctx context.Context, session domain.FeatureID) bool {
	if e.cfg.Store == nil {
		return false
	}
	always, _ := e.cfg.Store.WorkflowAlwaysAllow(ctx, session)
	return always
}

// WorkflowAskAgain takes back a person's always-allow on session: its next
// proposal asks again.
func (e *Engine) WorkflowAskAgain(ctx context.Context, session domain.FeatureID) error {
	if e.cfg.Store == nil {
		return errors.New("workflows need the store")
	}
	if err := e.cfg.Store.SetWorkflowAlwaysAllow(ctx, session, false); err != nil {
		return err
	}
	e.runNote(session, "the person asks to allow each run again")
	e.workflowEvent(session)
	return nil
}

// WorkflowSteer sends a note to the session's open run: every running step
// gets it as a turn, and a step not yet started sees it in its prompt.
func (e *Engine) WorkflowSteer(ctx context.Context, session domain.FeatureID, note, by string) error {
	note = strings.TrimSpace(note)
	if note == "" {
		return errors.New("a steer needs a note")
	}
	who := "the session"
	if by != ActorAgent {
		who = "the person"
	}
	unlock := e.lockRuns(session)
	defer unlock()
	run, err := e.openRunLocked(ctx, session)
	if err != nil {
		return err
	}
	return e.steerLocked(ctx, run, who+": "+note)
}

func (e *Engine) steerLocked(ctx context.Context, run *domain.Run, note string) error {
	run.Steers = append(run.Steers, note)
	if err := e.cfg.Store.UpdateRun(ctx, run); err != nil {
		return err
	}
	steps, _ := e.cfg.Store.RunSteps(ctx, run.Session, run.N)
	for _, s := range steps {
		if s.Card != "" && s.State.Live() && s.EndTree == "" {
			e.sendStepTurn(s.Card, "A note steering the run, from "+note)
		}
	}
	e.runNote(run.Session, fmt.Sprintf("run %d steered by %s", run.N, firstLine(note)))
	e.workflowEvent(run.Session)
	return nil
}

// workflowSpecEdited offers an edit of the spec an open run froze to the
// run as a steer (§25.3).
func (e *Engine) workflowSpecEdited(ctx context.Context, f domain.Feature, n, rev int) {
	if e.cfg.Store == nil {
		return
	}
	unlock := e.lockRuns(f.ID)
	defer unlock()
	run, err := e.cfg.Store.OpenRun(ctx, f.ID)
	if err != nil || run == nil || run.Spec != n || run.SpecRev == rev {
		return
	}
	text, ok := e.SessionSpec(f, n)
	if !ok {
		return
	}
	// the edited spec goes into the run's directory, and the steer points
	// at it: every live step gets the steer as a turn and every later
	// step's brief carries it, so the whole spec in it would be copied
	// into each of them again on every edit
	path, err := e.writeRunFile(f.ID, run.N, fmt.Sprintf("spec-revision-%d.md", rev), text)
	if err != nil {
		return
	}
	_ = e.steerLocked(ctx, run, fmt.Sprintf("an edit of the spec (S%d revision %d; the run is judged against revision %d). "+
		"Read it at %s", n, rev, run.SpecRev, path))
}

// --- the tick --------------------------------------------------------------

// WorkflowTickResult says what one tick did.
type WorkflowTickResult struct {
	Actions []string
}

// WorkflowTick advances the live run of every session this process may
// conduct one step (§25.7): it starts a queued run whose session has none
// open, parks a run that was running when gummi quit, and executes
// workflowpolicy.Decide's actions on a running one. A process conducts
// only the runs of the sessions whose card lock it holds (holdRuns): a
// session another gummi drives is that gummi's to tick. Every long action
// runs in the background and sends EventWorkflow when it settles, so the
// driving loop ticks again.
func (e *Engine) WorkflowTick(ctx context.Context) (WorkflowTickResult, error) {
	var res WorkflowTickResult
	if e.cfg.Store == nil {
		return res, nil
	}
	runs, err := e.cfg.Store.LiveRuns(ctx)
	if err != nil {
		return res, err
	}
	seen := map[domain.FeatureID]bool{}
	var errs []error
	for _, r := range runs {
		e.rebaseline(ctx, r)
		if seen[r.Session] {
			continue
		}
		seen[r.Session] = true
		acts, err := e.tickHeld(ctx, r.Session)
		res.Actions = append(res.Actions, acts...)
		if err != nil {
			// one session's run in trouble is no reason the others stand
			// still: each is ticked, and every error is said
			errs = append(errs, fmt.Errorf("%s: %w", r.Session, err))
		}
	}
	// a hold on a session whose runs have all ended goes
	rt := e.wfRT()
	rt.mu.Lock()
	var gone []domain.FeatureID
	for s := range rt.held {
		if !seen[s] {
			gone = append(gone, s)
		}
	}
	rt.mu.Unlock()
	for _, s := range gone {
		e.letGoRuns(s)
	}
	return res, errors.Join(errs...)
}

// WorkflowTickSession advances session's live run alone: the headless
// driver's tick, which conducts its own session's run and no other.
func (e *Engine) WorkflowTickSession(ctx context.Context, session domain.FeatureID) (WorkflowTickResult, error) {
	var res WorkflowTickResult
	if e.cfg.Store == nil {
		return res, nil
	}
	acts, err := e.tickHeld(ctx, session)
	res.Actions = acts
	return res, err
}

// tickHeld ticks session's runs when this process holds its card, and
// lets the hold go once the session has nothing open or queued.
func (e *Engine) tickHeld(ctx context.Context, session domain.FeatureID) ([]string, error) {
	st := e.cfg.Store
	open, err := st.OpenRun(ctx, session)
	if err != nil {
		return nil, err
	}
	queued, err := st.QueuedRun(ctx, session)
	if err != nil {
		return nil, err
	}
	if open == nil && queued == nil {
		e.letGoRuns(session)
		return nil, nil
	}
	if !e.holdRuns(session) {
		return nil, nil
	}
	return e.tickSession(ctx, session)
}

func (e *Engine) tickSession(ctx context.Context, session domain.FeatureID) ([]string, error) {
	st := e.cfg.Store
	// steps this tick started are running from now: the faces are told
	// once the run's lock is let go, rather than when each step's backend
	// has come up, which can take a while
	started := false
	defer func() {
		if started {
			e.workflowEvent(session)
		}
	}()
	unlock := e.lockRuns(session)
	defer unlock()
	var acts []string
	run, err := st.OpenRun(ctx, session)
	if err != nil {
		return nil, err
	}
	if run == nil {
		q, err := st.QueuedRun(ctx, session)
		if err != nil || q == nil {
			return nil, err
		}
		if e.freeformBusy(session) {
			// the session is mid-turn — reading the run before's report,
			// committing what it keeps — in the tree the queued run would
			// write: it starts once that turn ends
			return nil, nil
		}
		if err := e.startRunLocked(ctx, q); err != nil {
			return nil, err
		}
		e.runNote(session, fmt.Sprintf("run %d started: the run before it ended", q.N))
		acts = append(acts, fmt.Sprintf("start run %d", q.N))
		run = q
	}
	rt := e.wfRT()
	// a run this process did not start or resume was running under a gummi
	// that is gone — this one before a restart, or another that let the card
	// go: its steps have no backend now. It stays parked until a person
	// resumes it (§25.7), and asks them to.
	if run.State == domain.RunRunning && !rt.conducts(session, run.N) && !run.Stopping && e.resumeWatching(ctx, run) {
		acts = append(acts, "poll after restart")
	}
	if run.State == domain.RunRunning && !rt.conducts(session, run.N) && !run.Stopping {
		if err := e.parkRunLocked(ctx, run, "parked: gummi stopped while it ran"); err != nil {
			return acts, err
		}
		if e.raiseRunDecision(run, domain.RunDecision{
			Kind: domain.RunDecideResume,
			Question: fmt.Sprintf("Run %d was running when gummi stopped, and is parked: its steps' work so far is in the tree. "+
				"Resume it, or stop it?", run.N),
			Options: []string{WorkflowResumeRun, WorkflowStopRun},
		}) {
			if err := st.UpdateRun(ctx, run); err != nil {
				return acts, err
			}
		}
		return append(acts, "park"), nil
	}
	in, steps, err := e.runSnapshot(ctx, run)
	if err != nil {
		return acts, err
	}
	if run.State == domain.RunPaused && !run.Stopping && run.OutOfBudget && nothingToSpendOn(in) &&
		(run.Pending == nil || run.Pending.Kind == domain.RunDecideBudget) {
		// out of money, but what is left costs none — the step that spent
		// the pool has settled, and the run has only to interrupt a branch
		// its join no longer needs, judge an until, ask the person or end:
		// the pause is lifted rather than holding that behind a top-up
		run.Pending, run.OutOfBudget, run.State, run.Reason = nil, false, domain.RunRunning, ""
		in.State = domain.RunRunning
		e.clockStart(run)
		rt.conduct(session, run.N)
		if err := st.UpdateRun(ctx, run); err != nil {
			return acts, err
		}
		e.runNote(session, fmt.Sprintf("run %d: the session's budget is spent, but nothing left to do spends; the run carries on", run.N))
		acts = append(acts, "unpause")
	}
	if run.State == domain.RunPaused && !run.Stopping {
		// out of money with another decision in the way: put the top-up to
		// the person once that one is answered
		if run.OutOfBudget && run.Pending == nil {
			if e.raiseRunDecision(run, e.budgetDecision(ctx, run)) {
				acts = append(acts, "budget")
				return acts, st.UpdateRun(ctx, run)
			}
		}
		return acts, nil
	}
	// the first step starts on a baseline taken in the tree the run starts
	// in: one taken at the proposal stands while the tree and the spec have
	// not moved since, else the checks run again first (§25.5). What it
	// finds holds nothing up.
	if run.State == domain.RunRunning && !run.Stopping && len(steps) == 0 && !baselineCurrent(run) {
		done, err := e.askBaselineLocked(ctx, run)
		if err != nil {
			return acts, err
		}
		if !done {
			return append(acts, "baseline"), nil
		}
	}
	if e.raisePlanDecision(run, steps) {
		acts = append(acts, "plan")
	}
	e.pollWatches(run, steps)
	byPath := map[string]domain.RunStep{}
	for _, s := range steps {
		byPath[s.Path] = s
	}
	dirty := false
	for _, a := range workflowpolicy.Decide(in) {
		acts = append(acts, a.String())
		switch a.Kind {
		case workflowpolicy.Start:
			if err := e.newStepLocked(ctx, run, a, byPath); err != nil {
				return acts, err
			}
			started = true
			e.startStepAsync(session, run.N, a.Path)
		case workflowpolicy.Interrupt:
			e.settleStepLocked(ctx, session, run.N, a.Path, domain.StepStopped, a.Reason)
			e.interruptStep(byPath[a.Path].Card)
		case workflowpolicy.NeedUntil:
			e.judgeUntilAsync(run, a.Path, a.Step)
		case workflowpolicy.AskOnMax:
			dirty = e.raiseRunDecision(run, domain.RunDecision{
				Kind: domain.RunDecideMax, Step: a.Path,
				Question: fmt.Sprintf("Run %d: %s. Run one more iteration, continue to the next step, or stop the run?", run.N, a.Reason),
				Options:  []string{WorkflowOneMore, WorkflowContinue, WorkflowStopRun},
			}) || dirty
		case workflowpolicy.AskUnjudged:
			// the loop cannot tell whether it is done: the run waits on the
			// person rather than counting the iteration as not met
			if e.raiseRunDecision(run, domain.RunDecision{
				Kind: domain.RunDecideUnjudged, Step: a.Path,
				Question: fmt.Sprintf("Run %d waits after %s: %s. Run one more iteration (it does not count against the loop's max), "+
					"continue to the next step, or stop the run?", run.N, a.Path, a.Reason),
				Options: []string{WorkflowOneMore, WorkflowContinue, WorkflowStopRun},
			}) {
				dirty = true
				e.runNote(session, fmt.Sprintf("run %d waits on the person after %s: %s", run.N, a.Path, a.Reason))
			}
		case workflowpolicy.Pause:
			// the pool is spent: nothing new starts, and what runs stops
			if err := e.pauseForBudgetLocked(ctx, run, a.Reason); err != nil {
				return acts, err
			}
		case workflowpolicy.End:
			e.endRunAsync(session, run.N, a.State, a.Reason)
		case workflowpolicy.StartPass, workflowpolicy.PushPass, workflowpolicy.AskWatch,
			workflowpolicy.ParkWatch, workflowpolicy.EndWatch:
			d, err := e.watchActionLocked(ctx, run, a)
			if err != nil {
				return acts, err
			}
			dirty = d || dirty
			started = true
		}
	}
	if dirty {
		return acts, st.UpdateRun(ctx, run)
	}
	return acts, nil
}

// budgetDecision is the top-up a run paused on a spent pool puts to the
// person.
func (e *Engine) budgetDecision(ctx context.Context, run *domain.Run) domain.RunDecision {
	amount := e.runTopUp(ctx, run.Session)
	return domain.RunDecision{
		Kind: domain.RunDecideBudget, Amount: amount,
		Question: fmt.Sprintf("The session's budget is spent: run %d paused, its steps' work kept. Top the session up by %s to carry on?",
			run.N, domain.FormatDollars(float64(amount))),
		Options: []string{WorkflowTopUp, WorkflowStopRun},
	}
}

// runTopUp is the top-up a paused run suggests: as much again as the
// session's envelope, rounded up to 50 credits and never below 100. gummi
// does not guess what a run will spend; the person may top up by another
// amount.
func (e *Engine) runTopUp(ctx context.Context, session domain.FeatureID) int {
	env := 0
	if sess, err := e.cfg.Store.GetFeature(ctx, session); err == nil {
		env = sess.Budget.Envelope
	}
	return max((env+49)/50*50, 100)
}

// raisePlanDecision puts a planner's revised plan, waiting since its turn
// ended, to the person once no other decision is open. The caller writes
// the run when it reports true.
func (e *Engine) raisePlanDecision(run *domain.Run, steps []domain.RunStep) bool {
	if run.Pending != nil {
		return false
	}
	for _, s := range steps {
		if s.Role != domain.RolePlanner || s.State != domain.StepAsking || s.EndTree == "" {
			continue
		}
		entries, err := workflowrecipe.ParsePlanBlock(s.Output)
		if err != nil {
			continue
		}
		text, err := workflowrecipe.MarshalEntries(entries)
		if err != nil {
			continue
		}
		started := startedEntries(run.Recipe, steps)
		_, _, warnings := workflowrecipe.CheckReplacement(run.Recipe, started, entries)
		var b strings.Builder
		fmt.Fprintf(&b, "Run %d: step %s (planner) revised the plan. Its steps replace the run's remaining ones:\n\n", run.N, s.Path)
		profile := e.sessionProfile(run.Session)
		e.writeRecipeTree(&b, profile, entries, "")
		if started < len(run.Recipe.Steps) {
			b.WriteString("\nWhat they replace, which would not run:\n")
			e.writeRecipeTree(&b, profile, run.Recipe.Steps[started:], "")
		} else {
			b.WriteString("\nThey replace nothing: every step of the plan has run, and they would run after it.\n")
		}
		if _, items := e.frozenSpec(context.Background(), run); hasChecks(items) {
			fmt.Fprintf(&b, "\nThe run is still judged against spec S%d revision %d's done-when checks:\n", run.Spec, run.SpecRev)
			for _, it := range items {
				if strings.TrimSpace(it.Check) != "" {
					fmt.Fprintf(&b, "- %s: %s\n", it.ID, it.Check)
				}
			}
		}
		b.WriteString("\nAllow the revised plan?")
		if e.raiseRunDecision(run, domain.RunDecision{
			Kind: domain.RunDecidePlan, Step: s.Path, Question: b.String(),
			Options: []string{WorkflowAllowPlan, WorkflowKeepPlan}, RecipeYAML: string(text), Warnings: warnings,
		}) {
			return e.cfg.Store.UpdateRun(context.Background(), run) == nil
		}
	}
	return false
}

// newStepLocked writes a step instance's row as running, so the next tick
// does not start it again; its card, snapshot and prompt follow.
func (e *Engine) newStepLocked(ctx context.Context, run *domain.Run, a workflowpolicy.Action, have map[string]domain.RunStep) error {
	def, ok := e.stepDef(ctx, run, a.Path, a.Step)
	if !ok {
		return fmt.Errorf("the recipe has no step %s", a.Step)
	}
	inst := domain.RunStep{
		Session: run.Session, Run: run.N, Path: a.Path, StepID: a.Step, Role: domain.WorkflowRole(def.Kind()), Slot: def.Slot,
		State: domain.StepRunning, StartedAt: e.now(),
	}
	if _, exists := have[a.Path]; exists {
		return e.cfg.Store.UpdateRunStep(ctx, &inst)
	}
	return e.cfg.Store.CreateRunStep(ctx, &inst)
}

// runSnapshot builds the conductor's input, bringing each step's spend and
// whether it waits on a person up to date as it reads them.
func (e *Engine) runSnapshot(ctx context.Context, run *domain.Run) (workflowpolicy.Snapshot, []domain.RunStep, error) {
	st := e.cfg.Store
	steps, err := st.RunSteps(ctx, run.Session, run.N)
	if err != nil {
		return workflowpolicy.Snapshot{}, nil, err
	}
	in := workflowpolicy.Snapshot{
		State: run.State, Recipe: run.Recipe, Stopping: run.Stopping, Now: e.now(),
		Until: map[string]workflowpolicy.UntilResult{}, OnMax: map[string]workflowpolicy.OnMaxAnswer{},
	}
	spent := 0.0
	for i := range steps {
		s := &steps[i]
		changed := false
		if s.Card != "" {
			if f, err := st.GetFeature(ctx, s.Card); err == nil {
				if sp := f.Spend.CreditEquivalent(); sp != s.Spent {
					s.Spent, changed = sp, true
				}
			}
			asking := e.cardAsking(s.Card)
			switch {
			case s.State == domain.StepRunning && asking:
				s.State, changed = domain.StepAsking, true
			case s.State == domain.StepAsking && !asking && s.EndTree == "":
				s.State, changed = domain.StepRunning, true
			}
		}
		if changed {
			if err := st.UpdateRunStep(ctx, s); err != nil {
				return in, nil, err
			}
		}
		spent += s.Spent
		in.Steps = append(in.Steps, workflowpolicy.Step{Path: s.Path, State: s.State, Spent: s.Spent, Watch: s.Watch})
	}
	for k, v := range run.Until {
		in.Until[k] = workflowpolicy.UntilResult{Met: v.Met, Unjudged: v.Unjudged, Detail: v.Detail}
	}
	for k, v := range run.OnMax {
		in.OnMax[k] = workflowpolicy.OnMaxAnswer(v)
	}
	if spent != run.Spent {
		run.Spent = spent
		if err := st.UpdateRun(ctx, run); err != nil {
			return in, nil, err
		}
	}
	if sess, err := st.GetFeature(ctx, run.Session); err == nil {
		in.Left, in.Bounded = e.sessionPoolLeft(ctx, sess)
	}
	return in, steps, nil
}

// --- a repeat's until --------------------------------------------------------

func (e *Engine) judgeUntilAsync(run *domain.Run, iter, repeatID string) {
	rt := e.wfRT()
	session, n := run.Session, run.N
	key := fmt.Sprintf("until/%s/%d/%s", session, n, iter)
	if !rt.claim(key) {
		return
	}
	if !e.goBrief(func() {
		defer func() {
			rt.release(key)
			e.workflowEvent(session)
		}()
		ctx := e.lifetime()
		cur, err := e.cfg.Store.Run(ctx, session, n)
		if err != nil || cur == nil {
			return
		}
		j := e.judgeUntil(ctx, cur, iter, repeatID)
		unlock := e.lockRuns(session)
		defer unlock()
		cur, err = e.cfg.Store.Run(ctx, session, n)
		if err != nil || cur == nil {
			return
		}
		if cur.Until == nil {
			cur.Until = map[string]domain.UntilJudgement{}
		}
		cur.Until[iter] = j
		if err := e.cfg.Store.UpdateRun(ctx, cur); err != nil {
			return
		}
		verdict := "not met"
		switch {
		case j.Met:
			verdict = "met"
		case j.Unjudged:
			verdict = "could not be judged"
		}
		e.runNote(session, fmt.Sprintf("run %d: %s's condition %s after %s (%s)", n, repeatID, verdict, iter, j.Detail))
	}) {
		rt.release(key)
	}
}

// judgeUntil judges a repeat's until after the iteration at iter (§25.4):
// the done-when checks in the tree, an artifact's verdict, or the last
// step's output.
func (e *Engine) judgeUntil(ctx context.Context, run *domain.Run, iter, repeatID string) domain.UntilJudgement {
	r, ok := findRepeat(e.entriesFor(ctx, run, iter), repeatID)
	if !ok {
		return domain.UntilJudgement{Detail: "the recipe has no repeat " + repeatID}
	}
	switch r.Until.Kind() {
	case domain.UntilChecks:
		dir, err := e.sessionTree(ctx, run.Session)
		if err != nil {
			return domain.UntilJudgement{Detail: err.Error()}
		}
		_, items := e.frozenSpec(ctx, run)
		checks := doneWhenChecks(ctx, dir, items)
		if len(checks) == 0 {
			// nothing to judge by is not done: the person decides
			return domain.UntilJudgement{Unjudged: true, Detail: "the loop can't judge: the spec has no done-when checks"}
		}
		var failed, empty, undecided []string
		for id, c := range checks {
			switch {
			case c.Status == verify.StatusTimeout || c.Status == verify.StatusNotRun:
				undecided = append(undecided, id)
			case !c.OK:
				failed = append(failed, id)
			case c.RanNoTests:
				empty = append(empty, id)
			}
		}
		sort.Strings(failed)
		sort.Strings(empty)
		sort.Strings(undecided)
		switch {
		case len(failed) > 0:
			return domain.UntilJudgement{Detail: "failing: " + strings.Join(failed, ", ")}
		case len(undecided) > 0:
			// a check that timed out or never ran decided nothing
			return domain.UntilJudgement{Unjudged: true, Detail: "the loop can't judge: the check of " + strings.Join(undecided, ", ") + " did not finish"}
		case len(empty) > 0:
			// a check that ran no tests proves nothing either way
			return domain.UntilJudgement{Unjudged: true, Detail: "the loop can't judge: the check of " + strings.Join(empty, ", ") + " ran no tests"}
		}
		return domain.UntilJudgement{Met: true, Detail: "every done-when check passes"}
	case domain.UntilVerdict:
		path := e.runArtifacts(run)[r.Until.In]
		raw, err := os.ReadFile(path) // #nosec G304 -- an artifact in the run's own directory
		if errors.Is(err, os.ErrNotExist) {
			// never written, nor captured from the step's output: the loop
			// cannot judge, which is not the same as not met
			return domain.UntilJudgement{Unjudged: true, Detail: fmt.Sprintf("the loop can't judge: %s (%s) was never written",
				r.Until.In, filepath.Base(path))}
		}
		if err != nil {
			return domain.UntilJudgement{Detail: "reading " + r.Until.In + ": " + err.Error()}
		}
		var v struct {
			Verdict string `json:"verdict"`
		}
		if err := json.Unmarshal(raw, &v); err != nil {
			return domain.UntilJudgement{Detail: r.Until.In + " is not JSON: " + err.Error()}
		}
		if strings.EqualFold(strings.TrimSpace(v.Verdict), r.Until.Verdict) {
			return domain.UntilJudgement{Met: true, Detail: "verdict " + v.Verdict}
		}
		return domain.UntilJudgement{Detail: "verdict " + strconv.Quote(v.Verdict)}
	case domain.UntilContains:
		steps, _ := e.cfg.Store.RunSteps(ctx, run.Session, run.N)
		last := ""
		for _, s := range steps {
			if strings.HasPrefix(s.Path, iter+"/") && s.State.Settled() {
				last = s.Output
			}
		}
		if strings.Contains(last, r.Until.Contains) {
			return domain.UntilJudgement{Met: true, Detail: "the output says " + strconv.Quote(r.Until.Contains)}
		}
		return domain.UntilJudgement{Detail: "the output does not say " + strconv.Quote(r.Until.Contains)}
	}
	return domain.UntilJudgement{Detail: "the repeat has no condition"}
}

func findRepeat(es []domain.Entry, id string) (domain.Repeat, bool) {
	for _, en := range es {
		if en.Repeat == nil {
			continue
		}
		if en.Repeat.ID == id {
			return *en.Repeat, true
		}
		if r, ok := findRepeat(en.Repeat.Steps, id); ok {
			return r, true
		}
	}
	return domain.Repeat{}, false
}

// --- a run's end -----------------------------------------------------------

func (e *Engine) endRunAsync(session domain.FeatureID, n int, end domain.RunState, why string) {
	rt := e.wfRT()
	key := fmt.Sprintf("end/%s/%d", session, n)
	if !rt.claim(key) {
		return
	}
	if !e.goBrief(func() {
		defer func() {
			rt.release(key)
			e.workflowEvent(session)
		}()
		e.endRun(e.lifetime(), session, n, end, why)
	}) {
		rt.release(key)
	}
}

// endRun ends a run (§25.6): the done-when checks in the tree, each item's
// result, and the report — into the spec's Report section, the run's directory, and the
// session as a turn under the workflow's actor. A queued run starts on
// the next tick.
func (e *Engine) endRun(ctx context.Context, session domain.FeatureID, n int, end domain.RunState, why string) {
	st := e.cfg.Store
	run, err := st.Run(ctx, session, n)
	if err != nil || run == nil || !run.State.Open() {
		return
	}
	// a tree gummi cannot read (an unreadable file, a checkout gone) still
	// ends the run: left open, it would hold the session's landing and be
	// tried again on every tick, and nothing would tell the person
	var treeErr error
	dir, err := e.sessionTree(ctx, session)
	endTree := ""
	if err != nil {
		treeErr = err
	} else if endTree, err = e.snapshotRun(ctx, session, n); err != nil {
		// every tree the run spans: the primary and each attachment's
		treeErr, endTree = err, ""
	}
	if treeErr != nil && end == domain.RunDone {
		end, why = domain.RunPartial, "gummi could not read the tree at the end: "+treeErr.Error()
	}
	_, items := e.frozenSpec(ctx, run)
	steps, _ := st.RunSteps(ctx, session, n)
	last := ""
	for _, s := range steps {
		if s.State == domain.StepDone {
			last = s.Output
		}
	}
	results := withBaseline(itemResults(items, doneWhenChecks(ctx, dir, items), last), run.Baseline)
	pass := checkedPass(results)
	switch {
	case end == domain.RunDone && !pass && vacuous(results):
		end, why = domain.RunPartial, "a done-when check ran no tests"
	case end == domain.RunDone && !pass:
		end, why = domain.RunPartial, "a checked done-when item does not pass"
	case end == domain.RunStopped && why == "" || end == domain.RunStopped && why == "stopped":
		why = run.Reason
	}
	var paths []string
	if endTree != "" {
		_, paths, _ = e.diffTrees(ctx, session, run.StartTree, endTree)
	}
	var commits []string
	if run.StartHead != "" && dir != "" {
		if out, err := objectiveGit(ctx, dir, "log", "--format=%h %s", run.StartHead+"..HEAD"); err == nil && out != "" {
			commits = strings.Split(out, "\n")
		}
	}
	report := runReport(run, end, why, items, results, steps, paths, commits)
	if treeErr != nil {
		report += "\n\ngummi could not read the session's tree at the end (" + treeErr.Error() + "): the files the run changed are not listed, " +
			"and Revert run cannot put them back."
	}

	unlock := e.lockRuns(session)
	cur, err := st.Run(ctx, session, n)
	if err != nil || cur == nil || !cur.State.Open() {
		unlock()
		return
	}
	cur.State, cur.Reason, cur.EndedAt = end, why, e.now()
	e.clockStop(cur)
	cur.EndTree, cur.Results, cur.Report, cur.Commits = endTree, results, report, commits
	cur.Stopping, cur.Pending, cur.OutOfBudget = false, nil, false
	err = st.UpdateRun(ctx, cur)
	unlock()
	if err != nil {
		return
	}
	_, _ = e.writeRunFile(session, n, "report.md", report)
	e.writeRunReport(ctx, cur)
	e.runNote(session, fmt.Sprintf("run %d ended %s", n, end))
	e.noteDispatcher(ctx, session, n, end, report)
	if e.keepsReports(session) {
		return
	}
	msg := fmt.Sprintf("Workflow run %d has reported. Read the report and the diff it left in the tree, tell the person what it means, "+
		"and commit what you keep — gummi commits nothing.\n\n%s", n, report)
	_ = e.sendSessionTurn(WithActor(ctx, state.ActorWorkflow), session, msg)
}

// runReport writes a run's report: each done-when item's result, the
// DECISION: and OPEN: lines its steps wrote, the files it changed, the
// commits made while it ran, and what it spent.
func runReport(run *domain.Run, end domain.RunState, why string, items []domain.DoneWhen, results []domain.ItemResult,
	steps []domain.RunStep, paths, commits []string,
) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Run %d (%s) on spec S%d, revision %d: %s", run.N, recipeLabel(run), run.Spec, run.SpecRev, end)
	if why != "" && end != domain.RunDone {
		b.WriteString(" — " + why)
	}
	b.WriteString(".\n")
	says := map[string]string{}
	for _, it := range items {
		says[it.ID] = it.Says
	}
	if len(results) > 0 {
		b.WriteString("\nDone when:\n")
		for _, r := range results {
			how := "judged"
			switch {
			case r.Checked:
				how = "check"
			case r.Verdict == domain.ItemUnjudged:
				how = "unjudged"
			}
			fmt.Fprintf(&b, "%s %s %s (%s)", verdictMark(r.Verdict), r.Item, says[r.Item], how)
			if ch := ResultChange(r); ch != "" {
				b.WriteString(": " + ch)
			}
			if r.Detail != "" && r.Verdict != domain.ItemMet {
				b.WriteString(": " + firstLine(r.Detail))
			}
			b.WriteString("\n")
		}
	}
	var decisions, open, failed, strayed []string
	for _, s := range steps {
		if len(s.Changed) > 0 {
			strayed = append(strayed, fmt.Sprintf("%s (%s) changed %s", s.Path, s.Role, strings.Join(s.Changed, ", ")))
		}
		n := workflowrecipe.GatherNotes(s.Output)
		for _, d := range n.Decisions {
			decisions = append(decisions, s.Path+": "+d)
		}
		for _, o := range n.Open {
			open = append(open, s.Path+": "+o)
		}
		if s.State == domain.StepFailed || s.State == domain.StepStopped {
			failed = append(failed, fmt.Sprintf("%s %s: %s", s.Path, s.State, s.Reason))
		}
	}
	if len(failed) > 0 {
		b.WriteString("\nSteps that did not finish:\n- " + strings.Join(failed, "\n- ") + "\n")
	}
	if len(strayed) > 0 {
		b.WriteString("\nReading steps that changed files, although they only read:\n- " + strings.Join(strayed, "\n- ") + "\n")
	}
	if len(decisions) > 0 {
		b.WriteString("\nDecisions:\n- " + strings.Join(decisions, "\n- ") + "\n")
	}
	if len(open) > 0 {
		b.WriteString("\nLeft open:\n- " + strings.Join(open, "\n- ") + "\n")
	}
	if len(paths) > 0 {
		b.WriteString("\nFiles changed, uncommitted unless a commit below holds them:\n- " + strings.Join(paths, "\n- ") + "\n")
	} else {
		b.WriteString("\nThe run left the tree as it found it.\n")
	}
	if len(commits) > 0 {
		b.WriteString("\nCommits made while it ran:\n- " + strings.Join(commits, "\n- ") + "\n")
	}
	spent := 0.0
	for _, s := range steps {
		spent += s.Spent
	}
	fmt.Fprintf(&b, "\nSpent %.0f credits (%s) from the session's budget.\n", spent, domain.FormatDollars(spent))
	return strings.TrimRight(b.String(), "\n")
}

// writeRunReport writes the report into its spec's Report section,
// replacing the last one, as a new revision by the workflow.
func (e *Engine) writeRunReport(ctx context.Context, run *domain.Run) {
	sess, err := e.cfg.Store.GetFeature(ctx, run.Session)
	if err != nil {
		return
	}
	path := e.SessionSpecPath(sess, run.Spec)
	if path == "" {
		return
	}
	unlock := spec.LockFile(path)
	raw, err := os.ReadFile(path) // #nosec G304 -- the session's own spec file
	if err != nil {
		unlock()
		return
	}
	body := "\n" + run.Report + "\n\n"
	out, _, err := spec.ReplaceSection(string(raw), specSectionReport, body)
	if errors.Is(err, spec.ErrSectionUnknown) {
		out, err = strings.TrimRight(string(raw), "\n")+"\n\n## "+specSectionReport+"\n"+body, nil
	}
	if err == nil {
		err = writeFile(path, strings.TrimRight(out, "\n")+"\n")
	}
	unlock()
	if err != nil {
		return
	}
	if raw, err := os.ReadFile(path); err == nil { // #nosec G304 -- as above
		_, _ = e.recordSpecRevision(ctx, run.Session, run.Spec, string(raw), state.ActorWorkflow)
	}
}

// vacuous reports whether a checked result is unjudged: its check passed
// and ran no tests.
func vacuous(rs []domain.ItemResult) bool {
	for _, r := range rs {
		if r.Checked && r.Verdict == domain.ItemUnjudged {
			return true
		}
	}
	return false
}

// specSectionReport is the section a run's report is written into.
const specSectionReport = "Report"

// --- the session's view ----------------------------------------------------

// WorkflowStatusText is workflow_status's answer: the session's latest
// run.
func (e *Engine) WorkflowStatusText(ctx context.Context, session domain.FeatureID) (string, error) {
	runs, err := e.cfg.Store.SessionRuns(ctx, session)
	if err != nil {
		return "", err
	}
	if len(runs) == 0 {
		return "", errors.New("this session has no workflow runs")
	}
	r := runs[len(runs)-1]
	if open, _ := e.cfg.Store.OpenRun(ctx, session); open != nil {
		r = *open
	}
	var b strings.Builder
	fmt.Fprintf(&b, "run %d (%s) on spec S%d: %s", r.N, recipeLabel(&r), r.Spec, r.State)
	if r.Reason != "" && r.State != domain.RunRunning {
		b.WriteString(" — " + r.Reason)
	}
	fmt.Fprintf(&b, " · spent %.0f", r.Spent)
	if sess, err := e.cfg.Store.GetFeature(ctx, session); err == nil {
		if left, bounded := e.sessionPoolLeft(ctx, sess); bounded {
			fmt.Fprintf(&b, " · the session has %.0f left", math.Max(0, left))
		}
	}
	b.WriteString("\n")
	if r.Pending != nil {
		fmt.Fprintf(&b, "waiting on the person: %s\n", firstLine(r.Pending.Question))
	}
	steps, _ := e.cfg.Store.RunSteps(ctx, session, r.N)
	for _, s := range steps {
		fmt.Fprintf(&b, "- %s %s: %s · spent %.0f", s.Path, s.Role, s.State, s.Spent)
		if s.Reason != "" {
			fmt.Fprintf(&b, " (%s)", s.Reason)
		}
		b.WriteString("\n")
	}
	if q, _ := e.cfg.Store.QueuedRun(ctx, session); q != nil && q.N != r.N {
		fmt.Fprintf(&b, "queued behind it: run %d (%s)\n", q.N, recipeLabel(q))
	}
	if r.Report != "" {
		b.WriteString("\n" + r.Report)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// RunHolds says why a session may not land yet: a workflow run is still
// open on its tree (§25.8).
func (e *Engine) RunHolds(ctx context.Context, session domain.FeatureID) string {
	if e.cfg.Store == nil {
		return ""
	}
	r, err := e.cfg.Store.OpenRun(ctx, session)
	if err != nil || r == nil {
		return ""
	}
	return fmt.Sprintf("its workflow run %d is still %s — let it finish or stop it first", r.N, r.State)
}

// RunHoldsClose says why a session may not be closed without landing
// (a hand-off) yet, "" when it may: a workflow run is open or queued on
// its tree. A run is refused a close as an agent mid-turn is — the hand-off
// is not offered while either writes the branch it keeps — so the run's
// report reaches the session it was for (§25.14). A run still proposed is
// the person's to answer; the close withdraws it.
func (e *Engine) RunHoldsClose(ctx context.Context, session domain.FeatureID) string {
	if e.cfg.Store == nil {
		return ""
	}
	r, err := e.cfg.Store.OpenRun(ctx, session)
	if err == nil && r == nil {
		r, err = e.cfg.Store.QueuedRun(ctx, session)
	}
	if err != nil || r == nil {
		return ""
	}
	return fmt.Sprintf("its workflow run %d is still %s, and a session is not closed under a run, as it is not under its agent mid-turn — "+
		"let the run finish, or stop it, first", r.N, r.State)
}

// withdrawProposals withdraws the session's runs still waiting on the
// person, as the session closes.
func (e *Engine) withdrawProposals(ctx context.Context, session domain.FeatureID, why string) error {
	if e.cfg.Store == nil {
		return nil
	}
	unlock := e.lockRuns(session)
	defer unlock()
	runs, err := e.cfg.Store.SessionRuns(ctx, session)
	if err != nil {
		return err
	}
	for i := range runs {
		r := runs[i]
		if r.State != domain.RunProposed {
			continue
		}
		r.State, r.Pending, r.Reason = domain.RunDenied, nil, "withdrawn: "+why
		if err := e.cfg.Store.UpdateRun(ctx, &r); err != nil {
			return err
		}
		e.workflowEvent(session)
	}
	return nil
}

// workflowTurnNote is the note a session's turn begins with while a run
// is open on its tree (§25.8), "" when none is.
func (e *Engine) workflowTurnNote(ctx context.Context, session domain.FeatureID) string {
	if e.cfg.Store == nil || actorOf(ctx) == state.ActorWorkflow {
		return ""
	}
	r, err := e.cfg.Store.OpenRun(ctx, session)
	if err != nil || r == nil {
		return ""
	}
	return fmt.Sprintf("[gummi: workflow run %d is changing this session's tree. Do not touch files until its report arrives; "+
		"talking, reading and planning are fine.]", r.N)
}

// sessionRunsSpent is what every workflow run of the session has spent:
// all of it came out of the session's envelope.
func (e *Engine) sessionRunsSpent(ctx context.Context, session domain.FeatureID) float64 {
	if e.cfg.Store == nil {
		return 0
	}
	spends, err := e.cfg.Store.SessionStepSpends(ctx, session)
	if err != nil {
		return 0
	}
	t := 0.0
	for _, sp := range spends {
		t += sp.CreditEquivalent()
	}
	return t
}

// stepBudget is a step card's cap: what its session's pool has left, at
// least one turn's reserve while it has any, 0 once it is spent, or the
// board's stage budget for a session with no envelope. It is read when the
// step's backend spawns; the pool is held across all the session's steps
// as they spend (checkRunPool).
func (e *Engine) stepBudget(ctx context.Context, f domain.Feature) float64 {
	sess, err := e.cfg.Store.GetFeature(ctx, f.Step().Session)
	if err != nil {
		return e.cfg.StageBudget
	}
	left, bounded := e.sessionPoolLeft(ctx, sess)
	switch {
	case !bounded:
		return e.cfg.StageBudget
	case left <= 0:
		return 0
	}
	return math.Max(left, e.turnReserve())
}

// errStepPoolSpent refuses a step's backend while its session's pool is
// spent.
var errStepPoolSpent = errors.New("the session's budget is spent: raise the session's envelope to carry on")

// stepPoolSpent reports whether f is a step's card whose session's pool
// is spent: no backend of it starts until the session is topped up.
func (e *Engine) stepPoolSpent(ctx context.Context, f domain.Feature) bool {
	if e.stepOf(ctx, f) == nil {
		return false
	}
	sess, err := e.cfg.Store.GetFeature(ctx, f.Step().Session)
	if err != nil {
		return false
	}
	left, bounded := e.sessionPoolLeft(ctx, sess)
	return bounded && left <= 0
}

// checkRunPool holds the session's envelope across its steps: a step's
// spend lands, and when the session's pool is spent the open run pauses
// at once and every live step's backend stops — the steps share one pool,
// and each one's own cap was what the pool had left when it spawned.
func (e *Engine) checkRunPool(card domain.FeatureID) {
	e.goBrief(func() {
		ctx := e.lifetime()
		f, err := e.cfg.Store.GetFeature(ctx, card)
		if err != nil || !f.IsChild() {
			return
		}
		inst := e.stepOf(ctx, f)
		if inst == nil {
			return
		}
		sess, err := e.cfg.Store.GetFeature(ctx, inst.Session)
		if err != nil {
			return
		}
		if left, bounded := e.sessionPoolLeft(ctx, sess); !bounded || left > 0 {
			return
		}
		unlock := e.lockRuns(inst.Session)
		defer unlock()
		run, err := e.cfg.Store.OpenRun(ctx, inst.Session)
		if err != nil || run == nil || run.N != inst.Run || run.State != domain.RunRunning || run.Stopping {
			return
		}
		in, _, err := e.runSnapshot(ctx, run)
		if err != nil {
			return
		}
		why := fmt.Sprintf("the session's budget is spent (the run has spent %s)", domain.FormatDollars(workflowpolicy.Spent(in)))
		_ = e.pauseForBudgetLocked(ctx, run, why)
	})
}

// nothingToSpendOn reports whether a run paused on a spent pool has only
// work left that costs no money: conducted as running, it would neither
// start a step nor keep one live, so Decide would not pause it. A pool
// with money left answers no: a run paused out of budget with some left
// waits on what that money could not cover (a dispatch's envelope), which
// Decide does not see.
func nothingToSpendOn(in workflowpolicy.Snapshot) bool {
	if !in.Bounded || in.Left > 0 {
		return false
	}
	in.State = domain.RunRunning
	for _, a := range workflowpolicy.Decide(in) {
		if a.Kind == workflowpolicy.Pause || a.Kind == workflowpolicy.Start {
			return false
		}
	}
	return true
}

// pauseForBudgetLocked parks a running run whose session's pool is spent:
// it puts the top-up to the person and stops every live step's backend,
// its work kept in the tree. The caller holds the run lock.
func (e *Engine) pauseForBudgetLocked(ctx context.Context, run *domain.Run, why string) error {
	run.State, run.OutOfBudget, run.Reason = domain.RunPaused, true, why
	e.clockStop(run)
	e.raiseRunDecision(run, e.budgetDecision(ctx, run))
	if err := e.cfg.Store.UpdateRun(ctx, run); err != nil {
		return err
	}
	e.stopRunSteps(ctx, run)
	e.runNote(run.Session, fmt.Sprintf("run %d paused: %s", run.N, why))
	e.noteDispatcherEvent(ctx, run.Session, fmt.Sprintf("run %d paused, waiting for the person: %s", run.N, why))
	e.workflowEvent(run.Session)
	return nil
}

// stopRunSteps stops the backend of every live step of run: the run is
// parked, and nothing of it spends until it carries on.
func (e *Engine) stopRunSteps(ctx context.Context, run *domain.Run) {
	steps, _ := e.cfg.Store.RunSteps(ctx, run.Session, run.N)
	for _, s := range steps {
		if s.Card != "" && s.State.Live() {
			if ff := e.Freeform(s.Card); ff != nil {
				stopStepBackend(ff)
			}
		}
	}
}

// stopStepBackend stops a step's backend as an idle-out does: marked done,
// so the turn that carries the step on spawns a fresh one, picking its
// conversation up, rather than finding this one stopped.
func stopStepBackend(ff *FreeformSession) {
	if sess := ff.Session(); sess != nil {
		sess.setState(StateDone)
	}
	ff.stopBackend()
}
