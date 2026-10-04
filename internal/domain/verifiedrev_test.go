package domain

import (
	"errors"
	"testing"
	"time"
)

// The verified floor is about the revision that lands: a card verified on
// one tip may land that tip and no other, a card stamped with no
// revision reads as needing its verify run again, and the refusal is
// still ErrNotVerified to every caller that only asks that.
func TestMayLandAtBindsTheVerifyToItsRevision(t *testing.T) {
	const tip, moved = "1111111aaaaaaa", "2222222bbbbbbb"
	f := Feature{ID: "FD-003", Kind: KindFeature, Stage: StageVerify, VerifiedAt: time.Now(), VerifiedRev: tip}

	if err := f.MayLandAt(tip); err != nil {
		t.Errorf("the verified tip: MayLandAt = %v, want nil", err)
	}
	err := f.MayLandAt(moved)
	if !errors.Is(err, ErrVerifyStale) || !errors.Is(err, ErrNotVerified) {
		t.Errorf("a moved tip: MayLandAt = %v, want ErrVerifyStale (an ErrNotVerified)", err)
	}
	if !f.VerifyStale(moved) || f.VerifyStale(tip) || f.VerifyStale("") {
		t.Error("VerifyStale disagrees with MayLandAt")
	}

	legacy := f
	legacy.VerifiedRev = ""
	if err := legacy.MayLandAt(tip); !errors.Is(err, ErrVerifyStale) {
		t.Errorf("a stamp with no revision: MayLandAt = %v, want a verify to run again", err)
	}

	unverified := f
	unverified.VerifiedAt = time.Time{}
	if err := unverified.MayLandAt(tip); !errors.Is(err, ErrNotVerified) || errors.Is(err, ErrVerifyStale) || unverified.VerifyStale(moved) {
		t.Errorf("an unverified card: MayLandAt = %v, want plain ErrNotVerified and not stale", err)
	}

	// a goal re-checks its own branches when its landing moves them, and
	// a freeform card has no verify to be stale
	goal := f
	goal.ID, goal.Kind, goal.VerifiedRev = "GL-001", KindGoal, ""
	if err := goal.MayLandAt(moved); err != nil {
		t.Errorf("a verified goal: MayLandAt = %v, want nil", err)
	}
	ff := Feature{ID: "FF-001", Kind: KindFreeform, Stage: StageOpen}
	if err := ff.MayLandAt(moved); err != nil {
		t.Errorf("a freeform card: MayLandAt = %v, want nil", err)
	}
}
