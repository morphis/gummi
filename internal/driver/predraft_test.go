package driver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// TestDriveStopsAtVerifiedWithALandingMessage: a headless drive is the way
// most cards reach a verified branch, and nobody is at the keyboard while
// one runs — which makes it the best moment in the whole system to compose
// the landing message, and the worst one to skip. Without this the entire
// autonomous fleet would be exactly the set of cards that still pay the
// ~60s pass at the keypress, one after another, in the close-out ritual
// where they all come due at once.
func TestDriveStopsAtVerifiedWithALandingMessage(t *testing.T) {
	const subject = "feat(export): answer json from the export path"
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StagePlan: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return msgIdle(o.Model, "Spec drafted.")
		},
		domain.StageImplement: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			_ = os.WriteFile(filepath.Join(o.WorkDir, "feature.txt"), []byte("work\n"), 0o600)
			return msgIdle(o.Model, "Implemented.")
		},
		stageCritique: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return toolVerdict(o.Model, "pass")
		},
		domain.StageVerify: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			// the landing-message pass borrows the card's stage but runs as
			// the scribe; everything else here is verify itself.
			if o.Role == agent.RoleScribe {
				return msgIdle(o.Model, "```gummi-commit\n"+subject+"\n\n- a rationale bullet\n```")
			}
			return toolVerdict(o.Model, "pass")
		},
	})

	out, err := h.driver(Options{}).Run(context.Background(), "add a json export")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Status != StatusVerified {
		t.Fatalf("status = %q, want verified; stream=%v", out.Status, h.eventKinds())
	}
	f, err := h.store.GetFeature(context.Background(), domain.FeatureID(out.ID))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.CommitDraft, subject) {
		t.Fatalf("stopped at a verified branch with no landing message: %q", f.CommitDraft)
	}
	// stamped with the tree it describes, or the reader who lands cannot
	// tell it from a message written for an older branch
	if f.CommitDraftSHA == "" {
		t.Error("the stored landing message carries no branch tip")
	}
	// and narrated: this is a model pass at the very end of a run, and a
	// stream that goes quiet there reads as a hang.
	if !strings.Contains(h.buf.String(), "drafting the landing message") {
		t.Errorf("the drive never said it was drafting; stream=%v", h.eventKinds())
	}
}

// TestDriveNarratesNoDraftItWillNotMake: a goal's card is landed by its
// goal, not by a person, so the pass is skipped — and a stream line
// announcing a draft that was never going to happen is worse than no line
// at all.
func TestDriveNarratesNoDraftItWillNotMake(t *testing.T) {
	h := newHarness(t, true, map[domain.Stage]stageFn{})
	d := h.driver(Options{})
	f := domain.Feature{ID: "FD-001", Num: 1, Title: "a goal's card", Slug: "a-goals-card",
		Stage: domain.StageVerify, GoalID: domain.FeatureID("GL-001")}

	d.predraftLanding(context.Background(), f)

	if strings.Contains(h.buf.String(), "drafting the landing message") {
		t.Errorf("announced a draft for a card whose goal writes its message; stream=%s", h.buf.String())
	}
}
