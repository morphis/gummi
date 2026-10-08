package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/ui/theme"
	"github.com/morphis/gummi/internal/verifydoc"
)

// A pass on a card with no gummi-checks is the reviewer's word on commands
// it chose. The sentence a person reads before landing says so, instead of
// "the work is ready" as though gummi had checked anything.
func TestAPassWithNoChecksSaysWhatItRestsOn(t *testing.T) {
	in := nextInput{stage: domain.StageVerify, verdict: verdictPass}
	if got := verifyStopped(in, "spec"); !strings.HasPrefix(got, "Verify passed — the work is ready") {
		t.Errorf("ordinary pass = %q", got)
	}
	in.noChecks = true
	got := verifyStopped(in, "spec")
	if !strings.Contains(got, "reviewer's own commands") || !strings.Contains(got, "no gummi-checks") {
		t.Errorf("no-checks pass = %q, want it to say the pass rests on the reviewer's own commands", got)
	}
}

// Discovery that left a card with no block is said at once, with why and
// what it costs; the same broken scribe failing again a moment later does
// not say it again.
func TestDiscoveryThatLeavesNoChecksIsSaidOnce(t *testing.T) {
	m := NewShell(theme.GummiDark(), "t")
	id := domain.FeatureID("FD-002")
	sf := &engine.ScribeFailure{
		Pass: "check discovery", Model: "claude-haiku-4.5", Backend: "claude",
		Err: errors.New("There's an issue with the selected model (claude-haiku-4.5)"),
	}

	model, _ := m.Update(checksDiscoveredMsg{id: id, err: sf, missing: true})
	m = model.(*Shell)
	got := m.notice.text
	for _, want := range []string{"FD-002", engine.NoChecksRow, "claude-haiku-4.5", engine.NoChecksConsequence, "gummi-checks block"} {
		if !strings.Contains(got, want) {
			t.Errorf("no-checks notice missing %q: %q", want, got)
		}
	}
	// a warning, not an error: it arrives in the wake of an approval that
	// succeeded, and an error there reads to a web answer as its refusal
	if m.notice.isErr {
		t.Error("the no-checks notice is raised as an error")
	}

	m.notice = noticeMsg{}
	model, _ = m.Update(checksDiscoveredMsg{id: id, err: &engine.ScribeFailure{Pass: "check discovery", Model: "claude-haiku-4.5", Err: sf.Err}})
	m = model.(*Shell)
	if m.notice.text != "" {
		t.Errorf("the same scribe failure was said twice on one card: %q", m.notice.text)
	}

	// a different card's first failure is still said
	model, _ = m.Update(checksDiscoveredMsg{id: "FD-003", err: &engine.ScribeFailure{Pass: "check discovery", Model: "claude-haiku-4.5", Err: sf.Err}})
	m = model.(*Shell)
	if !strings.Contains(m.notice.text, "FD-003") || !strings.Contains(m.notice.text, "profiles.yaml") {
		t.Errorf("another card's scribe failure = %q, want it said with the fix", m.notice.text)
	}
}

// A research verify the document floor failed says so, instead of
// pointing at a finding in the document that nobody wrote.
func TestAResearchVerifyFailedByTheFloorSaysWhich(t *testing.T) {
	in := nextInput{
		stage: domain.StageVerify, kind: domain.KindResearch, verdict: verdictFail,
		verdictFloorReason: "the document floor failed — 0 open threads, 0 broken citations, 5 unmapped questions",
	}
	got := verifyStopped(in, "research document")
	if !strings.Contains(got, "5 unmapped questions") || !strings.Contains(got, "send it back") {
		t.Errorf("floor-failed research verify = %q", got)
	}
}

// The document-floor refusal names what is unmapped and the rule, and
// speaks no terminal keys.
func TestDocFloorRefusalIsActionable(t *testing.T) {
	f := domain.Feature{ID: "RS-003", Kind: domain.KindResearch}
	rep := verifydoc.Report{Coverage: []verifydoc.CoverageIssue{
		{Item: "Does Lines count the last line?"}, {Item: "q2"}, {Item: "q3"}, {Item: "q4"}, {Item: "q5"},
	}}
	got := docFloorRefusal(f, rep)
	for _, want := range []string{"RS-003", "5 unmapped questions", "Does Lines count the last line?", "(+2 more)", "## Slices", "requirements", "## Out of scope", "send it back"} {
		if !strings.Contains(got, want) {
			t.Errorf("refusal missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "(s)") {
		t.Errorf("refusal still counts in (s): %s", got)
	}
}
