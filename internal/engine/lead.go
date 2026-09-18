package engine

// The lead (agent.RoleLead) runs a goal's judgment: the part of conducting
// a goal that plain rules cannot do. GoalTick decides WHEN a lead turn is
// owed (goalpolicy's Lead action: a kickoff, your notes, a rework note, a
// stuck or exhausted card, findings to settle) and the synchronous hooks
// decide when a card needs an answer or a plan check. A lead turn is a
// short, synchronous agent session — never the goal's live stage session,
// so neither driving loop ever sees it — working in the goal's worktree,
// reading the goal doc, and acting only through goal tools. Every tool
// writes its own entry to the goal's log, and the turn's spend is booked
// to the goal card, inside the goal budget.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/atomicfile"
	"github.com/morphis/gummi/internal/cardmint"
	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/goalpolicy"
	"github.com/morphis/gummi/internal/mcp"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/verify"
	"github.com/morphis/gummi/internal/worktree"
)

// leadTurnTimeout bounds one lead turn. A lead that cannot decide in this
// long has failed the turn; three failures in a row wrap the goal up.
const leadTurnTimeout = 15 * time.Minute

// resolveLeadRole picks the lead's model and backend as one decision: the
// profile's lead role when declared, else its architect's — the closest
// analogue for reasoning about a goal's work rather than doing it.
func (e *Engine) resolveLeadRole(profileName string) (config.RoleConfig, string) {
	for _, role := range []agent.Role{agent.RoleLead, agent.RoleArchitect} {
		if rc, ok := e.lookupRole(profileName, role); ok {
			return rc, rc.Backend
		}
	}
	return config.RoleConfig{Model: e.cfg.Model}, ""
}

// leadAgent returns the agent a goal's lead runs on, and whether that
// agent can reach tools at all — a lead with no tools cannot act.
func (e *Engine) leadAgent(goal domain.Feature) (agent.Agent, config.RoleConfig, bool) {
	rc, backend := e.resolveLeadRole(goal.Profile)
	ag := e.agentFor(backend)
	if ag == nil {
		return nil, rc, false
	}
	caps := ag.Capabilities()
	return ag, rc, caps.ClientTools || caps.MCPTools
}

// leadAvailable reports whether a lead turn can run for goal: a lead agent
// with tools resolves for its profile.
func (e *Engine) leadAvailable(goal domain.Feature) bool {
	_, _, ok := e.leadAgent(goal)
	return ok
}

// leadTurn is one lead session's scope: the goal, what woke it, and what
// its tools may do this turn.
type leadTurn struct {
	e    *Engine
	view GoalView
	// question, when set, is the card question this turn exists to answer
	// (GoalAnswer); plan, the card whose plan it exists to check.
	question *leadQuestion
	planCard domain.FeatureID

	mu       sync.Mutex
	starts   []GoalStart
	answer   string
	answered bool
	approve  *bool
	planNote string
	acted    bool
	// decided holds the decisions this turn already recorded, so a
	// card_answer's decision is not recorded a second time by a
	// decision_record for the same card in the same turn
	decided []state.GoalEntry
}

// recordedThisTurn names a decision this turn already recorded for card.
func (lt *leadTurn) recordedThisTurn(card domain.FeatureID) string {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	for _, en := range lt.decided {
		if en.Card == card {
			return en.DecisionRef()
		}
	}
	return ""
}

type leadQuestion struct {
	card domain.FeatureID
	ask  *Ask
}

var cardIDRe = regexp.MustCompile(`\b(?:FD|BG|RS)-[0-9]{3,}\b`)

// runLeadTurn runs one lead turn over reasons and returns the cards its
// actions left for the driving loop to start.
func (e *Engine) runLeadTurn(ctx context.Context, view GoalView, reasons []string) ([]GoalStart, error) {
	lt := &leadTurn{e: e, view: view}
	prompt := leadWakePrompt(view, reasons)
	text, err := e.leadSession(ctx, lt, prompt)
	ids := uniqueIDs(cardIDRe.FindAllString(strings.Join(reasons, "\n"), -1))
	if err != nil {
		return lt.starts, err
	}
	e.goalLog(ctx, view.Goal.ID, state.GoalPayload{Action: state.GoalLeadTurn, Detail: clip(text, 600), Ref: strings.Join(ids, ","), By: "lead"})
	return lt.starts, nil
}

func uniqueIDs(ids []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:runeCut(s, n)]) + "…"
}

// runeCut backs n off to the start of a rune, so a cut never splits one.
func runeCut(s string, n int) int {
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return n
}

// GoalAnswer is the question hook: a goal card's ask_user goes to its
// goal's lead instead of to a person. It returns the answer to deliver;
// when the lead cannot answer (unavailable, failed, or declined to), the
// question's recommended option is taken, exactly as a plain autopilot
// card would. ok is false when id is not a goal card, or one its goal
// dropped.
func (e *Engine) GoalAnswer(ctx context.Context, id domain.FeatureID, ask *Ask) (answer string, ok bool, err error) {
	card, err := e.cfg.Store.GetFeature(ctx, id)
	// a card its goal dropped is no longer the lead's to answer for
	if err != nil || !card.InGoal() || card.GoalDropped() || ask == nil {
		return "", false, err
	}
	goal, err := e.cfg.Store.GetFeature(ctx, card.GoalID)
	if err != nil {
		return "", false, err
	}
	fallback := RecommendedOption(ask)
	mu := e.goalLock(goal.ID)
	mu.Lock()
	defer mu.Unlock()
	view, verr := e.goalView(ctx, goal)
	if verr != nil || !view.Input.LeadAvailable || view.Ledger.Available < e.turnReserve() {
		return e.goalFallbackAnswer(ctx, goal, card, ask, fallback, "no lead turn could run"), true, nil
	}
	lt := &leadTurn{e: e, view: view, question: &leadQuestion{card: id, ask: ask}}
	text, lerr := e.leadSession(ctx, lt, leadQuestionPrompt(view, card, ask))
	if lerr != nil {
		e.logLeadFailure(ctx, goal.ID, id, lerr)
		return e.goalFallbackAnswer(ctx, goal, card, ask, fallback, "the lead turn failed"), true, nil
	}
	e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalLeadTurn, Card: id, Detail: clip(text, 600), By: "lead"})
	lt.mu.Lock()
	defer lt.mu.Unlock()
	if !lt.answered {
		return e.goalFallbackAnswer(ctx, goal, card, ask, fallback, "the lead did not answer"), true, nil
	}
	return lt.answer, true, nil
}

func (e *Engine) goalFallbackAnswer(ctx context.Context, goal, card domain.Feature, ask *Ask, fallback, why string) string {
	e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalAnswered, Card: card.ID, By: ActorGoal,
		Detail: fmt.Sprintf("%s: took the recommended answer %q to %q", why, fallback, clip(ask.Question, 200))})
	return fallback
}

// GoalPlanCheck is the plan hook: before a goal card leaves its plan for
// implement, its goal's lead reads the plan against the goal doc. It
// returns whether to cross, and the note to send the plan back with when
// not. A lead that cannot run, or does not decide, approves: the card's
// own plan critique already passed. ok is false when id is not a goal
// card, or one its goal dropped.
func (e *Engine) GoalPlanCheck(ctx context.Context, id domain.FeatureID) (approve bool, note string, ok bool, err error) {
	card, err := e.cfg.Store.GetFeature(ctx, id)
	if err != nil || !card.InGoal() || card.GoalDropped() {
		return true, "", false, err
	}
	goal, err := e.cfg.Store.GetFeature(ctx, card.GoalID)
	if err != nil {
		return true, "", false, err
	}
	mu := e.goalLock(goal.ID)
	mu.Lock()
	defer mu.Unlock()
	view, verr := e.goalView(ctx, goal)
	if verr != nil || !view.Input.LeadAvailable || view.Ledger.Available < e.turnReserve() {
		return true, "", true, nil
	}
	lt := &leadTurn{e: e, view: view, planCard: id}
	text, lerr := e.leadSession(ctx, lt, leadPlanPrompt(view, card, e.artifactFile(&card)))
	if lerr != nil {
		e.logLeadFailure(ctx, goal.ID, id, lerr)
		return true, "", true, nil
	}
	e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalLeadTurn, Card: id, Detail: clip(text, 600), By: "lead"})
	lt.mu.Lock()
	defer lt.mu.Unlock()
	if lt.approve != nil && !*lt.approve {
		return false, lt.planNote, true, nil
	}
	return true, "", true, nil
}

