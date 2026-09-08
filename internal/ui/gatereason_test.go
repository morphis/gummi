package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui/theme"
)

// TestGateReasonNamesLandingOnlyAtVerify: the one wording function every
// gate item goes through says "land on main" exactly where the next
// keypress lands the branch on main, and "review & advance" everywhere
// else. It also keeps the stage first, which is what inboxRowText trims
// so an inbox row does not print its stage twice.
func TestGateReasonNamesLandingOnlyAtVerify(t *testing.T) {
	for _, stage := range []domain.Stage{domain.StagePlan, domain.StageImplement, domain.StageVerify} {
		for _, kind := range []domain.Kind{domain.KindFeature, domain.KindBug, domain.KindResearch} {
			got := gateReason(stage, kind, true)
			if !strings.HasPrefix(got, string(stage)+" ") {
				t.Errorf("%s/%s: %q does not lead with its stage", stage, kind, got)
			}
			lands := strings.Contains(got, "land on main")
			wantLands := stage == domain.StageVerify && kind != domain.KindResearch
			if lands != wantLands {
				t.Errorf("%s/%s: %q offers landing = %v, want %v", stage, kind, got, lands, wantLands)
			}
			if stage == domain.StageVerify && got != verifyGateReason(kind, true) {
				t.Errorf("%s/%s: verify wording %q forked from verifyGateReason %q",
					stage, kind, got, verifyGateReason(kind, true))
			}
		}
	}
}

// TestReconstructedVerifyGateSaysItLands: a card that reached its verify
// gate while the TUI was closed is rebuilt by reconstructInbox, and used
// to be handed the flat "<stage> finished — review & advance" whatever
// stage it was at. So whether the reader was told the next keypress
// merges to main depended on whether gummi happened to be running at the
// time, and one inbox could show both wordings on two cards at the same
// gate.
//
// The act is now unconditional; only the outcome word varies, and it
// varies on VerifiedAt rather than on the session. A verdict does not
// survive a restart — the sessions row's verdict column is empty in
// practice even for a card driven headlessly to a verified branch — so an
// unstamped card must not be told its verify passed, or a failed verify
// reads as an invitation to land the branch.
func TestReconstructedVerifyGateSaysItLands(t *testing.T) {
	ws, store, wt := uiRepo(t)
	ctx := context.Background()

	fPassed := mkFeature(t, store, 1, "stamped verify gate", domain.StageVerify)
	fUnknown := mkFeature(t, store, 2, "unstamped verify gate", domain.StageVerify)
	fImplement := mkFeature(t, store, 3, "implement gate", domain.StageImplement)
	for _, f := range []domain.Feature{fPassed, fUnknown, fImplement} {
		if err := store.SaveSession(ctx, state.SessionSnapshot{
			Feature: f.ID, Stage: f.Stage, Role: "reviewer", State: "done",
		}); err != nil {
			t.Fatal(err)
		}
	}
	// only the first card carries the marker a clean verify stamps
	if err := store.SetVerifiedAt(ctx, fPassed.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	eng := engine.New(engine.Config{
		Agents: singleAgent(agent.NewFake("ok")), Store: store, Pool: wt,
		Workspace: ws, Model: "m", Persist: true,
	})
	t.Cleanup(func() { eng.Close() })
	if err := eng.Restore(ctx); err != nil {
		t.Fatal(err)
	}

	m := NewShell(theme.GummiDark(), "v0-test")
	m.Attach(store, wt, ws)
	m.AttachEngine(eng)
	m.reconstructInbox()

	got := map[domain.FeatureID]string{}
	for _, it := range m.inbox.list() {
		got[it.Feature] = it.Text
	}
	// both verify cards say what the next press does...
	for _, f := range []domain.Feature{fPassed, fUnknown} {
		if !strings.Contains(got[f.ID], "land on main") {
			t.Errorf("%s: a reconstructed verify gate does not say the next press lands it: %q", f.ID, got[f.ID])
		}
	}
	// ...and only the stamped one claims an outcome
	if want := verifyGateReason(domain.KindFeature, true); got[fPassed.ID] != want {
		t.Errorf("stamped verify gate = %q, want %q", got[fPassed.ID], want)
	}
	if want := verifyGateReason(domain.KindFeature, false); got[fUnknown.ID] != want {
		t.Errorf("unstamped verify gate = %q, want %q", got[fUnknown.ID], want)
	}
	if strings.Contains(got[fUnknown.ID], "passed") {
		t.Errorf("an unstamped verify gate claims it passed: %q", got[fUnknown.ID])
	}
	if want := gateReason(domain.StageImplement, domain.KindFeature, true); got[fImplement.ID] != want {
		t.Errorf("reconstructed implement gate = %q, want %q", got[fImplement.ID], want)
	}
	if strings.Contains(got[fImplement.ID], "land on main") {
		t.Errorf("an implement gate offers to land: %q", got[fImplement.ID])
	}
}

// TestLiveGateWordingMatchesTheReconstructedOne pins the third call site
// against the other two: the item a live stage completion raises is the
// same sentence the restart would have rebuilt, so the two can no longer
// be seen side by side disagreeing.
func TestLiveGateWordingMatchesTheReconstructedOne(t *testing.T) {
	m := runVerify(t, "All checks green.\nVERDICT: pass")
	it := verifyGate(t, m)
	if want := gateReason(domain.StageVerify, domain.KindFeature, true); it.Text != want {
		t.Errorf("live verify gate = %q, want the shared wording %q", it.Text, want)
	}
}
