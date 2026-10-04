package web

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/webapi"
)

// workingAgent runs a stage and keeps working: a turn that never ends,
// which is what a pause stops.
func workingAgent() *agent.Fake {
	return &agent.Fake{Caps: agent.Capabilities{Interrupt: true}, Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		if opts.Role == agent.RoleScribe {
			return []agent.Event{{Kind: agent.EventIdle}}
		}
		return []agent.Event{{Kind: agent.EventMessage, Text: "working on it"}}
	}}
}

func (h *cardBoard) actionRaw(id, action string, req webapi.ActionRequest) (int, json.RawMessage) {
	h.t.Helper()
	var raw json.RawMessage
	st := h.call(http.MethodPost, "/api/cards/"+id+"/actions/"+action, req, &raw)
	return st, raw
}

// Pausing a running stage is the menu's pause: the run stops and the
// card reads paused.
func TestActionPausesARun(t *testing.T) {
	h := newCardBoard(t, workingAgent())
	c := h.planCard("Dark mode")
	st, raw := h.answer(c.ID, webapi.AnswerRequest{Ref: c.Decision.Ref, Option: "advance", Against: c.Decision.Against.Token})
	if st != http.StatusOK {
		t.Fatalf("approve: %d %s", st, raw)
	}
	c = h.card(c.ID)
	st, raw = h.answer(c.ID, webapi.AnswerRequest{Ref: c.Decision.Ref, Option: "run", Against: c.Decision.Against.Token})
	if st != http.StatusOK {
		t.Fatalf("run implement: %d %s", st, raw)
	}
	c = h.waitCard(c.ID, "the implement run", func(c webapi.Card) bool {
		return c.Status == webapi.StatusRunning && hasAction(c.Actions, "pause")
	})
	c = h.action(c.ID, "pause", webapi.ActionRequest{})
	c = h.waitCard(c.ID, "the pause", func(c webapi.Card) bool { return c.Status == webapi.StatusPaused })
	if c.Running != nil {
		t.Errorf("a paused card still reads running: %+v", c.Running)
	}
	// the card's history says who took it back
	evs, err := h.store.Events(context.Background(), domain.FeatureID(c.ID))
	if err != nil {
		t.Fatal(err)
	}
	var by []string
	for _, ev := range evs {
		var p state.AutopilotPayload
		if ev.Kind == state.EventAutopilot && json.Unmarshal([]byte(ev.Payload), &p) == nil && p.Event == state.AutopilotHandedBack {
			by = append(by, p.By)
		}
	}
	if len(by) == 0 || by[len(by)-1] != state.PersonActor("Simon") {
		t.Errorf("the pause's handback is recorded by %q, want Simon", by)
	}
}

// The budget is the budget dialog's number; delete asks first and then
// removes the card.
func TestActionBudgetAndDelete(t *testing.T) {
	h := newCardBoard(t, agent.NewFake("ok"))
	c := h.create(webapi.CreateCardRequest{Kind: "feature", Title: "Dark mode"})
	n := 750
	if c = h.action(c.ID, "envelope", webapi.ActionRequest{Number: &n}); c.Envelope != 750 {
		t.Errorf("envelope after setting it = %d", c.Envelope)
	}
	if st, raw := h.actionRaw(c.ID, "envelope", webapi.ActionRequest{}); st != webapi.StatusQuestion || errorOf(t, raw).Needs != webapi.ActionNeedsNumber {
		t.Errorf("a budget with no number = %d %s, want 202 needs number", st, raw)
	}

	st, raw := h.actionRaw(c.ID, "delete", webapi.ActionRequest{})
	e := errorOf(t, raw)
	if st != webapi.StatusQuestion || e.Error != "confirm" || e.Needs != webapi.ActionNeedsConfirm || e.Text == "" {
		t.Fatalf("delete unconfirmed = %d %s, want 202 confirm with the question", st, raw)
	}
	if h.card(c.ID).ID != c.ID {
		t.Fatal("an unconfirmed delete removed the card")
	}
	if e.Confirm == "" || !strings.HasPrefix(e.Text, "delete "+c.ID+"?\n") {
		t.Fatalf("the confirm %+v carries no token, or not the question whole", e)
	}
	// a bare yes is not a yes: the JSON boolean the page used to send is
	// refused outright, and a token for another question is asked again
	if st := h.call(http.MethodPost, "/api/cards/"+c.ID+"/actions/delete", map[string]any{"confirm": true}, nil); st != http.StatusBadRequest {
		t.Fatalf("delete with a bare confirm: true = %d, want 400", st)
	}
	if st, raw := h.actionRaw(c.ID, "delete", webapi.ActionRequest{Confirm: "c0000000000000000000000000"}); st != webapi.StatusQuestion || errorOf(t, raw).Error != "confirm" {
		t.Fatalf("delete with a token for another question = %d %s, want 202 confirm", st, raw)
	}
	if h.card(c.ID).ID != c.ID {
		t.Fatal("a delete confirmed for another question removed the card")
	}
	var ok webapi.OK
	if st := h.call(http.MethodPost, "/api/cards/"+c.ID+"/actions/delete", webapi.ActionRequest{Confirm: e.Confirm}, &ok); st != http.StatusOK || !ok.OK {
		t.Fatalf("delete = %d %+v", st, ok)
	}
	if st := h.call(http.MethodGet, "/api/cards/"+c.ID, nil, nil); st != http.StatusNotFound {
		t.Errorf("the deleted card still answers %d", st)
	}
	// an action the card does not offer is refused, not guessed at
	c = h.create(webapi.CreateCardRequest{Kind: "feature", Title: "Later"})
	if st, _ := h.actionRaw(c.ID, "clean", webapi.ActionRequest{Confirm: e.Confirm}); st != http.StatusConflict {
		t.Errorf("clean on an unlanded card = %d, want 409", st)
	}
}

