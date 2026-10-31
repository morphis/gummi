package engine

// A workflow run's step (DESIGN §25.6, §25.10): a fresh-context agent on a
// hidden card, a child of the session, whose working directory is the
// session's own checkout — no branch and no worktree of its own. gummi
// snapshots the tree when the step starts, fills its prompt, and sends it
// under the workflow's actor; when its turn ends gummi records the end
// snapshot and its output, which is its final message. A person may talk
// to a step's agent while it runs and after it has ended (StepSend).

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/workflowpolicy"
	"github.com/morphis/gummi/internal/workflowrecipe"
)

// planTriesCap is how many times a planner's gummi-workflow block that
// fails the recipe check goes back to it before the step fails.
const planTriesCap = 2

// stepOf is the step instance a card works, nil when it is no step's card.
func (e *Engine) stepOf(ctx context.Context, f domain.Feature) *domain.RunStep {
	if e.cfg.Store == nil || !f.IsChild() {
		return nil
	}
	st, err := e.cfg.Store.RunStepByCard(ctx, f.ID)
	if err != nil {
		return nil
	}
	return st
}

// stepTreeDir is the working directory of a step's card: its session's
// checkout. ok is false for a card that is no step's.
func (e *Engine) stepTreeDir(ctx context.Context, f domain.Feature) (string, bool, error) {
	if e.stepOf(ctx, f) == nil {
		return "", false, nil
	}
	dir, err := e.sessionTree(ctx, f.Step().Session)
	return dir, true, err
}

// stepSlots is the profile slots a step's model is looked up in, in
// order: the slot its recipe named, then its role's own, then the role a
// role with no slot of its own resembles (a planner's and a contractor's
// design work, the architect), then the implementer.
func stepSlots(role domain.WorkflowRole, slot string) []agent.Role {
	var out []agent.Role
	if slot != "" {
		out = append(out, agent.Role(slot))
	}
	out = append(out, agent.Role(role))
	switch role {
	case domain.RolePlanner, domain.RoleContractor:
		out = append(out, agent.RoleArchitect)
	}
	if role != domain.RoleImplementer {
		out = append(out, agent.RoleImplementer)
	}
	return out
}

// stepRole is the model and backend a step of role runs on under a
// profile: the first of its stepSlots the profile declares, else the
// engine's single model on the default backend.
func (e *Engine) stepRole(profile string, role domain.WorkflowRole, slot string) (config.RoleConfig, string) {
	return e.resolveRoleWithFallback(profile, stepSlots(role, slot)...)
}

// stepRoleFor is stepRole for a step as its recipe wrote it: an agent or
// model the step names directly wins over its slot. An agent alone runs
// that backend's default model; a model alone runs on the backend the
// profile gives the step.
func (e *Engine) stepRoleFor(profile string, step domain.Step) (config.RoleConfig, string) {
	rc, backend := e.stepRole(profile, step.Role, step.Slot)
	if step.Agent == "" && step.Model == "" {
		return rc, backend
	}
	if step.Agent != "" {
		backend = step.Agent
		rc = config.RoleConfig{Backend: backend}
	}
	if step.Model != "" {
		rc.Model = step.Model
	}
	return rc, backend
}

// stepModelProblem refuses a step whose model its backend cannot run —
// a model alone runs on the backend the profile gives the step — with the
// refusal a session's picker gives. A backend gummi has no adapter for in
// SessionBackends (a test's fake) is not judged.
func (e *Engine) stepModelProblem(profile string, step domain.Step) error {
	if step.Model == "" {
		return nil
	}
	_, backend := e.stepRoleFor(profile, step)
	if backend == "" {
		if a := e.agentFor(""); a != nil {
			backend = a.Name()
		}
	}
	if !slices.Contains(SessionBackends, backend) {
		return nil
	}
	if err := CheckSessionModel(backend, step.Model); err != nil {
		return fmt.Errorf("step %s: %w", step.ID, err)
	}
	return nil
}

// sessionProfile is a session's profile, "" when the card cannot be read
// (the default profile, then).
func (e *Engine) sessionProfile(id domain.FeatureID) string {
	f, err := e.feature(context.Background(), id)
	if err != nil {
		return ""
	}
	return f.Profile
}

// stepModelWords is what a step runs on, as a proposal says it.
func (e *Engine) stepModelWords(profile string, step domain.Step) string {
	rc, backend := e.stepRoleFor(profile, step)
	if backend == "" {
		if a := e.agentFor(""); a != nil {
			backend = a.Name()
		}
	}
	if rc.Model == "" {
		return backend + "'s default model"
	}
	return rc.Model + " on " + backend
}

