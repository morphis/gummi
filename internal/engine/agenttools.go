package engine

// An agent's tools for a watch (DESIGN §26): propose the schedule, see the
// sessions it dispatched, and talk to them. They are offered to an agent
// only, and answer for nothing it did not dispatch. None enables, edits or
// deletes a schedule, raises an envelope or lands anything.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/schedule"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/workflowrecipe"
)

const (
	scheduleProposeToolName = "schedule_propose"
	sessionStatusToolName   = "session_status"
	sessionSendToolName     = "session_send"
)

func agentTools() []agent.ToolDef {
	return []agent.ToolDef{
		mkTool(scheduleProposeToolName,
			"Propose a watch: a schedule that starts a run of a recipe on this agent against one of its specs, "+
				"on a cadence. The schedule is stored OFF; the person enables it from the schedules view after "+
				"reading the recipe, its commands and the repositories it dispatches into, and that enable is the "+
				"allowance for every run it starts. You cannot enable, edit or delete a schedule. A recipe whose first "+
				"step is a run: command gate (quietExit says nothing changed) spends no model until something has.",
			map[string]any{
				"name":        str("The schedule's name; its id is its slug."),
				"cron":        str("A five-field cron expression. Or give preset."),
				"preset":      str("A cadence as 5m, 1h, 6h, @hourly, @daily or @weekly. Ignored when cron is given."),
				"timezone":    str("An IANA zone for the cron; the host's by default."),
				"recipe":      str("A recipe's name, as workflow_recipes lists them."),
				"recipe_yaml": str("A recipe written out in full instead of a name."),
				"spec":        specArg,
			}, "name"),
		mkTool(sessionStatusToolName,
			"The sessions this agent dispatched: each one's title, state, open run and spend against its envelope. "+
				"Given a card, that one in full. Answers only for sessions you dispatched.",
			map[string]any{"card": str("A dispatched session's id.")}),
		mkTool(sessionSendToolName,
			"Send a turn into a session this agent dispatched. Refused for any other card, for one that is closed or has spent its envelope, and while your own envelope is spent.",
			map[string]any{"card": str("A dispatched session's id."), "text": str("The message.")}, "card", "text"),
	}
}

const agentWatchHint = `This card is an agent, and may start work in the person's repositories (gummi's DESIGN §26): not by writing into one, but by a dispatch step in a workflow run of yours, which mints a session in a repository the recipe names (a literal, one repo_list gives), with an envelope and a spec or prompt of its own, and may start a recipe there. A run of yours is your own tree, your home; its dispatch step's session works in its repository's tree. For work now, propose the run with workflow_run and a recipe holding the dispatch (written out as recipe_yaml; the workflow-recipes skill shows the dispatch step); the person allows it, and allowing it allows exactly the repositories it names. For work on a change you watch for, propose a schedule. A watch is a schedule that starts a run of a recipe on you, against one of your specs; its first step is a run: command gate that decides, with no model, whether anything changed (quietExit is the exit that says nothing did), and only then does a dispatch step mint a session in a repository the person allowed, with an envelope of its own. schedule_propose stores the schedule off; the person enables it from the schedules view, and that is the allowance — you never enable, edit or delete one. Everything a dispatched session spends, and its envelope while it is open, comes out of your own envelope. session_status and session_send answer only for the sessions you dispatched. You are woken by a turn when a session you dispatched has a run end, when it finishes a turn with no run open and waits for input, and when it closes; the turn names the session, the run and the head of its report or reply, and you are not woken while your own envelope is spent or after you are closed. Read the whole of it with session_status and hand it its next piece of work with session_send. A child's own questions and its runs' decisions go to the person, never to you.`

