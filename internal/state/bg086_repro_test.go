package state

import (
	"context"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

// TestBG086VerdictFloorSurvivesARestart is BG-086's regression test.
//
// The verdict floor is gummi's own deterministic ceiling on a stage's
// outcome, stamped when a live gummi-check fails or an environment gate
// blocks. The verdict it overrules was persisted; the floor was not, so
// it died with the process. BG-074 patched the dangerous half — a
// restarted gummi no longer reads a blocked verify as a clean pass,
// because the durable escalation flag says otherwise — but the reason
// was gone, so the card could say a verify was blocked and not what to
// fix, which is the half a reader acts on.
//
// The round trip is through SaveSession/LoadSessions rather than a
// struct copy: the defect was a missing column, and only a real write
// and read back can prove one exists.
func TestBG086VerdictFloorSurvivesARestart(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	f := feat(1, "a card whose checks failed")
	f.Stage = domain.StageVerify
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}

	const reason = "check go-test failed"
	if err := s.SaveSession(ctx, SessionSnapshot{
		Feature: f.ID, Stage: domain.StageVerify, Role: "reviewer", State: "done",
		// the agent's own word, which the floor exists to overrule
		Verdict:            "pass",
		VerdictFloor:       "blocked",
		VerdictFloorReason: reason,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.LoadSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("loaded %d sessions, want 1", len(got))
	}
	if got[0].VerdictFloor != "blocked" {
		t.Errorf("VerdictFloor = %q, want %q — the ceiling did not survive the restart",
			got[0].VerdictFloor, "blocked")
	}
	if got[0].VerdictFloorReason != reason {
		t.Errorf("VerdictFloorReason = %q, want %q — the card can say it is blocked but not why",
			got[0].VerdictFloorReason, reason)
	}
	// and the verdict it overrules is still there, so the two can still be
	// compared the way the live process compared them
	if got[0].Verdict != "pass" {
		t.Errorf("Verdict = %q, want the agent's own %q", got[0].Verdict, "pass")
	}
}

// TestBG086SessionWithNoFloorRoundTripsEmpty: the columns default empty,
// so a session gummi never overruled reads back with no floor rather
// than an accidental "blocked" — the failure that would mark every clean
// verify as blocked after an upgrade.
func TestBG086SessionWithNoFloorRoundTripsEmpty(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	f := feat(2, "a card that verified cleanly")
	f.Stage = domain.StageVerify
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSession(ctx, SessionSnapshot{
		Feature: f.ID, Stage: domain.StageVerify, Role: "reviewer", State: "done",
		Verdict: "pass",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("loaded %d sessions, want 1", len(got))
	}
	if got[0].VerdictFloor != "" || got[0].VerdictFloorReason != "" {
		t.Errorf("a clean session came back floored: floor=%q reason=%q",
			got[0].VerdictFloor, got[0].VerdictFloorReason)
	}
}
