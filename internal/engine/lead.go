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
	"strings"
	"sync"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/atomicfile"
	"github.com/morphis/gummi/internal/cardmint"
	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/goalpolicy"
	"github.com/morphis/gummi/internal/mcp"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
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
	return strings.TrimSpace(s[:n]) + "…"
}

// GoalAnswer is the question hook: a goal card's ask_user goes to its
// goal's lead instead of to a person. It returns the answer to deliver;
// when the lead cannot answer (unavailable, failed, or declined to), the
// question's recommended option is taken, exactly as a plain autopilot
// card would. ok is false when id is not a goal card.
func (e *Engine) GoalAnswer(ctx context.Context, id domain.FeatureID, ask *Ask) (answer string, ok bool, err error) {
	card, err := e.cfg.Store.GetFeature(ctx, id)
	if err != nil || !card.InGoal() || ask == nil {
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
	text, lerr := e.leadSession(ctx, lt, leadQuestionPrompt(card, ask))
	if lerr != nil {
		e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalLeadFailed, Card: id, Detail: lerr.Error(), By: ActorGoal})
		return e.goalFallbackAnswer(ctx, goal, card, ask, fallback, "the lead turn failed"), true, nil
	}
	e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalLeadTurn, Detail: clip(text, 600), Ref: string(id), By: "lead"})
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
// card.
func (e *Engine) GoalPlanCheck(ctx context.Context, id domain.FeatureID) (approve bool, note string, ok bool, err error) {
	card, err := e.cfg.Store.GetFeature(ctx, id)
	if err != nil || !card.InGoal() {
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
	text, lerr := e.leadSession(ctx, lt, leadPlanPrompt(card))
	if lerr != nil {
		e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalLeadFailed, Card: id, Detail: lerr.Error(), By: ActorGoal})
		return true, "", true, nil
	}
	e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalLeadTurn, Detail: clip(text, 600), Ref: string(id), By: "lead"})
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
	_ = e.cfg.Store.RecordStageSpend(ctx, goal, domain.StageImplement, string(agent.RoleLead), u.Model,
		credits, estimated, u.InputTokens, u.CachedTokens, u.OutputTokens)
}

// --- prompts ---------------------------------------------------------------