// handleAgentTool serves the agent's tools.
func (e *Engine) handleAgentTool(s *Session, tc *agent.ToolCall) {
	f := s.Feature
	if !f.Home || f.IsChild() {
		e.resolveNow(s, tc.ID, tc.Name+" is an agent's: this card is not one")
		return
	}
	ctx := context.Background()
	var out string
	var err error
	switch tc.Name {
	case scheduleProposeToolName:
		out, err = e.scheduleProposeText(ctx, f, tc.Args)
	case sessionStatusToolName:
		out, err = e.sessionStatusText(ctx, f, tc.Args)
	case sessionSendToolName:
		out, err = e.sessionSendText(ctx, f, tc.Args)
	}
	if err != nil {
		out = "error: " + err.Error()
	}
	e.resolveNow(s, tc.ID, out)
}

// scheduleProposeText stores the agent's proposal as a run schedule that
// is off, with every dispatch's recipe frozen into it.
func (e *Engine) scheduleProposeText(ctx context.Context, agentCard domain.Feature, args json.RawMessage) (string, error) {
	var a struct {
		Name       string          `json:"name"`
		Cron       string          `json:"cron"`
		Preset     string          `json:"preset"`
		Timezone   string          `json:"timezone"`
		Recipe     string          `json:"recipe"`
		RecipeYAML string          `json:"recipe_yaml"`
		Spec       json.RawMessage `json:"spec"`
	}
	_ = json.Unmarshal(args, &a)
	if agentCard.Stage == domain.StageDone {
		return "", fmt.Errorf("%s is closed", agentCard.ID)
	}
	id, err := domain.NewScheduleID(a.Name)
	if err != nil {
		return "", err
	}
	recipe, _, text, err := e.loadRecipe(WorkflowRequest{Recipe: a.Recipe, RecipeYAML: a.RecipeYAML})
	if err != nil {
		return "", err
	}
	if changed, err := e.freezeDispatches(&recipe); err != nil {
		return "", err
	} else if changed {
		b, err := workflowrecipe.Marshal(recipe)
		if err != nil {
			return "", err
		}
		text = string(b)
	}
	problems, _ := safeCheck(recipe)
	if problems = append(problems, e.modelProblems(agentCard.Profile, recipe.Steps)...); len(problems) > 0 {
		return "", fmt.Errorf("the recipe cannot run:\n- %s", strings.Join(problems, "\n- "))
	}
	if len(workflowrecipe.RequiredInputs(recipe)) > 0 {
		return "", errors.New("a scheduled recipe takes no required inputs: nobody is there to give them")
	}
	n, given, err := parseSpecArg(a.Spec)
	if err != nil {
		return "", err
	}
	if !given {
		if n, err = e.runSpec(ctx, agentCard, 0); err != nil {
			return "", err
		}
	} else if _, ok, err := e.cfg.Store.SessionSpec(ctx, agentCard.ID, n); err != nil || !ok {
		return "", fmt.Errorf("this agent has no spec %d; spec_list lists them", n)
	}
	pv := schedule.Preview(a.Preset, a.Cron, a.Timezone, e.now())
	if pv.Err != nil {
		return "", pv.Err
	}
	sc := &domain.Schedule{
		ID: id, Name: strings.TrimSpace(a.Name), Kind: domain.ScheduleRun, Target: agentCard.ID,
		Cron: pv.Cron, Timezone: strings.TrimSpace(a.Timezone), Prompt: text, Spec: n,
		// off, whatever the caller asked: only a person enables a schedule
		Enabled: false, CreatedAt: e.now(),
	}
	if err := e.cfg.Store.CreateSchedule(ctx, sc); err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "proposed schedule %q (%s) on spec %d, off: %s", sc.Name, pv.Cron, n, "the person enables it from the schedules view")
	e.agentNote(agentCard.ID, fmt.Sprintf("proposed the schedule %q (%s, spec %d); it is off until the person enables it", sc.Name, pv.Cron, n))
	return b.String(), nil
}