// leadSession runs one synchronous lead session and returns its final
// text. Tool calls are dispatched to lt as they arrive.
func (e *Engine) leadSession(ctx context.Context, lt *leadTurn, prompt string) (string, error) {
	goal := lt.view.Goal
	ag, rc, ok := e.leadAgent(goal)
	if !ok {
		return "", errors.New("no lead with tools is configured for this goal's profile")
	}
	caps := ag.Capabilities()
	maxCredits := lt.view.Ledger.Available
	if maxCredits < e.turnReserve() {
		return "", fmt.Errorf("the goal has %.0f credits left to give — not enough for a lead turn", max(0, maxCredits))
	}
	workDir := filepath.Join(e.pool.Root(), goal.WorktreePath())
	if _, err := os.Stat(workDir); err != nil {
		workDir = e.pool.Root()
	}
	var tools []agent.ToolDef
	var sockPath string
	var teardown func()
	switch {
	case caps.ClientTools:
		tools = lt.tools()
	case caps.MCPTools:
		p, td, err := e.startToolEndpoint(ctx, goal.ID, "lead", lt.tools, lt.dispatch)
		if err != nil {
			return "", err
		}
		sockPath, teardown = p, td
		defer teardown()
	}
	docPath := lt.view.DocPath
	hints := []string{leadHint(goal, docPath)}
	if card := e.goalReposCard(ctx, goal); card != "" {
		hints = append(hints, card)
	}
	if env := e.environmentCard(); env != "" {
		hints = append([]string{env}, hints...)
	}
	tctx, cancel := context.WithTimeout(ctx, leadTurnTimeout)
	defer cancel()
	sess, err := ag.NewSession(tctx, agent.SessionOpts{
		WorkDir:         workDir,
		ArtifactPath:    docPath,
		Role:            agent.RoleLead,
		Model:           rc.Model,
		Provider:        rc.Provider,
		Think:           rc.Think,
		OutputTokenMax:  rc.OutputTokenMax,
		Permission:      e.cfg.Permission,
		SystemHints:     hints,
		Tools:           tools,
		MaxCredits:      maxCredits * capHeadroom,
		FeatureID:       string(goal.ID),
		MCPSockPath:     sockPath,
		ReadOnly:        caps.ReadOnlyEnforce,
		ExtraReadAllows: []string{docPath},
	})
	if err != nil {
		return "", fmt.Errorf("starting the lead session: %w", err)
	}
	defer func() { _ = sess.Close() }()
	if err := sess.Send(tctx, prompt); err != nil {
		return "", err
	}
	var text assistantText
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				return text.String(), nil
			}
			switch ev.Kind {
			case agent.EventUsage:
				e.recordLeadUsage(goal.ID, ev.Usage)
			case agent.EventClientToolCall:
				if ev.ToolCall == nil {
					continue
				}
				out, derr := lt.dispatch(tctx, ev.ToolCall.Name, ev.ToolCall.Args)
				if derr != nil {
					out = "error: " + derr.Error()
				}
				resolve(tctx, sess, ev.ToolCall.ID, out)
			case agent.EventTextDelta:
				text.delta(ev.Text)
			case agent.EventMessage:
				text.message(ev.Text)
			case agent.EventIdle:
				return text.String(), nil
			case agent.EventBudgetExhausted:
				return text.String(), errors.New("the lead turn ran out of budget")
			case agent.EventError:
				return text.String(), ev.Err
			}
		case <-tctx.Done():
			return text.String(), fmt.Errorf("the lead turn did not finish: %w", tctx.Err())
		}
	}
}

// recordLeadUsage books a lead turn's spend to the goal card at implement,
// under the lead role, so the goal's own spend (and its ceiling) sees it.
func (e *Engine) recordLeadUsage(goal domain.FeatureID, u agent.Usage) {
	if e.cfg.Store == nil {
		return
	}
	credits := domain.Spend{Credits: u.Credits, InputTokens: u.InputTokens, OutputTokens: u.OutputTokens}.CreditEquivalent()
	var estimated float64
	if u.Credits <= 0 || u.Estimate {
		estimated = credits
	}
	if credits == 0 && u.InputTokens == 0 && u.OutputTokens == 0 {
		return
	}
	ctx := context.Background()
	_ = e.cfg.Store.AddSpend(ctx, goal, credits, estimated, u.InputTokens, u.OutputTokens)
	// No session key: the lead conducts the goal across every card's
	// stages, so its spend is the goal's, not one pass of one stage's.
	_ = e.cfg.Store.RecordStageSpend(ctx, goal, state.SpendSample{
		Stage: domain.StageImplement, Role: string(agent.RoleLead), Model: u.Model,
		Credits: credits, Estimated: estimated,
		InputTokens: u.InputTokens, CachedTokens: u.CachedTokens, OutputTokens: u.OutputTokens,
	})
}

// --- prompts ---------------------------------------------------------------

func leadHint(goal domain.Feature, docPath string) string {
	return strings.TrimSpace(fmt.Sprintf(`
You are the lead for goal %s: %s.
The goal doc at %s is the contract: its Objective, its Done when list,
its Limits and its Cards were agreed with the person who owns this goal,
who is not here and will only look at the result at the end. Each turn's
prompt carries what you need to act on: the goal's status (done-when
list, cards, budget, decisions, recent log) and the doc's Objective,
Limits and Notes — and for a plan check, the plan. Read more (goal_doc,
card_spec, card_diff, card_checks) only when the turn needs something
that is not there; every extra read makes the turn slower and dearer.

Your job is the goal's judgment, one short turn at a time. gummi runs the
rest by rule: it starts cards whose dependencies landed, lands verified
cards on the goal branch, and finishes the goal when every card has
landed or been dropped. You act only through the goal tools; you never
edit code, never run git, and never start work yourself.

What you may do: create cards that serve a done-when item (card_create),
drop cards (card_drop), raise a card's envelope from what the goal has
left to give (card_raise), send a card back with a note (card_send_back),
add a dependency (dep_add), answer a card's question (card_answer), approve
or send back a card's plan (card_plan), record a decision for review
(decision_record), decline a reviewer finding with your reason
(finding_decline), mark a done-when item not met with evidence
(done_when_not_met), repair the command of a done-when check that cannot
observe what its item says (done_when_check_fix), add a done-when item
that one of the owner's notes asked for (done_when_add), file an
out-of-goal bug or idea on the open board (backlog_file), set the reserve
you hold back for finishing (reserve_set), write the Try it section
(goal_doc_write), and wrap the goal up now (goal_wrap_up).

What you never do: go past the goal budget (the tools refuse it), change
what an agreed done-when item says or remove one (repairing a check's
command so it can observe the item is not that; weakening it so it
passes is), create a card that serves no done-when item, take a board
card the owner did not attach, or work around a sandbox refusal by
widening what a card may do — plan around
it, or mark the item it blocks not met.

A card reported as one that "cannot be verified in this environment" is
not a card that failed. Its verify said the machine it ran on cannot run
its verification plan, which is no opinion on the work. You are shown it
once. Send it back only if its plan asks the environment for something
the item does not need (a step that belongs under [CI-only], a service
the check could do without); otherwise leave it exactly as it is — the
goal keeps it, waits, and says what it is waiting on. Do not drop it and
do not mark its item not met: nothing has been found wrong.

When you are woken over a regression — something an experiment showed
holding that a later run shows failing — it is the strongest statement
about the code a substrate makes: a thing that never held failing is
expected, a thing that did is a landing's doing. gummi has already
bisected the landings where it could and names the card. Act on it with a
card that fixes it, citing the evidence directory; do not re-derive the
blame, and do not send the named card back — it has landed, and what is
on the goal branch is changed by a new card, not by reopening an old one.

Record a decision for review for every call a user of the result would
notice — behaviour, interface, user-visible naming, a dependency added, a
data format — and for anything that trades against a done-when item,
with the option you did not take. Internal code choices are not
decisions for review.

Be brief. End each turn with one or two sentences saying what you did and
why; that line is the goal's log.`, goal.ID, goal.Title, docPath))
}

