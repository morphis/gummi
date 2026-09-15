package verifydoc

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
)

// survey is the research layout every test here reads under unless it is
// specifically about the diagnosis one.
var survey = spec.LayoutFor(domain.ModeSurvey)

func TestNoOpenThreads(t *testing.T) {
	open := "# RS-001: doc\n\nSome content line.\n%% @user: what about X?\n"
	r := Check(open, nil, survey)
	if r.OpenThreads != 1 {
		t.Errorf("OpenThreads = %d, want 1", r.OpenThreads)
	}
	if r.Pass() {
		t.Error("Pass() = true with one open user thread, want false")
	}

	none := "# RS-001: doc\n\nSome content line.\n"
	r = Check(none, nil, survey)
	if r.OpenThreads != 0 {
		t.Errorf("OpenThreads = %d, want 0", r.OpenThreads)
	}
	if !r.Pass() {
		t.Error("Pass() = false with no open threads and no other sections, want true")
	}
}

func citationFixture() (string, map[string][]string) {
	artifact := "# RS-001: doc\n\n## Findings\n\n" +
		"The loop lives at `internal/foo.go:4`\n" +
		"```go\n" +
		"return 42\n" +
		"```\n" +
		"Missing file citation `internal/missing.go:1` has no snippet.\n" +
		"Out of range citation `internal/foo.go:99` has no snippet either.\n" +
		"Altered snippet citation `internal/foo.go:3`\n" +
		"```go\n" +
		"func Baz() int {\n" +
		"```\n" +
		"Escaping path citation `../secret.go:1` has no snippet.\n"
	files := map[string][]string{
		"internal/foo.go": {
			"package foo",
			"",
			"func Bar() int {",
			"return 42",
			"}",
		},
	}
	return artifact, files
}

func TestCitations(t *testing.T) {
	artifact, files := citationFixture()
	r := Check(artifact, files, survey)
	if len(r.Citations) != 4 {
		t.Fatalf("Citations = %+v, want 4 issues", r.Citations)
	}
	want := map[string]string{
		"`internal/missing.go:1`": "file not found",
		"`internal/foo.go:99`":    "out of range",
		"`internal/foo.go:3`":     "no longer matches",
		"`../secret.go:1`":        "escapes",
	}
	for _, issue := range r.Citations {
		reasonWant, ok := want[issue.Citation]
		if !ok {
			t.Errorf("unexpected citation issue: %+v", issue)
			continue
		}
		if !strings.Contains(issue.Reason, reasonWant) {
			t.Errorf("citation %s: reason %q does not contain %q", issue.Citation, issue.Reason, reasonWant)
		}
		delete(want, issue.Citation)
	}
	if len(want) != 0 {
		t.Errorf("missing expected citation issues: %+v", want)
	}
}

func TestCitationsPassingFixtureHasNoIssues(t *testing.T) {
	_, files := citationFixture()
	// isolate the passing citation from the four failing ones
	only := "# RS-001: doc\n\n## Findings\n\n" +
		"The loop lives at `internal/foo.go:4`\n" +
		"```go\n" +
		"return 42\n" +
		"```\n"
	r := Check(only, files, survey)
	if len(r.Citations) != 0 {
		t.Errorf("Citations = %+v, want none for a passing citation", r.Citations)
	}
}

func coverageFixture(thirdQuestionAnswered bool) string {
	third := "- unmapped stray question?\n%% @gummi: open question from the brief\n\n"
	if thirdQuestionAnswered {
		third = ""
	}
	return "# RS-001: doc\n\n## Questions\n\n" +
		"- max concurrency supported?\n%% @gummi: open question from the brief\n\n" +
		"- cache invalidation model?\n%% @gummi: open question from the brief\n\n" +
		third +
		"## Slices\n\n" +
		"```yaml\n" +
		"- title: concurrency slice\n" +
		"  one-liner: caps it\n" +
		"  depends-on: []\n" +
		"  requirements:\n" +
		"    - max concurrency supported?\n" +
		"  id: \"\"\n" +
		"```\n\n" +
		"## Out of scope\n\n" +
		"- cache invalidation model?: deferred to a future research pass\n"
}

func TestCoverage(t *testing.T) {
	// mapped-via-slice and mapped-via-out-of-scope both pass
	pass := coverageFixture(true)
	r := Check(pass, nil, survey)
	if len(r.Coverage) != 0 {
		t.Errorf("Coverage = %+v, want none (both questions mapped)", r.Coverage)
	}

	// the third, unmapped question fails loudly
	fail := coverageFixture(false)
	r = Check(fail, nil, survey)
	if len(r.Coverage) != 1 {
		t.Fatalf("Coverage = %+v, want exactly 1 unmapped issue", r.Coverage)
	}
	if r.Coverage[0].Item != "unmapped stray question?" {
		t.Errorf("Coverage[0].Item = %q, want the unmapped question named", r.Coverage[0].Item)
	}
}

