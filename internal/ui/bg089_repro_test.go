package ui

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
)

// hasAttach reports whether any of the offered actions is the raw-attach
// row.
func hasAttach(acts []nextAction) bool {
	for _, a := range acts {
		if a.id == "attach" {
			return true
		}
	}
	return false
}

// TestBG089AttachIsNotRecommendedWithoutAWorktree: the two arms that fire
// under a paused or failed run — the "choose what happens next" picker
// the card page shows, and the only two ways forward it offers there —
// recommended attaching the agent CLI on every card. Attaching runs the
// backend's own CLI inside the card's worktree, so on a card with no
// worktree the recommendation can only dead-end, and on a research card,
// which never gets one, it can never be anything else.
//
// The card's own action inventory has always asked exactly this question
// (cardactions.go gates its attach row on HasWorktree); the picker was
// the surface that fell behind.
func TestBG089AttachIsNotRecommendedWithoutAWorktree(t *testing.T) {
	states := []struct {
		name string
		in   nextInput
	}{
		{"paused", nextInput{sess: engine.StatePaused}},
		{"failed", nextInput{attn: attnFailure}},
	}
	for _, k := range []domain.Kind{domain.KindFeature, domain.KindBug, domain.KindResearch} {
		for _, st := range domain.Stages {
			for _, tc := range states {
				in := tc.in
				in.kind, in.stage = k, st
				in.hasWorktree = false
				if acts := nextActions(in); hasAttach(acts) {
					t.Errorf("%s %s at %s: attach recommended with no worktree", k, tc.name, st)
				}
			}
		}
	}
}

// TestBG089AttachSurvivesWhereItWorks is the other half: the row must
// still be offered on a card that does have a worktree, so the fix
// narrows the recommendation instead of removing it.
func TestBG089AttachSurvivesWhereItWorks(t *testing.T) {
	for _, in := range []nextInput{
		{sess: engine.StatePaused, kind: domain.KindFeature, stage: domain.StageImplement, hasWorktree: true},
		{attn: attnFailure, kind: domain.KindBug, stage: domain.StageFix, hasWorktree: true},
	} {
		if !hasAttach(nextActions(in)) {
			t.Errorf("%v: attach dropped on a card that has a worktree", in.stage)
		}
	}
}

// TestBG089ResearchBoardKeysDropAttach: the status bar and the ? help
// overlay render from one binding slice, and the research filter that
// drops the branch verbs from it left "a" behind — so a research card's
// help listed "raw-attach the agent CLI in the worktree" on a card that
// has none. The key itself now refuses with the same reason the branch
// verbs give.
func TestBG089ResearchBoardKeysDropAttach(t *testing.T) {
	id, _ := domain.NewID(domain.KindResearch, 5)
	r := featureRow{F: domain.Feature{ID: id, Kind: domain.KindResearch, Stage: domain.StageReview}}

	m := &Shell{rows: []featureRow{r}}
	for _, b := range m.backlogBindings() {
		if b.key == "a" {
			t.Errorf("research card's key table still offers %q: %s", b.key, b.help)
		}
	}
	n := branchVerbRefusal(r, "attach")
	if n == nil {
		t.Fatal("attach allowed on a research card")
	}
	if !strings.Contains(n.text, "no attach") {
		t.Errorf("attach refusal = %q, want it to name the verb", n.text)
	}
}