// ownedDispatch reads the dispatch of the card id names, refusing a card
// agentCard did not dispatch.
func (e *Engine) ownedDispatch(ctx context.Context, agentCard domain.Feature, id string) (state.Dispatch, domain.Feature, error) {
	card, err := e.cfg.Store.ResolveFeature(ctx, strings.TrimSpace(id))
	if err != nil {
		return state.Dispatch{}, domain.Feature{}, fmt.Errorf("%q is not a session you dispatched", id)
	}
	d, ok, err := e.cfg.Store.DispatchOf(ctx, card.ID)
	if err != nil {
		return d, card, err
	}
	if !ok || d.Agent != agentCard.ID {
		return d, card, fmt.Errorf("%s is not a session you dispatched", card.ID)
	}
	return d, card, nil
}

func (e *Engine) sessionStatusText(ctx context.Context, agentCard domain.Feature, args json.RawMessage) (string, error) {
	var a struct {
		Card string `json:"card"`
	}
	_ = json.Unmarshal(args, &a)
	if strings.TrimSpace(a.Card) != "" {
		d, card, err := e.ownedDispatch(ctx, agentCard, a.Card)
		if err != nil {
			return "", err
		}
		var b strings.Builder
		b.WriteString(e.dispatchLine(ctx, d, card))
		runs, _ := e.cfg.Store.SessionRuns(ctx, card.ID)
		for _, r := range runs {
			fmt.Fprintf(&b, "\nrun %d (%s): %s", r.N, recipeLabel(&r), r.State)
			if r.Reason != "" {
				b.WriteString(" — " + r.Reason)
			}
		}
		if n := len(runs); n > 0 && runs[n-1].Report != "" {
			b.WriteString("\n\nlast report:\n" + tail(runs[n-1].Report, 4000))
		}
		return b.String(), nil
	}
	ds, err := e.cfg.Store.Dispatched(ctx, agentCard.ID)
	if err != nil {
		return "", err
	}
	if len(ds) == 0 {
		return "you have dispatched no sessions", nil
	}
	lines := make([]string, 0, len(ds))
	for _, d := range ds {
		card, err := e.cfg.Store.GetFeature(ctx, d.Card)
		if err != nil {
			lines = append(lines, DispatchView{Card: d.Card, Gone: true, Envelope: float64(d.Envelope)}.Line())
			continue
		}
		lines = append(lines, e.dispatchLine(ctx, d, card))
	}
	return strings.Join(lines, "\n"), nil
}

// dispatchLine is one dispatched session: title, state, its open run and
// its spend against the envelope carved for it.
func (e *Engine) dispatchLine(ctx context.Context, d state.Dispatch, card domain.Feature) string {
	return e.dispatchView(ctx, d, card).Line()
}

func (e *Engine) sessionSendText(ctx context.Context, agentCard domain.Feature, args json.RawMessage) (string, error) {
	var a struct {
		Card string `json:"card"`
		Text string `json:"text"`
	}
	_ = json.Unmarshal(args, &a)
	_, card, err := e.ownedDispatch(ctx, agentCard, a.Card)
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(a.Text)
	switch {
	case text == "":
		return "", errors.New("an empty message")
	case card.Stage == domain.StageDone:
		return "", fmt.Errorf("%s is closed", card.ID)
	case card.Budget.Envelope > 0 && float64(card.Budget.Envelope)-e.SessionPoolSpent(ctx, card) <= 0:
		return "", fmt.Errorf("%s has spent its envelope of %s", card.ID, domain.FormatDollars(float64(card.Budget.Envelope)))
	}
	if fresh, err := e.cfg.Store.GetFeature(ctx, agentCard.ID); err == nil {
		agentCard = fresh
	}
	if left, bounded := e.sessionPoolLeft(ctx, agentCard); bounded && left <= 0 {
		return "", fmt.Errorf("your own envelope of %s is spent: ask the person to top it up", domain.FormatDollars(float64(agentCard.Budget.Envelope)))
	}
	if err := e.sendSessionTurn(WithActor(ctx, "agent:"+string(agentCard.ID)), card.ID, text); err != nil {
		return "", err
	}
	return "sent to " + string(card.ID), nil
}
