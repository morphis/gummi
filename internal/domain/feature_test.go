package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestNewFeatureID(t *testing.T) {
	cases := []struct {
		n    int
		want FeatureID
		err  bool
	}{
		{1, "FD-001", false},
		{42, "FD-042", false},
		{999, "FD-999", false},
		{1000, "FD-1000", false},
		{0, "", true},
		{-3, "", true},
	}
	for _, c := range cases {
		got, err := NewFeatureID(c.n)
		if (err != nil) != c.err {
			t.Errorf("NewFeatureID(%d) error = %v, want err=%v", c.n, err, c.err)
			continue
		}
		if got != c.want {
			t.Errorf("NewFeatureID(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestParseFeatureID(t *testing.T) {
	valid := []string{"FD-001", "FD-042", "FD-1234", "RS-007", "RS-1234"}
	for _, s := range valid {
		if _, err := ParseFeatureID(s); err != nil {
			t.Errorf("ParseFeatureID(%q) = %v, want nil", s, err)
		}
	}
	id, err := ParseFeatureID("RS-007")
	if err != nil {
		t.Fatalf("ParseFeatureID(\"RS-007\") errored: %v", err)
	}
	if id.Kind() != KindResearch {
		t.Error("ParseFeatureID(\"RS-007\").Kind() should be KindResearch")
	}
	invalid := []string{
		"", "FD-", "FD-42", "fd-042", "FD-04a", "FD-042 ", " FD-042",
		"FD--042", "FD-042/../evil", "XX-042", "FD-042\n",
		"rs-007", "RS-42", "RS-",
	}
	for _, s := range invalid {
		if _, err := ParseFeatureID(s); err == nil {
			t.Errorf("ParseFeatureID(%q) = nil error, want error", s)
		}
	}
}

func TestSlugify(t *testing.T) {
	cases := []struct {
		title string
		want  string
		err   bool
	}{
		{"Dark mode toggle", "dark-mode-toggle", false},
		{"  CSV   export!!  ", "csv-export", false},
		{"Fix: auth/session bug #42", "fix-auth-session-bug-42", false},
		{"UPPER lower 123", "upper-lower-123", false},
		{"../../etc/passwd", "etc-passwd", false},
		{"$(rm -rf /)", "rm-rf", false},
		{"`touch pwned`; git push --force", "touch-pwned-git-push-force", false},
		{"---", "", true},
		{"", "", true},
		{"日本語のみ", "", true},
		{strings.Repeat("very long title ", 10), "very-long-title-very-long-title-very-lon", false},
	}
	for _, c := range cases {
		got, err := Slugify(c.title)
		if (err != nil) != c.err {
			t.Errorf("Slugify(%q) error = %v, want err=%v", c.title, err, c.err)
			continue
		}
		if got != c.want {
			t.Errorf("Slugify(%q) = %q, want %q", c.title, got, c.want)
		}
		if got != "" {
			if verr := ValidateSlug(got); verr != nil {
				t.Errorf("Slugify(%q) = %q fails its own validation: %v", c.title, got, verr)
			}
		}
	}
}

func TestValidateSlug(t *testing.T) {
	bad := []string{"", "-lead", "trail-", "a--b", "UPPER", "sp ace", "sl/ash", "dot.dot", "a" + strings.Repeat("b", 41)}
	for _, s := range bad {
		if err := ValidateSlug(s); err == nil {
			t.Errorf("ValidateSlug(%q) = nil, want error", s)
		}
	}
	good := []string{"a", "dark-mode", "x1-y2-z3"}
	for _, s := range good {
		if err := ValidateSlug(s); err != nil {
			t.Errorf("ValidateSlug(%q) = %v, want nil", s, err)
		}
	}
}

func testFeature() Feature {
	now := time.Now()
	return Feature{
		ID: "FD-042", Num: 42, Title: "Dark mode", OneLiner: "toggle",
		Slug: "dark-mode", Stage: StageTodo, Profile: "thrifty",
		CreatedAt: now, UpdatedAt: now,
	}
}

func TestFeatureValidate(t *testing.T) {
	f := testFeature()
	if err := f.Validate(); err != nil {
		t.Fatalf("valid feature rejected: %v", err)
	}
	mutations := map[string]func(*Feature){
		"bad id":          func(f *Feature) { f.ID = "FD-42" },
		"id/num mismatch": func(f *Feature) { f.Num = 7 },
		"empty title":     func(f *Feature) { f.Title = "  " },
		"bad slug":        func(f *Feature) { f.Slug = "../evil" },
		"bad stage":       func(f *Feature) { f.Stage = "shipping" },
		"neg envelope":    func(f *Feature) { f.Budget.Envelope = -1 },
	}
	for name, mut := range mutations {
		g := testFeature()
		mut(&g)
		if err := g.Validate(); err == nil {
			t.Errorf("%s: Validate() = nil, want error", name)
		}
	}
}

func TestDerivedPaths(t *testing.T) {
	f := testFeature()
	if got, want := f.BranchName(), "gummi/FD-042-dark-mode"; got != want {
		t.Errorf("BranchName() = %q, want %q", got, want)
	}
	if got, want := f.WorktreePath(), ".gummi/worktrees/FD-042"; got != want {
		t.Errorf("WorktreePath() = %q, want %q", got, want)
	}
	if got, want := f.SpecPath(), ".gummi/specs/FD-042-dark-mode.md"; got != want {
		t.Errorf("SpecPath() = %q, want %q", got, want)
	}
}

func TestStageSuperState(t *testing.T) {
	want := map[Stage]SuperState{
		StageTodo:        SuperTodo,
		StageInvestigate: SuperResearch,
		StageShape:       SuperResearch,
		StageBrainstorm:  SuperInProgress,
		StageSpec:        SuperInProgress,
		StagePlan:        SuperInProgress,
		StageTriage:      SuperInProgress,
		StageDiagnose:    SuperInProgress,
		StageFix:         SuperInProgress,
		StageImplement:   SuperInProgress,
		StageReview:      SuperReviewVerify,
		StageVerify:      SuperReviewVerify,
		StageDone:        SuperDone,
	}
	if len(want) != len(Stages) {
		t.Fatalf("test table covers %d stages, domain has %d", len(want), len(Stages))
	}
	for s, ss := range want {
		if got := s.SuperState(); got != ss {
			t.Errorf("%s.SuperState() = %q, want %q", s, got, ss)
		}
	}
	if Stage("bogus").Valid() {
		t.Error(`Stage("bogus").Valid() = true, want false`)
	}
}

func TestDeriveTitle(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Dark mode toggle", "Dark mode toggle"},
		{"Add a healthz endpoint. It returns status and version.", "Add a healthz endpoint"},
		{"  spaced   out   words  ", "spaced out words"},
		{"Support idempotency keys on transfers so retried requests never double-charge the account", "Support idempotency keys on transfers so retried requests…"},
		{"Bump to v1.2.3 across the board", "Bump to v1.2.3 across the board"},
		{"", ""},
	}
	for _, c := range cases {
		if got := DeriveTitle(c.in); got != c.want {
			t.Errorf("DeriveTitle(%q) = %q, want %q", c.in, got, c.want)
		}
		if len(DeriveTitle(c.in)) > maxTitleLen+len("…") {
			t.Errorf("DeriveTitle(%q) too long: %q", c.in, DeriveTitle(c.in))
		}
	}
}

func TestSplitDescription(t *testing.T) {
	// a short description is its own title with no separate one-liner
	title, oneLiner := SplitDescription("Dark mode toggle")
	if title != "Dark mode toggle" || oneLiner != "" {
		t.Errorf("short desc split = (%q, %q)", title, oneLiner)
	}
	// a long one keeps the full text as the one-liner
	full := "Add a healthz endpoint. It returns status and version for the load balancer."
	title, oneLiner = SplitDescription(full)
	if title != "Add a healthz endpoint" || oneLiner != full {
		t.Errorf("long desc split = (%q, %q)", title, oneLiner)
	}
}

func TestSplitFreeform(t *testing.T) {
	// a single line splits exactly as SplitDescription and seeds nothing
	full := "Add a healthz endpoint. It returns status and version for the load balancer."
	title, oneLiner, seed := SplitFreeform(full)
	if title != "Add a healthz endpoint" || oneLiner != full || seed != "" {
		t.Errorf("single-line split = (%q, %q, %q)", title, oneLiner, seed)
	}

	// more lines: title from the first, the whole text as seed, verbatim
	desc := "Dark mode toggle\n\nRespect the OS preference by default.\nPersist an explicit override in config."
	title, oneLiner, seed = SplitFreeform(desc)
	if title != "Dark mode toggle" || oneLiner != "" {
		t.Errorf("multi-line split = (%q, %q)", title, oneLiner)
	}
	if seed != desc {
		t.Errorf("seed = %q, want the description verbatim", seed)
	}

	// CRLF paste normalizes to plain newlines
	_, _, seed = SplitFreeform("Title line\r\nBody line\r\n")
	if seed != "Title line\nBody line" {
		t.Errorf("CRLF seed = %q", seed)
	}

	// trailing blank lines after the first are not a body
	if _, _, seed := SplitFreeform("Dark mode toggle\n\n  \n"); seed != "" {
		t.Errorf("whitespace-only body seeded %q", seed)
	}
}

// TestFeatureRepoValidate: the repo name is plain metadata — empty (the
// default) is always legal, a configured name is legal, and a whitespace or
// path-like name is rejected so it can never be mistaken for a path.
func TestFeatureRepoValidate(t *testing.T) {
	g := testFeature()
	g.Repo = ""
	if err := g.Validate(); err != nil {
		t.Errorf("empty repo should be legal: %v", err)
	}
	g.Repo = "lxd"
	if err := g.Validate(); err != nil {
		t.Errorf("a plain configured name should be legal: %v", err)
	}
	for _, bad := range []string{" lxd", "lxd ", "a/b", `a\b`, "../x"} {
		g.Repo = bad
		if err := g.Validate(); err == nil {
			t.Errorf("repo %q should be rejected", bad)
		}
	}
}

func TestPullRequestRefEmpty(t *testing.T) {
	if !(PullRequestRef{}).Empty() {
		t.Error("zero-value ref should read Empty()")
	}
	nonEmpty := []PullRequestRef{
		{Repo: "o/r"},
		{Number: 1},
		{URL: "https://github.com/o/r/pull/1"},
		{HeadSHA: "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b"},
	}
	for _, r := range nonEmpty {
		if r.Empty() {
			t.Errorf("ref %+v should read non-empty", r)
		}
	}
}

func TestPullRequestRefValidate(t *testing.T) {
	good := PullRequestRef{Repo: "o/r", Number: 42, URL: "https://github.com/o/r/pull/42", HeadSHA: "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b"}
	if err := good.Validate(); err != nil {
		t.Errorf("well-formed ref rejected: %v", err)
	}
	// HeadSHA is optional (unset until resolved).
	woSHA := good
	woSHA.HeadSHA = ""
	if err := woSHA.Validate(); err != nil {
		t.Errorf("ref with no HeadSHA should be legal: %v", err)
	}
	mutations := map[string]func(*PullRequestRef){
		"bare owner repo": func(r *PullRequestRef) { r.Repo = "o" },
		"empty repo":      func(r *PullRequestRef) { r.Repo = "" },
		"number zero":     func(r *PullRequestRef) { r.Number = 0 },
		"negative number": func(r *PullRequestRef) { r.Number = -1 },
		"non-github URL":  func(r *PullRequestRef) { r.URL = "https://example.com/o/r/pull/42" },
		"empty URL":       func(r *PullRequestRef) { r.URL = "" },
		"bad hex SHA":     func(r *PullRequestRef) { r.HeadSHA = "not-a-sha" },
		"short SHA":       func(r *PullRequestRef) { r.HeadSHA = "9f86d08" },
	}
	for name, mut := range mutations {
		r := good
		mut(&r)
		if err := r.Validate(); err == nil {
			t.Errorf("%s: Validate() = nil, want error", name)
		}
	}
}

func TestFeatureValidatePullRequest(t *testing.T) {
	f := testFeature()
	f.PullRequest = PullRequestRef{Repo: "o", Number: 1, URL: "https://github.com/o/r/pull/1"}
	if err := f.Validate(); err == nil {
		t.Error("feature with malformed linked PR should be rejected")
	}
	f.PullRequest = PullRequestRef{Repo: "o/r", Number: 1, URL: "https://github.com/o/r/pull/1"}
	if err := f.Validate(); err != nil {
		t.Errorf("feature with well-formed linked PR rejected: %v", err)
	}
}

func TestPullRequestRefPresenters(t *testing.T) {
	var empty PullRequestRef
	if got := empty.Badge(); got != "" {
		t.Errorf("empty.Badge() = %q, want \"\"", got)
	}
	if got := empty.PlainLine(); got != "" {
		t.Errorf("empty.PlainLine() = %q, want \"\"", got)
	}
	if got := empty.NextStepsHint(true); got != "" {
		t.Errorf("empty.NextStepsHint(true) = %q, want \"\"", got)
	}
	if got := empty.StatusPayload(); got != nil {
		t.Errorf("empty.StatusPayload() = %#v, want nil", got)
	}

	ref := PullRequestRef{Repo: "o/r", Number: 42, URL: "https://github.com/o/r/pull/42", HeadSHA: "abc123"}
	if got, want := ref.Badge(), "PR#42"; got != want {
		t.Errorf("Badge() = %q, want %q", got, want)
	}
	if got, want := ref.PlainLine(), "o/r#42"; got != want {
		t.Errorf("PlainLine() = %q, want %q", got, want)
	}
	if got, want := ref.NextStepsHint(true), "PR #42 — merge on GitHub, then pull main"; got != want {
		t.Errorf("NextStepsHint(true) = %q, want %q", got, want)
	}
	if got := ref.NextStepsHint(false); got != "" {
		t.Errorf("NextStepsHint(false) = %q, want \"\"", got)
	}
	b, err := json.Marshal(ref.StatusPayload())
	if err != nil {
		t.Fatalf("json.Marshal(StatusPayload()): %v", err)
	}
	if got, want := string(b), `{"repo":"o/r","number":42,"url":"https://github.com/o/r/pull/42","head_sha":"abc123"}`; got != want {
		t.Errorf("json.Marshal(StatusPayload()) = %s, want %s", got, want)
	}
}
