package domain

import "testing"

func TestNewIDByKind(t *testing.T) {
	if id, err := NewID(KindBug, 7); err != nil || id != "BG-007" {
		t.Errorf("NewID(bug, 7) = %q, %v; want BG-007", id, err)
	}
	if id, err := NewID(KindFeature, 42); err != nil || id != "FD-042" {
		t.Errorf("NewID(feature, 42) = %q, %v; want FD-042", id, err)
	}
	// empty kind reads as a feature (pre-bug items need no backfill)
	if id, err := NewID("", 1); err != nil || id != "FD-001" {
		t.Errorf("NewID(\"\", 1) = %q, %v; want FD-001", id, err)
	}
	if _, err := NewID(KindBug, 0); err == nil {
		t.Error("NewID with n<1 should error")
	}
}

func TestFeatureIDKind(t *testing.T) {
	if FeatureID("BG-007").Kind() != KindBug {
		t.Error("BG- prefix should read as a bug")
	}
	if FeatureID("FD-001").Kind() != KindFeature {
		t.Error("FD- prefix should read as a feature")
	}
}

func TestParseFeatureIDAcceptsBothKinds(t *testing.T) {
	for _, id := range []string{"FD-001", "BG-042", "FD-1000"} {
		if _, err := ParseFeatureID(id); err != nil {
			t.Errorf("ParseFeatureID(%q) errored: %v", id, err)
		}
	}
	for _, bad := range []string{"XX-001", "BG-1", "bug-001", "FD001", ""} {
		if _, err := ParseFeatureID(bad); err == nil {
			t.Errorf("ParseFeatureID(%q) should have errored", bad)
		}
	}
}

func TestBugValidateAndPaths(t *testing.T) {
	b := &Feature{ID: "BG-007", Num: 7, Kind: KindBug, Title: "Crash on empty input", Slug: "crash-on-empty-input", Stage: StageTodo}
	if err := b.Validate(); err != nil {
		t.Fatalf("valid bug rejected: %v", err)
	}
	if got := b.ArtifactPath(); got != ".gummi/bugs/BG-007-crash-on-empty-input.md" {
		t.Errorf("bug ArtifactPath = %q", got)
	}
	if got := b.BranchName(); got != "gummi/BG-007-crash-on-empty-input" {
		t.Errorf("bug BranchName = %q", got)
	}

	// a bug ID with a feature kind (or vice versa) is a corrupt row.
	mismatch := &Feature{ID: "BG-007", Num: 7, Kind: KindFeature, Title: "x", Slug: "x", Stage: StageTodo}
	if err := mismatch.Validate(); err == nil {
		t.Error("BG- id with feature kind should fail validation")
	}

	// features still route to the spec path.
	f := &Feature{ID: "FD-007", Num: 7, Title: "Dark mode", Slug: "dark-mode", Stage: StageTodo}
	if got := f.ArtifactPath(); got != f.SpecPath() {
		t.Errorf("feature ArtifactPath = %q, want SpecPath %q", got, f.SpecPath())
	}
}

func TestNormalizeSeverity(t *testing.T) {
	cases := map[string]Severity{
		"P0": SeverityCritical, "blocker": SeverityCritical, "SEV1": SeverityCritical,
		"high": SeverityHigh, "major": SeverityHigh,
		"medium": SeverityMedium, "normal": SeverityMedium,
		"low": SeverityLow, "trivial": SeverityLow,
		"needs-triage": "", "": "", "wishlist": "",
	}
	for in, want := range cases {
		if got := NormalizeSeverity(in); got != want {
			t.Errorf("NormalizeSeverity(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBugProposalSlugAndProvenance(t *testing.T) {
	p := BugProposal{Title: "Crash on Empty Input!", Source: "github", ExternalRef: "https://x/issues/9"}
	if s, err := p.Slug(); err != nil || s != "crash-on-empty-input" {
		t.Errorf("Slug() = %q, %v", s, err)
	}
	if prov := p.Provenance(); prov.Source != "github" || prov.ExternalRef != "https://x/issues/9" {
		t.Errorf("Provenance() = %+v", prov)
	}
	if (BugProvenance{}).Empty() != true {
		t.Error("zero provenance should be Empty")
	}
	if (BugProvenance{Source: "manual"}).Empty() {
		t.Error("provenance with a source is not Empty")
	}
}
