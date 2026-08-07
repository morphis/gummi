package driver

import (
	"regexp"
	"strings"

	"github.com/morphis/gummi/internal/engine"
)

// The autonomous loop is driven by stage verdicts exactly as the TUI's
// internal/ui/reviewloop.go drives it — the driver replicates that logic
// headlessly so the quality floor behaves identically whoever is at the
// keyboard. These caps mirror ui.maxReviewRounds / ui.maxPlanRounds
// (DESIGN §10 decision 4): past the cap the driver escalates instead of
// looping forever.
const (
	maxReviewRounds = 3
	maxPlanRounds   = 2
)

// verdict is the outcome parsed from a review/verify/critique session.
type verdict int

const (
	verdictUnclear verdict = iota
	verdictPass
	verdictChanges // review/critique: findings to bounce back
	verdictFail    // verify: verification found real problems
	verdictBlocked // verify: the environment can't run the plan
)

var verdictRe = regexp.MustCompile(`(?im)^\s*VERDICT:\s*(pass|changes|fail|blocked)\s*$`)

// verdictTailRe catches models that emit the verdict glued to the
// preceding sentence ("…redundant.VERDICT: changes") with no newline
// before it — the strict verdictRe misses those. It anchors to the end
// of the trimmed text, so a stray mid-text mention still doesn't count.
var verdictTailRe = regexp.MustCompile(`(?i)\bVERDICT:\s*(pass|changes|fail|blocked)\s*$`)

// verdictFromTool maps a submit_verdict tool result to a verdict.
func verdictFromTool(v string) verdict {
	switch v {
	case "pass":
		return verdictPass
	case "changes":
		return verdictChanges
	case "fail":
		return verdictFail
	case "blocked":
		return verdictBlocked
	default:
		return verdictUnclear
	}
}

// parseVerdict finds the last VERDICT: line in a session's prose — the
// fallback for a backend/agent that didn't call submit_verdict.
func parseVerdict(text string) verdict {
	if m := verdictRe.FindAllStringSubmatch(text, -1); len(m) > 0 {
		return verdictFromTool(strings.ToLower(m[len(m)-1][1]))
	}
	trimmed := strings.TrimRight(text, " \t\r\n")
	if m := verdictTailRe.FindStringSubmatch(trimmed); m != nil {
		return verdictFromTool(strings.ToLower(m[1]))
	}
	return verdictUnclear
}

// sessionVerdict reads a finished session's outcome, preferring the
// structured submit_verdict tool result and falling back to the VERDICT:
// line (mirrors ui.sessionVerdict).
func sessionVerdict(snap engine.Snapshot) verdict {
	if v := verdictFromTool(snap.Verdict); v != verdictUnclear {
		return v
	}
	return parseVerdict(lastAssistant(snap))
}

// lastAssistant returns the content of the most recent assistant message
// in a snapshot's transcript, or "".
func lastAssistant(snap engine.Snapshot) string {
	for i := len(snap.Transcript) - 1; i >= 0; i-- {
		if snap.Transcript[i].Author == engine.AuthorAssistant {
			return snap.Transcript[i].Content
		}
	}
	return ""
}
