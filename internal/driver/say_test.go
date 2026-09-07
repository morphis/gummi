package driver

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
)

// --say reports and stops. With no scribe backend the event says the
// reader was absent and reports the act a bare resume would take; the
// card does not move and nothing runs.
func TestResumeSayReportsWithoutActing(t *testing.T) {
	h := newHarness(t, false, nil)
	id, _ := domain.NewFeatureID(1)
	slug, _ := domain.Slugify("dark mode")
	now := time.Now()
	f := domain.Feature{ID: id, Num: 1, Kind: domain.KindFeature, Title: "dark mode", Slug: slug, Stage: domain.StageImplement, CreatedAt: now, UpdatedAt: now}
	if err := h.store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	// the reader runs in the card's worktree (it reads the diff there)
	if _, err := h.wt.Create(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	d := h.driver(Options{})

	out, err := d.Resume(context.Background(), f.ID, ResumeInput{Say: strp("the retry path is wrong")})
	if err != nil {
		t.Fatalf("Resume --say: %v", err)
	}
	if out.Status != StatusSaid || out.Status.ExitCode() != 0 {
		t.Fatalf("status = %q (exit %d), want said/0", out.Status, out.Status.ExitCode())
	}
	ev := lastEvent(h, "say")
	if ev == nil {
		t.Fatalf("no say event; stream=%v", h.eventKinds())
	}
	// the harness's scripted agent answers no classification prompt, so
	// the reading is nothing and the act is a turn — whether or not a
	// reader was reachable, the card must not move
	if ev["line"] != "the retry path is wrong" || ev["action"] != "turn" || ev["confirms"] != false {
		t.Errorf("say event = %v", ev)
	}
	if got, _ := h.store.GetFeature(context.Background(), f.ID); got.Stage != domain.StageImplement {
		t.Errorf("--say moved the card to %s", got.Stage)
	}
	if h.eng.Get(f.ID) != nil {
		t.Error("--say started a session")
	}
	raw, _ := json.Marshal(ev)
	t.Logf("say: %s", raw)
}

func strp(s string) *string { return &s }
