package engine

// A dispatch step (DESIGN §26.4): an agent's run mints a session in a
// repository the person allowed, with an envelope carved from the agent's
// own, and gives it work — a spec and a run of the frozen recipe, started
// already allowed, or a prompt as its opening turn. gummi does it; no
// model runs for the step.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/morphis/gummi/internal/cardmint"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/workflowrecipe"
)

// DispatchNotePrefix starts every line a dispatch puts into its agent's
// thread. Unlike a run's notes the faces keep these: they are how the
// person reads what the agent did.
const DispatchNotePrefix = "dispatch: "

// agentNote puts a line about a dispatch into the agent's thread. It is
// only the line: the turn that wakes the agent is wakeDispatcher's.
func (e *Engine) agentNote(agent domain.FeatureID, line string) {
	ff := e.Freeform(agent)
	if ff == nil {
		return
	}
	if sess := ff.Session(); sess != nil {
		sess.appendActivity(DispatchNotePrefix + line)
		e.persist(sess)
		e.send(Event{Feature: agent, Stage: domain.StageOpen, Kind: EventUpdated})
	}
}

// dispatchStep runs a dispatch step of agent's run. A step that settles
// itself (a refusal, a mint, a pause on a pool that cannot cover the
// envelope) returns nil; an error is the step's failure to start.
func (e *Engine) dispatchStep(ctx context.Context, run *domain.Run, agent domain.Feature, inst *domain.RunStep,
	step domain.Step, vars workflowrecipe.Vars,
) error {
	st := e.cfg.Store
	d := *step.Dispatch
	fail := func(why string) error {
		e.endStep(ctx, inst, domain.StepFailed, why, "", e.endTreeOf(ctx, run))
		return nil
	}
	switch {
	case !agent.Home:
		return fail("only an agent dispatches sessions")
	case agent.Budget.Envelope <= 0:
		return fail("a dispatch's envelope comes out of the agent's own: give the agent an envelope first")
	case d.Recipe != "" && strings.TrimSpace(d.RecipeYAML) == "":
		return fail(fmt.Sprintf("its recipe %s was never frozen into the run; propose it again", d.Recipe))
	}
	if err := e.checkDispatchRepos(inst.Path, &d); err != nil {
		return fail(err.Error())
	}
	title, err := workflowrecipe.FillPlain(d.Title, vars)
	if err != nil {
		return err
	}
	title, _, _ = strings.Cut(strings.TrimSpace(title), "\n")
	prompt, err := workflowrecipe.FillPlain(d.Prompt, vars)
	if err != nil {
		return err
	}
	specText, err := workflowrecipe.FillPlain(d.Spec, vars)
	if err != nil {
		return err
	}

	// the dispatch of this step, when an earlier start made it before it
	// ended: the step goes on from it instead of minting twice
	rec, found, err := st.DispatchAt(ctx, agent.ID, run.N, inst.Path)
	if err != nil {
		return err
	}
	var child domain.Feature
	if found {
		if child, err = st.GetFeature(ctx, rec.Card); err != nil {
			return err
		}
	} else {
		unlock := e.lockRuns(run.Session)
		now, err := st.Run(ctx, run.Session, run.N)
		if err != nil || now == nil || now.State != domain.RunRunning || now.Stopping {
			unlock()
			return err
		}
		// the pool is the agent's: an open child holds its envelope
		if left, bounded := e.sessionPoolLeft(ctx, agent); bounded && left < float64(d.Envelope) {
			err := e.pauseForBudgetLocked(ctx, now, fmt.Sprintf(
				"the dispatch to %s needs an envelope of %s and the agent's budget has %s left, with its open sessions' envelopes held",
				d.Repo, domain.FormatDollars(float64(d.Envelope)), domain.FormatDollars(max(0, left))))
			unlock()
			return err
		}
		child, err = cardmint.Mint(ctx, st, e.cfg.Workspace, cardmint.Input{
			Description: title,
			Envelope:    d.Envelope,
			Profile:     agent.Profile,
			Repo:        d.Repo,
			RequireRepo: e.RequireRepo,
			// a dispatched session works in a worktree of its own, and no
			// person is present to retitle a title that is taken
			MainCheckout: false,
			Unattended:   true,
			// the record goes in with the card, so a restart always finds
			// the session this step minted and never mints a second
			Dispatch: &state.Dispatch{Agent: agent.ID, Run: run.N, Path: inst.Path, Envelope: d.Envelope, CreatedAt: e.now()},
		})
		unlock()
		if err != nil {
			return fmt.Errorf("minting a session in %s: %w", d.Repo, err)
		}
	}
	// the session spans the repositories the person allowed; one an earlier
	// start already attached is kept
	for _, r := range d.Repos {
		if _, err := e.AttachRepo(ctx, child.ID, r, ""); err != nil && !errors.Is(err, errAlreadyAttached) {
			return fail(fmt.Sprintf("attaching %s to the session %s: %v", r, child.ID, err))
		}
	}

	if strings.TrimSpace(d.RecipeYAML) != "" {
		// the session exists to run this recipe: it is offered workflows
		// whatever the workspace setting says
		on := true
		if err := st.SetSessionWorkflows(ctx, child.ID, &on); err != nil {
			return err
		}
		if err := e.startDispatchedRun(ctx, agent, child, d, specText); err != nil {
			return err
		}
	} else {
		ff, err := e.OpenFreeform(ctx, child)
		if err != nil {
			return err
		}
		// The record is written before the turn, so a restart that finds it
		// kicks off again: KickoffWith is a no-op once the conversation has
		// started, and a child minted just before a crash still gets its
		// prompt.
		if err := ff.KickoffWith(WithActor(ctx, "dispatch:"+string(agent.ID)), prompt); err != nil {
			return err
		}
	}
	e.agentNote(agent.ID, fmt.Sprintf("run %d dispatched session %s (%s) in %s with an envelope of %s",
		run.N, child.ID, child.Name(), d.Repo, domain.FormatDollars(float64(d.Envelope))))
	e.endStep(ctx, inst, domain.StepDone, "", "dispatched "+string(child.ID), e.endTreeOf(ctx, run))
	return nil
}

