package driver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// researchCardAt builds an RS-kind card at the given stage, ready for the
// decompose gate: verify's blocker checks (open threads, open diff
// comments, verifydoc citations) all pass trivially against a doc with no
// %% markers and no Findings citations.
func researchCardAt(num int, stage domain.Stage) domain.Feature {
	id, _ := domain.NewID(domain.KindResearch, num)
	slug, _ := domain.Slugify("research card")
	now := time.Now()
	return domain.Feature{
		ID: id, Num: num, Kind: domain.KindResearch, Title: "research card", Slug: slug,
		Stage: stage, Budget: domain.Budget{Envelope: 500}, CreatedAt: now, UpdatedAt: now,
	}
}

// putPromotedArtifact writes body at f's promoted artifact home — the
// research equivalent of putDraft, used because these tests start the
// card already past shape/review with a settled `## Slices` doc.
func putPromotedArtifact(t *testing.T, h *harness, f *domain.Feature, body string) {
	t.Helper()
	path := filepath.Join(h.root, f.ArtifactPath())
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

const decomposeTwoRows = "# RS-001: research card\n\n## Findings\n\nNothing cited.\n\n## Slices\n\n" +
	"```yaml\n" +
	"- title: Row one\n  one-liner: first\n  depends-on: []\n  requirements: []\n  id: \"\"\n" +
	"- title: Row two\n  one-liner: second\n  depends-on: [Row one]\n  requirements: []\n  id: \"\"\n" +
	"```\n"

const decomposeScaffoldOnly = "# RS-001: research card\n\n## Slices\n\n```yaml\n" +
	"- title: example slice\n  one-liner: what it mints\n  depends-on: []\n  requirements: []\n  id: \"\"\n" +
	"```\n"

// decomposeProposalsWire builds a propose_features tool-call payload with
// one proposal per title, in order.
func decomposeProposalsWire(titles ...string) json.RawMessage {
	type feat struct {
		Title    string `json:"title"`
		OneLiner string `json:"one_liner"`
	}
	var w struct {
		Features []feat `json:"features"`
	}
	for _, ti := range titles {
		w.Features = append(w.Features, feat{Title: ti, OneLiner: "expanded"})
	}
	raw, _ := json.Marshal(w)
	return raw
}

// decomposeArchitectFn scripts the fake architect's decompose-pass replies
// (StageDone, since the card has already transitioned by the time
// DecomposeForCard spawns its session — harness.stageFromWorkDir reads the
// store's current stage). replies[n] answers the (n+1)-th call; prompts
// records every prompt seen, for assertions on --request-changes notes.
func decomposeArchitectFn(replies []json.RawMessage, prompts *[]string) stageFn {
	return func(_ *harness, n int, opts agent.SessionOpts, msg string) []agent.Event {
		*prompts = append(*prompts, strings.Join(opts.SystemHints, "\n")+"\n"+msg)
		if n >= len(replies) {
			return []agent.Event{{Kind: agent.EventIdle}}
		}
		return []agent.Event{
			{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 3, Model: opts.Model}},
			{Kind: agent.EventClientToolCall, ToolCall: &agent.ToolCall{ID: "c", Name: "propose_features", Args: replies[n]}},
			{Kind: agent.EventIdle},
		}
	}
}

