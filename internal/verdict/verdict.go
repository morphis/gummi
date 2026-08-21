// Package verdict is the single owner of the verdict grammar shared by
// the TUI and the headless driver. Both consumers drive the autonomous
// loop by stage verdicts, and DESIGN §13 requires them to behave
// identically — this package is the seam that makes that a shared
// implementation rather than a copy-paste invariant. It is a leaf
// consumer of internal/engine (it needs engine.Snapshot for the
// session-level helpers) and imports nothing else.
package verdict

import (
	"regexp"
	"strings"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
)

// MaxRounds caps the automatic loop for round kind k (DESIGN §10 decision
// 4): 3 for the review→fix→review loop (RoundKindReview — the research
// slice's review loop reuses this cap verbatim, with no separate
// constant), 2 for the plan→critique→replan loop (RoundKindPlan — lower
// than the review cap, since plan revisions are cheap to judge and the
// human gate is right behind the critique anyway). Past the cap, every
// consumer escalates to the human instead of looping.
func MaxRounds(k domain.RoundKind) int {
	if k == domain.RoundKindPlan {
		return 2
	}
	return 3
}

// Verdict is the outcome parsed from a review/verify/critique session.
type Verdict int

const (
	Unclear Verdict = iota
	Pass
	Changes // review/critique: findings to bounce back
	Fail    // verify: verification found real problems
	Blocked // verify: the environment can't run the plan
)

var verdictRe = regexp.MustCompile(`(?im)^\s*VERDICT:\s*(pass|changes|fail|blocked)\s*$`)

// verdictTailRe catches models that emit the verdict glued to the
// preceding sentence ("…redundant.VERDICT: changes") with no newline
// before it — the strict verdictRe misses those. It anchors to the end
// of the trimmed text, so a stray mid-text mention still doesn't count.
var verdictTailRe = regexp.MustCompile(`(?i)\bVERDICT:\s*(pass|changes|fail|blocked)\s*$`)

// FromTool maps a submit_verdict tool result to a Verdict.
func FromTool(v string) Verdict {
	switch v {
	case "pass":
		return Pass
	case "changes":
		return Changes
	case "fail":
		return Fail
	case "blocked":
		return Blocked
	default:
		return Unclear
	}
}

// Parse finds the last VERDICT: line in review output, falling back to
// the glued-to-the-tail form when no strict line matches.
func Parse(text string) Verdict {
	if matches := verdictRe.FindAllStringSubmatch(text, -1); len(matches) > 0 {
		return FromTool(strings.ToLower(matches[len(matches)-1][1]))
	}
	trimmed := strings.TrimRight(text, " \t\r\n")
	if m := verdictTailRe.FindStringSubmatch(trimmed); m != nil {
		return FromTool(strings.ToLower(m[1]))
	}
	return Unclear
}

// String is the label for each verdict value — the inverse of FromTool.
func (v Verdict) String() string {
	switch v {
	case Pass:
		return "pass"
	case Changes:
		return "changes"
	case Fail:
		return "fail"
	case Blocked:
		return "blocked"
	default:
		return "unclear"
	}
}

// SessionVerdict reads a session's outcome, preferring the structured
// submit_verdict tool result and falling back to the VERDICT: line for
// backends/agents that didn't use it. A stamped verdict floor is applied
// before returning: it only ever downgrades a raw Pass to Blocked.
func SessionVerdict(snap engine.Snapshot) Verdict {
	var raw Verdict
	if v := FromTool(snap.Verdict); v != Unclear {
		raw = v
	} else {
		raw = Parse(LastAssistant(snap))
	}
	if snap.VerdictFloor == "blocked" && raw == Pass {
		return Blocked
	}
	return raw
}

// LastAssistant returns the content of the most recent assistant message
// in a snapshot's transcript, or "".
func LastAssistant(snap engine.Snapshot) string {
	for i := len(snap.Transcript) - 1; i >= 0; i-- {
		if snap.Transcript[i].Author == engine.AuthorAssistant {
			return snap.Transcript[i].Content
		}
	}
	return ""
}

// ReplanNote is the kickoff for a replan run: the critique's findings
// live in the spec (single source of truth), so the architect is
// pointed at the threads rather than handed a copy.
const ReplanNote = "The plan critique found issues. Address each open `%% @reviewer:` " +
	"thread in the spec: revise the plan in Implementation notes accordingly and " +
	"mark each thread resolved with a line like `%% @architect: resolved — <how>`."

// ReCritiqueNote is the kickoff for a critique after a replan round:
// burn down the prior round's threads instead of re-judging the plan
// from scratch, so the loop converges rather than churning out fresh
// findings every round.
const ReCritiqueNote = "This is a re-critique: a prior round's findings were addressed " +
	"and the plan revised. Start from the resolved `%% @reviewer:` threads and verify " +
	"each resolution against the revised plan — reopen a thread only if its resolution " +
	"does not hold. Raise a new finding only if it is blocking."
