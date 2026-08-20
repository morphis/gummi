package verifydoc

import (
	"strings"
	"testing"
)

func TestNoOpenThreads(t *testing.T) {
	open := "# RS-001: doc\n\nSome content line.\n%% @user: what about X?\n"
	r := Check(open, nil)
	if r.OpenThreads != 1 {
		t.Errorf("OpenThreads = %d, want 1", r.OpenThreads)
	}
	if r.Pass() {
		t.Error("Pass() = true with one open user thread, want false")
	}

	none := "# RS-001: doc\n\nSome content line.\n"
	r = Check(none, nil)
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
	r := Check(artifact, files)
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
	r := Check(only, files)
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
	r := Check(pass, nil)
	if len(r.Coverage) != 0 {
		t.Errorf("Coverage = %+v, want none (both questions mapped)", r.Coverage)
	}

	// the third, unmapped question fails loudly
	fail := coverageFixture(false)
	r = Check(fail, nil)
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
	r := Check(citeOnly, files)
	if r.Pass() {
		t.Error("a broken citation must fail Pass()")
	}

	covOnly := coverageFixture(false)
	r = Check(covOnly, nil)
	if r.Pass() {
		t.Error("an unmapped question must fail Pass()")
	}
}

// TestCitedPaths: the distinct paths named by Findings citations, in
// document order, regardless of whether each citation ultimately resolves
// — engine callers use this to build the files map before running Check.
func TestCitedPaths(t *testing.T) {
	artifact, _ := citationFixture()
	got := CitedPaths(artifact)
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
	r := Check(artifact, files)
	if len(r.Citations) != 0 {
		t.Errorf("Citations = %+v, a shifted-but-present snippet must not be flagged", r.Citations)
	}
}
