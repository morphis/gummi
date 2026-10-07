package ui

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/ui/theme"
)

func answerIDs(acts []nextAction) []string {
	out := make([]string, 0, len(acts))
	for _, a := range acts {
		out = append(out, a.id+":"+a.label)
	}
	return out
}

// TestADependencyBlockedGateLeadsWithWhatItWaitsOn: a design gate held
// shut by an unmet dependency does not offer the approval the server
// refuses; it leads with what the card waits on.
func TestADependencyBlockedGateLeadsWithWhatItWaitsOn(t *testing.T) {
	in := nextInput{
		stage: domain.StagePlan, kind: domain.KindFeature, exited: true, verdict: verdictPass,
		depBlockers: []domain.FeatureID{"FD-013"},
	}
	acts := stageActions(in)
	if len(acts) == 0 || acts[0].id != "wait" || !strings.Contains(acts[0].label, "FD-013") {
		t.Fatalf("answers = %v, want a lead row naming FD-013", answerIDs(acts))
	}
	for _, a := range acts {
		if a.id == "advance" {
			t.Errorf("a dependency-blocked gate offers %q, which the gate refuses", a.label)
		}
	}
}

// TestAStackedCardWaitsForTheCardBelowIt: a stacked card whose lower card
// has not landed says it lands after that one, instead of offering the
// landing the merge refuses.
func TestAStackedCardWaitsForTheCardBelowIt(t *testing.T) {
	in := nextInput{
		stage: domain.StageVerify, kind: domain.KindFeature, exited: true, verdict: verdictPass,
		stackBlocker: "FD-012",
	}
	acts := stageActions(in)
	if len(acts) == 0 || acts[0].id != "wait" || !strings.Contains(acts[0].label, "lands after FD-012") {
		t.Fatalf("answers = %v, want a lead row saying it lands after FD-012", answerIDs(acts))
	}
	for _, a := range acts {
		if a.id == "advance" {
			t.Errorf("a stacked card offers %q while the card below it is unlanded", a.label)
		}
	}

	// the board's own stack rows are what the input is read from
	m := NewShell(theme.GummiDark(), "v0-test")
	m.stackRows = map[domain.FeatureID]stackRow{
		"FD-011": {ID: "st", Pos: 1, Of: 3, Landed: true},
		"FD-012": {ID: "st", Pos: 2, Of: 3},
		"FD-015": {ID: "st", Pos: 3, Of: 3},
	}
	if got := m.stackLandBlocker("FD-015"); got != "FD-012" {
		t.Errorf("stackLandBlocker(FD-015) = %q, want FD-012", got)
	}
	if got := m.stackLandBlocker("FD-012"); got != "" {
		t.Errorf("stackLandBlocker(FD-012) = %q, want none — the card below it landed", got)
	}
}

// TestALandingConflictOffersTheRebase: after a landing hit conflicts, the
// decision leads with the rebase that resolves them — not the landing
// that just failed — and the notice names the act, not a terminal key.
func TestALandingConflictOffersTheRebase(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0-test")
	model, _ := m.update(landConflictMsg{
		id: "FD-005", files: []string{"fd005.go"},
		notice: noticeMsg{text: "FD-005: squash merge conflicts in fd005.go — undone, main checkout clean — rebase it onto main to resolve them, then land again", isErr: true},
	})
	m = model.(*Shell)
	if m.landConflicts["FD-005"] == nil {
		t.Fatal("the conflict was not recorded")
	}

	in := nextInput{
		stage: domain.StageVerify, kind: domain.KindFeature, exited: true, verdict: verdictPass,
		landConflicts: m.landConflicts["FD-005"],
	}
	acts := stageActions(in)
	if len(acts) == 0 || acts[0].id != "rebase" {
		t.Fatalf("answers = %v, want the rebase to lead", answerIDs(acts))
	}
	if !strings.Contains(acts[0].detail, "fd005.go") {
		t.Errorf("rebase row = %q, want it to name the conflicting file", acts[0].detail)
	}
}
