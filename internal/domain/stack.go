package domain

import (
	"fmt"
	"regexp"
	"strings"
)

// A stack is an ordered set of cards in one repository whose branches are
// chained: the card at the bottom forks from its own Base, and every card
// above it forks from the branch of the card below. It exists so a
// developer can slice one piece of work into several separately
// reviewable branches, land them one at a time, take review feedback on
// the ones below, and have the cards above replayed onto the new commits
// without doing it by hand.
//
// A stack is TOPOLOGY, never scheduling. It says nothing about when a
// card may run, and Feature.StackID's comment records why: a dependency
// is met only at StageDone, so treating a position as a dependency would
// hold every card above the bottom out of its coding stage until the one
// below had landed, which is the serialized waiting a stack removes.
// Dependency edges still exist, still gate, and can be added between
// stack members for the rare card that truly cannot start — they are just
// a different fact.

// StackID identifies a stack. Unlike a card's ID it is not minted from
// the shared counter: a stack is not a unit of work, carries no branch or
// artifact of its own, and never appears in a commit message, so it needs
// no place in the FD/BG/RS/GL numbering.
type StackID string

// stackIDRe is the allowlist for a stack id: the slug rules, so an id is
// safe to print in a notice and to pass to git as part of nothing at all.
var stackIDRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// maxStackNameLen caps a stack's display name at the same length a card
// title gets, for the same reason: it has to fit on one board row.
const maxStackNameLen = 100

// Stack is the record of one stack: an identity, the name a person reads,
// and the single repository every member shares.
//
// The membership and the order are NOT here — they live on the cards, as
// Feature.StackID and Feature.StackPos. A member list stored in both
// places is a list that can disagree with itself, and the card already
// has to carry its stack to resolve its own base.
type Stack struct {
	ID StackID
	// Name is what the board shows. It starts as the slug of the card the
	// stack was created from, because a stack is born by stacking a second
	// card onto a first, and the bottom card is the only name available at
	// that moment that means anything to the reader.
	Name string
	// Repo is the configured repository name every member belongs to,
	// empty for the workspace default — the same convention as
	// Feature.Repo. A stack cannot span repositories: a branch has no way
	// to fork from a branch in a different checkout.
	Repo string
}

// Validate reports whether the stack record is well formed.
func (s *Stack) Validate() error {
	if !stackIDRe.MatchString(string(s.ID)) {
		return fmt.Errorf("invalid stack id %q: want lowercase words joined by dashes", s.ID)
	}
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("stack %s has no name", s.ID)
	}
	if len(s.Name) > maxStackNameLen {
		return fmt.Errorf("stack %s name is %d chars, max %d", s.ID, len(s.Name), maxStackNameLen)
	}
	if strings.ContainsAny(s.Repo, "/\\") {
		return fmt.Errorf("stack %s repo %q must be a configured repository name, not a path", s.ID, s.Repo)
	}
	return nil
}

// Stacked reports whether the card sits in a stack.
func (f *Feature) Stacked() bool { return f.StackID != "" }

// StackBottom reports whether the card is the bottom of its stack — the
// one member that forks from a plain branch rather than from another
// card. False for a card that is not stacked at all.
func (f *Feature) StackBottom() bool { return f.Stacked() && f.StackPos == 0 }

// NewStackID derives a stack id from the name it will be shown under,
// falling back to the given card when the name yields nothing usable
// (a title of punctuation, say). It never returns an invalid id.
func NewStackID(name string, from FeatureID) (StackID, error) {
	if slug, err := Slugify(name); err == nil {
		return StackID(slug), nil
	}
	lowered := strings.ToLower(string(from))
	if stackIDRe.MatchString(lowered) {
		return StackID(lowered), nil
	}
	return "", fmt.Errorf("cannot derive a stack id from %q", name)
}
