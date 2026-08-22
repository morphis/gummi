package driver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// rsShapeSlicesBody is the `## Slices` body a shape turn writes via the
// spec_replace_section client tool — the same two-row shape decompose_test
// seeds directly, so decomposeGate has real (non-scaffold) proposals to
// find once the card reaches StageDone.
const rsShapeSlicesBody = "```yaml\n" +
	"- title: Row one\n  one-liner: first\n  depends-on: []\n  requirements: []\n  id: \"\"\n" +
	"- title: Row two\n  one-liner: second\n  depends-on: [Row one]\n  requirements: []\n  id: \"\"\n" +
	"```\n"

// rsFullRouteScript scripts investigate as a plain assistant turn, shape as
// a spec_replace_section write of real slices (research stages are read-
// only in the main checkout — shape is the one interactive stage that
// isn't, so it's the only one that can author the `## Slices` section a
// real architect turn would write), and review/verify as passing
// autonomous verdicts — the shape every research card takes from Create
// through the verify→done decompose gate.
func rsFullRouteScript(t *testing.T, prompts *[]string, replies []json.RawMessage) map[domain.Stage]stageFn {
	t.Helper()
	return map[domain.Stage]stageFn{
		domain.StageInvestigate: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return msgIdle(o.Model, "Investigated.")
		},
		domain.StageShape: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			args, _ := json.Marshal(map[string]string{"section": "Slices", "body": rsShapeSlicesBody})
			return []agent.Event{
				{Kind: agent.EventClientToolCall, ToolCall: &agent.ToolCall{ID: "s", Name: "spec_replace_section", Args: args}},
				{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 1, Model: o.Model}},
				{Kind: agent.EventIdle},
			}
		},
		domain.StageReview: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return toolVerdict(o.Model, "pass")
		},
		domain.StageVerify: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return toolVerdict(o.Model, "pass")
		},
		domain.StageDone: decomposeArchitectFn(replies, prompts),
	}
}

