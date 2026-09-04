package ui

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/gatepolicy"
	"github.com/morphis/gummi/internal/verdict"
	"github.com/morphis/gummi/internal/workflow"
)

// TestBG090ResearchVerifyGateDoesNotOfferToLand: a passing verify puts
// the same decision on screen three times — the question heading the
// picker, the rows under it, and the park receipt the thread keeps
// afterwards — and two of the three said "land". A research card carries
// no branch, so there is nothing to land; the rows already knew that and
// said "mark done", leaving the heading asking about one act and its
// only answer performing another. The receipt is the worse half: it is
// written into card_events, so it stayed in the card's history.
func TestBG090ResearchVerifyGateDoesNotOfferToLand(t *testing.T) {
	id, err := domain.NewID(domain.KindResearch, 5)
	if err != nil {
		t.Fatal(err)
	}
	r := featureRow{F: domain.Feature{ID: id, Kind: domain.KindResearch, Stage: domain.StageVerify}}
	in := nextInput{stage: domain.StageVerify, kind: domain.KindResearch, attn: attnGate, verdict: verdictPass}

	q := decisionQuestion(decisionVerify, r, in)
	if strings.Contains(q, "land") {
		t.Errorf("verify question on a research card = %q, want no talk of landing", q)
	}

	// the picker's own rows are the thing the question has to agree with
	acts := nextActions(in)
	if len(acts) == 0 {
		t.Fatal("a passing research verify offered nothing")
	}
	for _, a := range acts {
		if strings.Contains(a.label, "land") || strings.Contains(a.why, "land on main") {
			t.Errorf("research verify row %q/%q talks about landing", a.label, a.why)
		}
	}
}

// TestBG090VerifyGateReasonMatchesTheKind covers the receipt half over
// every kind: the gate reason onVerifyDone raises is what the inbox
// shows and what card_events keeps, and it must offer to land exactly
// when the kind has a branch to land — which workflow.NeedsWorktree is
// the standing answer to.
func TestBG090VerifyGateReasonMatchesTheKind(t *testing.T) {
	for _, k := range []domain.Kind{domain.KindFeature, domain.KindBug, domain.KindResearch} {
		id, err := domain.NewID(k, 5)
		if err != nil {
			t.Fatal(err)
		}
		reason := verifyGateReason(id.Kind())
		branchy := workflow.NeedsWorktree(k, domain.StageVerify)
		if got := strings.Contains(reason, "land on main"); got != branchy {
			t.Errorf("%s: gate reason %q offers landing = %v, want %v", k, reason, got, branchy)
		}
		if !branchy && !strings.Contains(reason, "done") {
			t.Errorf("%s: gate reason %q says neither land nor done", k, reason)
		}
	}
}

// TestBG090GatePolicyStillRaisesTheGate pins the precondition the two
// assertions above rest on: a clean research verify really does reach
// the RaiseGate arm, so the wording under test is the wording shown.
func TestBG090GatePolicyStillRaisesTheGate(t *testing.T) {
	out := gatepolicy.Decide(gatepolicy.Input{
		Stage:     domain.StageVerify,
		Kind:      domain.KindResearch,
		Verdict:   verdict.Pass,
		WorkStage: workflow.WorkStage(domain.KindResearch),
	})
	if out.Action != gatepolicy.RaiseGate {
		t.Fatalf("a clean research verify took the %v arm, not RaiseGate", out.Action)
	}
}