func TestVerifyToDoneEmitsQuestion(t *testing.T) {
	var prompts []string
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageDone: decomposeArchitectFn([]json.RawMessage{decomposeProposalsWire("Row one", "Row two")}, &prompts),
	})
	f := researchCardAt(1, domain.StageVerify)
	if err := h.store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	putPromotedArtifact(t, h, &f, decomposeTwoRows)

	d := h.driver(Options{})
	loaded, err := h.store.GetFeature(context.Background(), f.ID)
	if err != nil {
		t.Fatal(err)
	}
	out, err := d.autoAdvance(context.Background(), loaded)
	if err != nil {
		t.Fatalf("autoAdvance: %v", err)
	}
	if out.Status != StatusQuestion {
		t.Fatalf("status = %q, want question; stream=%v", out.Status, h.eventKinds())
	}
	got, err := h.store.GetFeature(context.Background(), f.ID)
	if err != nil || got.Stage != domain.StageDone {
		t.Fatalf("RS card stage = %v, %v; want done", got.Stage, err)
	}
	feats, _ := h.store.ListFeatures(context.Background())
	if len(feats) != 1 {
		t.Fatalf("features = %d, want 1 (no auto-mint)", len(feats))
	}
	q := lastEvent(h, "question")
	if q == nil {
		t.Fatalf("no question event; stream=%v", h.eventKinds())
	}
	props, _ := q["proposals"].([]any)
	if len(props) != 2 {
		t.Fatalf("question proposals = %v, want 2", q["proposals"])
	}
	if _, ok, err := h.eng.LoadPendingDecompose(f.ID); err != nil || !ok {
		t.Fatalf("LoadPendingDecompose = ok=%v err=%v, want a pending file", ok, err)
	}
}

func TestDecomposeErrorLeavesRSDone(t *testing.T) {
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageDone: func(_ *harness, _ int, _ agent.SessionOpts, _ string) []agent.Event {
			return []agent.Event{{Kind: agent.EventError, Err: context.DeadlineExceeded}}
		},
	})
	f := researchCardAt(1, domain.StageVerify)
	if err := h.store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	putPromotedArtifact(t, h, &f, decomposeTwoRows)

	d := h.driver(Options{})
	loaded, _ := h.store.GetFeature(context.Background(), f.ID)
	out, err := d.autoAdvance(context.Background(), loaded)
	if err != nil {
		t.Fatalf("autoAdvance: %v", err)
	}
	if out.Status != StatusEscalation {
		t.Fatalf("status = %q, want escalation; stream=%v", out.Status, h.eventKinds())
	}
	got, err := h.store.GetFeature(context.Background(), f.ID)
	if err != nil || got.Stage != domain.StageDone {
		t.Fatalf("RS card stage = %v, %v; want done (never wedges)", got.Stage, err)
	}
	feats, _ := h.store.ListFeatures(context.Background())
	if len(feats) != 1 {
		t.Fatalf("features = %d, want 1 (no FDs minted on error)", len(feats))
	}
	esc := lastEvent(h, "decompose_failed")
	if esc == nil {
		t.Fatalf("no decompose_failed event; stream=%v", h.eventKinds())
	}
	raw, err := os.ReadFile(filepath.Join(h.root, f.ArtifactPath()))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "FD-") {
		t.Errorf("doc was back-annotated despite the decompose error: %s", raw)
	}
}

func TestResumeApproveMintsPendingDecompose(t *testing.T) {
	var prompts []string
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageDone: decomposeArchitectFn([]json.RawMessage{decomposeProposalsWire("Row one", "Row two")}, &prompts),
	})
	f := researchCardAt(1, domain.StageVerify)
	if err := h.store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	putPromotedArtifact(t, h, &f, decomposeTwoRows)

	d := h.driver(Options{})
	loaded, _ := h.store.GetFeature(context.Background(), f.ID)
	if _, err := d.autoAdvance(context.Background(), loaded); err != nil {
		t.Fatalf("autoAdvance: %v", err)
	}

	out, err := d.Resume(context.Background(), f.ID, ResumeInput{Approve: true})
	if err != nil {
		t.Fatalf("Resume --approve: %v", err)
	}
	if out.Status != StatusDone {
		t.Fatalf("status = %q, want done; stream=%v", out.Status, h.eventKinds())
	}
	feats, _ := h.store.ListFeatures(context.Background())
	if len(feats) != 3 { // RS card + 2 minted FDs
		t.Fatalf("features = %d, want 3", len(feats))
	}
	minted := lastEvent(h, "decompose_minted")
	if minted == nil {
		t.Fatalf("no decompose_minted event; stream=%v", h.eventKinds())
	}
	ids, _ := minted["feature_ids"].([]any)
	if len(ids) != 2 {
		t.Fatalf("decompose_minted feature_ids = %v, want 2", minted["feature_ids"])
	}
	if _, ok, err := h.eng.LoadPendingDecompose(f.ID); err != nil || ok {
		t.Fatalf("pending decompose after approve: ok=%v err=%v, want cleared", ok, err)
	}
	raw, err := os.ReadFile(filepath.Join(h.root, f.ArtifactPath()))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if !strings.Contains(string(raw), id.(string)) {
			t.Errorf("minted id %v not back-annotated into the doc", id)
		}
	}
}

