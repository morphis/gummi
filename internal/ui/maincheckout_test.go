package ui

import (
	"slices"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

func mainCheckoutRow(num int, title string) featureRow {
	f := freeformRow(num, title, false).F
	f.MainCheckout = true
	return featureRow{F: f}
}

// TestAMainCheckoutSessionOffersNoLanding: there is no branch to squash, so
// the floor refuses the landing with what to do instead — and the menu's
// own answer set never offers one in the first place, while offering the
// diff and the ending that keeps the work.
func TestAMainCheckoutSessionOffersNoLanding(t *testing.T) {
	m := &Shell{}
	r := mainCheckoutRow(12, "drop the leaked pty fd")
	in := nextInput{stage: domain.StageOpen, kind: domain.KindFreeform, mainCheckout: true}

	if why := m.landingRefusalIn(r.F, r, true, &in); why == "" {
		t.Fatal("a main-checkout session landed")
	} else if !strings.Contains(why, "main checkout") {
		t.Errorf("the refusal does not say where the work lives: %q", why)
	}

	ids := nextActionIDs(nextActions(in))
	if !strings.Contains(ids, "diff") || !strings.Contains(ids, "handoff") {
		t.Errorf("the answer set = %q, want diff and handoff", ids)
	}
	if strings.Contains(ids, "merge") {
		t.Errorf("the answer set offers a landing: %q", ids)
	}
}

// TestAMainCheckoutSessionsMenu: the action inventory matches the answer
// set — hand-off is in it (the work it keeps is loose in the checkout),
// the landing is out, and the branch verbs a card without a branch cannot
// run are out with it.
func TestAMainCheckoutSessionsMenu(t *testing.T) {
	r := mainCheckoutRow(13, "tidy the parser")
	in := nextInput{stage: domain.StageOpen, kind: domain.KindFreeform, mainCheckout: true}

	ids := actionIDs(cardActionsFor(in, r))
	if !slices.Contains(ids, "handoff") {
		t.Errorf("no hand-off in %v — the ending that keeps the work is the one such a card has", ids)
	}
	for _, absent := range []string{"merge", "rebase", "squash"} {
		if slices.Contains(ids, absent) {
			t.Errorf("%s in %v — the card holds no branch", absent, ids)
		}
	}
}

// TestAMainCheckoutKeysMatchTheMenu: the board's verb guard and the action
// inventory must not diverge (cardActionsFor's own warning) — the diff and
// the hand-off pass for a main-checkout session, and every branch verb is
// refused with the reason that is true rather than "when you approve the
// spec", which never will be.
func TestAMainCheckoutKeysMatchTheMenu(t *testing.T) {
	r := mainCheckoutRow(15, "drop the leaked pty fd")
	for _, verb := range []string{"diff", "hand-off"} {
		if n := branchVerbRefusal(r, verb); n != nil {
			t.Errorf("%s refused for a main-checkout session: %q", verb, n.text)
		}
	}
	for _, verb := range []string{"merge", "squash", "rebase", "cleanup"} {
		n := branchVerbRefusal(r, verb)
		if n == nil {
			t.Errorf("%s went through for a main-checkout session", verb)
		} else if !strings.Contains(n.text, "main checkout") {
			t.Errorf("the %s refusal does not say why: %q", verb, n.text)
		}
	}
	if n := branchVerbRefusal(r, "attach"); n == nil {
		t.Error("attach went through for a main-checkout session")
	}
}

// TestAMainCheckoutRefusalReachesTheKeysOnlyThroughTheRow: a caller without
// a board row (ok false) gets no refusal from the freeform branch — the row
// is what carries the floor, as it does for the busy and comment checks.
func TestAMainCheckoutRefusalReachesTheKeysOnlyThroughTheRow(t *testing.T) {
	m := &Shell{}
	r := mainCheckoutRow(14, "drop the leaked pty fd")
	if why := m.landingRefusalIn(r.F, r, false, nil); why != "" {
		t.Errorf("a rowless caller got a refusal: %q", why)
	}
}
