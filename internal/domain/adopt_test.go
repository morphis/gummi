package domain

import (
	"strings"
	"testing"
)

func adopted(branch string) Feature {
	return Feature{
		ID: "FD-042", Num: 42, Kind: KindFeature, Title: "Rework", Slug: "rework",
		Stage: StageTodo, BranchScheme: BranchSchemeAdopted, Branch: branch,
	}
}

func TestAdoptedBranchNameIsStoredNotDerived(t *testing.T) {
	f := adopted("feat/somebody-elses-name")
	if got := f.BranchName(); got != "feat/somebody-elses-name" {
		t.Errorf("BranchName = %q, want the ref verbatim", got)
	}
	if !f.Adopted() {
		t.Error("Adopted() = false on a card minted onto a branch")
	}
	// Renaming the card must not rename the branch: the ref exists in
	// checkouts this process cannot see.
	f.Slug, f.Title = "something-else", "Something else"
	if got := f.BranchName(); got != "feat/somebody-elses-name" {
		t.Errorf("BranchName = %q after a retitle, want the adopted ref unchanged", got)
	}

	ordinary := Feature{ID: "FD-043", Kind: KindFeature, Slug: "rework", BranchScheme: BranchSchemeKind}
	if ordinary.Adopted() {
		t.Error("Adopted() = true on a card gummi cut a branch for")
	}
}

func TestValidateAdoptedCard(t *testing.T) {
	for _, tc := range []struct {
		name string
		f    Feature
		want string
	}{
		{"a well-formed adoption", adopted("feat/theirs"), ""},
		{"no branch at all", adopted(""), "empty"},
		{"an option, not a ref", adopted("--force"), "dash"},
		{"revision syntax", adopted("feat/theirs..main"), "revision syntax"},
		{"characters git refuses", adopted("feat/their branch"), "refuses"},
	} {
		err := tc.f.Validate()
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: Validate = %v, want nil", tc.name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: Validate = %v, want one mentioning %q", tc.name, err, tc.want)
		}
	}

	// Adopting the branch the card lands on would put the implementer's
	// commits straight onto it, with no diff to review and nothing to merge.
	onBase := adopted("main")
	onBase.Base = "main"
	if err := onBase.Validate(); err == nil || !strings.Contains(err.Error(), "lands on") {
		t.Errorf("adopting the base: err = %v, want a refusal", err)
	}

	// A research card runs in a detached scratch tree and never holds a branch.
	rs := adopted("feat/theirs")
	rs.ID, rs.Num, rs.Kind = "RS-042", 42, KindResearch
	if err := rs.Validate(); err == nil || !strings.Contains(err.Error(), "no branch") {
		t.Errorf("a research card adopting a branch: err = %v, want a refusal", err)
	}

	// The inverse: a derived-name card must not carry a stored branch, or
	// two fields would disagree about what the card's branch is called.
	stray := Feature{ID: "FD-044", Num: 44, Kind: KindFeature, Title: "Stray", Slug: "stray", Stage: StageTodo, BranchScheme: BranchSchemeKind, Branch: "feat/stray"}
	if err := stray.Validate(); err == nil || !strings.Contains(err.Error(), "derived") {
		t.Errorf("a derived card carrying a branch: err = %v, want a refusal", err)
	}
}

func TestStalenessSaysWhatGummiWillNotDo(t *testing.T) {
	for _, tc := range []struct {
		behind int
		want   string
	}{
		{0, "up to date with main"},
		{1, "1 commit behind main — gummi will not rebase it for you"},
		{40, "40 commits behind main — gummi will not rebase it for you"},
	} {
		w := &AdoptedWork{Branch: "feat/theirs", Base: "main", Behind: tc.behind}
		if got := w.Staleness(); got != tc.want {
			t.Errorf("behind %d: %q, want %q", tc.behind, got, tc.want)
		}
	}
	var nilWork *AdoptedWork
	if !nilWork.Empty() {
		t.Error("a nil AdoptedWork is not Empty")
	}
	if got := (&AdoptedWork{Behind: 2}).Staleness(); !strings.Contains(got, "its base") {
		t.Errorf("staleness with no base named = %q", got)
	}
}