// TestHardFailParity: a broken citation and an unmapped question are
// equal-severity hard failures — both fail Pass().
func TestHardFailParity(t *testing.T) {
	_, files := citationFixture()
	citeOnly := "# RS-001: doc\n\n## Findings\n\nBroken cite `internal/missing.go:1` here.\n"
	r := Check(citeOnly, files, survey)
	if r.Pass() {
		t.Error("a broken citation must fail Pass()")
	}

	covOnly := coverageFixture(false)
	r = Check(covOnly, nil, survey)
	if r.Pass() {
		t.Error("an unmapped question must fail Pass()")
	}
}

// TestCitedPaths: the distinct paths named by Findings citations, in
// document order, regardless of whether each citation ultimately resolves
// — engine callers use this to build the files map before running Check.
func TestCitedPaths(t *testing.T) {
	artifact, _ := citationFixture()
	got := CitedPaths(artifact, survey)
	want := []string{"internal/foo.go", "internal/missing.go", "../secret.go"}
	if len(got) != len(want) {
		t.Fatalf("CitedPaths = %v, want %v", got, want)
	}
	for i, p := range want {
		if got[i] != p {
			t.Errorf("CitedPaths[%d] = %q, want %q", i, got[i], p)
		}
	}
}

// TestSnippetSurvivesShift: a snippet that still appears in the file,
// just not at the cited line, resolves — the check is content-based, not
// line-number based.
func TestSnippetSurvivesShift(t *testing.T) {
	files := map[string][]string{
		"internal/foo.go": {
			"package foo",
			"",
			"",
			"func Bar() int {",
			"return 42",
			"}",
		},
	}
	// cited at line 4 ("func Bar() int {"), but the snippet is the body
	// that now sits at line 5 after a rebase shifted everything down.
	artifact := "# RS-001: doc\n\n## Findings\n\n" +
		"Cite `internal/foo.go:4`\n" +
		"```go\n" +
		"return 42\n" +
		"```\n"
	r := Check(artifact, files, survey)
	if len(r.Citations) != 0 {
		t.Errorf("Citations = %+v, a shifted-but-present snippet must not be flagged", r.Citations)
	}
}

// diagnosis is the other layout: citations under Evidence, the checklist
// over Causes.
var diagnosis = spec.LayoutFor(domain.ModeDiagnosis)

const diagDoc = "# RS-009: picker drops answers\n\n" +
	"## Evidence\n\n" +
	"The picker delivers the label verbatim at `ui/decision.go:2`.\n\n" +
	"## Causes\n\n" +
	"- an unlabelled option is an unanswerable question\n" +
	"- did not fit is indistinguishable from does not exist\n\n" +
	"## Slices\n\n" +
	"```yaml\n" +
	"- title: reject unlabelled options at the boundary\n" +
	"  kind: bug\n" +
	"  requirements: [an unlabelled option is an unanswerable question]\n" +
	"```\n\n" +
	"## Out of scope\n\n" +
	"- did not fit is indistinguishable from does not exist: narrow trigger, tracked separately\n"

// TestDiagnosisLayoutReadsItsOwnSections: the same two checks, pointed at
// the diagnosis document's headings. The survey layout finds nothing in
// this document, which is the point — the layout, not the reader, decides.
func TestDiagnosisLayoutReadsItsOwnSections(t *testing.T) {
	files := map[string][]string{"ui/decision.go": {"package ui", "func decisionAnswerText() {}"}}
	if got := CitedPaths(diagDoc, diagnosis); len(got) != 1 || got[0] != "ui/decision.go" {
		t.Fatalf("CitedPaths under the diagnosis layout = %v, want [ui/decision.go]", got)
	}
	if got := CitedPaths(diagDoc, survey); got != nil {
		t.Errorf("the survey layout should find no Findings section here, got %v", got)
	}
	if r := Check(diagDoc, files, diagnosis); !r.Pass() {
		t.Errorf("a diagnosis whose causes are all settled should pass: %+v", r)
	}
}

// TestDiagnosisCoverageNeedsEveryCauseSettled: a cause with neither a fix
// slice nor an out-of-scope line is exactly the failure this check exists
// for — the diagnosis found something and then dropped it.
func TestDiagnosisCoverageNeedsEveryCauseSettled(t *testing.T) {
	files := map[string][]string{"ui/decision.go": {"package ui", "func decisionAnswerText() {}"}}
	unsettled := strings.Replace(diagDoc,
		"- did not fit is indistinguishable from does not exist: narrow trigger, tracked separately\n", "", 1)
	r := Check(unsettled, files, diagnosis)
	if len(r.Coverage) != 1 {
		t.Fatalf("coverage issues = %+v, want the second cause unsettled", r.Coverage)
	}
	if r.Coverage[0].Item != "did not fit is indistinguishable from does not exist" {
		t.Errorf("unsettled item = %q", r.Coverage[0].Item)
	}
}

// TestDiagnosisCitationsStillResolve: the citation check is the same code,
// so a stale citation in Evidence fails exactly as one in Findings does.
func TestDiagnosisCitationsStillResolve(t *testing.T) {
	r := Check(diagDoc, map[string][]string{}, diagnosis)
	if len(r.Citations) != 1 || !strings.Contains(r.Citations[0].Reason, "file not found") {
		t.Fatalf("citations = %+v, want one unresolved", r.Citations)
	}
}
