package state

import "strings"

// A person who answers through the web face is recorded by name, so a
// receipt can say who crossed a gate when more than one person has the
// board open (DESIGN §20). The name rides on ActorUser rather than
// replacing it — "user:Simon" — because everything that asks "was this a
// person?" was written against the bare word, and a named person is the
// same kind of actor the TUI's "user" has always been: a human at a
// surface, not a loop.
const personActorPrefix = ActorUser + ":"

// PersonActor is the actor string recorded for a named person. An empty
// name is the bare ActorUser, which is what the terminal records.
func PersonActor(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ActorUser
	}
	return personActorPrefix + name
}

// IsPersonActor reports whether an actor is a person at a board surface:
// the terminal's bare ActorUser, or a named person from the web face.
// It is the check every "did a human do this?" question asks, so a named
// person can never be miscounted as a machine.
func IsPersonActor(actor string) bool {
	return actor == ActorUser || strings.HasPrefix(actor, personActorPrefix)
}

// PersonName is the name a person actor carries, "" for the bare
// ActorUser and for anything that is not a person.
func PersonName(actor string) string {
	name, ok := strings.CutPrefix(actor, personActorPrefix)
	if !ok {
		return ""
	}
	return name
}

// ActorObjective is the actor of the turns gummi sends for a freeform
// card's objective (DESIGN §19.11): not a person, so a thread draws them
// as gummi's and never as the person's.
const ActorObjective = "objective"
