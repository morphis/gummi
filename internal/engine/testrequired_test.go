package engine

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

func stageText(t *testing.T, kind domain.Kind, stage domain.Stage) string {
	t.Helper()
	f := domain.Feature{ID: "FD-001", Kind: kind, Stage: stage}
	return strings.Join(stageHints(f, "/tmp/spec.md", "", flavorStage), "\n\n")
}

// TestFeatureImplementRequiresATest: a bug card has always been told to
// land a regression test and that Verify requires it. A feature card was
// told only to "run the relevant checks as you go", and a cheap model read
// that as permission to ship no test at all — which then passed review and
// verify, because nothing downstream asked either.
func TestFeatureImplementRequiresATest(t *testing.T) {
	text := stageText(t, domain.KindFeature, domain.StageImplement)
	if !strings.Contains(text, "Add automated tests for the behavior you add") {
		t.Errorf("the feature implement contract does not ask for tests:\n%s", text)
	}
	if !strings.Contains(text, "the Verify stage requires them") {
		t.Error("the feature implement contract does not say Verify will hold it to them")
	}
	// the escape hatch must stay open and stay explicit: a genuinely
	// untestable seam is a finding, not a silent pass
	if !strings.Contains(text, "cannot be tested at any existing seam") {
		t.Error("no stated route for behavior with no test seam")
	}
}

// And Verify must actually ask, or the implement-side promise is empty.
func TestFeatureVerifyChecksForATest(t *testing.T) {
	text := stageText(t, domain.KindFeature, domain.StageVerify)
	// the contract is wrapped prose; match within a line, not across one
	if !strings.Contains(text, "repo's own test command runs") {
		t.Errorf("verify never asks whether the behavior is covered by a test:\n%s", text)
	}
	if !strings.Contains(text, "that is a fail with the gap named") {
		t.Error("verify does not say an uncovered feature fails")
	}
}

// The bug contract already carried this and must keep carrying it.
func TestBugImplementStillRequiresARegressionTest(t *testing.T) {
	if !strings.Contains(stageText(t, domain.KindBug, domain.StageImplement), "regression test") {
		t.Error("the bug fix contract lost its regression-test requirement")
	}
}
