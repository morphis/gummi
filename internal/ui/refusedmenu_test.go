package ui

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

// menuIDs is the ids the selected card's menu lists, on both faces: the
// TUI's action list and the web's projection of it.
func menuIDs(m *Shell) (tui, web map[string]bool) {
	tui, web = map[string]bool{}, map[string]bool{}
	for _, a := range m.cardActions().actions {
		tui[a.id] = true
	}
	for _, a := range m.webActions(m.rows[0]) {
		web[a.ID] = true
	}
	return tui, web
}

// TestTheMenuOffersNoLandingItWouldRefuse: the ⋯ menu lists a landing —
// merge, and "next stage" out of verify — only where the landing floor
// (landingRefusal) lets one through, on both faces. A card at verify that
// never finished a verify pass lists neither; a failed verify keeps them
// (the menu's landing is the stop's own "land anyway"); a verified card
// lists them, unless it sits on an unlanded card in its stack.
func TestTheMenuOffersNoLandingItWouldRefuse(t *testing.T) {
	expect := func(t *testing.T, m *Shell, offered bool, why string) {
		t.Helper()
		tui, web := menuIDs(m)
		for _, id := range []string{"merge", "advance"} {
			if tui[id] != offered {
				t.Errorf("%s: TUI menu lists %q = %v, want %v", why, id, tui[id], offered)
			}
			if web[id] != offered {
				t.Errorf("%s: web menu lists %q = %v, want %v", why, id, web[id], offered)
			}
		}
		if !tui["verify"] || !web["verify"] {
			t.Errorf("%s: the menu lost verify", why)
		}
	}

	m, _, _ := unverifiedFixture(t)
	m.sel = 0
	if m.landingRefusal(m.rows[0].F) == "" {
		t.Fatal("fixture: the unverified card may land")
	}
	expect(t, m, false, "never verified")

	// the pass finished and failed: "land anyway" is on offer, and so is
	// the menu's landing — it is that overrule
	m.rows[0].Exited, m.rows[0].ExitVerdict = true, verdictFail
	m.inbox.addEscalated(m.rows[0].F.ID, attnGate, "verify FAILED — read the evidence and bounce or overrule")
	expect(t, m, true, "failed verify")

	m2, _, _ := mergeFixture(t)
	m2.sel = 0
	expect(t, m2, true, "verified")

	// the card below it in its stack has not landed: the landing waits,
	// and the decision says so ("lands after FD-000")
	m2.stackRows = map[domain.FeatureID]stackRow{
		"FD-000": {ID: "st", Pos: 1, Of: 2},
		"FD-001": {ID: "st", Pos: 2, Of: 2},
	}
	expect(t, m2, false, "stacked on an unlanded card")
}

// TestTheMenuOffersNoOtherAnswerItWouldRefuse: the same rule for the
// menu's other doors that always come back refused in the state the card
// is in — approving a design gate an unmet dependency holds (the decision
// leads with what it waits on), and a local landing on a card linked to a
// pull request (it lands there).
func TestTheMenuOffersNoOtherAnswerItWouldRefuse(t *testing.T) {
	has := func(acts []cardAction, id string) bool {
		for _, a := range acts {
			if a.id == id {
				return true
			}
		}
		return false
	}
	plan := nextInput{stage: domain.StagePlan, kind: domain.KindFeature, exited: true, verdict: verdictPass}
	row := featureRow{F: domain.Feature{ID: "FD-014", Kind: domain.KindFeature, Stage: domain.StagePlan}}
	if !has(cardActionsFor(plan, row), "advance") {
		t.Fatal("a clear design gate lists no next stage")
	}
	plan.depBlockers = []domain.FeatureID{"FD-013"}
	if has(cardActionsFor(plan, row), "advance") {
		t.Error("a design gate an unmet dependency holds lists next stage, which the gate refuses")
	}

	verified := nextInput{stage: domain.StageVerify, kind: domain.KindFeature, hasWorktree: true, exited: true, verdict: verdictPass}
	vrow := featureRow{F: domain.Feature{ID: "FD-020", Kind: domain.KindFeature, Stage: domain.StageVerify}, HasWorktree: true}
	if !has(cardActionsFor(verified, vrow), "merge") {
		t.Fatal("a verified card lists no merge")
	}
	vrow.F.PullRequest = domain.PullRequestRef{Repo: "o/r", Number: 7, URL: "https://github.com/o/r/pull/7"}
	if has(cardActionsFor(verified, vrow), "merge") {
		t.Error("a card linked to a pull request lists a local merge, which is refused")
	}
}

// TestTheWebMenusLandingSaysItLands: on the web menu, "next stage" out of
// verify is worded as the landing it is — the decision's own answer, "land
// anyway" over a failed verify — and marked dangerous, so a plain step
// forward never lands on the trunk unannounced.
func TestTheWebMenusLandingSaysItLands(t *testing.T) {
	advance := func(m *Shell) (string, bool) {
		for _, a := range m.webActions(m.rows[0]) {
			if a.ID == "advance" {
				return a.Label, a.Danger
			}
		}
		t.Fatal("the menu lists no advance")
		return "", false
	}

	m, _, _ := mergeFixture(t)
	m.sel = 0
	if label, danger := advance(m); !strings.HasPrefix(label, "land on ") || !danger {
		t.Errorf("verified: advance reads %q (danger %v), want the landing, marked dangerous", label, danger)
	}

	f, _, _ := unverifiedFixture(t)
	f.sel = 0
	f.rows[0].Exited, f.rows[0].ExitVerdict = true, verdictFail
	f.inbox.addEscalated(f.rows[0].F.ID, attnGate, "verify FAILED — read the evidence and bounce or overrule")
	if label, danger := advance(f); label != "land anyway" || !danger {
		t.Errorf("failed verify: advance reads %q (danger %v), want \"land anyway\", marked dangerous", label, danger)
	}
}