// startDispatchedRun gives a dispatched session its spec 1 and starts the
// run of the frozen recipe on it, already allowed: the person allowed it
// with the agent's recipe. A session that has its spec or its run (an
// earlier start got that far) keeps them.
func (e *Engine) startDispatchedRun(ctx context.Context, agent, child domain.Feature, d domain.Dispatch, specText string) error {
	st := e.cfg.Store
	if specs, err := st.SessionSpecs(ctx, child.ID); err != nil {
		return err
	} else if len(specs) == 0 {
		if _, _, err := e.StartSessionSpec(ctx, child, child.Title, state.ActorWorkflow); err != nil {
			return err
		}
		if _, err := e.WriteSessionSpec(ctx, child, 1, specText, state.ActorWorkflow); err != nil {
			return err
		}
	}
	if runs, err := st.SessionRuns(ctx, child.ID); err != nil {
		return err
	} else if len(runs) > 0 {
		// an earlier start created the run and may have died before it was
		// allowed: it is the person's recipe all the same, and is allowed now
		if last := runs[len(runs)-1]; last.State == domain.RunProposed && last.RecipeYAML == d.RecipeYAML {
			unlock := e.lockRuns(child.ID)
			defer unlock()
			if err := e.allowRunLocked(ctx, &last, "allowed with "+agent.Name()+"'s schedule"); err != nil {
				return err
			}
			e.workflowEvent(child.ID)
		}
		return nil
	}
	recipe, err := workflowrecipe.Parse([]byte(d.RecipeYAML))
	if err != nil {
		return fmt.Errorf("its frozen recipe: %w", err)
	}
	preflight, hasPreflight, err := e.preflightFor(ctx, child.ID, recipe)
	if err != nil {
		return err
	}
	run := &domain.Run{
		Session: child.ID, RecipeName: d.Recipe, Recipe: recipe, RecipeYAML: d.RecipeYAML,
		Spec: 1, State: domain.RunProposed, CreatedAt: e.now(),
	}
	if err := st.CreateRun(ctx, run); err != nil {
		return err
	}
	_, _ = e.writeRunFile(child.ID, run.N, "recipe.yaml", d.RecipeYAML)
	if hasPreflight {
		e.keepPreflight(child.ID, run.N, preflight)
	}
	unlock := e.lockRuns(child.ID)
	defer unlock()
	if err := e.allowRunLocked(ctx, run, "allowed with "+agent.Name()+"'s schedule"); err != nil {
		_ = st.DeleteRun(ctx, child.ID, run.N)
		return err
	}
	e.workflowEvent(child.ID)
	return nil
}

