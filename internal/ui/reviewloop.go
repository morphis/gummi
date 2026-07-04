package ui

import (
	"context"
	"regexp"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/morphia/gummi/internal/domain"
)

// maxReviewRounds caps the automatic review→fix→review loop (DESIGN §10
// decision 4). Past the cap, gummi escalates to the human instead of
// looping.
const maxReviewRounds = 3

// reviewVerdict is the outcome parsed from a review session's message.
type reviewVerdict int

const (
	verdictUnclear reviewVerdict = iota
	verdictPass
	verdictChanges
)

var verdictRe = regexp.MustCompile(`(?im)^\s*VERDICT:\s*(pass|changes)\s*$`)

// verdictFromTool maps a submit_verdict tool result to a reviewVerdict.
func verdictFromTool(v string) reviewVerdict {
	switch v {
	case "pass":
		return verdictPass
	case "changes":
		return verdictChanges
	default:
		return verdictUnclear
	}
}

// parseVerdict finds the last VERDICT line in review output.
func parseVerdict(text string) reviewVerdict {
	matches := verdictRe.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return verdictUnclear
	}
	switch strings.ToLower(matches[len(matches)-1][1]) {
	case "pass":
		return verdictPass
	case "changes":
		return verdictChanges
	}
	return verdictUnclear
}

// onAutonomousDone drives the review loop when an autonomous session
// finishes. It returns (handled, cmd): handled means the loop consumed
// this completion (so the caller must not also raise a generic gate),
// and cmd is any automatic follow-up (bounce+re-implement, re-review, or
// advance), which may be nil (e.g. an escalation with no follow-up).
func (m *Shell) onAutonomousDone(id domain.FeatureID, stage domain.Stage) (bool, tea.Cmd) {
	switch stage {
	case domain.StageReview:
		return true, m.onReviewDone(id)
	case domain.StageImplement:
		// only auto-continue implements that are part of a review loop
		if m.reviewRounds[id] > 0 {
			return true, m.autoStep(id, domain.StageReview, "re-reviewing")
		}
	}
	return false, nil
}

// onReviewDone reads the review verdict and either advances (pass),
// bounces to implement for another round (changes, under the cap), or
// escalates to the human (changes past the cap, or an unclear verdict).
func (m *Shell) onReviewDone(id domain.FeatureID) tea.Cmd {
	s := m.engine.Get(id)
	if s == nil {
		return nil
	}
	// prefer the structured submit_verdict tool result; fall back to
	// parsing the VERDICT: line for backends/agents that didn't use it.
	snap := s.Snapshot()
	verdict := verdictFromTool(snap.Verdict)
	if verdict == verdictUnclear {
		verdict = parseVerdict(lastAssistant(snap))
	}
	switch verdict {
	case verdictPass:
		m.reviewRounds[id] = 0
		return m.autoStep(id, domain.StageVerify, "review passed → verify")
	case verdictChanges:
		if m.reviewRounds[id] >= maxReviewRounds {
			m.reviewRounds[id] = 0
			m.raiseAttention(id, attnGate, "review still requesting changes after "+itoa(maxReviewRounds)+" rounds — needs you")
			m.notice = noticeMsg{text: string(id) + " review escalated after " + itoa(maxReviewRounds) + " rounds", isErr: true}
			return nil
		}
		m.reviewRounds[id]++
		return m.autoStep(id, domain.StageImplement, "review requested changes → fixing (round "+itoa(m.reviewRounds[id])+")")
	default:
		// no clear verdict: don't guess — reset the loop and hand it to
		// the human.
		m.reviewRounds[id] = 0
		m.raiseAttention(id, attnGate, "review finished with no clear verdict — review manually")
		return nil
	}
}

// autoStep transitions a feature to the next loop stage and auto-runs
// it, all in one command. The transition actor is "review" so the audit
// trail shows the automatic loop.
func (m *Shell) autoStep(id domain.FeatureID, to domain.Stage, note string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		m.dropSession(id) // the completed session is stale
		if _, err := m.store.Transition(ctx, id, to, "review"); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		nf, err := m.store.GetFeature(ctx, id)
		if err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		if err := m.engine.Run(nf); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		return noticeMsg{text: string(id) + ": " + note}
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