func leadWakePrompt(view GoalView, reasons []string) string {
	var b strings.Builder
	b.WriteString("You were woken because:\n")
	for _, r := range reasons {
		b.WriteString("- " + r + "\n")
	}
	b.WriteString("\n")
	b.WriteString(goalStatusText(view))
	b.WriteString(goalDocBrief(view))
	b.WriteString("\nDecide and act with the goal tools. If nothing needs doing, say so.")
	return b.String()
}

func leadQuestionPrompt(view GoalView, card domain.Feature, ask *Ask) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Card %s (%s) is asking a question while it works:\n\n%s\n", card.ID, card.Title, ask.Question)
	if len(ask.Options) > 0 {
		b.WriteString("\nOptions:\n")
		for _, o := range ask.Options {
			line := "- " + o.Label
			if o.Detail != "" {
				line += " — " + o.Detail
			}
			b.WriteString(line + "\n")
		}
	}
	b.WriteString("\nAnswer it with card_answer, choosing what best serves the goal doc. " +
		"If a user of the result would notice the choice, include a decision (summary and the alternative you did not take).\n\n")
	b.WriteString(goalStatusText(view))
	b.WriteString(goalDocBrief(view))
	return b.String()
}

// leadPlanMax bounds the plan a plan check carries in its prompt; a longer
// one is cut, and the lead reads the rest with card_spec.
const leadPlanMax = 16000

func leadPlanPrompt(view GoalView, card domain.Feature, planPath string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Card %s (%s) has finished its plan and is about to start implementing. "+
		"Check its plan against the goal doc: does it serve the done-when item(s) "+
		"it was created for, stay inside the goal's Limits, and fit the other cards? "+
		"Then call card_plan exactly once: approve, or send it back with a note saying what to change.\n", card.ID, card.Title)
	if raw, err := os.ReadFile(planPath); planPath != "" && err == nil {
		plan := string(raw)
		if len(plan) > leadPlanMax {
			plan = plan[:runeCut(plan, leadPlanMax)] + "\n[… cut here; read the rest with card_spec]"
		}
		fmt.Fprintf(&b, "\n--- %s's plan ---\n%s\n--- end of plan ---\n", card.ID, strings.TrimSpace(plan))
	} else {
		b.WriteString("\nRead its plan with card_spec.\n")
	}
	b.WriteString("\n")
	b.WriteString(goalStatusText(view))
	b.WriteString(goalDocBrief(view))
	return b.String()
}

// goalDocBriefSections are the goal doc's agreed sections: the contract
// every lead turn is judged against, carried in its prompt so a turn does
// not spend a tool round, and a re-read of its whole context, fetching them.
var goalDocBriefSections = []string{spec.GoalSectionObjective, spec.GoalSectionLimits, spec.GoalSectionNotes}

// goalDocBriefMax bounds one section in the brief.
const goalDocBriefMax = 4000

// goalDocBrief renders the goal doc's objective, limits and your notes
// for a lead prompt (the done-when list and cards are in the status).
func goalDocBrief(v GoalView) string {
	if v.DocPath == "" {
		return ""
	}
	raw, err := os.ReadFile(v.DocPath)
	if err != nil {
		return ""
	}
	var b strings.Builder
	for _, name := range goalDocBriefSections {
		body, ok := spec.ViewSection(string(raw), name)
		body = strings.TrimSpace(stripPrompt(body))
		if !ok || body == "" {
			continue
		}
		if len(body) > goalDocBriefMax {
			body = body[:runeCut(body, goalDocBriefMax)] + "\n[… cut; goal_doc has the rest]"
		}
		fmt.Fprintf(&b, "\nGoal doc — %s:\n%s\n", name, body)
	}
	return b.String()
}