func stepKey(session domain.FeatureID, n int, path string) string {
	return fmt.Sprintf("step/%s/%d/%s", session, n, path)
}

// startStepAsync starts a step instance whose row is written, off the
// tick: its snapshot, prompt, card and first turn.
func (e *Engine) startStepAsync(session domain.FeatureID, n int, path string) {
	rt := e.wfRT()
	key := stepKey(session, n, path)
	if !rt.claim(key) {
		return
	}
	if !e.goWorkflow(func() {
		defer func() {
			rt.release(key)
			e.workflowEvent(session)
		}()
		ctx := e.lifetime()
		err := e.startStep(ctx, session, n, path)
		switch {
		case errors.Is(err, errStepPoolSpent):
			// a sibling spent the pool between the tick and this start: the
			// run pauses for a top-up, and the step, never briefed, starts
			// again when it carries on — it did nothing to fail over
			e.pauseOnSpentPool(ctx, session, n)
		case err != nil && ctx.Err() == nil:
			e.settleStep(ctx, session, n, path, domain.StepFailed, "it could not start: "+err.Error())
		}
	}) {
		rt.release(key)
	}
}

func (e *Engine) startStep(ctx context.Context, session domain.FeatureID, n int, path string) error {
	st := e.cfg.Store
	run, err := st.Run(ctx, session, n)
	if err != nil || run == nil {
		return fmt.Errorf("run %d is gone", n)
	}
	inst, err := st.RunStep(ctx, session, n, path)
	if err != nil || inst == nil {
		return fmt.Errorf("step %s is gone", path)
	}
	step, ok := e.stepDef(ctx, run, inst.Path, inst.StepID)
	if !ok {
		return fmt.Errorf("the recipe has no step %s", inst.StepID)
	}
	sess, err := st.GetFeature(ctx, session)
	if err != nil {
		return err
	}
	e.stampArtifacts(session, n, path, step)
	startTree, err := e.snapshotRun(ctx, session, n)
	if err != nil {
		return fmt.Errorf("snapshotting the tree: %w", err)
	}
	steps, err := st.RunSteps(ctx, session, n)
	if err != nil {
		return err
	}
	text, _ := e.runItems(ctx, run)
	vars := workflowrecipe.Vars{Spec: text, Inputs: run.Inputs, Artifacts: e.runArtifacts(run)}
	vars.Outputs, vars.Previous, vars.PreviousOutput = stepOutputs(run.Recipe, steps, path)
	e.passVars(ctx, run, path, &vars)
	if strings.Contains(step.Prompt, "run.diff") {
		if vars.RunDiff, err = e.writeRunDiff(ctx, run, path, startTree); err != nil {
			return fmt.Errorf("writing the run's diff: %w", err)
		}
	}
	if step.Agentless() {
		return e.startAgentless(ctx, run, sess, inst, step, startTree, vars)
	}
	filled, err := workflowrecipe.Fill(step.Prompt, vars)
	if err != nil {
		return err
	}
	brief := stepBrief(run, step, path, filled)
	card := domain.Feature{}
	minted := false
	if inst.Card != "" {
		if card, err = st.GetFeature(ctx, inst.Card); err != nil {
			return err
		}
	} else {
		rc, backend := e.stepRoleFor(sess.Profile, step)
		if err := e.stepModelProblem(sess.Profile, step); err != nil {
			return err
		}
		card, err = e.mintStepCard(ctx, sess, domain.StepLink{Session: session, Run: n, Path: path},
			fmt.Sprintf("%s · run %d · %s", sess.Title, n, path), backend, rc.Model)
		if err != nil {
			return err
		}
		minted = true
	}
	unlock := e.lockRuns(session)
	cur, err := st.RunStep(ctx, session, n, path)
	if err != nil || cur == nil || !cur.State.Live() {
		unlock()
		if minted {
			// settled meanwhile, and the card is recorded nowhere
			e.dropStepCard(ctx, card.ID)
		}
		return err
	}
	cur.Card, cur.StartTree = card.ID, startTree
	err = st.UpdateRunStep(ctx, cur)
	unlock()
	if err != nil {
		return err
	}
	ff, err := e.OpenFreeform(ctx, card)
	if err != nil {
		return err
	}
	// The backend took a while to come up, and the run may have moved
	// meanwhile: a stop, a pause, a dropped step or a join that settled
	// it. Its brief goes only to a step still live in a running run, under
	// the run lock, so whatever moves the run next finds the turn in
	// flight and stops it; a step not briefed carries no prompt, and
	// starts again when its run carries on (carryOnRun).
	unlock = e.lockRuns(session)
	defer unlock()
	cur, err = st.RunStep(ctx, session, n, path)
	if err != nil || cur == nil {
		return err
	}
	if now, err := st.Run(ctx, session, n); err != nil || now == nil || now.State != domain.RunRunning || now.Stopping || !cur.State.Live() {
		if now != nil && now.State == domain.RunPaused {
			stopStepBackend(ff)
		}
		return err
	}
	e.runNote(session, fmt.Sprintf("run %d: %s (%s) started", n, path, step.Role))
	if err := ff.SendTurn(WithActor(ctx, state.ActorWorkflow), brief, nil); err != nil {
		return err
	}
	// the prompt marks the step briefed: written once the brief is sent, so
	// a crash before it starts the step again instead of carrying on from a
	// conversation that never got its brief
	cur.Prompt = filled
	return st.UpdateRunStep(ctx, cur)
}

