package spec

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
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

func TestSeededBugTemplate_DiscussionSection(t *testing.T) {
	f := &domain.Feature{ID: "BG-020", Num: 20, Kind: domain.KindBug, Title: "Login loop", Slug: "login-loop", Stage: domain.StageTodo}
	r := domain.BugReport{
		Discussion: "**alice:** likely the SAML session cookie\n\n**bob:** repros on Chrome 120 too",
	}
	out := SeededBugTemplate(f, r, domain.BugProvenance{Source: "github"}, "")

	// Discussion renders with its content, positioned between Environment
	// and Root cause, and only when non-empty.
	envIdx := strings.Index(out, "## Environment")
	discIdx := strings.Index(out, "## Discussion")
	rootIdx := strings.Index(out, "## Root cause")
	if envIdx < 0 || discIdx < 0 || rootIdx < 0 {
		t.Fatalf("missing section\n---\n%s", out)
	}
	if envIdx >= discIdx || discIdx >= rootIdx {
		t.Errorf("Discussion must sit between Environment and Root cause\n---\n%s", out)
	}
	if !strings.Contains(out, "likely the SAML session cookie") || !strings.Contains(out, "**bob:** repros") {
		t.Errorf("Discussion content missing\n---\n%s", out)
	}
}

// TestSeededBugTemplate_ManualProvenanceHidden covers REVIEW
// §3.6 (2026-09-10 round-2 UX drive): a bug typed by hand into the
// new-card dialog carries Source: "manual", and rendering "Reported via
// manual" back at the person who just typed it is noise, not
// information — they already know where it came from. Severity is
// still worth showing when the person set one: it's triage information,
// not a provenance claim.
func TestSeededBugTemplate_ManualProvenanceHidden(t *testing.T) {
	f := &domain.Feature{ID: "BG-002", Num: 2, Kind: domain.KindBug, Title: "Off by one", Slug: "off-by-one", Stage: domain.StageTodo}
	r := domain.BugReport{Description: "tally count reports one character too many"}

	out := SeededBugTemplate(f, r, domain.BugProvenance{Source: "manual"}, "")
	if strings.Contains(out, "Reported via") {
		t.Errorf("manual bug report should not carry a provenance line\n---\n%s", out)
	}

	// severity still renders on a manual report, and without the blank
	// "provenance line" connector since there is no provenance line above it.
	withSev := SeededBugTemplate(f, r, domain.BugProvenance{Source: "manual"}, domain.SeverityHigh)
	if strings.Contains(withSev, "Reported via") {
		t.Errorf("manual bug report should not carry a provenance line\n---\n%s", withSev)
	}
	if !strings.Contains(withSev, "> Severity: high") {
		t.Errorf("manual bug report with a severity should still render it\n---\n%s", withSev)
	}
}

func TestSeededBugTemplate_NoDiscussionSection(t *testing.T) {
	f := &domain.Feature{ID: "BG-020", Num: 20, Kind: domain.KindBug, Title: "Login loop", Slug: "login-loop", Stage: domain.StageTodo}
	out := SeededBugTemplate(f, domain.BugReport{Description: "something broke"}, domain.BugProvenance{}, "")
	if strings.Contains(out, "## Discussion") {
		t.Errorf("empty Discussion must not render a heading\n---\n%s", out)
	}
}