// goalStatusText renders a goal view for the lead (and goal_status).
func goalStatusText(v GoalView) string {
	var b strings.Builder
	g := v.Goal
	fmt.Fprintf(&b, "Goal %s: %s — stage %s\n", g.ID, g.Title, g.Stage)
	l := v.Ledger
	fmt.Fprintf(&b, "Budget %d · goal's own spend %.0f · held by cards %.0f · reserve %d · left to give %.0f\n",
		l.Envelope, l.Own, l.Given, l.Reserve, max(0, l.Available))
	if sb := v.Substrate; sb.Agreed() {
		// the goal's other ledger, in the same breath: what to spend on
		// finding out early is a trade against it, not against credits
		fmt.Fprintf(&b, "Substrate budget: %d of %s runs · %.0f of %s minutes spent · %d run(s) held back for being judged · a run has cost about %.0f minutes\n",
			sb.RunsSpent, orUnbounded(sb.Runs), sb.MinutesSpent, orUnbounded(sb.Minutes), goalpolicy.ReserveRuns, sb.TypicalMinutes)
	}
	if g.Goal.WrappingUp() {
		b.WriteString("The goal is wrapping up.\n")
	}
	b.WriteString("\nDone when:\n")
	for _, d := range v.DoneWhen {
		how := "judged"
		switch {
		case d.Experiment != "":
			how = "experiment: " + d.Experiment
			if len(d.Assertions) > 0 {
				how += " [" + strings.Join(d.Assertions, ", ") + "]"
			}
		case d.Check != "":
			how = "check: " + d.Check
		}
		fmt.Fprintf(&b, "- %s: %s (%s)\n", d.ID, d.Says, how)
	}
	if len(v.Experiments) > 0 {
		b.WriteString("\nExperiments (against the goal's current heads):\n")
		for _, x := range v.Experiments {
			fmt.Fprintf(&b, "- %s proves %s: ", x.Name, strings.Join(x.Items, ","))
			switch {
			case x.Problem != "":
				b.WriteString("cannot be run — " + x.Problem)
			case x.Running != nil:
				fmt.Fprintf(&b, "run %s in flight (%s)", x.Running.ID, x.Running.Purpose)
			case x.Evidence != nil:
				b.WriteString(describeEvidence(*x.Evidence, nil))
			default:
				b.WriteString("no conclusive run on these heads yet")
				if x.LastReason != "" {
					fmt.Fprintf(&b, " — the last %d judged nothing: %s", x.Inconclusive, x.LastReason)
				}
			}
			b.WriteString("\n")
		}
	}
	b.WriteString("\nCards:\n")
	if len(v.Cards) == 0 {
		b.WriteString("- none\n")
	}
	for _, c := range v.Cards {
		fmt.Fprintf(&b, "- %s %s [%s] %s · stage %s · spent %.0f of %d", c.Feature.ID, c.Feature.Kind, c.State, c.Feature.Title, c.Feature.Stage, c.Spent, c.Envelope)
		if len(c.Serves) > 0 {
			b.WriteString(" · serves " + strings.Join(c.Serves, ","))
		}
		if len(c.DependsOn) > 0 {
			b.WriteString(" · after " + strings.Join(sortedIDs(c.DependsOn), ","))
		}
		if c.TakenOver {
			b.WriteString(" · driven by the owner")
		}
		if c.Reason != "" {
			b.WriteString(" · " + c.Reason)
		}
		if c.Findings > 0 {
			fmt.Fprintf(&b, " · %d open reviewer finding(s)", c.Findings)
		}
		b.WriteString("\n")
	}
	var decisions []string
	for _, en := range v.Log {
		if en.Action == state.GoalDecision {
			decisions = append(decisions, en.DecisionRef()+" "+clip(en.Detail, 160))
		}
	}
	if len(decisions) > 0 {
		b.WriteString("\nDecisions for review already recorded (do not record one again):\n")
		for _, d := range decisions {
			b.WriteString("- " + d + "\n")
		}
	}
	var recent []state.GoalEntry
	if n := len(v.Log); n > 12 {
		recent = v.Log[n-12:]
	} else {
		recent = v.Log
	}
	if len(recent) > 0 {
		b.WriteString("\nRecent log:\n")
		for _, en := range recent {
			fmt.Fprintf(&b, "- %s", en.Action)
			if en.Card != "" {
				b.WriteString(" " + string(en.Card))
			}
			if en.N > 0 {
				b.WriteString(" " + en.DecisionRef())
			}
			if en.Detail != "" {
				b.WriteString(": " + clip(en.Detail, 160))
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

// --- tools -----------------------------------------------------------------

func leadTool(name, desc string, props map[string]any, required ...string) agent.ToolDef {
	params := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		params["required"] = required
	}
	return agent.ToolDef{Name: name, Description: desc, Parameters: params}
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func num(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }
func strs(desc string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
}

func (lt *leadTurn) tools() []agent.ToolDef {
	card := str("A card id of this goal, e.g. FD-012.")
	return []agent.ToolDef{
		leadTool("goal_status", "The goal's budget, done-when list, cards and recent log.", map[string]any{}),
		leadTool("goal_doc", "Read the goal doc, or one section of it.", map[string]any{"section": str("A section title, e.g. Objective; empty for the whole doc.")}),
		leadTool("card_spec", "Read a goal card's spec, bug report or research document.", map[string]any{"card": card}, "card"),
		leadTool("card_diff", "Read a goal card's diff against the goal branch (summary and the start of the patch).", map[string]any{"card": card}, "card"),
		leadTool("card_checks", "Read a goal card's latest check results, with the output of the ones that failed — what a card's own claim that its checks pass can be held to.", map[string]any{"card": card}, "card"),
		leadTool("goal_diff", "Read the goal branch's combined diff against main (summary and the start of the patch) — what a review of the goal reads.", map[string]any{}),
		leadTool("card_create", "Create a card inside the goal. It must serve at least one done-when item; its envelope comes out of what the goal has left to give.",
			map[string]any{
				"kind":       str("feature (default), bug, research, or diagnosis — a read-only investigation into why something already behaves wrong, which proposes fix cards rather than writing a fix"),
				"title":      str("Short title."),
				"one_liner":  str("What the card must do, in a sentence or two."),
				"serves":     strs("Done-when ids this card is for."),
				"repo":       str("The managed repository the card is in; omit for the goal's own."),
				"depends_on": strs("Card ids that must land first."),
				"envelope":   num("Credits for the card; 0 for a fair share of what is left."),
			}, "title", "serves"),
		leadTool("card_drop", "Drop a goal card. An attached card goes back to the board with its work kept.", map[string]any{"card": card, "reason": str("Why.")}, "card", "reason"),
		leadTool("card_raise", "Raise a goal card's envelope from what the goal has left to give.", map[string]any{"card": card, "envelope": num("The new envelope in credits."), "reason": str("Why.")}, "card", "envelope", "reason"),
		leadTool("card_send_back", "Send a goal card back with a note: a verified or failed card returns to implement, a stuck card restarts its stage.", map[string]any{"card": card, "note": str("What to change.")}, "card", "note"),
		leadTool("dep_add", "Make a goal card wait for another to land.", map[string]any{"card": card, "depends_on": card}, "card", "depends_on"),
		leadTool("card_answer", "Answer the card question this turn was woken for.", map[string]any{
			"answer":      str("The answer, one of the options when options were given."),
			"decision":    str("When a user would notice this choice: the decision in one line."),
			"alternative": str("The option you did not take."),
		}, "answer"),
		leadTool("card_plan", "Approve the plan this turn was woken to check, or send it back with a note.", map[string]any{
			"approve": map[string]any{"type": "boolean"},
			"note":    str("What to change, when not approving."),
		}, "approve"),
		leadTool("decision_record", "Record a decision for review: a call a user of the result would notice.", map[string]any{
			"decision":    str("The decision, in one line."),
			"alternative": str("The option not taken."),
			"card":        str("The card it affected, if one."),
			"item":        str("The done-when id it trades against, if one."),
		}, "decision", "alternative"),
		leadTool("finding_decline", "Decline a reviewer finding on a goal card, with your reason. It is listed at the end.", map[string]any{"card": card, "finding": str("The finding, quoted or summarized."), "reason": str("Why it is declined.")}, "card", "finding", "reason"),
		leadTool("done_when_not_met", "Mark a done-when item not met, with the evidence. The item itself is never changed.", map[string]any{"item": str("DW-N"), "reason": str("Why, with evidence.")}, "item", "reason"),
		leadTool("done_when_check_fix", "Repair the command of a done-when item's check when the command cannot observe its statement — for example a wrapper like `go run` that replaces the program's exit code with its own. What the item says never changes. The new command must fail on main, and pass on the goal branch once every card serving the item has landed; the repair is listed as a decision for review.",
			map[string]any{"item": str("DW-N"), "check": str("The repaired command."), "reason": str("Why the agreed command cannot prove the statement, with evidence.")}, "item", "check", "reason"),
		leadTool("done_when_add", "Add a done-when item one of the owner's notes asked for.", map[string]any{
			"says": str("The statement."), "check": str("A command that exits 0 when it holds; empty to have verify judge it."),
			"repo": str("The managed repository the check runs in; omit for the goal's own."),
			"note": str("The note that asked for it, quoted."),
		}, "says", "note"),
		leadTool("backlog_file", "File a real bug or idea outside this goal as a card on the open board. The goal never works it.", map[string]any{"kind": str("bug or feature"), "description": str("First line is the title."), "repo": str("The managed repository it is in; omit for the goal's own.")}, "description"),
		leadTool("reserve_set", "Set the credits you hold back for finishing the goal cleanly.", map[string]any{"credits": num("The reserve."), "reason": str("Your estimate's basis.")}, "credits"),
		leadTool("goal_doc_write", "Write the goal doc's Try it section.", map[string]any{"section": str("Try it"), "body": str("The section body.")}, "section", "body"),
		leadTool("goal_wrap_up", "Wrap the goal up now: nothing new starts, verified work lands, the rest is dropped.", map[string]any{"reason": str("Why.")}, "reason"),
	}
}

type leadArgs struct {
	Card        string          `json:"card"`
	Section     string          `json:"section"`
	Kind        string          `json:"kind"`
	Title       string          `json:"title"`
	OneLiner    string          `json:"one_liner"`
	Serves      []string        `json:"serves"`
	Repo        string          `json:"repo"`
	DependsOn   json.RawMessage `json:"depends_on"`
	Envelope    int             `json:"envelope"`
	Reason      string          `json:"reason"`
	Note        string          `json:"note"`
	Answer      string          `json:"answer"`
	Decision    string          `json:"decision"`
	Alternative string          `json:"alternative"`
	Approve     *bool           `json:"approve"`
	Item        string          `json:"item"`
	Finding     string          `json:"finding"`
	Says        string          `json:"says"`
	Check       string          `json:"check"`
	Description string          `json:"description"`
	Credits     int             `json:"credits"`
	Body        string          `json:"body"`
}

// dependsOn reads depends_on as a list (card_create) or a single id
// (dep_add).
func (a leadArgs) dependsOn() []string {
	if len(a.DependsOn) == 0 {
		return nil
	}
	var list []string
	if json.Unmarshal(a.DependsOn, &list) == nil {
		return list
	}
	var one string
	if json.Unmarshal(a.DependsOn, &one) == nil && one != "" {
		return []string{one}
	}
	return nil
}

// dispatch runs one lead tool call.
func (lt *leadTurn) dispatch(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	e := lt.e
	var a leadArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return "", fmt.Errorf("arguments do not parse: %w", err)
		}
	}
	goal, err := e.cfg.Store.GetFeature(ctx, lt.view.Goal.ID)
	if err != nil {
		return "", err
	}
	view, err := e.goalView(ctx, goal)
	if err != nil {
		return "", err
	}
	mark := func() { lt.mu.Lock(); lt.acted = true; lt.mu.Unlock() }
	cardOf := func() (GoalCard, error) {
		c, ok := view.Card(domain.FeatureID(strings.TrimSpace(a.Card)))
		if !ok {
			return GoalCard{}, fmt.Errorf("%q is not a card of %s", a.Card, goal.ID)
		}
		return c, nil
	}

	switch name {
	case "goal_status":
		return goalStatusText(view), nil
	case "goal_doc":
		if view.DocPath == "" {
			return "", errors.New("the goal doc is missing")
		}
		rawDoc, err := os.ReadFile(view.DocPath)
		if err != nil {
			return "", err
		}
		if s := strings.TrimSpace(a.Section); s != "" {
			body, ok := spec.ViewSection(string(rawDoc), s)
			if !ok {
				return "", fmt.Errorf("the goal doc has no section %q", s)
			}
			return body, nil
		}
		return string(rawDoc), nil
	case "card_spec":
		c, err := cardOf()
		if err != nil {
			return "", err
		}
		path := e.artifactFile(&c.Feature)
		if path == "" {
			return "", fmt.Errorf("%s has no document yet", c.Feature.ID)
		}
		b, err := os.ReadFile(path)
		return string(b), err
	case "card_diff":
		c, err := cardOf()
		if err != nil {
			return "", err
		}
		wt, err := e.mgr(ctx, &c.Feature)
		if err != nil {
			return "", err
		}
		stat, _ := wt.DiffStat(ctx, &c.Feature)
		diff, _ := wt.Diff(ctx, &c.Feature)
		return clip(stat+"\n\n"+diff, 12000), nil

	case "card_checks":
		c, err := cardOf()
		if err != nil {
			return "", err
		}
		return e.cardCheckResults(ctx, c.Feature.ID)

	case "goal_diff":
		wt, err := e.mgr(ctx, &goal)
		if err != nil {
			return "", err
		}
		stat, _ := wt.DiffStat(ctx, &goal)
		diff, _ := wt.Diff(ctx, &goal)
		return clip(stat+"\n\n"+diff, 12000), nil

	case "card_create":
		if goal.Goal.WrappingUp() {
			return "", errors.New("the goal is wrapping up; nothing new starts")
		}
		row := domain.GoalCardRow{Title: strings.TrimSpace(a.Title), OneLiner: a.OneLiner, Kind: strings.TrimSpace(a.Kind), Serves: a.Serves, Envelope: a.Envelope, Repo: strings.TrimSpace(a.Repo)}
		if row.Repo == "" {
			row.Repo = goal.Repo
		}
		if problem := e.goalRepoProblem(row.Repo); problem != "" {
			return "", fmt.Errorf("the card %s", problem)
		}
		items := map[string]bool{}
		itemText := map[string]string{}
		for _, d := range view.DoneWhen {
			items[d.ID], itemText[d.ID] = true, d.Says
		}
		if err := row.Validate(items); err != nil {
			return "", err
		}
		deps := a.dependsOn()
		for _, d := range deps {
			if _, ok := view.Card(domain.FeatureID(d)); !ok {
				return "", fmt.Errorf("depends_on %q is not a card of %s", d, goal.ID)
			}
		}
		avail := view.Ledger.Available
		env := a.Envelope
		if env <= 0 {
			env = int(avail / 2)
		}
		if env < domain.MinEnvelope {
			env = domain.MinEnvelope
		}
		if float64(env) > avail {
			return "", fmt.Errorf("a %d-credit card needs more than the %.0f credits the goal has left to give", env, max(0, avail))
		}
		// row.Validate above has already refused an unresolvable kind.
		ct, _ := row.EffectiveType()
		// A card forks from the goal branch of its own repository, so one
		// has to be there before the card is — the lead is free to put a
		// card in a repository the goal has not touched yet.
		if ct.Kind != domain.KindResearch {
			if _, terr := e.goalTreeIn(ctx, goal, row.Repo); terr != nil {
				return "", terr
			}
		}
		f, err := cardmint.Mint(ctx, e.cfg.Store, e.cfg.Workspace, cardmint.Input{
			Kind: ct.Kind, Mode: ct.Mode, Description: goalCardDescription(goal, row, itemText), Profile: goal.Profile,
			Envelope: env, Repo: row.Repo, RequireRepo: e.RequireRepo, GateApproval: domain.GateAutopilot, Goal: goal.ID,
		})
		if err != nil {
			return "", err
		}
		for _, d := range deps {
			if err := e.cfg.Store.AddDependency(ctx, f.ID, domain.FeatureID(d)); err != nil {
				return "", err
			}
		}
		row.ID = f.ID
		row.DependsOn = deps
		lt.appendCardRow(row)
		e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalMinted, Card: f.ID, Detail: f.Title, To: env, By: "lead"})
		mark()
		e.send(Event{Feature: f.ID, Kind: EventCardCreated})
		return fmt.Sprintf("created %s with %d credits; gummi starts it once its dependencies land", f.ID, env), nil

	case "card_drop":
		c, err := cardOf()
		if err != nil {
			return "", err
		}
		if c.State == goalpolicy.Landed || c.State == goalpolicy.Dropped {
			return "", fmt.Errorf("%s has already %s", c.Feature.ID, c.State)
		}
		if err := e.goalDrop(ctx, goal, c.Feature, a.Reason, "lead"); err != nil {
			return "", err
		}
		if c.Feature.GoalAttached {
			e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalDecision, Card: c.Feature.ID,
				Detail: "dropped attached card " + string(c.Feature.ID) + ": " + a.Reason, Alternative: "keep it in the goal", By: "lead"})
		}
		mark()
		return "dropped " + string(c.Feature.ID), nil

	case "card_raise":
		c, err := cardOf()
		if err != nil {
			return "", err
		}
		if c.TakenOver {
			return "", fmt.Errorf("%s is driven by the owner", c.Feature.ID)
		}
		if err := e.goalRaise(ctx, goal, c, a.Envelope, a.Reason, "lead"); err != nil {
			return "", err
		}
		if c.State == goalpolicy.Exhausted {
			lt.addStart(GoalStart{ID: c.Feature.ID})
		}
		mark()
		return fmt.Sprintf("%s raised to %d credits", c.Feature.ID, a.Envelope), nil

	case "card_send_back":
		c, err := cardOf()
		if err != nil {
			return "", err
		}
		if c.TakenOver {
			return "", fmt.Errorf("%s is driven by the owner", c.Feature.ID)
		}
		if strings.TrimSpace(a.Note) == "" {
			return "", errors.New("a send-back needs a note")
		}
		switch c.State {
		case goalpolicy.Landed, goalpolicy.Dropped, goalpolicy.Waiting:
			return "", fmt.Errorf("%s is %s; there is nothing to send back", c.Feature.ID, c.State)
		}
		st, err := e.goalBounce(ctx, goal, c.Feature, a.Note, "lead")
		if err != nil {
			return "", err
		}
		lt.addStart(st)
		mark()
		return "sent " + string(c.Feature.ID) + " back", nil

	case "dep_add":
		c, err := cardOf()
		if err != nil {
			return "", err
		}
		deps := a.dependsOn()
		if len(deps) != 1 {
			return "", errors.New("dep_add takes one depends_on id")
		}
		if _, ok := view.Card(domain.FeatureID(deps[0])); !ok {
			return "", fmt.Errorf("%q is not a card of %s", deps[0], goal.ID)
		}
		if err := e.cfg.Store.AddDependency(ctx, c.Feature.ID, domain.FeatureID(deps[0])); err != nil {
			return "", err
		}
		mark()
		return fmt.Sprintf("%s now waits for %s", c.Feature.ID, deps[0]), nil

	case "card_answer":
		if lt.question == nil {
			return "", errors.New("no card question is waiting on this turn")
		}
		if strings.TrimSpace(a.Answer) == "" {
			return "", errors.New("an empty answer answers nothing")
		}
		lt.mu.Lock()
		lt.answer, lt.answered, lt.acted = strings.TrimSpace(a.Answer), true, true
		lt.mu.Unlock()
		e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalAnswered, Card: lt.question.card, Detail: clip(lt.question.ask.Question, 200) + " → " + a.Answer, By: "lead"})
		if strings.TrimSpace(a.Decision) != "" {
			en := e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalDecision, Card: lt.question.card, Detail: a.Decision, Alternative: a.Alternative, By: "lead"})
			lt.mu.Lock()
			lt.decided = append(lt.decided, en)
			lt.mu.Unlock()
		}
		return "answer recorded; it is delivered when this turn ends", nil

	case "card_plan":
		if lt.planCard == "" {
			return "", errors.New("no card plan is waiting on this turn")
		}
		if a.Approve == nil {
			return "", errors.New("say approve: true or false")
		}
		if !*a.Approve && strings.TrimSpace(a.Note) == "" {
			return "", errors.New("sending a plan back needs a note")
		}
		lt.mu.Lock()
		lt.approve, lt.planNote, lt.acted = a.Approve, strings.TrimSpace(a.Note), true
		lt.mu.Unlock()
		if *a.Approve {
			e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalPlanApproved, Card: lt.planCard, By: "lead"})
			return "plan approved", nil
		}
		e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalBounced, Card: lt.planCard, Detail: "plan sent back: " + a.Note, By: "lead"})
		return "plan sent back", nil

	case "decision_record":
		if strings.TrimSpace(a.Decision) == "" || strings.TrimSpace(a.Alternative) == "" {
			return "", errors.New("a decision for review needs the decision and the alternative not taken")
		}
		if ref := lt.recordedThisTurn(domain.FeatureID(a.Card)); ref != "" {
			return "already recorded as " + ref + " this turn (card_answer records the decision it carries)", nil
		}
		en := e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalDecision, Card: domain.FeatureID(a.Card), Item: a.Item, Detail: a.Decision, Alternative: a.Alternative, By: "lead"})
		mark()
		return "recorded " + en.DecisionRef(), nil

	case "finding_decline":
		c, err := cardOf()
		if err != nil {
			return "", err
		}
		e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalDeclined, Card: c.Feature.ID, Ref: clip(a.Finding, 300), Detail: a.Reason, By: "lead"})
		mark()
		return "declined; it is listed at the end", nil

	case "done_when_not_met":
		found := false
		for _, d := range view.DoneWhen {
			if d.ID == strings.TrimSpace(a.Item) {
				found = true
			}
		}
		if !found {
			return "", fmt.Errorf("%q is not on the done-when list", a.Item)
		}
		e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalNotMet, Item: strings.TrimSpace(a.Item), Detail: a.Reason, By: "lead"})
		mark()
		return a.Item + " marked not met", nil

	case "done_when_check_fix":
		return lt.fixDoneWhenCheck(ctx, goal, view, a)

	case "done_when_add":
		hasNote := false
		for _, en := range view.Log {
			if en.Action == state.GoalNote {
				hasNote = true
			}
		}
		if !hasNote {
			return "", errors.New("only the owner adds done-when items; no note of theirs asks for one")
		}
		return lt.addDoneWhen(ctx, goal, view, a)

	case "backlog_file":
		kind := domain.KindBug
		if strings.TrimSpace(a.Kind) == string(domain.KindFeature) {
			kind = domain.KindFeature
		}
		repo := strings.TrimSpace(a.Repo)
		if repo == "" {
			repo = goal.Repo
		}
		if problem := e.goalRepoProblem(repo); problem != "" {
			return "", fmt.Errorf("the card %s", problem)
		}
		f, err := cardmint.Mint(ctx, e.cfg.Store, e.cfg.Workspace, cardmint.Input{
			Kind: kind, Description: a.Description, Profile: goal.Profile, Repo: repo, RequireRepo: e.RequireRepo, FoundBy: goal.ID,
		})
		if err != nil {
			return "", err
		}
		e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalFound, Card: f.ID, Detail: f.Title, By: "lead"})
		mark()
		e.send(Event{Feature: f.ID, Kind: EventCardCreated})
		return "filed " + string(f.ID) + " on the open board", nil

	case "reserve_set":
		if a.Credits <= 0 {
			return "", errors.New("a reserve is a positive number of credits")
		}
		if a.Credits > goal.Budget.Envelope {
			return "", fmt.Errorf("a reserve cannot exceed the goal budget of %d", goal.Budget.Envelope)
		}
		if err := e.cfg.Store.SetGoalReserve(ctx, goal.ID, a.Credits); err != nil {
			return "", err
		}
		e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalReserve, From: goal.ReserveCredits(), To: a.Credits, Detail: a.Reason, By: "lead"})
		mark()
		return fmt.Sprintf("reserve set to %d credits", a.Credits), nil

	case "goal_doc_write":
		if !strings.EqualFold(strings.TrimSpace(a.Section), spec.GoalSectionTryIt) {
			return "", errors.New("the lead writes only the Try it section")
		}
		if view.DocPath == "" {
			return "", errors.New("the goal doc is missing")
		}
		rawDoc, err := os.ReadFile(view.DocPath)
		if err != nil {
			return "", err
		}
		doc, _, err := spec.ReplaceSection(string(rawDoc), spec.GoalSectionTryIt, a.Body)
		if err != nil {
			return "", err
		}
		if err := atomicfile.Write(view.DocPath, []byte(doc), 0o600); err != nil {
			return "", err
		}
		mark()
		return "Try it written", nil

	case "goal_wrap_up":
		if err := e.goalWrapUp(ctx, goal.ID, firstNonEmpty(a.Reason, "the lead wrapped the goal up"), "lead"); err != nil {
			return "", err
		}
		mark()
		return "wrapping up", nil
	}
	return "", fmt.Errorf("unknown tool %q", name)
}