// verifiedCard is a feature that passed verify, whose branch carries one
// commit of its own, and its worktree.
func (h *cardBoard) verifiedCard(title string) (webapi.Card, string) {
	h.t.Helper()
	return h.verifyCard(title, true)
}

// verifyCard is a feature at verify whose branch carries one commit of
// its own, and its worktree; passed says whether it passed verify, or
// never ran it.
func (h *cardBoard) verifyCard(title string, passed bool) (webapi.Card, string) {
	t := h.t
	t.Helper()
	ctx := context.Background()
	c := h.create(webapi.CreateCardRequest{Kind: "feature", Title: title})
	id := domain.FeatureID(c.ID)
	for _, to := range []domain.Stage{domain.StagePlan, domain.StageImplement, domain.StageVerify} {
		if _, err := h.store.Transition(ctx, id, to, "user"); err != nil {
			t.Fatal(err)
		}
	}
	f := h.feature(c.ID)
	if _, err := h.pool.Create(ctx, &f); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(h.root, f.WorktreePath())
	if err := os.WriteFile(filepath.Join(wt, "dark.go"), []byte("package dark\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, wt, "add", ".")
	gitIn(t, wt, "commit", "-q", "-m", "work")
	if passed {
		// verified on the tip that lands: the revision is what a landing checks
		if err := h.store.SetVerifiedAt(ctx, id, time.Now(), gitIn(t, wt, "rev-parse", "HEAD")); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.board.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	c = h.waitCard(c.ID, "the verified card", func(c webapi.Card) bool {
		// a card that never finished a verify pass lists no landing
		// (landingRefusal), so the worktree's own squash says it is up
		return c.Stage == string(domain.StageVerify) && hasAction(c.Actions, "squash") && hasAction(c.Actions, "merge") == passed
	})
	return c, wt
}

// Landing a verified card with a message is the landing dialog's: the
// branch is squash-merged onto main under that message and the card is
// done.
func TestActionLandsAVerifiedCard(t *testing.T) {
	h := newCardBoard(t, agent.NewFake("ok"))
	c, _ := h.verifiedCard("Dark mode")
	c = h.action(c.ID, "merge", webapi.ActionRequest{Message: "FD-001: dark mode\n\nThe board is dark at night."})
	c = h.waitCard(c.ID, "the landing", func(c webapi.Card) bool { return c.Stage == string(domain.StageDone) && c.Landed })
	if got := gitIn(t, h.root, "log", "-1", "--format=%s", "main"); got != "FD-001: dark mode" {
		t.Errorf("main's tip is %q", got)
	}
	if _, err := os.Stat(filepath.Join(h.root, "dark.go")); err != nil {
		t.Errorf("the landed work is not on main: %v", err)
	}
}

// The menu's landing asks for the floor the verify stop's own landing
// does: a card at verify that never finished a verify pass lists neither
// the land entry nor "next stage", and either sent anyway is refused and
// its branch stays off main. (Squash collapses the branch in place and
// lands nothing.)
func TestActionRefusesToLandAnUnverifiedCard(t *testing.T) {
	h := newCardBoard(t, agent.NewFake("ok"))
	c, _ := h.verifyCard("Dark mode", false)
	for _, action := range []string{"merge", "advance"} {
		if hasAction(c.Actions, action) {
			t.Errorf("the menu offers %s on a card that never ran verify", action)
		}
		st, raw := h.actionRaw(c.ID, action, webapi.ActionRequest{Message: "FD-001: dark mode", Against: h.card(c.ID).Decision.Against.Token})
		if st != http.StatusConflict {
			t.Errorf("%s on a card that never ran verify = %d %s, want a refusal", action, st, raw)
		}
	}
	time.Sleep(200 * time.Millisecond)
	if after := h.card(c.ID); after.Landed || after.Stage != string(domain.StageVerify) {
		t.Errorf("the unverified card landed: stage %s landed %v", after.Stage, after.Landed)
	}
	if _, err := os.Stat(filepath.Join(h.root, "dark.go")); err == nil {
		t.Error("the unverified work is on main")
	}
}

// The message a landing stops to have read says what sending it back
// does: land the branch on its base, or — a squash in place — collapse it
// to one commit and land nothing.
func TestTheMessageQuestionSaysWhatHappens(t *testing.T) {
	h := newCardBoard(t, agent.NewFake("ok"))
	c, _ := h.verifiedCard("Dark mode")
	for action, want := range map[string]string{
		"squash": "squash feat/dark-mode to one commit in place — nothing lands on main",
		"merge":  "land feat/dark-mode on main",
	} {
		st, raw := h.actionRaw(c.ID, action, webapi.ActionRequest{Against: h.card(c.ID).Decision.Against.Token})
		e := errorOf(t, raw)
		if st != webapi.StatusQuestion || e.Needs != webapi.ActionNeedsMessage || !strings.Contains(e.Text, want) {
			t.Errorf("%s with no message = %d %+v, want its question to say %q", action, st, e, want)
		}
		if action == "squash" && strings.Contains(e.Text, "landing") {
			t.Errorf("a squash in place asks about a landing: %q", e.Text)
		}
	}
}

// The landing entries offer the message the verify gate drafted as their
// default — the one the landing dialog would open on — so a person reads
// it before landing; a draft the branch has moved past is not offered.
func TestActionLandOffersTheDraftedMessage(t *testing.T) {
	h := newCardBoard(t, agent.NewFake("ok"))
	ctx := context.Background()
	c, wt := h.verifiedCard("Dark mode")
	for _, a := range c.Actions {
		if (a.ID == "merge" || a.ID == "squash") && a.Default != "" {
			t.Fatalf("%s offers %q with nothing drafted", a.ID, a.Default)
		}
	}
	const draft = "feat: dark mode\n\n- the board is dark at night"
	tip := gitIn(t, wt, "rev-parse", "HEAD")
	if err := h.store.SetCommitDraft(ctx, domain.FeatureID(c.ID), draft, tip); err != nil {
		t.Fatal(err)
	}
	c = h.card(c.ID)
	for _, id := range []string{"merge", "squash"} {
		a := actionOf(t, c.Actions, id)
		if a.Default != draft || a.Needs != webapi.ActionNeedsMessage {
			t.Errorf("%s = needs %q default %q, want the drafted message", id, a.Needs, a.Default)
		}
		if strings.Contains(a.Detail, "leave the message empty") {
			t.Errorf("%s offers a draft and still says to leave it empty: %q", id, a.Detail)
		}
	}
	// the branch moved on: the draft describes a tip that is gone
	if err := os.WriteFile(filepath.Join(wt, "more.go"), []byte("package dark\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, wt, "add", ".")
	gitIn(t, wt, "commit", "-q", "-m", "more")
	if d := actionOf(t, h.card(c.ID).Actions, "merge").Default; d != "" {
		t.Errorf("a stale draft is still offered: %q", d)
	}
}

func actionOf(t *testing.T, acts []webapi.Action, id string) webapi.Action {
	t.Helper()
	for _, a := range acts {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("no %s action in %+v", id, acts)
	return webapi.Action{}
}

// Dependencies are set as a whole, through the picker's own add and
// remove; an edge the picker refuses is refused whole.
func TestActionSetsDependencies(t *testing.T) {
	h := newCardBoard(t, agent.NewFake("ok"))
	a := h.create(webapi.CreateCardRequest{Kind: "feature", Title: "Row loader"})
	b := h.create(webapi.CreateCardRequest{Kind: "feature", Title: "Row cache"})
	deps := func(id string) []domain.FeatureID {
		t.Helper()
		got, err := h.store.ListDependencies(context.Background(), domain.FeatureID(id))
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	h.action(b.ID, "deps", webapi.ActionRequest{Cards: []string{a.ID}})
	if got := deps(b.ID); len(got) != 1 || string(got[0]) != a.ID {
		t.Fatalf("%s depends on %v", b.ID, got)
	}
	// the picker opens with what is set ticked
	if d := actionOf(t, h.card(b.ID).Actions, "deps").Default; d != a.ID {
		t.Errorf("deps default = %q, want %s", d, a.ID)
	}
	// the other way round would close a cycle
	st, raw := h.actionRaw(a.ID, "deps", webapi.ActionRequest{Cards: []string{b.ID}})
	if st != http.StatusConflict {
		t.Errorf("a cycle = %d %s, want 409", st, raw)
	}
	if got := deps(a.ID); len(got) != 0 {
		t.Errorf("a refused cycle left %s depending on %v", a.ID, got)
	}
	h.action(b.ID, "deps", webapi.ActionRequest{Cards: nil})
	if got := deps(b.ID); len(got) != 0 {
		t.Errorf("clearing left %s depending on %v", b.ID, got)
	}
}