// mintStepCard creates a step's hidden card, a child of the session. It
// has no branch and no tree of its own, so no branch is checked for, and
// no envelope: it spends from the session's pool (stepBudget).
func (e *Engine) mintStepCard(ctx context.Context, sess domain.Feature, link domain.StepLink, title, backend, model string) (domain.Feature, error) {
	st := e.cfg.Store
	num, err := st.MintFeatureNum(ctx, e.cfg.Workspace.SeqFile())
	if err != nil {
		return domain.Feature{}, err
	}
	id, err := domain.NewID(domain.KindFreeform, num)
	if err != nil {
		return domain.Feature{}, err
	}
	now := e.now().UTC()
	f := domain.Feature{
		ID: id, Num: num, Kind: domain.KindFreeform, Title: title, Slug: sess.Slug,
		Stage: domain.StageOpen, Profile: sess.Profile, Repo: sess.Repo, ParentID: sess.ID, Child: link.Link(),
		SessionBackend: backend, SessionModel: model,
		BranchScheme: domain.DefaultBranchScheme, CreatedAt: now, UpdatedAt: now,
	}
	if err := st.CreateFeature(ctx, &f); err != nil {
		return domain.Feature{}, err
	}
	return f, nil
}

// runArtifacts maps every artifact the recipe declares to its absolute
// path in the run's directory.
func (e *Engine) runArtifacts(run *domain.Run) map[string]string {
	out := map[string]string{}
	dir := e.RunDir(run.Session, run.N)
	for _, s := range workflowrecipe.Steps(run.Recipe) {
		for name, file := range s.Artifacts {
			out[name] = filepath.Join(dir, filepath.FromSlash(file))
		}
	}
	return out
}

// stepOutputs is what a step starting at path reads of the steps before
// it: each step's latest output by id (a step that keeps its output out
// is left out), and the step that ended last before it.
func stepOutputs(r domain.Recipe, steps []domain.RunStep, path string) (map[string]string, string, string) {
	var kept []domain.RunStep
	var prev *domain.RunStep
	for i := range steps {
		s := steps[i]
		if s.Path == path {
			continue
		}
		if def, ok := workflowrecipe.Find(r, s.StepID); ok && !def.Captures() {
			continue
		}
		kept = append(kept, s)
		if s.State.Settled() && (prev == nil || !s.EndedAt.Before(prev.EndedAt)) {
			prev = &steps[i]
		}
	}
	if prev == nil {
		return workflowrecipe.LatestOutputs(kept), "", ""
	}
	return workflowrecipe.LatestOutputs(kept), prev.StepID, prev.Output
}

// workflowRoleDuty is a role's line of the step's preamble.
func workflowRoleDuty(r domain.WorkflowRole) string {
	switch r {
	case domain.RoleScout:
		return "You read, reproduce and map."
	case domain.RolePlanner:
		return "You plan the work that follows."
	case domain.RoleContractor:
		return "You write the design, interfaces and stubs the steps after you build on."
	case domain.RoleTester:
		return "You write and extend tests."
	case domain.RoleScribe:
		return "You write docs and reports."
	case domain.RoleReviewer:
		return "You read the work against the spec, with fresh eyes and no stake in it."
	}
	return "You build."
}