func (lt *leadTurn) addStart(s GoalStart) {
	lt.mu.Lock()
	lt.starts = append(lt.starts, s)
	lt.mu.Unlock()
}

// appendCardRow adds a lead-created card to the goal doc's card list, so
// the doc keeps naming every card the goal runs.
func (lt *leadTurn) appendCardRow(row domain.GoalCardRow) {
	path := lt.view.DocPath
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	items, _, _ := spec.ParseDoneWhen(string(raw))
	rows, _, err := spec.ParseGoalCards(string(raw), items)
	if err != nil {
		return
	}
	rows = append(rows, row)
	if doc, err := spec.SetGoalCards(string(raw), rows); err == nil {
		_ = atomicfile.Write(path, []byte(doc), 0o600)
	}
}

// addDoneWhen adds an item a note asked for: into the doc's list, and its
// command into the goal's checks.
func (lt *leadTurn) addDoneWhen(ctx context.Context, goal domain.Feature, view GoalView, a leadArgs) (string, error) {
	e := lt.e
	if view.DocPath == "" {
		return "", errors.New("the goal doc is missing")
	}
	raw, err := os.ReadFile(view.DocPath)
	if err != nil {
		return "", err
	}
	items, _, err := spec.ParseDoneWhen(string(raw))
	if err != nil {
		return "", err
	}
	it := domain.DoneWhen{ID: fmt.Sprintf("DW-%d", len(items)+1), Says: strings.TrimSpace(a.Says), Check: strings.TrimSpace(a.Check), Repo: strings.TrimSpace(a.Repo)}
	it.Judge = it.Check == ""
	if err := it.Validate(); err != nil {
		return "", err
	}
	if it.Check != "" && it.Repo != "" {
		if problem := e.goalRepoProblem(it.Repo); problem != "" {
			return "", fmt.Errorf("the check %s", problem)
		}
	}
	items = append(items, it)
	doc, err := spec.SetDoneWhen(string(raw), items)
	if err != nil {
		return "", err
	}
	if it.Check != "" {
		checks, _, _ := spec.ParseChecks(doc)
		no := false
		checks = append(checks, domain.Check{Name: it.CheckName(), Cmd: it.Check, Baseline: &no})
		if doc, err = spec.UpsertChecks(doc, checks); err != nil {
			return "", err
		}
	}
	if err := atomicfile.Write(view.DocPath, []byte(doc), 0o600); err != nil {
		return "", err
	}
	e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalItemAdded, Item: it.ID, Detail: it.Says, Ref: clip(a.Note, 200), By: "lead"})
	lt.mu.Lock()
	lt.acted = true
	lt.mu.Unlock()
	return "added " + it.ID, nil
}

