package engine

// A freeform card's delegation (domain.Delegation, DESIGN §19.10): the
// person lets the card's session create workflow cards under a budget of
// its own. Off by default — a card with no delegation budget is offered
// none of these tools and told nothing about them, so a session cannot
// drift into filing a card for every change. On, each card_create is put
// to the person first ("this one", "this one and all that follow", "no")
// and the budget caps the session whatever they answer.
//
// The cards are a goal's cards without the goal: they fork from the
// freeform card's branch and land back on it as one squash each
// (worktree.Pool.ManagerFor routes on ParentID), and what each holds of
// the budget is the goal ledger's rule (domain.DelegateHeld). They run on
// autopilot through the whole graph — critique and verify included — and
// land only when the session calls card_land: the freeform worktree is
// the agent's live checkout, and a squash onto it while the agent has
// uncommitted edits there would be refused anyway.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/cardmint"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/worktree"
)

const (
	cardCreateToolName = "card_create"
	cardListToolName   = "card_list"
	cardLandToolName   = "card_land"

	// ActorDelegate is the actor a freeform session's card moves are
	// recorded under.
	ActorDelegate = "session"

	// The three answers card_create's question offers. The picker always
	// adds a free-text answer; anything that is not one of the first two
	// reads as no.
	delegateYes  = "Create this one"
	delegateAll  = "Create this one and all that follow"
	delegateDeny = "Don't create it"
)

func delegateTools() []agent.ToolDef {
	return []agent.ToolDef{
		{
			Name: cardCreateToolName,
			Description: "Create a workflow card (feature or bug) whose branch forks from this card's " +
				"branch and lands back on it. Its envelope comes out of this card's delegation budget. " +
				"The person is asked first unless they have said yes to all; the call returns once they answer.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"kind":        map[string]any{"type": "string", "description": "\"FD\" (feature) or \"BG\" (bug)."},
					"description": map[string]any{"type": "string", "description": "What the card is to do — its first line becomes its title."},
					"envelope":    map[string]any{"type": "integer", "description": "Credits to give it; omit for half of what is left."},
				},
				"required": []any{"kind", "description"},
			},
		},
		{
			Name:        cardListToolName,
			Description: "List the cards this session created: stage, spend, envelope, and which are ready to land; and what the delegation budget has left.",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			Name: cardLandToolName,
			Description: "Land a verified card this session created onto this card's branch as one squash commit. " +
				"Commit or clean your own changes first: the landing refuses a worktree with tracked changes. " +
				"A conflict comes back to you to resolve on the card's branch or here.",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"card": map[string]any{"type": "string", "description": "The card's id, e.g. FD-012."}},
				"required":   []any{"card"},
			},
		},
	}
}

// freeformDelegateHint is added to a freeform session offered the card
// tools. It is a backstop, not the guard: the person's yes is the guard.
const freeformDelegateHint = `The person has given this card a delegation budget: card_create, card_list and
card_land let you hand a piece of work to a workflow card (plan → implement →
verify, with its own critique) whose branch forks from this one and lands back
on it. Create a card only when the person asks for one — never to split up
work you can simply do here. Each card_create is put to the person before the
card exists. A created card runs on its own; check on it with card_list, and
when it reads "ready to land", commit your own work and call card_land.`