// stepBrief is the turn a step starts on: its role's preamble, the steers
// so far, and its prompt as filled in.
func stepBrief(run *domain.Run, step domain.Step, path, filled string) string {
	var b strings.Builder
	recipe := "an inline recipe"
	if run.RecipeName != "" {
		recipe = "the " + run.RecipeName + " recipe"
	}
	fmt.Fprintf(&b, "You are step %s of workflow run %d (%s) in gummi: a %s. %s\n\n", path, run.N, recipe, step.Role, workflowRoleDuty(step.Role))
	if w, ok := run.Recipe.WatchStep(); ok && strings.HasPrefix(path, w.ID+"/") && !step.Role.Reads() {
		// a watch pass: gummi pushes what the branch holds once the pass
		// ends, and a change left uncommitted would never reach the PR
		b.WriteString("You work in the session's own tree, which the run's other steps share. This step answers " +
			"what the pull request raised: commit your changes on the branch (gummi pushes it when the pass ends; " +
			"never push yourself).\n")
	} else {
		b.WriteString("You work in the session's own tree, which the run's other steps share. Do not commit: " +
			"the run leaves its changes in the tree, and the session commits what it keeps.\n")
	}
	if step.Role.Reads() {
		b.WriteString("You have a full shell, like every step: read the code, and build and run what you need — " +
			"the tests included. Do not change files in the tree: you read, run and report. gummi compares the tree " +
			"before and after you, and a reading step that changed files is flagged in the run's report.\n")
	} else {
		b.WriteString("Change only what your task needs.\n")
	}
	if len(run.Steers) > 0 {
		b.WriteString("\nNotes steering the run:\n")
		for _, s := range run.Steers {
			b.WriteString("- " + s + "\n")
		}
	}
	b.WriteString("\nIf something your task and the spec do not settle blocks you, ask with ask_user. " +
		"If you cannot do the task, make your last line FAILED: <why>. Otherwise write no FAILED: line at all, " +
		"not even FAILED: none: a finding, however bad, is not a failure. Your final message is your output, " +
		"which the steps after you read; DECISION: lines (a choice your task did not make for you) and " +
		"OPEN: lines (what you left) go into the run's report.\n")
	if step.Role == domain.RolePlanner {
		b.WriteString("To change the plan, end with a fenced gummi-workflow block listing the steps that replace " +
			"the run's remaining steps; the person allows the revised plan before it runs.\n")
	}
	b.WriteString("\nYour task:\n\n" + strings.TrimSpace(filled) + "\n")
	return b.String()
}

// sendStepTurn sends a step's card a turn under the workflow's actor.
func (e *Engine) sendStepTurn(card domain.FeatureID, msg string) {
	e.goWorkflow(func() {
		ctx := e.lifetime()
		f, err := e.cfg.Store.GetFeature(ctx, card)
		if err != nil {
			return
		}
		ff, err := e.OpenFreeform(ctx, f)
		if err != nil {
			return
		}
		_ = ff.SendTurn(WithActor(ctx, state.ActorWorkflow), msg, nil)
	})
}

// --- a step's turn ended ----------------------------------------------------

// workflowStepTurnEnded is a step's freeform turn ending with nothing
// queued: gummi records its end. It reports whether the card is a step's.
func (ff *FreeformSession) workflowStepTurnEnded(sess *Session) bool {
	f := sess.Feature
	if !f.IsChild() {
		return false
	}
	e := ff.engine
	if e.stepOf(context.Background(), f) == nil {
		return false
	}
	if sess.Snapshot().PendingAsk != nil {
		return true
	}
	reply := lastAssistant(sess)
	e.goWorkflow(func() { e.afterStepTurn(e.lifetime(), f, reply) })
	return true
}

// workflowStepErrored is a step's backend erroring: the step fails.
func (ff *FreeformSession) workflowStepErrored(sess *Session, err error) {
	f := sess.Feature
	if !f.IsChild() || err == nil {
		return
	}
	e := ff.engine
	e.goWorkflow(func() {
		ctx := e.lifetime()
		inst := e.stepOf(ctx, f)
		if inst == nil {
			return
		}
		e.settleStep(ctx, inst.Session, inst.Run, inst.Path, domain.StepFailed, "its backend errored: "+err.Error())
		e.workflowEvent(inst.Session)
	})
}

