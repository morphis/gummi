package agent

import (
	"errors"
	"strings"
)

// unavailableMarkers are the phrases backends use when they are working
// correctly and simply cannot serve this turn now: a provider quota or
// rate limit, a server-side overload, a transport that could not reach
// them. They are matched case-insensitively against the backend's own
// diagnostic.
//
// Matching on prose is unlovely, and it is what there is: every backend
// gummi drives is a CLI that reports these in its own words, and none of
// them promises a code. The list is deliberately short and specific —
// each entry is a phrase a provider uses for "come back later" and for
// nothing else — because a false positive here tells a user to wait for
// something that will never fix itself.
var unavailableMarkers = []string{
	"session limit",
	"usage limit",
	"rate limit",
	"rate_limit",
	"429",
	"quota",
	"overloaded",
	"temporarily unavailable",
	"try again later",
	"503 ",
	"502 ",
	"504 ",
	"service unavailable",
	"connection refused",
	"connection reset",
	"no route to host",
	"network is unreachable",
}

// Unavailable reports a failure that is about the backend's availability
// rather than about the work or the workspace's setup, and returns the
// backend's own words for it.
//
// The distinction is the difference between two opposite instructions to
// a user. A backend that is not configured wants `gummi doctor` and a
// change; a backend that is rate-limited wants nothing but time, and
// usually says when — "You've hit your session limit · resets 3:10pm
// (UTC)" carries the answer in the sentence, which is why this returns
// the sentence rather than a category.
//
// Auth failures and a CLI that is not on PATH are deliberately NOT
// unavailability: those are setup, doctor is the right answer, and
// telling someone to wait for a token that will not refresh itself is a
// worse error than the one this exists to stop.
//
// It reads the diagnostic a *RunFailure carries when there is one, and
// falls back to the error's own text so a caller that wraps differently
// still gets an answer.
func Unavailable(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	text := err.Error()
	var rf *RunFailure
	if errors.As(err, &rf) && strings.TrimSpace(rf.Diagnostic) != "" {
		text = rf.Diagnostic
	}
	lower := strings.ToLower(text)
	if strings.Contains(lower, "auth") || strings.Contains(lower, "not found in $path") {
		return "", false // setup, not availability
	}
	for _, m := range unavailableMarkers {
		if strings.Contains(lower, m) {
			return strings.TrimSpace(text), true
		}
	}
	return "", false
}