// fixDoneWhenCheck repairs the command of a done-when item's check. What
// the item says is the owner's and never changes; the command is only the
// means of proving it, and an agreed command can be unable to — a wrapper
// that swallows the exit code it is meant to observe. So a repair is held
// to what makes a check a check: it must not already pass on main, where
// the goal's work is absent, and once every card serving the item has
// landed it must pass on the goal branch. It is a decision for review,
// with the agreed command as the alternative not taken.
func (lt *leadTurn) fixDoneWhenCheck(ctx context.Context, goal domain.Feature, view GoalView, a leadArgs) (string, error) {
	e := lt.e
	id, cmd := strings.TrimSpace(a.Item), strings.TrimSpace(a.Check)
	if cmd == "" || strings.TrimSpace(a.Reason) == "" {
		return "", errors.New("a check repair needs the new command and the reason the agreed one cannot prove the item")
	}
	if view.DocPath == "" {
		return "", errors.New("the goal doc is missing")
	}
	raw, err := os.ReadFile(view.DocPath)
	if err != nil {
		return "", err
	}
	items, _, err := spec.ParseDoneWhen(string(raw))
	if err != nil {
		return "", err
	}
	idx := -1
	for i, d := range items {
		if d.ID == id {
			idx = i
		}
	}
	switch {
	case idx < 0:
		return "", fmt.Errorf("%q is not on the done-when list", a.Item)
	case items[idx].Check == "":
		return "", fmt.Errorf("%s is judged, not checked; there is no command to repair", id)
	case items[idx].Check == cmd:
		return "", fmt.Errorf("that is %s's agreed command already", id)
	}
	old := items[idx]
	probe := domain.Check{Name: old.CheckName(), Cmd: cmd}

	// The item's own repository, both times: what makes a check a check is
	// that it fails on that repo's main and passes on that repo's goal
	// branch, and a goal that spans repositories has one of each per repo.
	itemRepo := goalItemRepo(old, goal.Repo)
	tree, err := e.pool.GoalTree(&goal, itemRepo)
	if err != nil {
		return "", err
	}
	main, err := e.pool.ManagerForName(ctx, itemRepo)
	if err != nil {
		return "", err
	}
	var onMain verify.Result
	if err := main.WithMainCheckout(ctx, func(dir string) error {
		onMain = verify.Run(ctx, dir, []domain.Check{probe})[0]
		return nil
	}); err != nil {
		return "", err
	}
	if onMain.OK {
		return "", fmt.Errorf("the new command already passes on %s, where none of the goal's work is — it cannot tell whether %s holds", repoMain(itemRepo), id)
	}
	landed := true
	for _, c := range view.Cards {
		if slices.Contains(c.Serves, id) && c.State != goalpolicy.Landed && c.State != goalpolicy.Dropped {
			landed = false
		}
	}
	if landed {
		if !tree.Exists() {
			return "", fmt.Errorf("%s has no goal tree in %s, so the new command has nowhere to be proved", goal.ID, repoName(itemRepo))
		}
		if res := verify.Run(ctx, tree.Dir, []domain.Check{probe})[0]; !res.OK {
			return "", fmt.Errorf("the new command does not pass on the goal branch (exit %d):\n%s\nIf %s really is not met, mark it not met instead", res.ExitCode, clip(res.Output, 1500), id)
		}
	}

	items[idx].Check = cmd
	doc, err := spec.SetDoneWhen(string(raw), items)
	if err != nil {
		return "", err
	}
	checks, _, _ := spec.ParseChecks(doc)
	for i := range checks {
		if checks[i].Name == old.CheckName() {
			checks[i].Cmd = cmd
		}
	}
	if doc, err = spec.UpsertChecks(doc, checks); err != nil {
		return "", err
	}
	if err := atomicfile.Write(view.DocPath, []byte(doc), 0o600); err != nil {
		return "", err
	}
	e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalCheckFixed, Item: id, Detail: a.Reason, Ref: clip(old.Check, 300), By: "lead"})
	en := e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalDecision, Item: id,
		Detail:      fmt.Sprintf("%s's check now runs `%s`: %s", id, cmd, clip(a.Reason, 400)),
		Alternative: fmt.Sprintf("the agreed check `%s`", old.Check), By: "lead"})
	lt.mu.Lock()
	lt.acted = true
	lt.mu.Unlock()
	return fmt.Sprintf("%s's check repaired and recorded as %s; goal verify runs it", id, en.DecisionRef()), nil
}