func (e *Engine) handleDelegateTool(s *Session, tc *agent.ToolCall) {
	ctx := context.Background()
	if e.Freeform(s.Feature.ID) == nil || s.Feature.Stage != domain.StageOpen {
		e.resolveNow(s, tc.ID, tc.Name+" is only available in a freeform session")
		return
	}
	parent, err := e.cfg.Store.GetFeature(ctx, s.Feature.ID)
	if err != nil {
		e.resolveNow(s, tc.ID, err.Error())
		return
	}
	if !parent.Delegate.Enabled() {
		e.resolveNow(s, tc.ID, "this card has no delegation budget any more — do the work here instead")
		return
	}
	var a struct {
		Kind        string `json:"kind"`
		Description string `json:"description"`
		Envelope    int    `json:"envelope"`
		Card        string `json:"card"`
	}
	_ = json.Unmarshal(tc.Args, &a)
	switch tc.Name {
	case cardListToolName:
		out, err := e.delegateStatus(ctx, parent)
		if err != nil {
			out = err.Error()
		}
		e.resolveNow(s, tc.ID, out)
	case cardLandToolName:
		out, err := e.DelegateLand(ctx, parent.ID, domain.FeatureID(strings.ToUpper(strings.TrimSpace(a.Card))))
		if err != nil {
			out = err.Error()
		}
		e.resolveNow(s, tc.ID, out)
	case cardCreateToolName:
		req, err := e.delegateRequest(ctx, parent, a.Kind, a.Description, a.Envelope)
		if err != nil {
			e.resolveNow(s, tc.ID, err.Error())
			return
		}
		if parent.Delegate.ConfirmAll {
			e.resolveNow(s, tc.ID, e.delegateMint(ctx, parent.ID, req))
			return
		}
		e.installAsk(s, tc.ID, &Ask{
			CallID: tc.ID,
			Question: fmt.Sprintf("The session wants to create a %s card with %d credits (%.0f of the delegation left): %s",
				req.kind, req.envelope, req.avail, req.title()),
			Options: []AskOption{
				{Label: delegateYes, Detail: "Ask again for the next one."},
				{Label: delegateAll, Detail: "Stop asking for this card; the delegation budget still caps it."},
				{Label: delegateDeny, Detail: "The session is told no and carries on without it."},
			},
			onAnswer: func(answer string) string {
				switch {
				case strings.EqualFold(answer, delegateAll):
					cur, err := e.cfg.Store.GetFeature(ctx, parent.ID)
					if err == nil {
						cur.Delegate.ConfirmAll = true
						err = e.cfg.Store.SetDelegation(ctx, parent.ID, cur.Delegate)
					}
					if err != nil {
						return "not created: " + err.Error()
					}
					return e.delegateMint(ctx, parent.ID, req)
				case strings.EqualFold(answer, delegateYes):
					return e.delegateMint(ctx, parent.ID, req)
				}
				return "The person did not create the card. Their answer: " + answer
			},
		})
	}
}

type delegateReq struct {
	kind        domain.Kind
	description string
	envelope    int
	avail       float64
}

func (r delegateReq) title() string {
	first, _, _ := strings.Cut(strings.TrimSpace(r.description), "\n")
	return first
}

// delegateRequest checks a card_create against the delegation as it
// stands: a kind that can land on a branch, and an envelope the budget
// can still give.
func (e *Engine) delegateRequest(ctx context.Context, parent domain.Feature, kind, description string, envelope int) (delegateReq, error) {
	var r delegateReq
	switch strings.ToUpper(strings.TrimSpace(kind)) {
	case "FD", "FEATURE", strings.ToUpper(string(domain.KindFeature)):
		r.kind = domain.KindFeature
	case "BG", "BUG", strings.ToUpper(string(domain.KindBug)):
		r.kind = domain.KindBug
	default:
		return r, fmt.Errorf("kind %q: a delegated card is a feature (FD) or a bug (BG) — research has no branch to land", kind)
	}
	r.description = strings.TrimSpace(description)
	if r.description == "" {
		return r, errors.New("card_create needs a description")
	}
	cards, err := e.cfg.Store.DelegatedCards(ctx, parent.ID)
	if err != nil {
		return r, err
	}
	r.avail = domain.DelegateAvailable(parent, cards)
	r.envelope = envelope
	if r.envelope <= 0 {
		r.envelope = int(r.avail / 2)
	}
	r.envelope = max(r.envelope, domain.MinEnvelope)
	if float64(r.envelope) > r.avail {
		return r, fmt.Errorf("a %d-credit card needs more than the %.0f credits the delegation has left — ask the person to raise it, or do the work here",
			r.envelope, max(0, r.avail))
	}
	return r, nil
}

// delegateMint creates the card once the person has said yes, checking
// the budget again — it may have moved while the question waited — and
// returns what the session is told.
func (e *Engine) delegateMint(ctx context.Context, parentID domain.FeatureID, r delegateReq) string {
	parent, err := e.cfg.Store.GetFeature(ctx, parentID)
	if err != nil {
		return "not created: " + err.Error()
	}
	if !parent.Delegate.Enabled() {
		return "not created: the person withdrew this card's delegation budget"
	}
	if r, err = e.delegateRequest(ctx, parent, string(r.kind), r.description, r.envelope); err != nil {
		return "not created: " + err.Error()
	}
	f, err := cardmint.Mint(ctx, e.cfg.Store, e.cfg.Workspace, cardmint.Input{
		Kind: r.kind, Description: r.description, Profile: parent.Profile, Envelope: r.envelope,
		Repo: parent.Repo, RequireRepo: e.RequireRepo, GateApproval: domain.GateAutopilot,
		Parent: parent.ID, Unattended: true,
	})
	if err != nil {
		return "not created: " + err.Error()
	}
	e.send(Event{Feature: f.ID, Kind: EventDelegateCreated})
	return fmt.Sprintf("created %s (%q) with %d credits; it runs on autopilot from now — check it with card_list", f.ID, f.Title, r.envelope)
}

