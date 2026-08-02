package driver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
)

// --until spec stops cleanly at the spec design gate: the run exits with
// StatusStopped, the last stream line is `stopped`, and the feature stays
// parked at Spec with no branch created (nothing implemented).
func TestUntilSpecStopsBeforeImplement(t *testing.T) {
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageSpec: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return msgIdle(o.Model, "Spec drafted.")
		},
		// implement/review/verify are scripted but must never run.
		domain.StageImplement: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			t.Error("implement ran despite --until spec")
			return msgIdle(o.Model, "Implemented.")
		},
	})

	out, err := h.driver(Options{Until: domain.StageSpec}).Run(context.Background(), "add a json export")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Status != StatusStopped {
		t.Fatalf("status = %q, want stopped; stream=%v", out.Status, h.eventKinds())
	}
	id := domain.FeatureID(out.ID)
	if st := h.stageOf(id); st != domain.StageSpec {
		t.Fatalf("feature at %s, want Spec (stopped before crossing the gate)", st)
	}
	kinds := h.eventKinds()
	if kinds[len(kinds)-1] != "stopped" {
		t.Fatalf("last event = %q, want stopped; stream=%v", kinds[len(kinds)-1], kinds)
	}
	if h.has("gate") {
		t.Fatalf("a gate was crossed despite --until spec; stream=%v", kinds)
	}
	// the branch must not exist: nothing was implemented.
	f, _ := h.store.GetFeature(context.Background(), id)
	if exists, _ := h.wt.BranchExists(context.Background(), &f); exists {
		t.Fatal("branch created despite stopping at spec")
	}
}

// A run stopped at --until spec is resumable: after a human design review,
// resume --approve crosses the reviewed gate and drives the same feature on
// to a verified branch (the B3 stop-early-then-approve flow).
func TestUntilSpecThenResumeToVerified(t *testing.T) {
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageSpec: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return msgIdle(o.Model, "Spec drafted.")
		},
		domain.StageReview: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return toolVerdict(o.Model, "pass")
		},
		domain.StageVerify: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return toolVerdict(o.Model, "pass")
		},
	})

	out, err := h.driver(Options{Until: domain.StageSpec}).Run(context.Background(), "add export")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Status != StatusStopped {
		t.Fatalf("run status = %q, want stopped", out.Status)
	}
	// resume --approve crosses the spec gate the stop held back; drive the
	// tail to a verified branch. (A fresh driver models a fresh CLI process.)
	out2, err := h.driver(Options{}).Resume(context.Background(), domain.FeatureID(out.ID), ResumeInput{Approve: true})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if out2.Status != StatusDone {
		t.Fatalf("resume status = %q, want done; stream=%v", out2.Status, h.eventKinds())
	}
}

// --until validates against the route: plan is off-route on the quick route
// (plan is skipped), so a quick run with --until plan fails loud without
// minting a feature.
func TestUntilPlanRejectedOnQuickRoute(t *testing.T) {
	h := newHarness(t, true, nil)
	out, err := h.driver(Options{Until: domain.StagePlan}).Run(context.Background(), "add export")
	if err == nil {
		t.Fatal("--until plan accepted on the quick route")
	}
	if out.Status != StatusError {
		t.Fatalf("status = %q, want error", out.Status)
	}
	// no feature should have been created.
	feats, _ := h.store.ListFeatures(context.Background())
	if len(feats) != 0 {
		t.Fatalf("a feature was minted despite the invalid --until: %d", len(feats))
	}
}

// UntilStops enumerates the design-side stops per route.
func TestUntilStops(t *testing.T) {
	quick := UntilStops(domain.KindFeature, domain.QuickRoute())
	if len(quick) != 1 || quick[0] != domain.StageSpec {
		t.Fatalf("quick stops = %v, want [spec]", quick)
	}
	full := UntilStops(domain.KindFeature, domain.SkipFlags{})
	if len(full) != 3 || full[0] != domain.StageBrainstorm || full[1] != domain.StageSpec || full[2] != domain.StagePlan {
		t.Fatalf("full stops = %v, want [brainstorm spec plan]", full)
	}
	bug := UntilStops(domain.KindBug, domain.SkipFlags{})
	if len(bug) != 2 || bug[0] != domain.StageTriage || bug[1] != domain.StageDiagnose {
		t.Fatalf("bug stops = %v, want [triage diagnose]", bug)
	}
}

// --acceptance seeds the draft's Verification plan at creation, alongside
// the description's Problem seed, without any agent turn.
func TestAcceptanceSeedsDraft(t *testing.T) {
	h := newHarness(t, true, nil)
	d := h.driver(Options{Acceptance: "The export MUST be valid JSON and round-trip."})
	desc := "Add a JSON export\n\nUsers need to export their data as JSON."
	f, err := d.createFeature(context.Background(), desc)
	if err != nil {
		t.Fatalf("createFeature: %v", err)
	}
	draft := filepath.Join(h.ws.DraftsDir(), spec.DraftFilename(&f))
	raw, err := os.ReadFile(draft)
	if err != nil {
		t.Fatalf("reading draft: %v", err)
	}
	body := string(raw)
	// acceptance lands under Verification plan; the description under Problem.
	vp := sectionBody(body, "## Verification plan")
	if !strings.Contains(vp, "round-trip") {
		t.Fatalf("acceptance not under Verification plan:\n%s", body)
	}
	prob := sectionBody(body, "## Problem")
	if !strings.Contains(prob, "export their data") {
		t.Fatalf("description not under Problem:\n%s", body)
	}
}

// --acceptance alone (a title-sized description that seeds no Problem
// overflow) still writes a draft.
func TestAcceptanceAloneWritesDraft(t *testing.T) {
	h := newHarness(t, true, nil)
	d := h.driver(Options{Acceptance: "Round-trips through the parser."})
	f, err := d.createFeature(context.Background(), "Add JSON export")
	if err != nil {
		t.Fatalf("createFeature: %v", err)
	}
	draft := filepath.Join(h.ws.DraftsDir(), spec.DraftFilename(&f))
	if _, err := os.Stat(draft); err != nil {
		t.Fatalf("no draft written for an acceptance-only creation: %v", err)
	}
}

// createFeature persists --ref as the feature's external ref, so a run can
// be found by its correlation id afterward.
func TestRefPersistedAsExternalRef(t *testing.T) {
	h := newHarness(t, true, nil)
	d := h.driver(Options{Ref: "JIRA-42"})
	f, err := d.createFeature(context.Background(), "Add export")
	if err != nil {
		t.Fatalf("createFeature: %v", err)
	}
	if f.ExternalRef != "JIRA-42" {
		t.Fatalf("ExternalRef = %q, want JIRA-42", f.ExternalRef)
	}
	got, err := h.store.FeatureByExternalRef(context.Background(), "JIRA-42")
	if err != nil || got.ID != f.ID {
		t.Fatalf("FeatureByExternalRef = %+v, err=%v; want %s", got, err, f.ID)
	}
}

// sectionBody returns the text between a "## Heading" and the next "## " (or
// EOF) — a small helper to assert seed placement.
func sectionBody(doc, heading string) string {
	i := strings.Index(doc, heading)
	if i < 0 {
		return ""
	}
	rest := doc[i+len(heading):]
	if j := strings.Index(rest, "\n## "); j >= 0 {
		return rest[:j]
	}
	return rest
}