func (e *Engine) afterStepTurn(ctx context.Context, card domain.Feature, reply string) {
	st := e.cfg.Store
	inst := e.stepOf(ctx, card)
	if inst == nil {
		return
	}
	defer e.workflowEvent(inst.Session)
	if !inst.State.Live() {
		// a person's follow-up after the step ended: the tree may have
		// changed, which the run's diff shows; nothing after it re-runs
		return
	}
	run, err := st.Run(ctx, inst.Session, inst.Run)
	if err != nil || run == nil {
		return
	}
	endTree, err := e.snapshotRun(ctx, inst.Session, inst.Run)
	if err != nil {
		// with no end snapshot the step has no diff, and a planner's plan
		// would never read as waiting: the step fails, saying why
		e.endStep(ctx, inst, domain.StepFailed, "gummi could not read the tree when it ended: "+err.Error(), reply, "")
		return
	}
	if inst.Role == domain.RolePlanner {
		if handled := e.plannerEnded(ctx, run, inst, reply, endTree); handled {
			return
		}
	}
	state, why := domain.StepDone, ""
	if failed, w := workflowrecipe.Failed(reply); failed {
		state, why = domain.StepFailed, w
		if why == "" {
			why = "it said it failed"
		}
	}
	e.endStep(ctx, inst, state, why, reply, endTree)
}

// plannerEnded handles a planner's gummi-workflow block: one that passes
// the check waits on the person as a revised plan; one that does not goes
// back to the planner, and past the cap fails the step. It reports
// whether it settled the turn.
func (e *Engine) plannerEnded(ctx context.Context, run *domain.Run, inst *domain.RunStep, reply, endTree string) bool {
	entries, err := workflowrecipe.ParsePlanBlock(reply)
	if errors.Is(err, workflowrecipe.ErrNoPlanBlock) {
		return false
	}
	var problems []string
	if err == nil {
		workflowrecipe.Walk(entries, func(s domain.Step, _ []domain.Entry) {
			if s.Agentless() {
				problems = append(problems, fmt.Sprintf("step %s: a revised plan adds no command, dispatch, pr or watch step: the person allowed the recipe's own", s.ID))
			}
		})
	}
	if err != nil {
		problems = []string{err.Error()}
	} else if len(problems) == 0 {
		steps, _ := e.cfg.Store.RunSteps(ctx, run.Session, run.N)
		_, problems, _ = safeCheckReplacement(run.Recipe, startedEntries(run.Recipe, steps), entries)
		problems = append(problems, e.modelProblems(e.sessionProfile(run.Session), entries)...)
		if len(problems) == 0 {
			if _, ferr := e.freezePlan(run, entries); ferr != nil {
				problems = []string{ferr.Error()}
			}
		}
	}
	if len(problems) == 0 {
		unlock := e.lockRuns(run.Session)
		defer unlock()
		cur, err := e.cfg.Store.RunStep(ctx, run.Session, run.N, inst.Path)
		if err != nil || cur == nil || !cur.State.Live() {
			return true
		}
		cur.Output, cur.EndTree, cur.State = reply, endTree, domain.StepAsking
		cur.Reason = "its revised plan waits on the person"
		_ = e.cfg.Store.UpdateRunStep(ctx, cur)
		return true
	}
	key := stepKey(run.Session, run.N, inst.Path)
	if tries := e.wfRT().planTry(key); tries <= planTriesCap {
		e.sendStepTurn(inst.Card, "Your gummi-workflow block does not pass gummi's check:\n- "+strings.Join(problems, "\n- ")+
			"\n\nFix it and end with the block again, or end without one to keep the plan as it is.")
		return true
	}
	e.endStep(ctx, inst, domain.StepFailed, "its revised plan did not pass the check: "+strings.Join(problems, "; "), reply, endTree)
	return true
}

// startedEntries is how many of the recipe's top-level entries have a step
// instance at or under them: a planner's block replaces the ones after.
func startedEntries(r domain.Recipe, steps []domain.RunStep) int {
	n := 0
	for i, en := range r.Steps {
		id := en.ID()
		for _, s := range steps {
			if s.Path == id || strings.HasPrefix(s.Path, id+"/") {
				n = i + 1
				break
			}
		}
	}
	return n
}