func leadHint(goal domain.Feature, docPath string) string {
	return strings.TrimSpace(fmt.Sprintf(`
You are the lead for goal %s: %s.
The goal doc at %s is the contract: its Objective, its Done when list,
its Limits and its Cards were agreed with the person who owns this goal,
who is not here and will only look at the result at the end. Read it
with goal_doc (or your file reader) before you act.

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
(done_when_not_met), add a done-when item that one of the owner's notes
asked for (done_when_add), file an out-of-goal bug or idea on the open
board (backlog_file), set the reserve you hold back for finishing
(reserve_set), write the Try it section (goal_doc_write), and wrap the goal
up now (goal_wrap_up).

What you never do: go past the goal budget (the tools refuse it), rewrite
or remove a done-when item the owner agreed, create a card that serves no
done-when item, take a board card the owner did not attach, or work
around a sandbox refusal by widening what a card may do — plan around
it, or mark the item it blocks not met.

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
	b.WriteString("\nDecide and act with the goal tools. If nothing needs doing, say so.")
	return b.String()
}

func leadQuestionPrompt(card domain.Feature, ask *Ask) string {
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
		"If a user of the result would notice the choice, include a decision (summary and the alternative you did not take).")
	return b.String()
}

func leadPlanPrompt(card domain.Feature) string {
	return fmt.Sprintf("Card %s (%s) has finished its plan and is about to start implementing. "+
		"Read its plan with card_spec and check it against the goal doc: does it serve the done-when item(s) "+
		"it was created for, stay inside the goal's Limits, and fit the other cards? "+
		"Then call card_plan exactly once: approve, or send it back with a note saying what to change.", card.ID, card.Title)
}

// goalStatusText renders a goal view for the lead (and goal_status).
func goalStatusText(v GoalView) string {
	var b strings.Builder
	g := v.Goal
	fmt.Fprintf(&b, "Goal %s: %s — stage %s\n", g.ID, g.Title, g.Stage)
	l := v.Ledger
	fmt.Fprintf(&b, "Budget %d · goal's own spend %.0f · held by cards %.0f · reserve %d · left to give %.0f\n",
		l.Envelope, l.Own, l.Given, l.Reserve, max(0, l.Available))
	if g.Goal.WrappingUp() {
		b.WriteString("The goal is wrapping up.\n")
	}
	b.WriteString("\nDone when:\n")
	for _, d := range v.DoneWhen {
		how := "judged"
		if d.Check != "" {
			how = "check: " + d.Check
		}
		fmt.Fprintf(&b, "- %s: %s (%s)\n", d.ID, d.Says, how)
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
		leadTool("card_create", "Create a card inside the goal. It must serve at least one done-when item; its envelope comes out of what the goal has left to give.",
			map[string]any{
				"kind":       str("feature (default), bug or research"),
				"title":      str("Short title."),
				"one_liner":  str("What the card must do, in a sentence or two."),
				"serves":     strs("Done-when ids this card is for."),
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
		leadTool("done_when_add", "Add a done-when item one of the owner's notes asked for.", map[string]any{
			"says": str("The statement."), "check": str("A command that exits 0 when it holds; empty to have verify judge it."),
			"note": str("The note that asked for it, quoted."),
		}, "says", "note"),
		leadTool("backlog_file", "File a real bug or idea outside this goal as a card on the open board. The goal never works it.", map[string]any{"kind": str("bug or feature"), "description": str("First line is the title.")}, "description"),
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

	case "card_create":
		if goal.Goal.WrappingUp() {
			return "", errors.New("the goal is wrapping up; nothing new starts")
		}
		kind := domain.Kind(strings.TrimSpace(a.Kind))
		if kind == "" {
			kind = domain.KindFeature
		}
		row := domain.GoalCardRow{Title: strings.TrimSpace(a.Title), OneLiner: a.OneLiner, Kind: kind, Serves: a.Serves, Envelope: a.Envelope}
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
		f, err := cardmint.Mint(ctx, e.cfg.Store, e.cfg.Workspace, cardmint.Input{
			Kind: kind, Description: goalCardDescription(goal, row, itemText), Profile: goal.Profile,
			Envelope: env, Repo: goal.Repo, GateApproval: domain.GateAutopilot, Goal: goal.ID,
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
			e.goalLog(ctx, goal.ID, state.GoalPayload{Action: state.GoalDecision, Card: lt.question.card, Detail: a.Decision, Alternative: a.Alternative, By: "lead"})
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
		f, err := cardmint.Mint(ctx, e.cfg.Store, e.cfg.Workspace, cardmint.Input{
			Kind: kind, Description: a.Description, Profile: goal.Profile, Repo: goal.Repo, FoundBy: goal.ID,
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
	it := domain.DoneWhen{ID: fmt.Sprintf("DW-%d", len(items)+1), Says: strings.TrimSpace(a.Says), Check: strings.TrimSpace(a.Check)}
	it.Judge = it.Check == ""
	if err := it.Validate(); err != nil {
		return "", err
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

// --- catch-up resolver ------------------------------------------------------

// resolveGoalCatchUp resolves an open catch-up merge on a goal branch: an
// implementer session in the goal worktree, told to resolve the conflicted
// files and nothing else. gummi — not the session — concludes the merge,
// and only when no path is left unmerged.
func (e *Engine) resolveGoalCatchUp(ctx context.Context, goal domain.Feature, files []string) error {
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
	workDir := filepath.Join(e.pool.Root(), goal.WorktreePath())
	tctx, cancel := context.WithTimeout(ctx, leadTurnTimeout)
	defer cancel()
	sess, err := ag.NewSession(tctx, agent.SessionOpts{
		WorkDir: workDir, Role: agent.RoleImplementer, Model: rc.Model, Provider: rc.Provider, Think: rc.Think,
		Permission: e.cfg.Permission, MaxCredits: min(view.Ledger.OwnBudget(), 200) * capHeadroom,
		SystemHints: []string{"You are resolving a merge of main into a goal branch. The merge is already in progress in your working directory. " +
			"Resolve the conflict markers in the conflicted files so both sides' intent survives, `git add` each resolved file, and stop. " +
			"Do not commit, do not abort the merge, and do not change anything else."},
	})
	if err != nil {
		return err
	}
	defer func() { _ = sess.Close() }()
	if err := sess.Send(tctx, "Resolve the conflicts in: "+strings.Join(files, ", ")); err != nil {
		return err
	}
drain:
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				break drain
			}
			switch ev.Kind {
			case agent.EventUsage:
				e.recordLeadUsage(goal.ID, ev.Usage)
			case agent.EventIdle, agent.EventBudgetExhausted:
				break drain
			case agent.EventError:
				return ev.Err
			}
		case <-tctx.Done():
			return tctx.Err()
		}
	}
	main, err := e.mgr(ctx, &goal)
	if err != nil {
		return err
	}
	return main.ConcludeGoalMerge(ctx, &goal)
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
