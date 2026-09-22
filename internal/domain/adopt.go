package domain

import (
	"fmt"
	"strings"
)

// AdoptedWork is what a card inherited when it was minted onto a branch
// gummi did not cut (DESIGN §10 D22).
//
// It exists because an adopted card's first stage opens onto a diff
// nobody in the conversation has described. An ordinary card's plan
// stage starts from a problem statement and an empty branch; an adopted
// one starts from somebody else's commits, and the architect has to be
// told what they are before it can write a word about reworking them.
// So the mint captures this once and seeds it into the draft, rather
// than leaving each stage to rediscover it by running git.
//
// Behind is carried for the reason D22 gives: gummi deliberately does
// not catch a stale branch up, which makes saying how stale it is an
// obligation rather than a nicety.
type AdoptedWork struct {
	Branch  string   // the adopted ref
	Base    string   // what it forks from and is measured against
	Commits []string // "abc1234 subject", newest first, as git log --oneline gives them
	Stat    string   // git diff --stat against the fork point
	Behind  int      // commits on Base this branch does not have
	PR      string   // the pull request this branch has open, when one was named
}

// Empty reports whether there is nothing inherited worth rendering.
func (a *AdoptedWork) Empty() bool {
	return a == nil || (a.Branch == "" && len(a.Commits) == 0 && a.Stat == "")
}

// Staleness is the human sentence about how far behind the branch is —
// the whole of gummi's answer to an old branch, since it will not rebase
// one on its own.
func (a *AdoptedWork) Staleness() string {
	switch {
	case a == nil || a.Behind == 0:
		return "up to date with " + baseLabel(a)
	case a.Behind == 1:
		return "1 commit behind " + baseLabel(a) + " — gummi will not rebase it for you"
	default:
		return fmt.Sprintf("%d commits behind %s — gummi will not rebase it for you", a.Behind, baseLabel(a))
	}
}

func baseLabel(a *AdoptedWork) string {
	if a == nil || strings.TrimSpace(a.Base) == "" {
		return "its base"
	}
	return a.Base
}