// TestResumeApprovePartialMintClearsPendingKeepsRSDone forces a mid-batch
// Materialize failure through --approve: the second proposal's draft
// write is made to fail (a directory sits where its draft file would
// go), so only the first FD mints. Per the never-un-approve invariant,
// the RS card stays at done either way; the operator's recovery path is
// --request-changes, which only re-runs decompose over the row that's
// still unsettled.
func TestResumeApprovePartialMintClearsPendingKeepsRSDone(t *testing.T) {
	var prompts []string
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageDone: decomposeArchitectFn([]json.RawMessage{decomposeProposalsWire("Row one", "Row two")}, &prompts),
	})
	f := researchCardAt(1, domain.StageVerify)
	if err := h.store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	putPromotedArtifact(t, h, &f, decomposeTwoRows)

	d := h.driver(Options{})
	loaded, _ := h.store.GetFeature(context.Background(), f.ID)
	if _, err := d.autoAdvance(context.Background(), loaded); err != nil {
		t.Fatalf("autoAdvance: %v", err)
	}

	// MintFeatureNum skips nums already in use (the RS card itself holds
	// Num 1), so the two proposals mint as FD-002 and FD-003. Sit a
	// directory at the second's draft path so writeSeededDraft's
	// atomicfile.Write fails with EISDIR on rename — a deterministic
	// mid-batch failure that leaves FD-002 already created.
	secondID, _ := domain.NewFeatureID(3)
	secondSlug, _ := domain.Slugify("Row two")
	draftPath := filepath.Join(h.ws.DraftsDir(), string(secondID)+"-"+secondSlug+".md")
	if err := os.MkdirAll(draftPath, 0o750); err != nil {
		t.Fatal(err)
	}

	out, err := d.Resume(context.Background(), f.ID, ResumeInput{Approve: true})
	if err != nil {
		t.Fatalf("Resume --approve: %v", err)
	}
	if out.Status != StatusEscalation {
		t.Fatalf("status = %q, want escalation; stream=%v", out.Status, h.eventKinds())
	}

	feats, err := h.store.ListFeatures(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(feats) != 2 { // RS card + FD-001 only
		t.Fatalf("features = %d, want 2 (RS card + the one FD that minted)", len(feats))
	}

	esc := lastEvent(h, "escalation")
	if esc == nil {
		t.Fatalf("no escalation event; stream=%v", h.eventKinds())
	}
	ids, _ := esc["minted_ids"].([]any)
	if len(ids) != 1 {
		t.Fatalf("escalation minted_ids = %v, want 1", esc["minted_ids"])
	}

	if _, ok, err := h.eng.LoadPendingDecompose(f.ID); err != nil || ok {
		t.Fatalf("pending decompose after partial mint: ok=%v err=%v, want cleared", ok, err)
	}

	raw, err := os.ReadFile(filepath.Join(h.root, f.ArtifactPath()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), ids[0].(string)) {
		t.Errorf("minted id %v not back-annotated into the doc:\n%s", ids[0], raw)
	}
	if strings.Count(string(raw), "id: \"\"") == 0 {
		t.Errorf("Row two's row should still be unsettled after the partial mint:\n%s", raw)
	}

	got, err := h.store.GetFeature(context.Background(), f.ID)
	if err != nil || got.Stage != domain.StageDone {
		t.Fatalf("RS card stage = %v, %v; want done (never un-approves on partial mint)", got.Stage, err)
	}
}

