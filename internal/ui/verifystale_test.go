package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/ui/theme"
	"github.com/morphis/gummi/internal/webapi"
)

// A commit added after verify passed is work no check has run on. The
// verify stop stops saying "verification passed / land on main" and
// offers the re-verify instead, every landing door refuses, and a
// re-verify that passes records the new tip — after which the card lands.
func TestCommitsAfterVerifyAskForAReverifyNotALanding(t *testing.T) {
	m, _, wt := mergeFixture(t)
	ctx := context.Background()

	// the card's spec, with a check the re-verify can run
	f, err := m.store.GetFeature(ctx, "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	art := spec.BlankTemplate(&f)
	art, _, _ = spec.ReplaceSection(art, "Verification plan", "Run the checks.\n")
	art, err = spec.UpsertChecks(art, specChecks("ok", "exit 0"))
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(m.wt.Root(), f.ArtifactPath())
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(art), 0o600); err != nil {
		t.Fatal(err)
	}

	verified := gitOut(t, wt, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(wt, "unverified.go"), []byte("package x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, wt, "add", ".")
	git(t, wt, "commit", "-qm", "after verify")
	moved := gitOut(t, wt, "rev-parse", "HEAD")
	m = pump(t, m, m.loadRows)
	m.sel = 0
	m.inbox.addEscalated("FD-001", attnGate, "verify passed")

	r := m.rows[0]
	in := m.nextInputFor(r)
	if !in.verifyStale {
		t.Fatalf("a branch moved from %s to %s reads as still verified", verified[:7], moved[:7])
	}
	acts := stageActions(in)
	if len(acts) == 0 || acts[0].id != "reverify" {
		t.Fatalf("the stop leads with %+v, want the re-verify", acts)
	}
	for _, a := range acts {
		if a.id == "advance" {
			t.Errorf("the stop still offers the landing: %+v", a)
		}
	}
	if d := m.openDecision(r); d == nil || strings.HasPrefix(d.question, "verification passed") || !strings.Contains(d.question, "moved") {
		t.Errorf("decision = %+v, want one that says the branch moved", d)
	}
	if word, tone := m.webDecisionWord(webapi.DecisionVerify, r); word != "branch moved" || tone != "warn" {
		t.Errorf("the page heads it %q (%s), want the branch moved", word, tone)
	}
	if why := m.landingRefusal(r.F); !strings.Contains(why, "re-verify") {
		t.Errorf("landingRefusal = %q, want the re-verify refusal", why)
	}
	m = pressMerge(t, m)
	if _, ok := m.Overlay.Top().(*commitMsgDialog); ok || !strings.Contains(m.notice.text, "re-verify") {
		t.Fatalf("m on a moved branch: notice %q, want the re-verify refusal and no dialog", m.notice.text)
	}

	// re-verify: the checks run on the new tip, which the stamp records
	m = pump(t, m, m.runCardAction(cardAction{id: "reverify"}))
	if !strings.Contains(m.notice.text, "re-verified") {
		t.Fatalf("re-verify notice = %q", m.notice.text)
	}
	got, _ := m.store.GetFeature(ctx, "FD-001")
	if got.VerifiedRev != moved || got.MayLandAt(moved) != nil {
		t.Fatalf("after the re-verify the stamp names %q, want the tip %q", got.VerifiedRev, moved)
	}
	m = pump(t, m, m.loadRows)
	if m.nextInputFor(m.rows[0]).verifyStale {
		t.Error("a re-verified card still reads stale")
	}
	m = press(t, m, tea.KeyPressMsg{Code: 'm', Text: "m"})
	if _, ok := m.Overlay.Top().(*commitMsgDialog); !ok {
		t.Fatalf("a re-verified card did not reach the landing dialog (notice %q)", m.notice.text)
	}
}

// A commit made before any page watched the card still reaches the page:
// the probe's first sighting of a verified card compares the tip with the
// one the row read at load, rather than recording it as the baseline.
func TestFirstSightingOfAMovedVerifiedTipReloads(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0-test")
	m.rows = []featureRow{{F: domain.Feature{ID: "FD-001", Stage: domain.StageVerify}, Head: "aaaaaaa"}}
	if m.applyWatchedRevs(map[domain.FeatureID]watchRev{"FD-001": {head: "aaaaaaa"}}) {
		t.Error("an unmoved first sighting asked for a reload")
	}
	m.fresh = nil
	if !m.applyWatchedRevs(map[domain.FeatureID]watchRev{"FD-001": {head: "bbbbbbb"}}) {
		t.Error("a tip that moved before the first sighting asked for nothing")
	}
}
