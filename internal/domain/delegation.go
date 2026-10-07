package domain

import (
	"errors"
	"fmt"
)

// Delegation is what a person has let a freeform card's session do with
// cards: create workflow cards under a budget of its own. It is opt-in —
// a zero Budget means the session is offered no card tools and told
// nothing about them — and every card it creates is put to the person
// first, until they answer "this one and all that follow" (ConfirmAll).
// The budget still caps the session after that answer.
type Delegation struct {
	// Budget is the credits the session may hand out across every card
	// it creates. Zero withdraws the delegation.
	Budget int
	// ConfirmAll records that the person stopped being asked about each
	// card. Revocable: clearing it brings the question back.
	ConfirmAll bool
}

// Enabled reports whether the person has opted the card in.
func (d Delegation) Enabled() bool { return d.Budget > 0 }

// Validate refuses a negative budget.
func (d Delegation) Validate() error {
	if d.Budget < 0 {
		return fmt.Errorf("a delegation budget of %d credits is negative", d.Budget)
	}
	return nil
}

// Delegated reports whether a freeform card's session created this card.
func (f *Feature) Delegated() bool { return f.ParentID != "" }

func (f *Feature) validateDelegation() error {
	if err := f.Delegate.Validate(); err != nil {
		return fmt.Errorf("feature %s: %w", f.ID, err)
	}
	if f.Delegate != (Delegation{}) && f.kind() != KindFreeform {
		return fmt.Errorf("feature %s: only a freeform card delegates", f.ID)
	}
	if f.ParentID == "" {
		return nil
	}
	if _, err := ParseFeatureID(string(f.ParentID)); err != nil || f.ParentID.Kind() != KindFreeform {
		return fmt.Errorf("feature %s: parent %q is not a freeform card", f.ID, f.ParentID)
	}
	switch {
	case f.kind() == KindFreeform || f.kind() == KindGoal:
		return fmt.Errorf("feature %s: a %s card cannot be delegated", f.ID, f.kind())
	case f.GoalID != "":
		return fmt.Errorf("feature %s: a delegated card cannot also belong to %s", f.ID, f.GoalID)
	}
	return nil
}

// DelegateHeld is what one delegated card holds of its parent's
// delegation budget, the goal ledger's rule (DESIGN §17.3) applied to a
// freeform parent: an unfinished card holds the larger of its envelope and
// what it has spent; a finished one — landed on the parent, or dropped —
// holds what it spent and gives the rest back.
func DelegateHeld(c Feature) float64 {
	if c.Stage == StageDone {
		return c.Spend.Credits
	}
	return max(float64(c.Budget.Envelope), c.Spend.Credits)
}

// DelegateAvailable is what a freeform card's delegation has left to give
// its next card: the budget less everything its cards hold.
func DelegateAvailable(parent Feature, cards []Feature) float64 {
	avail := float64(parent.Delegate.Budget)
	for _, c := range cards {
		avail -= DelegateHeld(c)
	}
	return avail
}

// ErrUnlandedDelegates is the refusal a freeform card gets when it would
// land while a card its session created is still neither landed on it nor
// dropped: that card's work would be left forking from a branch that is
// gone.
var ErrUnlandedDelegates = errors.New("cards created by this session have not landed on it yet")

// UnlandedDelegates returns the ids of the cards that hold a freeform
// card from landing.
func UnlandedDelegates(cards []Feature) []FeatureID {
	var out []FeatureID
	for _, c := range cards {
		if c.Stage != StageDone {
			out = append(out, c.ID)
		}
	}
	return out
}