// noteDispatcher tells the agent that dispatched session that run n of
// the session ended: a line in its thread, and a turn naming the run, how
// it ended and the head of its report.
func (e *Engine) noteDispatcher(ctx context.Context, session domain.FeatureID, n int, end domain.RunState, report string) {
	what := fmt.Sprintf("run %d ended %s", n, end)
	if _, ok, err := e.cfg.Store.DispatchOf(ctx, session); err != nil || !ok {
		return
	}
	e.noteDispatcherEvent(ctx, session, what)
	if head := headOf(report, 600); head != "" {
		what += "; its report begins:\n" + head + "\n(session_status has all of it)"
	}
	e.wakeDispatcher(ctx, session, what)
}

// dispatchedHeld is what an agent's open children hold of its envelope —
// the larger of a child's envelope and its spend while it is open — and
// what its closed children spent. It is part of what the agent's pool has
// paid for, so a child's envelope comes out of the agent's own.
func (e *Engine) dispatchedHeld(ctx context.Context, agent domain.FeatureID) float64 {
	if e.cfg.Store == nil {
		return 0
	}
	ds, err := e.cfg.Store.Dispatched(ctx, agent)
	if err != nil {
		return 0
	}
	t := 0.0
	for _, d := range ds {
		f, err := e.cfg.Store.GetFeature(ctx, d.Card)
		if err != nil {
			t += float64(d.Envelope)
			continue
		}
		t += e.heldFor(ctx, d, f)
	}
	return t
}

// heldFor is what one dispatched child holds of its agent's pool: while it
// is open the larger of its envelope now, the one recorded at dispatch and
// its spend, so a person raising the child's own envelope cannot let it
// spend past what the agent holds for it; once closed, what it spent.
func (e *Engine) heldFor(ctx context.Context, d state.Dispatch, f domain.Feature) float64 {
	spent := e.SessionPoolSpent(ctx, f)
	if f.Stage == domain.StageDone {
		return spent
	}
	return max(float64(d.Envelope), float64(f.Budget.Envelope), spent)
}

// checkDispatchedRaise refuses setting a dispatched child's envelope to
// "to" when the agent's pool cannot cover what that adds to the child's
// hold: the agent's tree of work never spends past the agent's own
// envelope. Lowering is always allowed; removing the cap is not.
func (e *Engine) checkDispatchedRaise(ctx context.Context, f domain.Feature, to int) error {
	if e.cfg.Store == nil || f.Stage == domain.StageDone {
		return nil
	}
	d, ok, err := e.cfg.Store.DispatchOf(ctx, f.ID)
	if err != nil || !ok {
		return nil
	}
	agentCard, err := e.cfg.Store.GetFeature(ctx, d.Agent)
	if err != nil {
		return nil
	}
	if to == 0 && agentCard.Budget.Envelope <= 0 {
		return nil
	}
	if to == 0 {
		return fmt.Errorf("%s was dispatched by %s, which holds its envelope: it cannot be left uncapped", f.Name(), agentCard.Name())
	}
	next := f
	next.Budget.Envelope = to
	inc := e.heldFor(ctx, d, next) - e.heldFor(ctx, d, f)
	if inc <= 0 {
		return nil
	}
	left, bounded := e.sessionPoolLeft(ctx, agentCard)
	if !bounded || inc <= left {
		return nil
	}
	return fmt.Errorf("%s dispatched %s, and its pool has %s left, not the %s more this needs: top %s up first",
		agentCard.Name(), f.Name(), domain.FormatDollars(max(0, left)), domain.FormatDollars(inc), agentCard.Name())
}
