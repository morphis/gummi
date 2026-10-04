package web

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/threadfold"
	"github.com/morphis/gummi/internal/webapi"
)

// planCard mints a feature through the form and walks it to its design
// stage with the section it owes drafted, the way a finished architect
// leaves it.
func (h *cardBoard) planCard(title string) webapi.Card {
	h.t.Helper()
	c := h.create(webapi.CreateCardRequest{Kind: "feature", Title: title, Description: "make it so"})
	// drafted before the crossing into plan, so the board row the decision
	// is built from has read the drafted design rather than a blank one
	h.draft(c.ID, "Chosen approach", "Implementation notes")
	c = h.action(c.ID, "advance", webapi.ActionRequest{})
	if c.Stage != string(domain.StagePlan) {
		h.t.Fatalf("advance from todo left %s at %s", c.ID, c.Stage)
	}
	return h.card(c.ID)
}

// answer posts an answer and returns the status and the raw body.
func (h *cardBoard) answer(id string, req webapi.AnswerRequest) (int, json.RawMessage) {
	h.t.Helper()
	var raw json.RawMessage
	st := h.call(http.MethodPost, "/api/cards/"+id+"/answer", req, &raw)
	return st, raw
}

// Approving the design gate from the page is the TUI's approve: the card
// crosses into implement, recorded as the person who answered — a person,
// by every rule that asks. A second answer to the same decision is told
// it was answered, and by whom.
func TestAnswerApprovesTheDesignGate(t *testing.T) {
	h := newCardBoard(t, agent.NewFake("ok"))
	c := h.planCard("Dark mode")
	approve := option(t, c.Decision, "advance")
	if approve.Label != "approve" || approve.Words {
		t.Fatalf("approve option = %+v", approve)
	}
	req := webapi.AnswerRequest{Ref: c.Decision.Ref, Option: "advance", Against: c.Decision.Against.Token}
	st, raw := h.answer(c.ID, req)
	if st != http.StatusOK {
		t.Fatalf("answer: %d %s", st, raw)
	}
	var after webapi.Card
	if err := json.Unmarshal(raw, &after); err != nil {
		t.Fatal(err)
	}
	if after.Stage != string(domain.StageImplement) {
		t.Fatalf("after approving, %s is at %s", c.ID, after.Stage)
	}

	// the crossing is Simon's, and Simon is a person
	evs, err := h.store.Events(context.Background(), domain.FeatureID(c.ID))
	if err != nil {
		t.Fatal(err)
	}
	var gate state.GatePayload
	for _, ev := range evs {
		if ev.Kind == state.EventGate {
			_ = json.Unmarshal([]byte(ev.Payload), &gate)
		}
	}
	if gate.Actor != state.PersonActor("Simon") || !threadfold.HumanGateActor(gate.Actor) {
		t.Errorf("gate crossed by %q, want Simon as a person", gate.Actor)
	}

	// the same answer again: the decision is gone, and who took it is said
	st, raw = h.answer(c.ID, req)
	if st != http.StatusConflict {
		t.Fatalf("second answer: %d %s, want 409", st, raw)
	}
	e := errorOf(t, raw)
	if e.Error != webapi.ConflictAnswered || e.By != "Simon" || e.Receipt == "" {
		t.Errorf("second answer = %+v, want answered by Simon with a receipt", e)
	}
}

// An answer given against a revision the card has left is refused as
// moved, and nothing happens.
func TestAnswerAgainstAMovedCardIsRefused(t *testing.T) {
	h := newCardBoard(t, agent.NewFake("ok"))
	c := h.create(webapi.CreateCardRequest{Kind: "feature", Title: "Dark mode"})
	h.draft(c.ID, "Chosen approach", "Implementation notes")
	c = h.action(c.ID, "advance", webapi.ActionRequest{})
	// the page read the stop; then the architect wrote into the spec again,
	// and the revision the answers were given against moved
	stale := c.Decision
	h.draft(c.ID, "Problem")
	now := h.card(c.ID)
	if now.Decision == nil || now.Decision.Ref != stale.Ref || now.Decision.Against.Token == stale.Against.Token {
		t.Fatalf("the fixture did not move the stop: before %+v, after %+v", stale, now.Decision)
	}
	st, raw := h.answer(c.ID, webapi.AnswerRequest{Ref: stale.Ref, Option: "advance", Against: stale.Against.Token})
	if e := errorOf(t, raw); st != http.StatusConflict || e.Error != webapi.ConflictMoved {
		t.Fatalf("stale answer: %d %s, want 409 moved", st, raw)
	}
	if got := h.card(c.ID).Stage; got != string(domain.StagePlan) {
		t.Errorf("a refused answer moved the card to %s", got)
	}
	// the page refetches and answers what is there now
	st, raw = h.answer(c.ID, webapi.AnswerRequest{Ref: now.Decision.Ref, Option: "advance", Against: now.Decision.Against.Token})
	if st != http.StatusOK {
		t.Fatalf("fresh answer: %d %s", st, raw)
	}
}

// Two people answering the same decision at once: one answer goes
// through, and the other is told the decision was answered, and by whom —
// never that the card "moved" because it read the first answer halfway.
func TestTwoPeopleAnswerAtOnce(t *testing.T) {
	for range 5 {
		h := newCardBoard(t, agent.NewFake("ok"))
		c := h.planCard("Dark mode")
		yuki := h.client()
		h.pair(yuki, "Yuki")
		req := webapi.AnswerRequest{Ref: c.Decision.Ref, Option: "advance", Against: c.Decision.Against.Token}
		type result struct {
			who string
			st  int
			raw json.RawMessage
		}
		out := make(chan result, 2)
		for who, cl := range map[string]*http.Client{"Simon": h.c, "Yuki": yuki} {
			go func() {
				var raw json.RawMessage
				st := h.callAs(cl, http.MethodPost, "/api/cards/"+c.ID+"/answer", req, &raw)
				out <- result{who, st, raw}
			}()
		}
		a, b := <-out, <-out
		if a.st != http.StatusOK {
			a, b = b, a
		}
		if a.st != http.StatusOK || b.st != http.StatusConflict {
			t.Fatalf("answers = %d %s / %d %s, want one 200 and one 409", a.st, a.raw, b.st, b.raw)
		}
		if e := errorOf(t, b.raw); e.Error != webapi.ConflictAnswered || e.By != a.who {
			t.Fatalf("%s was told %+v, want answered by %s", b.who, e, a.who)
		}
	}
}
