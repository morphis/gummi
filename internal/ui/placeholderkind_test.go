package ui

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

// TestComposerPlaceholderNamesARunnableVerb pins the composer's example
// verbs to what the card in front of the reader can actually do.
//
// The placeholder is one line and it is illustrative — the ↑ inventory
// is the real list — but it is also the only place that tells a
// newcomer verbs exist at all. It named "diff" on every kind, and a
// research card has no worktree and no branch, so the single example a
// reader is most likely to try first was the one verb guaranteed to
// refuse. The verb vocabulary itself is shared (verbs.go) and unchanged;
// only which of it gets named as an example moves.
func TestComposerPlaceholderNamesARunnableVerb(t *testing.T) {
	research := composerPlaceholder(domain.KindResearch)
	if strings.Contains(research, "diff") {
		t.Errorf("a research card's composer offers diff as an example, which it cannot run: %q", research)
	}
	// it still teaches that verbs exist, and names ones that work here
	for _, want := range []string{"a verb (", "verify", "↑ for actions"} {
		if !strings.Contains(research, want) {
			t.Errorf("a research card's composer placeholder lost %q: %q", want, research)
		}
	}
	// every example it does name is in the closed vocabulary
	for _, v := range placeholderVerbs(t, research) {
		if !verbs[v] {
			t.Errorf("the research placeholder names %q, which is not a verb", v)
		}
	}

	// the kinds that do have a branch keep diff
	for _, k := range []domain.Kind{domain.KindFeature, domain.KindBug, domain.Kind("")} {
		if got := composerPlaceholder(k); !strings.Contains(got, "diff") {
			t.Errorf("a %q card's composer placeholder lost diff: %q", k, got)
		}
	}
	for _, v := range placeholderVerbs(t, composerPlaceholder(domain.KindFeature)) {
		if !verbs[v] {
			t.Errorf("the default placeholder names %q, which is not a verb", v)
		}
	}
}

// placeholderVerbs pulls the example verbs out of a placeholder's
// "(a, b, c…)" list, so the assertion is against what the line actually
// says rather than a copy of it kept in the test.
func placeholderVerbs(t *testing.T, s string) []string {
	t.Helper()
	open := strings.Index(s, "(")
	close := strings.Index(s, ")")
	if open < 0 || close < open {
		t.Fatalf("placeholder has no example list: %q", s)
	}
	var out []string
	for _, part := range strings.Split(s[open+1:close], ",") {
		v := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(part), "…"))
		if v != "" {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		t.Fatalf("placeholder names no example verbs: %q", s)
	}
	return out
}