// endStep records a step's end: its output, its end snapshot, its spend,
// and its output in the run's directory. A step that ends done without an
// artifact it declared has it captured from its output first, before the
// step reads as done, so the repeat it sits in is judged with it.
func (e *Engine) endStep(ctx context.Context, inst *domain.RunStep, st domain.StepState, why, output, endTree string) {
	if st == domain.StepDone {
		e.captureArtifacts(ctx, inst, output)
		e.baselineAdded(ctx, inst, output, endTree)
	}
	// the diff of a reading step's snapshots is git: read off the run lock,
	// then put into the row read afresh under it
	var changed []string
	if pre, err := e.cfg.Store.RunStep(ctx, inst.Session, inst.Run, inst.Path); err == nil && pre != nil && pre.State.Live() {
		pre.EndTree = endTree
		e.flagReaderChanges(ctx, pre)
		changed = pre.Changed
	}
	unlock := e.lockRuns(inst.Session)
	cur, err := e.cfg.Store.RunStep(ctx, inst.Session, inst.Run, inst.Path)
	if err != nil || cur == nil || !cur.State.Live() {
		unlock()
		return
	}
	cur.State, cur.Reason, cur.Output, cur.EndTree, cur.EndedAt = st, why, output, endTree, e.now()
	cur.Changed = changed
	if cur.Card != "" {
		if f, err := e.cfg.Store.GetFeature(ctx, cur.Card); err == nil {
			cur.Spent = f.Spend.CreditEquivalent()
		}
	}
	err = e.cfg.Store.UpdateRunStep(ctx, cur)
	unlock()
	if err != nil {
		return
	}
	_, _ = e.writeRunFile(cur.Session, cur.Run, stepFile("outputs", cur.Path, ".md"), output)
	line := fmt.Sprintf("run %d: %s %s", cur.Run, cur.Path, st)
	if why != "" {
		line += ": " + why
	}
	e.runNote(cur.Session, line)
}

// captureArtifacts writes each artifact a step declared and did not write
// from the step's output (workflowrecipe.CaptureArtifact): the last JSON
// object for a .json artifact, else the last fenced block. Each one gummi
// writes is said in the run's notes; one the output does not hold stays
// unwritten.
func (e *Engine) captureArtifacts(ctx context.Context, inst *domain.RunStep, output string) {
	run, err := e.cfg.Store.Run(ctx, inst.Session, inst.Run)
	if err != nil || run == nil {
		return
	}
	def, ok := workflowrecipe.Find(run.Recipe, inst.StepID)
	if !ok {
		return
	}
	names := make([]string, 0, len(def.Artifacts))
	for name := range def.Artifacts {
		names = append(names, name)
	}
	sort.Strings(names)
	stamps := e.takeStamps(inst.Session, inst.Run, inst.Path)
	for _, name := range names {
		file := def.Artifacts[name]
		fi, err := os.Stat(filepath.Join(e.RunDir(inst.Session, inst.Run), file))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			continue
		}
		// written by this step: its own. One an earlier iteration left,
		// untouched since this step started, is no answer of this step's —
		// a verdict step that only printed its verdict would otherwise
		// leave the last iteration's to be judged
		if err == nil {
			if stamps == nil {
				// a restart lost the stamps: the file is the step's own only
				// if written since the step started
				if fi.ModTime().After(inst.StartedAt) {
					continue
				}
			} else if was, stamped := stamps[file]; !stamped || !fi.ModTime().Equal(was) {
				continue
			}
		}
		body, ok := workflowrecipe.CaptureArtifact(output, file)
		if !ok {
			e.runNote(inst.Session, fmt.Sprintf("run %d: %s did not write its artifact %s (%s), and its output holds none to capture",
				inst.Run, inst.Path, name, file))
			continue
		}
		if _, err := e.writeRunFile(inst.Session, inst.Run, file, body); err != nil {
			continue
		}
		e.runNote(inst.Session, fmt.Sprintf("run %d: %s did not write its artifact %s (%s); gummi wrote it from the step's output",
			inst.Run, inst.Path, name, file))
	}
}

// stampArtifacts records, as a step starts, when each artifact it declares
// was last written, for captureArtifacts to tell one it wrote from one an
// earlier iteration left.
func (e *Engine) stampArtifacts(session domain.FeatureID, n int, path string, step domain.Step) {
	if len(step.Artifacts) == 0 {
		return
	}
	at := map[string]time.Time{}
	for _, file := range step.Artifacts {
		if fi, err := os.Stat(filepath.Join(e.RunDir(session, n), file)); err == nil {
			at[file] = fi.ModTime()
		}
	}
	rt := e.wfRT()
	rt.mu.Lock()
	rt.stamps[stepKey(session, n, path)] = at
	rt.mu.Unlock()
}

// takeStamps hands over (and forgets) a step's artifact stamps.
func (e *Engine) takeStamps(session domain.FeatureID, n int, path string) map[string]time.Time {
	rt := e.wfRT()
	key := stepKey(session, n, path)
	rt.mu.Lock()
	defer rt.mu.Unlock()
	at := rt.stamps[key]
	delete(rt.stamps, key)
	return at
}