// delegateStatus is card_list's answer.
func (e *Engine) delegateStatus(ctx context.Context, parent domain.Feature) (string, error) {
	cards, err := e.cfg.Store.DelegatedCards(ctx, parent.ID)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "delegation: %d credits, %.0f left to give", parent.Delegate.Budget, domain.DelegateAvailable(parent, cards))
	if parent.Delegate.ConfirmAll {
		b.WriteString(" (the person is not asked per card)")
	}
	b.WriteString("\n")
	if len(cards) == 0 {
		b.WriteString("no cards yet\n")
	}
	for _, c := range cards {
		fmt.Fprintf(&b, "%s %s — stage %s, spent %.0f of %d", c.ID, c.Title, c.Stage, c.Spend.Credits, c.Budget.Envelope)
		switch {
		case c.Stage == domain.StageDone:
			b.WriteString(", landed")
		case c.MayLand() == nil:
			b.WriteString(", ready to land")
		case e.Get(c.ID) != nil:
			b.WriteString(", running")
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

// DelegateLand lands a verified card a freeform session created onto the
// freeform card's branch as one squash commit and closes it. The verify
// floor is the card's own: what lands is the tip its verify passed on.
func (e *Engine) DelegateLand(ctx context.Context, parentID, id domain.FeatureID) (string, error) {
	card, err := e.cfg.Store.GetFeature(ctx, id)
	if err != nil {
		return "", err
	}
	if card.ParentID != parentID {
		return "", fmt.Errorf("%s was not created by %s", id, parentID)
	}
	if card.Stage == domain.StageDone {
		return "", fmt.Errorf("%s has already landed", id)
	}
	if err := card.MayLand(); err != nil {
		return "", fmt.Errorf("%s is not ready to land: %w", id, err)
	}
	m, err := e.mgr(ctx, &card)
	if err != nil {
		return "", err
	}
	if head, herr := m.Head(ctx, &card); herr == nil {
		if err := card.MayLandAt(head); err != nil {
			return "", fmt.Errorf("%s is not ready to land: %w", id, err)
		}
	}
	adv, err := e.Advance(ctx, card.ID, ActorDelegate)
	if err != nil {
		return "", err
	}
	switch adv.Status {
	case StatusNeedsMerge:
	case StatusAdvanced:
		return fmt.Sprintf("%s had nothing of its own to land; it is closed", id), nil
	default:
		return "", fmt.Errorf("%s could not cross its landing gate: %s", id, describeAdvance(adv))
	}
	msg := e.goalCardMessage(ctx, card)
	cur, _ := e.cfg.Store.GetFeature(ctx, card.ID)
	sha, err := m.SquashMerge(ctx, &cur, msg)
	if err != nil {
		var mc *worktree.MergeConflictError
		if errors.As(err, &mc) {
			return "", fmt.Errorf("%s conflicts with this branch in %s; nothing landed — merge this branch into %s's branch and resolve them there, or change your side",
				id, strings.Join(mc.Files, ", "), id)
		}
		return "", err
	}
	if _, err := e.cfg.Store.Transition(ctx, card.ID, domain.StageDone, ActorDelegate); err != nil {
		return "", err
	}
	e.Drop(card.ID)
	e.send(Event{Feature: card.ID, Kind: EventCardCreated})
	return fmt.Sprintf("landed %s on this branch as %s", id, shortSHA(sha)), nil
}

// SetDelegation sets or withdraws a freeform card's delegation. The card
// tools are fixed when its backend starts, so an idle backend is stopped
// here — the next turn respawns it, continuing the same conversation, with
// the tools the new delegation offers. A backend mid-turn keeps the tools
// it has until it next starts; withdrawing still takes effect at once,
// because every card tool re-reads the delegation before it acts.
func (e *Engine) SetDelegation(ctx context.Context, id domain.FeatureID, d domain.Delegation) error {
	cur, err := e.cfg.Store.GetFeature(ctx, id)
	if err != nil {
		return err
	}
	if !cur.IsFreeform() {
		return fmt.Errorf("%s is not a freeform card: only a session delegates", id)
	}
	if err := e.cfg.Store.SetDelegation(ctx, id, d); err != nil {
		return err
	}
	if cur.Delegate.Enabled() != d.Enabled() {
		if ff := e.Freeform(id); ff != nil {
			ff.mu.Lock()
			sess := ff.sess
			ff.mu.Unlock()
			if sess != nil && sess.Live() && !sess.Busy() {
				ff.onIdleTimeout()
			}
		}
	}
	e.send(Event{Feature: id, Stage: domain.StageOpen, Kind: EventUpdated})
	return nil
}
