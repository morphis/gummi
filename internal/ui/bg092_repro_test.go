package ui

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/workflow"
)

// TestBG092NoWorktreeRefusalNamesAGateTheCardHas: the refusal a surface
// gives when it wants a worktree the card has not got used to say
// "(created at spec approval)" on every card. A bug card's route is
// triage → diagnose → fix and contains no spec at all, so the sentence
// sent that reader looking for a gate that was never coming.
//
// Asserted over the canonical list of kinds rather than the one the
// drive saw, and against domain.Kind.ArtifactNoun rather than a repeated
// literal — the document the gate approves is what the rest of each
// kind's surfaces already call it.
func TestBG092NoWorktreeRefusalNamesAGateTheCardHas(t *testing.T) {
	for _, k := range []domain.Kind{domain.KindFeature, domain.KindBug, domain.KindResearch} {
		id, err := domain.NewID(k, 7)
		if err != nil {
			t.Fatal(err)
		}
		got := noWorktreeYet(domain.Feature{ID: id, Kind: k})
		if !strings.Contains(got, string(id)) {
			t.Errorf("%s: refusal %q does not name the card", k, got)
		}
		if !strings.Contains(got, k.ArtifactNoun()) {
			t.Errorf("%s: refusal %q does not name the artifact whose approval creates the worktree (%q)",
				k, got, k.ArtifactNoun())
		}
		// the specific regression: the feature word on a kind that is not
		// a feature. ArtifactNoun's own default is "spec", so this only
		// bites the kinds that have their own word.
		if k != domain.KindFeature && strings.Contains(got, "spec") {
			t.Errorf("%s: refusal %q still names the feature's gate", k, got)
		}
	}
}

// TestBG092RefusalReachesEveryStageThatCanRaiseIt is the other half: the
// message is only ever shown for a card whose worktree is still ahead of
// it, so every kind that can reach a worktree at all must produce a
// sentence naming a stage on its own route. This asserts the premise the
// wording rests on — that approving the artifact is what creates the
// worktree — rather than the wording itself.
func TestBG092RefusalReachesEveryStageThatCanRaiseIt(t *testing.T) {
	for _, k := range []domain.Kind{domain.KindFeature, domain.KindBug} {
		var firstWT domain.Stage
		for _, st := range domain.Stages {
			if workflow.NeedsWorktree(k, st) {
				firstWT = st
				break
			}
		}
		if firstWT == "" {
			t.Fatalf("%s reaches no worktree stage at all", k)
		}
		if workflow.Interactive(firstWT) {
			t.Errorf("%s: first worktree stage %s is interactive — the worktree cannot be created by approving out of the design phase", k, firstWT)
		}
	}
}
