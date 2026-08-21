package domain

import "testing"

func TestRoundKindValid(t *testing.T) {
	cases := []struct {
		k    RoundKind
		want bool
	}{
		{RoundKindPlan, true},
		{RoundKindReview, true},
		{RoundKind(""), false},
		{RoundKind("bogus"), false},
	}
	for _, c := range cases {
		if got := c.k.Valid(); got != c.want {
			t.Errorf("RoundKind(%q).Valid() = %v, want %v", c.k, got, c.want)
		}
	}
}

func TestRoundKindStoredForm(t *testing.T) {
	if string(RoundKindPlan) != "plan" {
		t.Errorf("RoundKindPlan stored form = %q, want %q", RoundKindPlan, "plan")
	}
	if string(RoundKindReview) != "review" {
		t.Errorf("RoundKindReview stored form = %q, want %q", RoundKindReview, "review")
	}
}
