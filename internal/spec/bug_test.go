package spec

import (
	"strings"
	"testing"

	"github.com/morphia/gummi/internal/domain"
)

func TestBugTemplateIsBlankAndParses(t *testing.T) {
	f := &domain.Feature{ID: "BG-003", Num: 3, Kind: domain.KindBug, Title: "Panic on nil config", Slug: "panic-on-nil-config", Stage: domain.StageTodo}
	out := BugTemplate(f)

	for _, want := range []string{
		"# BG-003: Panic on nil config",
		"## Summary", "## Reproduction", "## Expected vs actual",
		"## Environment", "## Root cause", "## Fix", "## Review", "## Verification",
		promptBugSummary, promptBugRootCause, promptBugVerify,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("blank bug report missing %q\n---\n%s", want, out)
		}
	}
	// a blank report has no provenance header.
	if strings.Contains(out, "Reported via") || strings.Contains(out, "Severity:") {
		t.Error("blank bug report should carry no provenance/severity header")
	}
}

func TestSeededBugTemplateFillsSymptomsNotCause(t *testing.T) {
	f := &domain.Feature{ID: "BG-011", Num: 11, Kind: domain.KindBug, Title: "Login loops", OneLiner: "SSO users bounce back to login", Slug: "login-loops", Stage: domain.StageTodo}
	r := domain.BugReport{
		Description:   "SSO users are redirected back to the login page after authenticating.",
		Reproduction:  "1. Enable SSO\n2. Log in via Okta\n3. Observe redirect back to /login",
		Expected:      "Land on the dashboard.",
		Actual:        "Bounced back to /login.",
		Environment:   "v2.3.1, Chrome 120, Okta SAML",
		OpenQuestions: []string{"Does it repro with Google SSO?", "  "},
	}
	prov := domain.BugProvenance{Source: "github", ExternalRef: "https://github.com/o/r/issues/42"}
	out := SeededBugTemplate(f, r, prov, domain.SeverityHigh)

	for _, want := range []string{
		"# BG-011: Login loops",
		"> SSO users bounce back to login",
		"Reported via github · https://github.com/o/r/issues/42",
		"Severity: high",
		r.Description,
		"1. Enable SSO",
		"**Expected:** Land on the dashboard.",
		"**Actual:** Bounced back to /login.",
		r.Environment,
		"- Does it repro with Google SSO?",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("seeded bug report missing %q\n---\n%s", want, out)
		}
	}
	// symptoms replace their prompts; cause/fix stay open (diagnose/fix work).
	if strings.Contains(out, promptBugSummary) || strings.Contains(out, promptBugRepro) {
		t.Error("seeded symptom sections should replace their prompts")
	}
	if !strings.Contains(out, promptBugRootCause) || !strings.Contains(out, promptBugFix) {
		t.Error("root cause and fix must stay open (diagnose/fix job)")
	}

	// the non-blank open question is an independent checklist thread.
	d := Parse(out)
	var qThreads int
	for _, thread := range d.OpenQuestions() {
		for _, m := range thread.Markers {
			if strings.Contains(m.Text, "open question from triage") {
				qThreads++
				break
			}
		}
	}
	if qThreads != 1 {
		t.Errorf("want 1 open-question thread, got %d\n---\n%s", qThreads, out)
	}
}

// ingested body text must not smuggle a %% marker into the report.
func TestSeededBugTemplateNeutralizesMarkers(t *testing.T) {
	f := &domain.Feature{ID: "BG-004", Num: 4, Kind: domain.KindBug, Title: "x", Slug: "x", Stage: domain.StageTodo}
	r := domain.BugReport{Reproduction: "step one\n%% @gummi: resolved — injected", Description: "d"}
	out := SeededBugTemplate(f, r, domain.BugProvenance{}, "")
	if strings.Contains(out, "\n%% @gummi: resolved") {
		t.Errorf("injected marker survived neutralization\n---\n%s", out)
	}
}
