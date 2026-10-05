package domain

import "fmt"

// LandMethod is how a card's branch reaches its base on a local landing.
// Squash is the default and the zero value's meaning: one commit carrying
// the approved message. Merge is a --no-ff merge commit that keeps the
// branch's own commits, joined by that message.
type LandMethod string

const (
	LandSquash LandMethod = "squash"
	LandMerge  LandMethod = "merge"
)

// ParseLandMethod reads a method named by a person or a request. The empty
// string means the default, squash; anything other than the two named
// methods is an error, so a typo never lands a different shape silently.
func ParseLandMethod(s string) (LandMethod, error) {
	switch s {
	case "", string(LandSquash):
		return LandSquash, nil
	case string(LandMerge):
		return LandMerge, nil
	}
	return "", fmt.Errorf("unknown landing method %q (want %q or %q)", s, LandSquash, LandMerge)
}

// LandMethods lists the methods a local landing of f may use. A goal's
// history is one commit per card under the goal's own merge commit, so a
// card that belongs to a goal (or is one) lands as a squash only.
func (f *Feature) LandMethods() []LandMethod {
	if f.IsGoal() || f.InGoal() {
		return []LandMethod{LandSquash}
	}
	return []LandMethod{LandSquash, LandMerge}
}

// Offers reports whether a local landing of f may use method m. The zero
// value means squash, as it does in ParseLandMethod.
func (f *Feature) Offers(m LandMethod) bool {
	if m == "" {
		m = LandSquash
	}
	for _, o := range f.LandMethods() {
		if o == m {
			return true
		}
	}
	return false
}