// pauseOnSpentPool pauses run n for a top-up when its session's pool is
// spent and it still runs.
func (e *Engine) pauseOnSpentPool(ctx context.Context, session domain.FeatureID, n int) {
	unlock := e.lockRuns(session)
	defer unlock()
	run, err := e.cfg.Store.Run(ctx, session, n)
	if err != nil || run == nil || run.State != domain.RunRunning || run.Stopping {
		return
	}
	in, _, err := e.runSnapshot(ctx, run)
	if err != nil {
		return
	}
	_ = e.pauseForBudgetLocked(ctx, run, fmt.Sprintf("the session's budget is spent (the run has spent %s)", domain.FormatDollars(workflowpolicy.Spent(in))))
}

// stepWriteDirs is what a step's backend must be able to write beyond its
// working directory, the session's checkout: its run's directory, where
// the artifacts its brief names go (SessionOpts.WriteDirs). It is made
// here, so a backend that opens it finds it. Nil for a card that is no
// step's.
func (e *Engine) stepWriteDirs(ctx context.Context, f domain.Feature) []string {
	st := e.stepOf(ctx, f)
	if st == nil {
		return nil
	}
	dir := e.RunDir(st.Session, st.Run)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil
	}
	return []string{dir}
}

// flagReaderChanges records the paths a reading step changed between its
// snapshots (RunStep.Changed): it was told to change no files, and one
// that did is flagged in its row and the run's report — evidence for the
// person, never a refusal. A writing step's are its work, and not kept.
func (e *Engine) flagReaderChanges(ctx context.Context, st *domain.RunStep) {
	st.Changed = nil
	if !st.Role.Reads() || st.StartTree == "" || st.EndTree == "" || st.StartTree == st.EndTree {
		return
	}
	paths, err := e.diffPaths(ctx, st.Session, st.StartTree, st.EndTree)
	if err != nil || len(paths) == 0 {
		return
	}
	// a writing step at work beside it (a parallel's implementer, say)
	// changes the same tree: the paths that step changed are its work, not
	// the reader's
	theirs := map[string]bool{}
	steps, _ := e.cfg.Store.RunSteps(ctx, st.Session, st.Run)
	for _, o := range steps {
		if o.Path == st.Path || o.Role.Reads() || o.StartTree == "" || !overlaps(o, *st) {
			continue
		}
		end := o.EndTree
		if end == "" {
			end = st.EndTree
		}
		if ps, err := e.diffPaths(ctx, st.Session, o.StartTree, end); err == nil {
			for _, p := range ps {
				theirs[p] = true
			}
		}
	}
	var mine []string
	for _, p := range paths {
		if !theirs[p] {
			mine = append(mine, p)
		}
	}
	if len(mine) > 0 {
		st.Changed = mine
		e.runNote(st.Session, fmt.Sprintf("run %d: %s, a reading step, changed %d file%s", st.Run, st.Path, len(mine), plural(len(mine))))
	}
}

// overlaps reports whether two steps were at work at the same time.
func overlaps(a, b domain.RunStep) bool {
	aEnd, bEnd := a.EndedAt, b.EndedAt
	if aEnd.IsZero() {
		aEnd = time.Now()
	}
	if bEnd.IsZero() {
		bEnd = time.Now()
	}
	return a.StartedAt.Before(bEnd) && b.StartedAt.Before(aEnd)
}

// settleStep ends a live step without an output of its own: stopped,
// failed to start, errored or dropped.
func (e *Engine) settleStep(ctx context.Context, session domain.FeatureID, n int, path string, st domain.StepState, why string) {
	unlock := e.lockRuns(session)
	defer unlock()
	e.settleStepLocked(ctx, session, n, path, st, why, "")
}

func (e *Engine) settleStepLocked(ctx context.Context, session domain.FeatureID, n int, path string, st domain.StepState, why, tree string) {
	cur, err := e.cfg.Store.RunStep(ctx, session, n, path)
	if err != nil || cur == nil || cur.State.Settled() {
		return
	}
	cur.State, cur.Reason, cur.EndedAt = st, why, e.now()
	if cur.EndTree == "" {
		cur.EndTree = tree
		if tree == "" {
			cur.EndTree, _ = e.snapshotRun(ctx, session, n)
		}
	}
	if cur.Card != "" {
		if f, err := e.cfg.Store.GetFeature(ctx, cur.Card); err == nil {
			cur.Spent = f.Spend.CreditEquivalent()
		}
	}
	e.takeStamps(session, n, path) // a step that never ends done is not captured
	e.flagReaderChanges(ctx, cur)
	if err := e.cfg.Store.UpdateRunStep(ctx, cur); err != nil {
		return
	}
	e.runNote(session, fmt.Sprintf("run %d: %s %s: %s", n, path, st, why))
}