func TestResumeRequestChangesRerunsDecompose(t *testing.T) {
	var prompts []string
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageDone: decomposeArchitectFn([]json.RawMessage{
			decomposeProposalsWire("Row one", "Row two"),
			decomposeProposalsWire("Row one v2", "Row two v2"),
		}, &prompts),
	})
	f := researchCardAt(1, domain.StageVerify)
	if err := h.store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	putPromotedArtifact(t, h, &f, decomposeTwoRows)

	d := h.driver(Options{})
	loaded, _ := h.store.GetFeature(context.Background(), f.ID)
	if _, err := d.autoAdvance(context.Background(), loaded); err != nil {
		t.Fatalf("autoAdvance: %v", err)
	}
	note := "tighten the second slice's scope"
	out, err := d.Resume(context.Background(), f.ID, ResumeInput{RequestChanges: &note})
	if err != nil {
		t.Fatalf("Resume --request-changes: %v", err)
	}
	if out.Status != StatusQuestion {
		t.Fatalf("status = %q, want question; stream=%v", out.Status, h.eventKinds())
	}
	if len(prompts) != 2 {
		t.Fatalf("architect called %d times, want 2 (auto-trigger + rerun)", len(prompts))
	}
	if !strings.Contains(prompts[1], note) {
		t.Errorf("rerun prompt = %q, want it to contain the note %q", prompts[1], note)
	}
	got, err := h.store.GetFeature(context.Background(), f.ID)
	if err != nil || got.Stage != domain.StageDone {
		t.Fatalf("RS card stage = %v, %v; want done", got.Stage, err)
	}
}

func TestZeroSliceRSExitsDoneCleanly(t *testing.T) {
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageDone: func(_ *harness, _ int, _ agent.SessionOpts, _ string) []agent.Event {
			t.Fatal("architect spawned for a zero-slice RS")
			return nil
		},
	})
	f := researchCardAt(1, domain.StageVerify)
	if err := h.store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	putPromotedArtifact(t, h, &f, decomposeScaffoldOnly)

	d := h.driver(Options{})
	loaded, _ := h.store.GetFeature(context.Background(), f.ID)
	out, err := d.autoAdvance(context.Background(), loaded)
	if err != nil {
		t.Fatalf("autoAdvance: %v", err)
	}
	if out.Status != StatusDone {
		t.Fatalf("status = %q, want done; stream=%v", out.Status, h.eventKinds())
	}
	if h.has("question") {
		t.Fatalf("question emitted for a zero-slice RS; stream=%v", h.eventKinds())
	}
	if _, ok, err := h.eng.LoadPendingDecompose(f.ID); err != nil || ok {
		t.Fatalf("pending decompose for a zero-slice RS: ok=%v err=%v", ok, err)
	}
}

func TestAutoTriggerFiresExactlyOncePerCrossing(t *testing.T) {
	var prompts []string
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageDone: decomposeArchitectFn([]json.RawMessage{decomposeProposalsWire("Row one", "Row two")}, &prompts),
	})
	f := researchCardAt(1, domain.StageVerify)
	if err := h.store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	putPromotedArtifact(t, h, &f, decomposeTwoRows)

	d := h.driver(Options{})
	loaded, _ := h.store.GetFeature(context.Background(), f.ID)
	if _, err := d.autoAdvance(context.Background(), loaded); err != nil {
		t.Fatalf("autoAdvance: %v", err)
	}
	if len(prompts) != 1 {
		t.Fatalf("architect called %d times on first crossing, want 1", len(prompts))
	}

	// a bare drive with no decision flag — the terminal-stage check in
	// drive() returns done before autoAdvance runs again.
	out, err := d.drive(context.Background(), f.ID)
	if err != nil {
		t.Fatalf("drive: %v", err)
	}
	if out.Status != StatusDone {
		t.Fatalf("status = %q, want done", out.Status)
	}
	if len(prompts) != 1 {
		t.Fatalf("architect called %d times after a bare re-drive, want still 1 (no second session)", len(prompts))
	}
}