// --- catch-up resolver ------------------------------------------------------

// resolveGoalCatchUp resolves an open catch-up merge on one of the goal's
// branches: an implementer session in that repository's goal tree, told to
// resolve the conflicted files and nothing else. gummi — not the session —
// concludes the merge, and only when no path is left unmerged.
func (e *Engine) resolveGoalCatchUp(ctx context.Context, goal domain.Feature, tree worktree.GoalTree, files []string) error {
	hint := "You are resolving a merge of main into a goal branch. The merge is already in progress in your working directory. " +
		"Resolve the conflict markers in the conflicted files so both sides' intent survives, `git add` each resolved file, and stop. " +
		"Do not commit, do not abort the merge, and do not change anything else."
	if err := e.resolveConflicts(ctx, goal, tree.Dir, hint, files); err != nil {
		return err
	}
	return tree.ConcludeMerge(ctx)
}

// resolveGoalRebase is the same bounded session one conflict site over:
// a card's own commits being replayed onto the goal branch as it lands,
// which the landing used to answer by sending the card back to a full
// implement stage — a fresh write, its critique and its verify — to
// replay a rebase. The card is already verified; what it needs is the
// conflict resolved, not to be built again.
func (e *Engine) resolveGoalRebase(ctx context.Context, goal, card domain.Feature, dir string, files []string) error {
	hint := "You are resolving a rebase of one card's branch onto the goal branch it lands on. The rebase is already in progress in your working directory " +
		"and has stopped on a conflicting commit. Resolve the conflict markers so both sides' intent survives — the goal branch's side is work that has " +
		"already landed from another card, and this branch's side is " + string(card.ID) + "'s own — then `git add` each resolved file and stop. " +
		"Do not commit, do not continue or abort the rebase, and do not change anything else."
	return e.resolveConflicts(ctx, goal, dir, hint, files)
}

