package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/ui/theme"
)

// The pass is a VIEW over the verified set, not a queue it owns — which
// is what keeps it from going stale against the board.

func verifiedRow(id domain.FeatureID, inGoal bool) featureRow {
	f := domain.Feature{
		ID: id, Stage: domain.StageVerify, Title: "a card",
		VerifiedAt: time.Now().Add(-time.Hour),
	}
	if inGoal {
		f.GoalID = "GL-001"
	}
	return featureRow{F: f, HasWorktree: true}
}

func TestCloseOutWalksTheVerifiedSet(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0-test")
	m.rows = []featureRow{
		{F: domain.Feature{ID: "FD-001", Stage: domain.StageImplement}},
		verifiedRow("FD-002", false),
		verifiedRow("FD-003", false),
		// a goal's card is its goal's to land, never a person's
		verifiedRow("FD-004", true),
		// verify without the stamp is not "ready": it has not reached the
		// landing gate
		{F: domain.Feature{ID: "FD-005", Stage: domain.StageVerify}},
	}
	d := &closeOutDialog{m: m, skipped: map[domain.FeatureID]bool{}}
	ready := d.readyCards()
	if len(ready) != 2 || ready[0].F.ID != "FD-002" {
		t.Fatalf("ready = %v, want FD-002 and FD-003", ids(ready))
	}

	// skipping is as cheap as landing, and it moves on rather than
	// blocking the pass
	d.skipped["FD-002"] = true
	if ready := d.readyCards(); len(ready) != 1 || ready[0].F.ID != "FD-003" {
		t.Fatalf("after a skip, ready = %v", ids(ready))
	}
}

// Landing a card through the ordinary merge dialog simply removes it from
// the list: nothing here can disagree with the board.
func TestCloseOutFollowsTheBoard(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0-test")
	m.rows = []featureRow{verifiedRow("FD-002", false)}
	d := &closeOutDialog{m: m, skipped: map[domain.FeatureID]bool{}}
	if _, ok := d.current(); !ok {
		t.Fatal("a verified card is ready to land")
	}
	m.rows[0].F.Stage = domain.StageDone // as a landing leaves it
	if _, ok := d.current(); ok {
		t.Fatal("a landed card is still in the pass")
	}
}

// Every refusal the sweep makes already existed and was already right.
// What it could not do was be read.
func TestSweepNamesWhatItHoldsBack(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0-test")
	kept := featureRow{
		F:           domain.Feature{ID: "FD-004", Stage: domain.StageDone, HandedOffAt: time.Now()},
		HasWorktree: true,
	}
	plan := sweepPlan{
		Clean: []sweepCard{{ID: "FD-002", Bytes: 340 << 20}},
		Held:  []sweepHold{{ID: kept.F.ID, Why: "handed off — kept, you asked for it"}},
		Bytes: 340 << 20,
	}
	d := &closeOutDialog{m: m, phase: phaseSweep, sweep: &plan}
	got := stripANSI(d.sweepView(m.styles, 80, 24))
	for _, want := range []string{"FD-002", "340 MB", "held back", "FD-004", "kept"} {
		if !strings.Contains(got, want) {
			t.Fatalf("sweep view is missing %q:\n%s", want, got)
		}
	}
}

// The nudge is how the cleanup ask becomes something noticed rather than
// remembered.
func TestMastheadNamesWhatCloseOutWouldFind(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0-test")
	m.rows = []featureRow{
		verifiedRow("FD-002", false),
		{F: domain.Feature{ID: "FD-003", Stage: domain.StageDone}, Landed: true, HasWorktree: true},
	}
	m.worktreeSizeText = "1 GB"
	got := m.closeOutText()
	for _, want := range []string{"1 ready to land", "1 to sweep", "1 GB", "C"} {
		if !strings.Contains(got, want) {
			t.Fatalf("masthead %q is missing %q", got, want)
		}
	}

	m.rows = nil
	if got := m.closeOutText(); got != "" {
		t.Fatalf("nothing to close out, nothing to say: %q", got)
	}
}

func TestHumanBytesReadsLikeADecision(t *testing.T) {
	for _, tc := range []struct {
		n    int64
		want string
	}{
		{0, ""},
		{512, "1 KB"},
		{340 << 20, "340 MB"},
		{int64(1) << 30, "1 GB"},
		{3 * (int64(1) << 29), "1.5 GB"},
	} {
		if got := humanBytes(tc.n); got != tc.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

func ids(rows []featureRow) []domain.FeatureID {
	out := make([]domain.FeatureID, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.F.ID)
	}
	return out
}
