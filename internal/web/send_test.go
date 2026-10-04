package web

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/webapi"
)

// liveAgent answers every turn and never goes idle: a conversation that
// stays open between turns, which is what a composer steers.
func liveAgent() *agent.Fake {
	return &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		if opts.Role == agent.RoleScribe {
			return []agent.Event{{Kind: agent.EventIdle}}
		}
		return []agent.Event{{Kind: agent.EventMessage, Text: "heard: " + msg}}
	}}
}

func (h *cardBoard) composer(id, text string) webapi.Composer {
	h.t.Helper()
	var c webapi.Composer
	if st := h.call(http.MethodPost, "/api/cards/"+id+"/composer", webapi.SendRequest{Text: text}, &c); st != http.StatusOK {
		h.t.Fatalf("composer %q: %d", text, st)
	}
	return c
}

// send sends a composer line as the page does: against the decision the
// card pins, if it pins one.
func (h *cardBoard) send(id, text string) (int, json.RawMessage) {
	h.t.Helper()
	req := webapi.SendRequest{Text: text}
	if d := h.card(id).Decision; d != nil {
		req.Against = d.Against.Token
	}
	var raw json.RawMessage
	st := h.call(http.MethodPost, "/api/cards/"+id+"/send", req, &raw)
	return st, raw
}

// The composer says where a line goes before it is sent, by the rules
// enter sends it with: prose steers the live architect, a verb the card
// offers only from its menu opens the menu, and a line the agent refused
// because it was mid-turn comes back to be sent again.
func TestSendRoutesALine(t *testing.T) {
	ag := liveAgent()
	h := newCardBoard(t, ag)
	c := h.planCard("Dark mode")

	// nobody is attached: prose at the stop is read first, and would go
	// with the answer that takes words
	if got := h.composer(c.ID, ""); got.Route != webapi.RouteAnswer || !strings.Contains(got.Says, "start the architect") {
		t.Errorf("empty composer at the stop = %+v", got)
	}
	// a verb the card has, but not among its answers, is the menu's
	if got := h.composer(c.ID, "/rebase"); got.Route != webapi.RouteMenu {
		t.Errorf("/rebase composer = %+v, want menu", got)
	}
	st, raw := h.send(c.ID, "/rebase")
	var res webapi.SendResponse
	if st != http.StatusOK || json.Unmarshal(raw, &res) != nil || res.Route != webapi.RouteMenu {
		t.Fatalf("send /rebase = %d %s, want route menu", st, raw)
	}

	// start the architect; the conversation stays open between turns
	st, raw = h.answer(c.ID, webapi.AnswerRequest{Ref: c.Decision.Ref, Option: "run", Against: c.Decision.Against.Token})
	if st != http.StatusOK {
		t.Fatalf("start the architect: %d %s", st, raw)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if s := h.eng.Get(domain.FeatureID(c.ID)); s != nil && s.Live() && len(s.Snapshot().Transcript) > 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the architect never attached")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := h.composer(c.ID, "use the system theme"); got.Route != webapi.RouteSteer {
		t.Errorf("composer with the architect live = %+v, want steer", got)
	}

	// mid-turn: the line is handed back
	ag.SendErr = agent.ErrBusy
	st, raw = h.send(c.ID, "use the system theme")
	if e := errorOf(t, raw); st != http.StatusConflict || e.Error != webapi.ConflictBusy || e.Text != "use the system theme" {
		t.Fatalf("send while busy = %d %s, want 409 busy with the line", st, raw)
	}
	ag.SendErr = nil
	st, raw = h.send(c.ID, "use the system theme")
	if st != http.StatusOK || json.Unmarshal(raw, &res) != nil || res.Route != webapi.RouteSteer {
		t.Fatalf("send = %d %s, want route steer", st, raw)
	}
	deadline = time.Now().Add(10 * time.Second)
	for !transcriptHas(h, c.ID, "heard: use the system theme") {
		if time.Now().After(deadline) {
			t.Fatal("the steered line never reached the architect")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func transcriptHas(h *cardBoard, id, text string) bool {
	s := h.eng.Get(domain.FeatureID(id))
	if s == nil {
		return false
	}
	for _, m := range s.Snapshot().Transcript {
		if strings.Contains(m.Content, text) {
			return true
		}
	}
	return false
}

// The composer offers the card's own slash vocabulary beside a project's
// command files: while the word is still being typed, the actions the
// card's menu offers come back as "/word" rows with what they do.
func TestComposerOffersTheCardsSlashWords(t *testing.T) {
	h := newCardBoard(t, agent.NewFake("ok"))
	c := h.planCard("Dark mode")

	comp := h.composer(c.ID, "/")
	if len(comp.Completions) == 0 {
		t.Fatal("a bare / offers nothing")
	}
	var words []string
	for _, o := range comp.Completions {
		words = append(words, o.Text)
		if o.Detail == "" {
			t.Errorf("%s carries no description", o.Text)
		}
		if !strings.HasPrefix(o.Text, "/") || !strings.HasSuffix(o.Text, " ") {
			t.Errorf("completion %q is not a /word row", o.Text)
		}
	}
	if !slices.Contains(words, "/run ") || !slices.Contains(words, "/rebase ") {
		t.Errorf("the vocabulary offers %v, want what the card's menu offers", words)
	}
	if got := h.composer(c.ID, "/reb").Completions; len(got) == 0 || got[0].Text != "/rebase " {
		t.Errorf("/reb offers %+v, want /rebase", got)
	}
	if got := h.composer(c.ID, "/zzz").Completions; len(got) != 0 {
		t.Errorf("/zzz offers %+v, want none", got)
	}
	// a completed word is a line for the server to route, not a picker
	if got := h.composer(c.ID, "/verify ").Completions; len(got) != 0 {
		t.Errorf("/verify␠ offers %+v, want none", got)
	}
}

// A verb the card answers runs: /approve at the design gate crosses into
// implement — the move the g key makes, from the composer.
func TestASendVerbActs(t *testing.T) {
	h := newCardBoard(t, agent.NewFake("ok"))
	c := h.planCard("Dark mode")
	st, raw := h.send(c.ID, "/approve")
	var res webapi.SendResponse
	if st != http.StatusOK || json.Unmarshal(raw, &res) != nil || res.Route != webapi.RouteVerb {
		t.Fatalf("send /approve = %d %s, want route verb", st, raw)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		if got := h.card(c.ID).Stage; got == string(domain.StageImplement) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("/approve left the card at %s", h.card(c.ID).Stage)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
