package engine

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/experiment"
)

// TestAnAssertionTheRunNeverMentionedIsNotOneThatFailed: an item bound to
// an assertion the experiment does not observe is unprovable, and it read
// exactly like an item that was merely not finished — the same sentence,
// word for word, as the items beside it that were met. Nobody, lead or
// owner, had anything to notice until the hand-over said "not met" about a
// statement no run could ever have proved.
func TestAnAssertionTheRunNeverMentionedIsNotOneThatFailed(t *testing.T) {
	r := experiment.Result{
		ID: "R1", Outcome: experiment.Fail, Dir: "/e/R1",
		Assertions: []experiment.Assertion{
			{ID: "ing-a-tor2-node1", OK: true},
			{ID: "ing-b-balance", OK: false, Detail: "reached 1 node"},
		},
	}

	// an item about an assertion that failed reads as it always did
	failed := describeEvidence(r, []string{"ing-b-balance"})
	if !strings.Contains(failed, "not held: ing-b-balance reached 1 node") {
		t.Fatalf("a failing assertion lost its detail: %q", failed)
	}
	if strings.Contains(failed, "NOT REPORTED") {
		t.Errorf("an assertion the run reported was called unreported: %q", failed)
	}

	// an item about one the run never mentioned says so, and says what it means
	silent := describeEvidence(r, []string{"ing-b-svc-10-0-9-200"})
	if !strings.Contains(silent, "NOT REPORTED by the run at all: ing-b-svc-10-0-9-200") {
		t.Fatalf("an assertion nothing observes is indistinguishable from one that failed: %q", silent)
	}
	if !strings.Contains(silent, "cannot hold as written") {
		t.Errorf("the sentence does not say what it means for the item: %q", silent)
	}
}