// interruptStep stops a step's turn in flight, keeping its conversation.
func (e *Engine) interruptStep(card domain.FeatureID) {
	if card == "" {
		return
	}
	e.goWorkflow(func() {
		if e.freeformBusy(card) {
			_ = e.InterruptFreeform(e.lifetime(), card)
		}
	})
}

// carryOnRun sends each live step whose turn is not in flight a turn to
// pick up where it left off, and starts again a step whose start never
// finished: a run resumed after a pause, a top-up or a restart.
func (e *Engine) carryOnRun(ctx context.Context, run *domain.Run) {
	steps, _ := e.cfg.Store.RunSteps(ctx, run.Session, run.N)
	rt := e.wfRT()
	for _, s := range steps {
		switch {
		case !s.State.Live() || rt.isBusy(stepKey(run.Session, run.N, s.Path)):
		case s.Card == "" || s.Prompt == "":
			// never briefed: its start begins again
			e.startStepAsync(run.Session, run.N, s.Path)
		case s.EndTree != "":
			// a planner's revised plan, waiting on the person
		case e.freeformBusy(s.Card) || e.cardAsking(s.Card):
		default:
			e.sendStepTurn(s.Card, "The run was paused and now carries on. The tree holds the work so far: "+
				"pick up where you left off, and end as your task says.")
		}
	}
}

// cardAsking reports whether a card's session waits on a person's answer.
func (e *Engine) cardAsking(card domain.FeatureID) bool {
	ff := e.Freeform(card)
	if ff == nil {
		return false
	}
	s := ff.Session()
	return s != nil && s.Snapshot().PendingAsk != nil
}

// StepSend is a person's message to a step's agent (§25.10): a turn while
// the step runs, which the step's output then follows; after the step has
// ended, a follow-up in its conversation, which may change the tree and
// re-runs nothing. It is paid from the session's pool, as every turn on a
// step's card is.
func (e *Engine) StepSend(ctx context.Context, stepCard domain.FeatureID, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return errors.New("an empty message")
	}
	if e.cfg.Store == nil {
		return errors.New("workflows need the store")
	}
	card, err := e.cfg.Store.GetFeature(ctx, stepCard)
	if err != nil {
		return err
	}
	inst := e.stepOf(ctx, card)
	if inst == nil {
		return fmt.Errorf("%s is no workflow step's card", stepCard)
	}
	if inst.State == domain.StepAsking && inst.EndTree != "" {
		return errors.New("the step's revised plan waits on the person: answer it first")
	}
	if inst.State.Live() {
		// a paused run's steps have no backend, and a message would start
		// one: the run carries on only when a person resumes it
		if run, err := e.cfg.Store.Run(ctx, inst.Session, inst.Run); err == nil && run != nil && run.State == domain.RunPaused {
			return fmt.Errorf("run %d is paused: resume it before talking to its step %s", run.N, inst.Path)
		}
	} else if busy := e.liveStepOf(ctx, inst.Session); busy != nil {
		// a follow-up may change the tree, which the open run's steps are
		// writing: its edits would land in their diffs as theirs
		return fmt.Errorf("step %s of run %d is at work in this tree: talk to %s once the run has paused or ended",
			busy.Path, busy.Run, inst.Path)
	}
	ff, err := e.OpenFreeform(ctx, card)
	if err != nil {
		return err
	}
	return ff.SendTurn(ctx, text, nil)
}

// liveStepOf is a step at work in the session's open run, nil when none is.
func (e *Engine) liveStepOf(ctx context.Context, session domain.FeatureID) *domain.RunStep {
	run, err := e.cfg.Store.OpenRun(ctx, session)
	if err != nil || run == nil || run.State != domain.RunRunning {
		return nil
	}
	steps, _ := e.cfg.Store.RunSteps(ctx, session, run.N)
	for i := range steps {
		if steps[i].State.Live() && steps[i].EndTree == "" {
			return &steps[i]
		}
	}
	return nil
}

// RunSteps lists a run's step instances, oldest first, for the faces.
func (e *Engine) RunSteps(ctx context.Context, session domain.FeatureID, n int) ([]domain.RunStep, error) {
	if e.cfg.Store == nil {
		return nil, nil
	}
	steps, err := e.cfg.Store.RunSteps(ctx, session, n)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(steps, func(i, j int) bool {
		return steps[i].StartedAt.Before(steps[j].StartedAt)
	})
	return steps, nil
}
