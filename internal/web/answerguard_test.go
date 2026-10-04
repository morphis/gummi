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
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/webapi"
)

// A card that never reached verify is not offered a landing on the web
// menu, and a merge asked for anyway is refused: nothing lands before the
// branch is verified (domain.Feature.MayLand).
func TestTheMenuNeverLandsAnUnverifiedCard(t *testing.T) {
	h := newCardBoard(t, agent.NewFake("ok"))
	ctx := context.Background()
	c := h.create(webapi.CreateCardRequest{Kind: "feature", Title: "Dark mode"})
	id := domain.FeatureID(c.ID)
	for _, to := range []domain.Stage{domain.StagePlan, domain.StageImplement} {
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
	if err := h.board.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	c = h.waitCard(c.ID, "the implement card", func(c webapi.Card) bool {
		return c.Stage == string(domain.StageImplement)
	})
	if hasAction(c.Actions, "merge") {
		t.Error("the menu offers merge on a card at implement")
	}
	if st, raw := h.actionRaw(c.ID, "merge", webapi.ActionRequest{Message: "FD-001: unverified"}); st == http.StatusOK {
		t.Errorf("merge at implement answered %d %s", st, raw)
	}
	if got := gitIn(t, h.root, "log", "-1", "--format=%s", "main"); got == "FD-001: unverified" {
		t.Errorf("an unverified card (stage implement, MayLand refuses) landed on main from the web menu")
	}
}

// The menu's "next stage" at verify is the landing, as the TUI's g there
// is: it stops to have the drafted message read, never answering 200 and
// doing nothing.
func TestTheMenusNextStageAtVerifyIsTheLanding(t *testing.T) {
	h := newCardBoard(t, agent.NewFake("ok"))
	ctx := context.Background()
	c, wt := h.verifiedCard("Dark mode")
	if err := h.store.SetVerifiedAt(ctx, domain.FeatureID(c.ID), time.Now(), gitIn(t, wt, "rev-parse", "HEAD")); err != nil {
		t.Fatal(err)
	}
	if err := h.board.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	for _, a := range h.card(c.ID).Actions {
		if a.ID == "advance" {
			t.Logf("menu advance: %q — %q", a.Label, a.Detail)
		}
	}
	st, raw := h.actionRaw(c.ID, "advance", webapi.ActionRequest{Against: h.card(c.ID).Decision.Against.Token})
	t.Logf("advance: %d %.300s", st, raw)
	time.Sleep(500 * time.Millisecond)
	after := h.card(c.ID)
	t.Logf("after: stage=%s landed=%v", after.Stage, after.Landed)
	if st == http.StatusOK && after.Stage == string(domain.StageVerify) && !after.Landed {
		t.Errorf("menu advance at verify answered 200 and nothing happened")
	}
}

// A gate crossing sent against a revision the card has moved past is
// refused whichever way it comes: the answer, the menu's advance, or the
// composer's /approve, each carrying the token the page showed.
func TestAStaleCrossingIsRefusedEveryWay(t *testing.T) {
	for _, via := range []string{"action", "send"} {
		t.Run(via, func(t *testing.T) {
			h := newCardBoard(t, agent.NewFake("ok"))
			c := h.planCard("Dark mode")
			stale := c.Decision
			// the spec moves under the page
			f := h.feature(c.ID)
			path := spec.LocateArtifact(
				filepath.Join(h.root, f.ArtifactPath()),
				filepath.Join(h.ws.DraftsDir(), spec.DraftFilename(&f)),
				filepath.Join(h.root, f.WorktreePath(), f.ArtifactPath()),
			)
			raw0, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, append(raw0, []byte("\nA late line the reader never saw.\n")...), 0o600); err != nil {
				t.Fatal(err)
			}
			now := h.card(c.ID)
			t.Logf("stale=%s now=%s", stale.Against.Token, now.Decision.Against.Token)
			if st, raw := h.answer(c.ID, webapi.AnswerRequest{Ref: stale.Ref, Option: "advance", Against: stale.Against.Token}); st != http.StatusConflict {
				t.Errorf("answer with a stale against: %d %s, want 409", st, raw)
			}
			var st int
			var raw json.RawMessage
			switch via {
			case "action":
				st, raw = h.actionRaw(c.ID, "advance", webapi.ActionRequest{Against: stale.Against.Token})
			case "send":
				st = h.call(http.MethodPost, "/api/cards/"+c.ID+"/send", webapi.SendRequest{Text: "/approve", Against: stale.Against.Token}, &raw)
			}
			if st != http.StatusConflict || !strings.Contains(string(raw), webapi.ConflictMoved) {
				t.Errorf("%s with a stale against: %d %.200s, want 409 moved", via, st, raw)
			}
			if got := h.card(c.ID).Stage; got != string(domain.StagePlan) {
				t.Errorf("the %s crossed the gate against a stale revision: now at %s", via, got)
			}
		})
	}
}

