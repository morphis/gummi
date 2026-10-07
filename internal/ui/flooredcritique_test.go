package ui

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

// TestAFlooredCritiquePassIsNotCalledPassedOrUnclear: a critique whose
// reviewer said "VERDICT: pass" but which gummi overruled (a check it ran
// failed) reaches the gate as fail or blocked. The gate reason used to
// call it "no clear verdict" while the row under it said "the critique
// passed"; both now name the overrule.
func TestAFlooredCritiquePassIsNotCalledPassedOrUnclear(t *testing.T) {
	const floor = "check unit failed"
	for _, v := range []reviewVerdict{verdictBlocked, verdictFail} {
		words := critiqueVerdictWords(v, floor)
		if strings.Contains(words, "no clear verdict") || !strings.Contains(words, floor) {
			t.Errorf("verdict %v floored by %q reads %q", v, floor, words)
		}
		in := nextInput{
			stage: domain.StageImplement, kind: domain.KindFeature, exited: true, attn: attnGate, escalated: true,
			verdict: v, verdictFloorReason: floor,
		}
		for _, a := range stageActions(in) {
			if strings.Contains(a.detail, "the critique passed") {
				t.Errorf("verdict %v: row %q says %q", v, a.label, a.detail)
			}
		}
		if !critiqueUnsettled(in) {
			t.Errorf("verdict %v: an overruled critique reads as settled", v)
		}
	}
	if got := critiqueVerdictWords(verdictUnclear, ""); got != "critique gave no clear verdict" {
		t.Errorf("unclear reads %q", got)
	}
}
