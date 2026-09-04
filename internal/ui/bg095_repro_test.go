package ui

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

// branchVerbs is the set of board verbs that need a branch in a worktree
// — the five that shared one guard and one sentence.
var branchVerbs = []string{"diff", "rebase", "merge", "squash", "cleanup"}

// TestBG095DesignPhaseCardIsNotCalledResearch: the five branch verbs all
// refused a card with no worktree by saying "research cards carry no
// branch". The predicate behind that sentence is false for two unrelated
// reasons — a research card, which never has a branch, and any other
// card still in the design phase, which does not have one yet — so a
// feature card sitting at spec was told it was a research card, and a
// bug card at triage likewise.
//
// Asserted over every non-research kind at every stage that can still be
// waiting for its worktree, not just the feature-at-spec the drive saw.
func TestBG095DesignPhaseCardIsNotCalledResearch(t *testing.T) {
	for _, k := range []domain.Kind{domain.KindFeature, domain.KindBug} {
		id, err := domain.NewID(k, 1)
		if err != nil {
			t.Fatal(err)
		}
		for _, st := range domain.Stages {
			r := featureRow{F: domain.Feature{ID: id, Kind: k, Stage: st}} // HasWorktree false
			for _, verb := range branchVerbs {
				n := branchVerbRefusal(r, verb)
				if n == nil {
					t.Errorf("%s at %s: %s allowed with no worktree", k, st, verb)
					continue
				}
				if strings.Contains(n.text, "research") {
					t.Errorf("%s at %s: %s refused with %q — that is a research card's reason", k, st, verb, n.text)
				}
				if !strings.Contains(n.text, "no worktree yet") {
					t.Errorf("%s at %s: %s refused with %q, want the not-yet refusal", k, st, verb, n.text)
				}
			}
		}
	}
}

// TestBG095ResearchKeepsItsOwnReason is the other half: the research
// sentence must survive for the kind it is true of, on every stage and
// every verb, so the fix narrows the message rather than deleting it.
func TestBG095ResearchKeepsItsOwnReason(t *testing.T) {
	id, err := domain.NewID(domain.KindResearch, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range domain.Stages {
		r := featureRow{F: domain.Feature{ID: id, Kind: domain.KindResearch, Stage: st}}
		for _, verb := range branchVerbs {
			n := branchVerbRefusal(r, verb)
			if n == nil {
				t.Fatalf("research at %s: %s allowed", st, verb)
			}
			if !strings.Contains(n.text, "research cards carry no branch") {
				t.Errorf("research at %s: %s refused with %q", st, verb, n.text)
			}
			if !strings.Contains(n.text, "no "+verb) {
				t.Errorf("research at %s: refusal %q does not name the verb", st, n.text)
			}
		}
	}
}

// TestBG095AWorktreeBearingCardIsNotRefused guards the third case: once
// the worktree exists, none of the five is refused by this gate.
func TestBG095AWorktreeBearingCardIsNotRefused(t *testing.T) {
	for _, k := range []domain.Kind{domain.KindFeature, domain.KindBug} {
		id, _ := domain.NewID(k, 1)
		r := featureRow{F: domain.Feature{ID: id, Kind: k, Stage: domain.StageReview}, HasWorktree: true}
		for _, verb := range branchVerbs {
			if n := branchVerbRefusal(r, verb); n != nil {
				t.Errorf("%s at review with a worktree: %s refused with %q", k, verb, n.text)
			}
		}
	}
}
