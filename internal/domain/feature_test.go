package domain

import (
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
	valid := []string{"FD-001", "FD-042", "FD-1234"}
	for _, s := range valid {
		if _, err := ParseFeatureID(s); err != nil {
			t.Errorf("ParseFeatureID(%q) = %v, want nil", s, err)
		}
	}
	invalid := []string{
		"", "FD-", "FD-42", "fd-042", "FD-04a", "FD-042 ", " FD-042",
		"FD--042", "FD-042/../evil", "XX-042", "FD-042\n",
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
		"neg spent":       func(f *Feature) { f.Budget.Spent = -5 },
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
		StageTodo:       SuperTodo,
		StageBrainstorm: SuperInProgress,
		StageSpec:       SuperInProgress,
		StagePlan:       SuperInProgress,
		StageImplement:  SuperInProgress,
		StageReview:     SuperReviewVerify,
		StageVerify:     SuperReviewVerify,
		StageDone:       SuperDone,
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
