package ui

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

// TestBG074AnEscalatedVerifyGateIsNeverAPass is BG-074's regression test.
// A live gummi-check failure floors the verify verdict at blocked, but
// the floor lives on the session and is never persisted; after a restart
// verdict.SessionVerdict falls back to the transcript, where the agent's
// own "VERDICT: pass" is still sitting. The card then read as a clean
// pass — "verification passed — decide whether this work is ready to
// land", with "land on main — verify passed" as the highlighted option —
// on a card whose inbox row still said BLOCKED.
//
// The escalation flag is the durable half of that judgement, so it wins.
func TestBG074AnEscalatedVerifyGateIsNeverAPass(t *testing.T) {
	if got := escalatedGateVerdict(verdictPass, true); got == verdictPass {
		t.Fatal("a pass read alongside an escalated gate survived as a pass")
	}
	if got := escalatedGateVerdict(verdictPass, false); got != verdictPass {
		t.Error("a clean gate's pass was downgraded — only escalations are give-ups")
	}
	if got := escalatedGateVerdict(verdictFail, true); got != verdictFail {
		t.Error("an escalated fail lost its own verdict")
	}

	// what the card actually renders once the rule has run: the verdict a
	// restarted blocked verify arrives with, through the same seam
	// nextInputFor uses.
	in := nextInput{
		stage: domain.StageVerify, kind: domain.KindFeature,
		attn: attnGate, escalated: true,
		verdict: escalatedGateVerdict(verdictPass, true),
	}
	row := featureRow{F: domain.Feature{ID: "FD-001", Kind: domain.KindFeature, Stage: domain.StageVerify}}
	if q := decisionQuestion(decisionVerify, row, in); strings.Contains(q, "verification passed") {
		t.Errorf("the decision claims a pass on an escalated gate: %q", q)
	}
	acts := stageActions(in)
	if len(acts) == 0 {
		t.Fatal("an escalated verify gate offers nothing at all")
	}
	if acts[0].id == "advance" {
		t.Errorf("landing on main leads the options on an escalated verify gate: %q", nextActionIDs(acts))
	}
	for _, a := range acts {
		if strings.Contains(a.why, "verify passed") {
			t.Errorf("an escalated verify gate still reads as a pass: %q — %q", a.label, a.why)
		}
	}
}