// TestCreateResearchDrivesToDecomposeQuestion proves the new KindResearch
// route through Create + Drive: investigate → shape → review → verify all
// run as ordinary stages (no research-specific driver code), and reaching
// StageDone auto-triggers the decompose gate exactly as it does when a card
// starts there directly (decompose_test.go). This is the first test to
// drive an RS card from Create rather than seeding one straight into the
// store, so it is what actually proves `gummi research` end to end.
func TestCreateResearchDrivesToDecomposeQuestion(t *testing.T) {
	var prompts []string
	h := newHarness(t, true, rsFullRouteScript(t, &prompts, []json.RawMessage{
		decomposeProposalsWire("Row one", "Row two"),
	}))
	h.fake.Caps.ReadOnlyEnforce = true

	d := h.driver(Options{})
	ctx := context.Background()
	f, err := d.Create(ctx, domain.KindResearch, "grounded look at auth")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if f.ID != domain.FeatureID("RS-001") {
		t.Fatalf("created id = %q, want RS-001", f.ID)
	}

	out, err := d.Drive(ctx, f)
	if err != nil {
		t.Fatalf("Drive: %v", err)
	}
	if out.Status != StatusQuestion {
		t.Fatalf("status = %q, want question; stream=%v", out.Status, h.eventKinds())
	}

	// created is first and question is last; every design stage on RS's
	// route reported at least one "stage" milestone in between. Exact
	// intra-stage event counts (entry vs. verdict-result "stage" lines,
	// the shape→review auto-approved "gate") are covered by the driver's
	// existing per-mechanism tests, not re-pinned here.
	kinds := h.eventKinds()
	if len(kinds) < 3 || kinds[0] != "created" || kinds[len(kinds)-1] != "question" {
		t.Fatalf("event kinds = %v, want created ... question", kinds)
	}
	if !h.has("stage") {
		t.Fatalf("no stage events; stream=%v", kinds)
	}
	created := lastEvent(h, "created")
	if created == nil || created["id"] != "RS-001" {
		t.Fatalf("created event = %v, want id RS-001", created)
	}
	q := lastEvent(h, "question")
	if q == nil {
		t.Fatalf("no question event; stream=%v", h.eventKinds())
	}
	props, _ := q["proposals"].([]any)
	if len(props) != 2 {
		t.Fatalf("question proposals = %v, want 2", q["proposals"])
	}

	// --approve mints both proposed FDs and moves the RS card to done.
	out, err = d.Resume(ctx, f.ID, ResumeInput{Approve: true})
	if err != nil {
		t.Fatalf("Resume --approve: %v", err)
	}
	if out.Status != StatusDone {
		t.Fatalf("status = %q, want done; stream=%v", out.Status, h.eventKinds())
	}
	if lastEvent(h, "decompose_minted") == nil {
		t.Fatalf("no decompose_minted event; stream=%v", h.eventKinds())
	}
	if lastEvent(h, "done") == nil {
		t.Fatalf("no done event; stream=%v", h.eventKinds())
	}
	feats, err := h.store.ListFeatures(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(feats) != 3 { // the RS card plus its two minted FDs
		t.Fatalf("features = %d, want 3", len(feats))
	}
	got, err := h.store.GetFeature(ctx, f.ID)
	if err != nil || got.Stage != domain.StageDone {
		t.Fatalf("RS card stage = %v, %v; want done", got.Stage, err)
	}
}

// TestCreateResearchRequestChangesRerunsDecompose proves --request-changes
// re-runs the decompose pass with the note attached and the RS card never
// leaves done (the decompose gate never un-approves the document).
func TestCreateResearchRequestChangesRerunsDecompose(t *testing.T) {
	var prompts []string
	h := newHarness(t, true, rsFullRouteScript(t, &prompts, []json.RawMessage{
		decomposeProposalsWire("Row one", "Row two"),
		decomposeProposalsWire("Row one v2", "Row two v2"),
	}))
	h.fake.Caps.ReadOnlyEnforce = true

	d := h.driver(Options{})
	ctx := context.Background()
	f, err := d.Create(ctx, domain.KindResearch, "grounded look at auth")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out, err := d.Drive(ctx, f); err != nil || out.Status != StatusQuestion {
		t.Fatalf("Drive to first question: out=%v err=%v", out, err)
	}

	note := "narrow to auth"
	out, err := d.Resume(ctx, f.ID, ResumeInput{RequestChanges: &note})
	if err != nil {
		t.Fatalf("Resume --request-changes: %v", err)
	}
	if out.Status != StatusQuestion {
		t.Fatalf("status = %q, want question; stream=%v", out.Status, h.eventKinds())
	}
	if len(prompts) != 2 {
		t.Fatalf("decompose prompts = %d, want 2 (initial + rerun)", len(prompts))
	}
	got, err := h.store.GetFeature(ctx, f.ID)
	if err != nil || got.Stage != domain.StageDone {
		t.Fatalf("RS card stage = %v, %v; want done throughout", got.Stage, err)
	}
}

// TestDriveResearchUntilShapeStops proves --until shape halts the drive
// loop cleanly right after investigate, at the sole pre-decompose stop on
// RS's route — the existing `d.opts.Until != "" && f.Stage == d.opts.Until`
// guard in crossGate (driver.go) requires no KindResearch-specific change.
func TestDriveResearchUntilShapeStops(t *testing.T) {
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageInvestigate: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return msgIdle(o.Model, "Investigated.")
		},
		domain.StageShape: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return msgIdle(o.Model, "Shaped.")
		},
	})
	h.fake.Caps.ReadOnlyEnforce = true

	d := h.driver(Options{Until: domain.StageShape})
	ctx := context.Background()
	f, err := d.Create(ctx, domain.KindResearch, "a research topic")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	out, err := d.Drive(ctx, f)
	if err != nil {
		t.Fatalf("Drive: %v", err)
	}
	if out.Status != StatusStopped {
		t.Fatalf("status = %q, want stopped; stream=%v", out.Status, h.eventKinds())
	}
	stopped := lastEvent(h, "stopped")
	if stopped == nil || stopped["stage"] != "shape" {
		t.Fatalf("stopped event = %v, want stage shape", stopped)
	}
	got, err := h.store.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Stage != domain.StageShape {
		t.Fatalf("RS card stage = %v, want shape", got.Stage)
	}
	if !got.VerifiedAt.IsZero() {
		t.Fatalf("RS card VerifiedAt = %v, want zero", got.VerifiedAt)
	}
}