// resolveConflicts runs one bounded, synchronous implementer session in
// dir to resolve the conflicts of an operation already in progress there.
// It is booked to the goal's own budget like a lead turn, and never
// concludes the operation itself — the caller owns that, because only the
// caller knows whether the thing in progress is a merge or a rebase.
func (e *Engine) resolveConflicts(ctx context.Context, goal domain.Feature, dir, hint string, files []string) error {
	rc, backend := e.resolveRole(goal.Profile, agent.RoleImplementer)
	ag := e.agentFor(backend)
	if ag == nil {
		return errors.New("no implementer is configured to resolve the conflicts")
	}
	view, err := e.goalView(ctx, goal)
	if err != nil {
		return err
	}
	if view.Ledger.OwnBudget() < e.turnReserve() {
		return errors.New("the goal has no budget left to resolve conflicts")
	}
	tctx, cancel := context.WithTimeout(ctx, leadTurnTimeout)
	defer cancel()
	sess, err := ag.NewSession(tctx, agent.SessionOpts{
		WorkDir: dir, Role: agent.RoleImplementer, Model: rc.Model, Provider: rc.Provider, Think: rc.Think,
		Permission: e.cfg.Permission, MaxCredits: min(view.Ledger.OwnBudget(), 200) * capHeadroom,
		SystemHints: []string{hint},
	})
	if err != nil {
		return err
	}
	defer func() { _ = sess.Close() }()
	if err := sess.Send(tctx, "Resolve the conflicts in: "+strings.Join(files, ", ")); err != nil {
		return err
	}
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				return nil
			}
			switch ev.Kind {
			case agent.EventUsage:
				e.recordLeadUsage(goal.ID, ev.Usage)
			case agent.EventIdle, agent.EventBudgetExhausted:
				return nil
			case agent.EventError:
				return ev.Err
			}
		case <-tctx.Done():
			return tctx.Err()
		}
	}
}

// --- a tool endpoint for MCP backends --------------------------------------

// startToolEndpoint serves a fixed tool set over a per-open unix socket for
// an MCPTools backend, speaking the session-socket protocol the `gummi
// __mcp --feature <id>` shim dials. It is the consult endpoint's shape with
// the tool list and dispatch passed in, so a goal's lead reaches exactly
// its goal tools and nothing else.
func (e *Engine) startToolEndpoint(ctx context.Context, id domain.FeatureID, label string, tools func() []agent.ToolDef, dispatch func(context.Context, string, json.RawMessage) (string, error)) (string, func(), error) {
	path := filepath.Join(e.cfg.Workspace.StateDir(), "mcp", fmt.Sprintf("%s-%s-%s.sock", label, id, workspaceMCPNonce()))
	if len(path) > unixPathMax {
		sum := sha256.Sum256([]byte(path))
		path = filepath.Join(os.TempDir(), fmt.Sprintf("gummi-mcp-%s-%x.sock", label, sum[:6]))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", nil, err
	}
	_ = os.Remove(path)
	ln, err := (&net.ListenConfig{}).Listen(ctx, "unix", path)
	if err != nil {
		return "", nil, fmt.Errorf("%s mcp listen %s: %w", label, path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return "", nil, err
	}
	epCtx, epCancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	var connMu sync.Mutex
	conns := map[net.Conn]struct{}{}
	closed := false
	serve := func(conn net.Conn) {
		sc := bufio.NewScanner(conn)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		var wmu sync.Mutex
		write := func(v any) {
			wmu.Lock()
			defer wmu.Unlock()
			_ = mcp.Encode(conn, v)
		}
		first := true
		for sc.Scan() {
			line := bytes.TrimSpace(sc.Bytes())
			if len(line) == 0 {
				continue
			}
			req, err := mcp.Decode(line)
			if err != nil {
				continue
			}
			if first {
				first = false
				var h helloParams
				_ = json.Unmarshal(req.Params, &h)
				if req.Method != "hello" || h.Feature != string(id) {
					if len(req.ID) > 0 {
						write(mcp.ErrorObject{JSONRPC: mcp.JSONRPC, ID: req.ID, Error: mcp.ErrorData{Code: mcp.FeatureMismatch, Message: "feature mismatch"}})
					}
					return
				}
				if len(req.ID) > 0 {
					write(mcp.Response{JSONRPC: mcp.JSONRPC, ID: req.ID, Result: json.RawMessage(fmt.Sprintf(`{"feature":%q}`, h.Feature))})
				}
				continue
			}
			switch req.Method {
			case "list_tools":
				res, err := mcp.MarshalTools(tools())
				if err != nil {
					write(mcp.ErrorObject{JSONRPC: mcp.JSONRPC, ID: req.ID, Error: mcp.ErrorData{Code: mcp.ToolError, Message: err.Error()}})
					continue
				}
				write(mcp.Response{JSONRPC: mcp.JSONRPC, ID: req.ID, Result: res})
			case "call_tool":
				wg.Add(1)
				go func(req *mcp.Request) {
					defer wg.Done()
					var p callToolParams
					if err := json.Unmarshal(req.Params, &p); err != nil {
						write(mcp.ErrorObject{JSONRPC: mcp.JSONRPC, ID: req.ID, Error: mcp.ErrorData{Code: mcp.ToolError, Message: err.Error()}})
						return
					}
					out, err := dispatch(epCtx, p.Name, p.Args)
					if err != nil {
						write(mcp.ErrorObject{JSONRPC: mcp.JSONRPC, ID: req.ID, Error: mcp.ErrorData{Code: mcp.ToolError, Message: err.Error()}})
						return
					}
					res, _ := json.Marshal(map[string]any{"result": out})
					write(mcp.Response{JSONRPC: mcp.JSONRPC, ID: req.ID, Result: res})
				}(req)
			default:
				if len(req.ID) > 0 {
					write(mcp.ErrorObject{JSONRPC: mcp.JSONRPC, ID: req.ID, Error: mcp.ErrorData{Code: mcp.MethodNotFound, Message: "method not found"}})
				}
			}
		}
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			connMu.Lock()
			if closed {
				connMu.Unlock()
				_ = conn.Close()
				return
			}
			conns[conn] = struct{}{}
			connMu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { connMu.Lock(); delete(conns, conn); connMu.Unlock() }()
				serve(conn)
			}()
		}
	}()
	var once sync.Once
	teardown := func() {
		once.Do(func() {
			epCancel()
			_ = ln.Close()
			connMu.Lock()
			closed = true
			for c := range conns {
				_ = c.Close()
			}
			connMu.Unlock()
			wg.Wait()
			_ = os.Remove(path)
		})
	}
	return path, teardown, nil
}

// cardCheckResults renders a card's most recent check run — the "check
// <name>: <status>" tool rows its stages record — newest run first, with
// the captured output of every failure.
func (e *Engine) cardCheckResults(ctx context.Context, id domain.FeatureID) (string, error) {
	events, err := e.cfg.Store.Events(ctx, id)
	if err != nil {
		return "", err
	}
	type row struct{ label, output string }
	var run []row
	seen := map[string]bool{}
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		if ev.Kind != state.EventTool {
			continue
		}
		var p struct {
			Label string `json:"label"`
		}
		if json.Unmarshal([]byte(ev.Payload), &p) != nil || !strings.HasPrefix(p.Label, "check ") {
			continue
		}
		name, _, _ := strings.Cut(strings.TrimPrefix(p.Label, "check "), ":")
		if seen[name] {
			continue // an older run of a check already reported
		}
		seen[name] = true
		out := ""
		if ev.Status == state.StatusFail {
			out = ev.Output
		}
		run = append(run, row{p.Label, out})
	}
	if len(run) == 0 {
		return "no check results recorded for " + string(id) + " yet", nil
	}
	var b strings.Builder
	for _, r := range run {
		b.WriteString("- " + r.label + "\n")
		if strings.TrimSpace(r.output) != "" {
			b.WriteString(clip(r.output, 1500) + "\n")
		}
	}
	b.WriteString("\nChecks run under sh -c in the card's worktree; a command that only works in bash fails here.")
	return b.String(), nil
}