// A write sent with no revision at all, while the card pins a decision,
// is refused as "moved" — the page has not shown that decision, and a
// crossing, a line or a run meant for some other state is not run at this
// one — except for the menu entries that do not answer it
// (webapi.DecisionIndependentActions), which run as they are.
func TestAWriteWithNoRevisionIsRefusedWhileADecisionIsPinned(t *testing.T) {
	h := newCardBoard(t, agent.NewFake("ok"))
	c := h.planCard("Dark mode")
	if c.Decision == nil {
		t.Fatal("the plan card pins no decision")
	}
	for _, tc := range []struct {
		name string
		call func() (int, json.RawMessage)
	}{
		{"menu advance", func() (int, json.RawMessage) { return h.actionRaw(c.ID, "advance", webapi.ActionRequest{}) }},
		{"menu run", func() (int, json.RawMessage) { return h.actionRaw(c.ID, "run", webapi.ActionRequest{}) }},
		{"a line", func() (int, json.RawMessage) {
			var raw json.RawMessage
			st := h.call(http.MethodPost, "/api/cards/"+c.ID+"/send", webapi.SendRequest{Text: "/approve"}, &raw)
			return st, raw
		}},
	} {
		st, raw := tc.call()
		if st != http.StatusConflict || errorOf(t, raw).Error != webapi.ConflictMoved {
			t.Errorf("%s with no against = %d %.200s, want 409 moved", tc.name, st, raw)
		}
	}
	if got := h.card(c.ID).Stage; got != string(domain.StagePlan) {
		t.Fatalf("a write with no revision moved the card to %s", got)
	}
	// the budget answers nothing the decision asks
	n := 900
	if st, raw := h.actionRaw(c.ID, "envelope", webapi.ActionRequest{Number: &n}); st != http.StatusOK {
		t.Errorf("a budget with no against = %d %.200s, want 200", st, raw)
	}
}

// An answer is refused without the revision it was given against, with a
// ref or an option the decision does not have, or with words an answer
// does not take — and the card stays where it is.
func TestAnAnswerNeedsItsTokenRefAndOption(t *testing.T) {
	h := newCardBoard(t, agent.NewFake("ok"))
	c := h.planCard("Dark mode")
	d := c.Decision
	for _, tc := range []struct {
		name string
		req  webapi.AnswerRequest
	}{
		{"empty against", webapi.AnswerRequest{Ref: d.Ref, Option: "advance"}},
		{"ref only as against", webapi.AnswerRequest{Ref: d.Ref, Option: "advance", Against: d.Ref}},
		{"empty ref", webapi.AnswerRequest{Option: "advance", Against: d.Against.Token}},
		{"unknown option", webapi.AnswerRequest{Ref: d.Ref, Option: "merge", Against: d.Against.Token}},
		{"ask option id", webapi.AnswerRequest{Ref: d.Ref, Option: "0", Against: d.Against.Token}},
		{"words on approve", webapi.AnswerRequest{Ref: d.Ref, Option: "advance", Words: "fine", Against: d.Against.Token}},
	} {
		st, raw := h.answer(c.ID, tc.req)
		t.Logf("%s: %d %s", tc.name, st, raw)
		if st == http.StatusOK {
			t.Errorf("%s went through", tc.name)
		}
	}
	if got := h.card(c.ID).Stage; got != string(domain.StagePlan) {
		t.Errorf("moved to %s", got)
	}
	_ = state.ActorUser
}
