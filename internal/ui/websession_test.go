package ui

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

// TestASpecsBriefSaysWhereItsWorkCameFrom: a spec written from a session is
// told which session it continues and that the work is already on its
// branch, and carries the handoff brief the writespec dialog handed in —
// the person's edited text, not a list re-derived at mint.
func TestASpecsBriefSaysWhereItsWorkCameFrom(t *testing.T) {
	id, _ := domain.NewID(domain.KindFreeform, 3)
	f := domain.Feature{ID: id, Num: 3, Kind: domain.KindFreeform, Title: "Fix the flaky retry", Slug: "fix-the-flaky-retry", BranchScheme: domain.BranchSchemeKind}
	brief := "asked\n- find why TestRetry flakes\n\ndecided\n- make the cap configurable\n\ndone\n- the loop retries twice now\n\nremaining\n- wire the cap through the config"
	got := specBrief(f, "Configurable sync retries", "a3f9c21deadbeef", brief)
	for _, want := range []string{
		"Configurable sync retries\n", "FF-003", f.BranchName(), "a3f9c21",
		"asked\n- find why TestRetry flakes", "make the cap configurable",
		"wire the cap through the config",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the brief does not carry %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "a3f9c21d") {
		t.Error("the brief names the full commit id rather than its short form")
	}
	if !strings.Contains(got, brief) {
		t.Errorf("the handed-in brief was re-derived or trimmed at mint:\n%s", got)
	}

	// an empty title falls back to the session's; an empty brief leaves
	// the continued-from paragraph to stand alone
	long := specBrief(f, "", "a3f9c21", "")
	if !strings.HasPrefix(long, f.Title+"\n") {
		t.Errorf("an empty title did not fall back to the session's:\n%s", long[:80])
	}
	if !strings.Contains(long, "Continued from the session FF-003") {
		t.Errorf("an empty brief lost the continued-from paragraph:\n%s", long)
	}
}
