package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
)

// putResearchCard persists an RS card in the store and writes body at its
// promoted artifact path, so DecomposeForCard/MintProposals (which load
// the card via the store, unlike IngestResearch) find both.
func putResearchCard(t *testing.T, store interface {
	CreateFeature(context.Context, *domain.Feature) error
}, root string, f domain.Feature, body string,
) {
	t.Helper()
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	writeResearchArtifact(t, root, f, body)
}

const decomposeThreeRows = "# RS-001: research topic\n\n" +
	"## Findings\n\nNothing cited.\n\n" +
	"## Slices\n\n" +
	"```yaml\n" +
	"- title: Row one\n" +
	"  one-liner: first\n" +
	"  depends-on: [Row three]\n" +
	"  requirements: []\n" +
	"  id: \"\"\n" +
	"- title: Row two\n" +
	"  one-liner: second\n" +
	"  depends-on: []\n" +
	"  requirements: []\n" +
	"  id: \"\"\n" +
	"- title: Row three\n" +
	"  one-liner: third\n" +
	"  depends-on: [Row two]\n" +
	"  requirements: []\n" +
	"  id: \"\"\n" +
	"```\n"

func decomposeResultFor(rows []sliceRow) domain.IngestResult {
	var res domain.IngestResult
	for _, r := range rows {
		res.Proposals = append(res.Proposals, domain.FeatureProposal{
			Title: r.Title, OneLiner: r.OneLiner, DependsOn: r.DependsOn,
		})
	}
	return res
}

func TestMintProposalsBackAnnotatesPositionallyAndWiresBothDirections(t *testing.T) {
	e, root := ingestResearchEngine(t)
	rsCard := researchCard(1, "research topic")
	rsCard.Stage = domain.StageDone
	putResearchCard(t, e.cfg.Store, root, rsCard, decomposeThreeRows)

	rows, err := unsettledSliceRows(decomposeThreeRows)
	if err != nil || len(rows) != 3 {
		t.Fatalf("unsettledSliceRows = %v, %v", rows, err)
	}
	res := decomposeResultFor(rows)

	created, err := e.MintProposals(context.Background(), rsCard.ID, res)
	if err != nil {
		t.Fatalf("MintProposals: %v", err)
	}
	if len(created) != 3 {
		t.Fatalf("created = %d, want 3", len(created))
	}

	// forward edge: row one (index 0) depends on row three (index 2, appears
	// later in doc order).
	deps0, err := e.cfg.Store.ListDependencies(context.Background(), created[0].ID)
	if err != nil || len(deps0) != 1 || deps0[0] != created[2].ID {
		t.Errorf("row one deps = %v, %v; want [%s] (forward edge)", deps0, err, created[2].ID)
	}
	// backward edge: row three (index 2) depends on row two (index 1,
	// appears earlier in doc order).
	deps2, err := e.cfg.Store.ListDependencies(context.Background(), created[2].ID)
	if err != nil || len(deps2) != 1 || deps2[0] != created[1].ID {
		t.Errorf("row three deps = %v, %v; want [%s] (backward edge)", deps2, err, created[1].ID)
	}

	raw, err := os.ReadFile(filepath.Join(root, rsCard.ArtifactPath()))
	if err != nil {
		t.Fatal(err)
	}
	stillUnsettled, err := unsettledSliceRows(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(stillUnsettled) != 0 {
		t.Fatalf("still-unsettled rows after mint = %v, want none", stillUnsettled)
	}
	body, _ := spec.ViewSection(string(raw), "Slices")
	for i, f := range created {
		if !bytes.Contains([]byte(body), []byte(string(f.ID))) {
			t.Errorf("row %d's minted id %s not back-annotated into the doc", i, f.ID)
		}
	}
}

func TestMintProposalsCountMismatchMintsNothing(t *testing.T) {
	e, root := ingestResearchEngine(t)
	rsCard := researchCard(1, "research topic")
	rsCard.Stage = domain.StageDone
	putResearchCard(t, e.cfg.Store, root, rsCard, decomposeThreeRows)

	res := domain.IngestResult{Proposals: []domain.FeatureProposal{{Title: "only one"}}}
	created, err := e.MintProposals(context.Background(), rsCard.ID, res)
	if err == nil || len(created) != 0 {
		t.Fatalf("MintProposals with mismatched count = %v, %v; want ErrDecomposeProposalCountMismatch, no creation", created, err)
	}
	feats, _ := e.cfg.Store.ListFeatures(context.Background())
	if len(feats) != 1 { // only the RS card itself
		t.Fatalf("features after mismatched mint = %d, want 1 (no FDs minted)", len(feats))
	}
}

// TestMintProposalsPropagatesBackAnnotationFailure proves a mint whose
// back-annotation write didn't persist is never reported as a clean
// success: the caller sees the error and can escalate instead of moving
// on believing the doc's rows were settled (they weren't, so the next
// pass would silently re-decompose and duplicate-mint them).
func TestMintProposalsPropagatesBackAnnotationFailure(t *testing.T) {
	e, root := ingestResearchEngine(t)
	rsCard := researchCard(1, "research topic")
	rsCard.Stage = domain.StageDone
	putResearchCard(t, e.cfg.Store, root, rsCard, decomposeThreeRows)

	rows, err := unsettledSliceRows(decomposeThreeRows)
	if err != nil || len(rows) != 3 {
		t.Fatalf("unsettledSliceRows = %v, %v", rows, err)
	}
	res := decomposeResultFor(rows)

	artifactDir := filepath.Dir(filepath.Join(root, rsCard.ArtifactPath()))
	if err := os.Chmod(artifactDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(artifactDir, 0o700) })

	created, err := e.MintProposals(context.Background(), rsCard.ID, res)
	if err == nil {
		t.Fatalf("MintProposals with an unwritable artifact dir = nil error, want one naming the back-annotation failure")
	}
	if len(created) != 3 {
		t.Fatalf("created = %d, want 3 (FDs mint even when back-annotation can't persist)", len(created))
	}
}

func TestDecomposeForCardZeroSliceNoOp(t *testing.T) {
	e, root := ingestResearchEngine(t)
	rsCard := researchCard(1, "research topic")
	rsCard.Stage = domain.StageDone
	body := "# RS-001: research topic\n\n## Slices\n\n```yaml\n" +
		"- title: example slice\n  one-liner: what it mints\n  depends-on: []\n  requirements: []\n  id: \"\"\n" +
		"```\n"
	putResearchCard(t, e.cfg.Store, root, rsCard, body)

	res, err := e.DecomposeForCard(context.Background(), rsCard.ID, "")
	if err != nil {
		t.Fatalf("DecomposeForCard: %v", err)
	}
	if len(res.Proposals) != 0 {
		t.Fatalf("proposals = %v, want none (scaffold-only doc)", res.Proposals)
	}
}

func TestBackAnnotationIdempotentOnSettledDoc(t *testing.T) {
	e, root := ingestResearchEngine(t)
	rsCard := researchCard(1, "research topic")
	rsCard.Stage = domain.StageDone
	settled := "# RS-001: research topic\n\n## Slices\n\n" +
		"```yaml\n" +
		"- title: Row one\n  one-liner: first\n  depends-on: []\n  requirements: []\n  id: \"FD-001\"\n" +
		"```\n"
	putResearchCard(t, e.cfg.Store, root, rsCard, settled)

	path := filepath.Join(root, rsCard.ArtifactPath())
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	res, err := e.DecomposeForCard(context.Background(), rsCard.ID, "")
	if err != nil {
		t.Fatalf("DecomposeForCard: %v", err)
	}
	if len(res.Proposals) != 0 {
		t.Fatalf("proposals = %v, want none (fully settled doc)", res.Proposals)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("settled doc changed on a no-op decompose pass:\nbefore=%s\nafter=%s", before, after)
	}
}

func TestClearingSliceIDReopensRow(t *testing.T) {
	doc := "# RS-001: t\n\n## Slices\n\n```yaml\n" +
		"- title: Row one\n  one-liner: a\n  depends-on: []\n  requirements: []\n  id: \"FD-001\"\n" +
		"- title: Row two\n  one-liner: b\n  depends-on: []\n  requirements: []\n  id: \"FD-002\"\n" +
		"```\n"
	rows, err := unsettledSliceRows(doc)
	if err != nil || len(rows) != 0 {
		t.Fatalf("unsettledSliceRows(fully settled) = %v, %v; want none", rows, err)
	}

	reopened := "# RS-001: t\n\n## Slices\n\n```yaml\n" +
		"- title: Row one\n  one-liner: a\n  depends-on: []\n  requirements: []\n  id: \"\"\n" +
		"- title: Row two\n  one-liner: b\n  depends-on: []\n  requirements: []\n  id: \"FD-002\"\n" +
		"```\n"
	rows, err = unsettledSliceRows(reopened)
	if err != nil || len(rows) != 1 || rows[0].Title != "Row one" {
		t.Fatalf("unsettledSliceRows(cleared id) = %v, %v; want [Row one]", rows, err)
	}
}

func TestAddingSliceIDSettlesRow(t *testing.T) {
	doc := "# RS-001: t\n\n## Slices\n\n```yaml\n" +
		"- title: Row one\n  one-liner: a\n  depends-on: []\n  requirements: []\n  id: \"\"\n" +
		"- title: Row two\n  one-liner: b\n  depends-on: []\n  requirements: []\n  id: \"\"\n" +
		"```\n"
	rows, err := unsettledSliceRows(doc)
	if err != nil || len(rows) != 2 {
		t.Fatalf("unsettledSliceRows(both open) = %v, %v; want 2", rows, err)
	}

	handWritten := "# RS-001: t\n\n## Slices\n\n```yaml\n" +
		"- title: Row one\n  one-liner: a\n  depends-on: []\n  requirements: []\n  id: \"FD-009\"\n" +
		"- title: Row two\n  one-liner: b\n  depends-on: []\n  requirements: []\n  id: \"\"\n" +
		"```\n"
	rows, err = unsettledSliceRows(handWritten)
	if err != nil || len(rows) != 1 || rows[0].Title != "Row two" {
		t.Fatalf("unsettledSliceRows(one hand-settled) = %v, %v; want [Row two]", rows, err)
	}
}

// TestDecomposeForCardArchitectPass exercises the full session path (not
// just the mechanics MintProposals/unsettledSliceRows own): a fake
// architect returns 3 proposals in doc order via propose_features, and the
// pass meters its usage onto the RS card's own spend (the "bills to the
// RS card's own envelope" contract).
func TestDecomposeForCardArchitectPass(t *testing.T) {
	rows, err := unsettledSliceRows(decomposeThreeRows)
	if err != nil || len(rows) != 3 {
		t.Fatalf("fixture unsettledSliceRows = %v, %v", rows, err)
	}
	wire, _ := json.Marshal(struct {
		Features []map[string]any `json:"features"`
	}{Features: []map[string]any{
		{"title": "Row one", "one_liner": "first", "depends_on": []string{"Row three"}},
		{"title": "Row two", "one_liner": "second"},
		{"title": "Row three", "one_liner": "third", "depends_on": []string{"Row two"}},
	}})
	var promptSeen string
	ag := &agent.Fake{
		Caps: agent.Capabilities{ClientTools: true},
		Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
			if opts.Role != agent.RoleArchitect {
				t.Errorf("decompose used role %q, want architect", opts.Role)
			}
			promptSeen = msg
			return []agent.Event{
				{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 5, Model: opts.Model}},
				{Kind: agent.EventClientToolCall, ToolCall: &agent.ToolCall{ID: "c1", Name: ingestToolName, Args: json.RawMessage(wire)}},
				{Kind: agent.EventIdle},
			}
		},
	}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	rsCard := researchCard(1, "research topic")
	rsCard.Stage = domain.StageDone
	if err := store.CreateFeature(context.Background(), &rsCard); err != nil {
		t.Fatal(err)
	}
	writeResearchArtifact(t, ws.Root, rsCard, decomposeThreeRows)

	res, err := e.DecomposeForCard(context.Background(), rsCard.ID, "")
	if err != nil {
		t.Fatalf("DecomposeForCard: %v", err)
	}
	if len(res.Proposals) != 3 {
		t.Fatalf("proposals = %d, want 3", len(res.Proposals))
	}
	if promptSeen == "" {
		t.Fatal("architect never received a prompt")
	}
	got, err := store.GetFeature(context.Background(), rsCard.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Spend.Credits != 5 {
		t.Errorf("RS card Spend.Credits = %v, want 5 (metered onto the card's own envelope)", got.Spend.Credits)
	}
	if got.Spend.DecomposeCredits != 5 {
		t.Errorf("RS card Spend.DecomposeCredits = %v, want 5 (charged to the decompose bucket, distinguishable in reporting)", got.Spend.DecomposeCredits)
	}
}

// TestDecomposeForCardArchitectPassByokMetersDecomposeTokenBucket is the
// BYOK counterpart to TestDecomposeForCardArchitectPass: a token-only
// usage event must grow both the overall and decompose token scalars
// identically, so DecomposeCreditEquivalentAt(rate) == CreditEquivalentAt(rate)
// at any rate — the "distinguishable in reporting" contract holds for
// BYOK billing, not just hosted.
func TestDecomposeForCardArchitectPassByokMetersDecomposeTokenBucket(t *testing.T) {
	rows, err := unsettledSliceRows(oneRowDoc)
	if err != nil || len(rows) != 1 {
		t.Fatalf("fixture unsettledSliceRows = %v, %v", rows, err)
	}
	wire, _ := json.Marshal(struct {
		Features []map[string]any `json:"features"`
	}{Features: []map[string]any{{"title": "Row one", "one_liner": "first"}}})
	ag := &agent.Fake{
		Caps: agent.Capabilities{ClientTools: true},
		Rate: 2.0,
		Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
			return []agent.Event{
				{Kind: agent.EventUsage, Usage: agent.Usage{InputTokens: 2000, OutputTokens: 1000, Model: opts.Model}},
				{Kind: agent.EventClientToolCall, ToolCall: &agent.ToolCall{ID: "c1", Name: ingestToolName, Args: json.RawMessage(wire)}},
			}
		},
	}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	rsCard := researchCard(1, "research topic")
	rsCard.Stage = domain.StageDone
	if err := store.CreateFeature(context.Background(), &rsCard); err != nil {
		t.Fatal(err)
	}
	writeResearchArtifact(t, ws.Root, rsCard, oneRowDoc)

	if _, err := e.DecomposeForCard(context.Background(), rsCard.ID, ""); err != nil {
		t.Fatalf("DecomposeForCard: %v", err)
	}
	got, err := store.GetFeature(context.Background(), rsCard.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Spend.InputTokens != 2000 || got.Spend.OutputTokens != 1000 {
		t.Fatalf("overall tokens = %d/%d, want 2000/1000", got.Spend.InputTokens, got.Spend.OutputTokens)
	}
	if got.Spend.DecomposeInputTokens != 2000 || got.Spend.DecomposeOutputTokens != 1000 {
		t.Fatalf("decompose tokens = %d/%d, want 2000/1000", got.Spend.DecomposeInputTokens, got.Spend.DecomposeOutputTokens)
	}
	if got.Spend.DecomposeCreditEquivalentAt(2.0) != got.Spend.CreditEquivalentAt(2.0) {
		t.Errorf("DecomposeCreditEquivalentAt(2.0) = %v, want equal to CreditEquivalentAt(2.0) = %v",
			got.Spend.DecomposeCreditEquivalentAt(2.0), got.Spend.CreditEquivalentAt(2.0))
	}
}

// oneRowDoc is a minimal single-slice-row RS doc for the reserve tests
// below, which care about the envelope math rather than the multi-row
// dependency wiring TestDecomposeForCardArchitectPass already covers.
const oneRowDoc = "# RS-001: research topic\n\n## Findings\n\nNothing cited.\n\n" +
	"## Slices\n\n```yaml\n" +
	"- title: Row one\n  one-liner: first\n  depends-on: []\n  requirements: []\n  id: \"\"\n" +
	"```\n"

func oneRowFakeArchitect(capture *[]float64) *agent.Fake {
	wire, _ := json.Marshal(struct {
		Features []map[string]any `json:"features"`
	}{Features: []map[string]any{{"title": "Row one", "one_liner": "first"}}})
	return &agent.Fake{
		Caps: agent.Capabilities{ClientTools: true},
		Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
			*capture = append(*capture, opts.MaxCredits)
			return []agent.Event{
				{Kind: agent.EventClientToolCall, ToolCall: &agent.ToolCall{ID: "c1", Name: ingestToolName, Args: json.RawMessage(wire)}},
			}
		},
	}
}

// TestDecomposeForCardCapsSessionAtRemainingReserve proves the decompose
// session's MaxCredits is the RS card's actual remaining envelope
// (Envelope − Spend.CreditEquivalentAt(rate)) — not the general stage
// budget ceiling — so a card that has already spent down to just inside
// the reserve boundary admits only what's left of it.
func TestDecomposeForCardCapsSessionAtRemainingReserve(t *testing.T) {
	var caps []float64
	ag := oneRowFakeArchitect(&caps)
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	rsCard := researchCard(1, "research topic")
	rsCard.Stage = domain.StageDone
	rsCard.Budget.Envelope = 100
	if err := store.CreateFeature(context.Background(), &rsCard); err != nil {
		t.Fatal(err)
	}
	// 75 spent of a 100 envelope: 25 remaining (5 inside the 30-credit
	// reserve boundary that non-decompose stages were held to).
	if err := store.AddSpend(context.Background(), rsCard.ID, 75, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	writeResearchArtifact(t, ws.Root, rsCard, oneRowDoc)

	if _, err := e.DecomposeForCard(context.Background(), rsCard.ID, ""); err != nil {
		t.Fatalf("DecomposeForCard: %v", err)
	}
	if len(caps) != 1 || caps[0] != 25 {
		t.Fatalf("admitted MaxCredits = %v, want [25]", caps)
	}
}

// TestDecomposeForCardExhaustedWhenReserveDrained proves a fully-drained
// envelope refuses to spawn a decompose session at all.
func TestDecomposeForCardExhaustedWhenReserveDrained(t *testing.T) {
	var caps []float64
	ag := oneRowFakeArchitect(&caps)
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	rsCard := researchCard(1, "research topic")
	rsCard.Stage = domain.StageDone
	rsCard.Budget.Envelope = 100
	if err := store.CreateFeature(context.Background(), &rsCard); err != nil {
		t.Fatal(err)
	}
	if err := store.AddSpend(context.Background(), rsCard.ID, 100, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	writeResearchArtifact(t, ws.Root, rsCard, oneRowDoc)

	_, err := e.DecomposeForCard(context.Background(), rsCard.ID, "")
	if !errors.Is(err, ErrDecomposeExhausted) {
		t.Fatalf("DecomposeForCard on a drained envelope = %v, want ErrDecomposeExhausted", err)
	}
	if len(caps) != 0 {
		t.Fatalf("architect session spawned on a drained envelope: %v", caps)
	}
}

// TestDecomposeForCardRerunShrinksSessionCap proves re-runs shrink the
// admitted cap by every credit a prior decompose session actually
// spent, rather than granting the same cap on every call — a card whose
// decompose keeps failing eventually admits no further sessions.
func TestDecomposeForCardRerunShrinksSessionCap(t *testing.T) {
	var caps []float64
	wire, _ := json.Marshal(struct {
		Features []map[string]any `json:"features"`
	}{Features: []map[string]any{{"title": "Row one", "one_liner": "first"}}})
	call := 0
	ag := &agent.Fake{
		Caps: agent.Capabilities{ClientTools: true},
		Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
			call++
			caps = append(caps, opts.MaxCredits)
			if call == 1 {
				// spend 20 credits, then die mid-session without minting —
				// the never-wedges error path (step 2), exercised here only
				// for its metering side effect on the reserve.
				return []agent.Event{
					{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 20}},
					{Kind: agent.EventError, Err: errors.New("transient failure")},
				}
			}
			return []agent.Event{
				{Kind: agent.EventClientToolCall, ToolCall: &agent.ToolCall{ID: "c1", Name: ingestToolName, Args: json.RawMessage(wire)}},
			}
		},
	}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	rsCard := researchCard(1, "research topic")
	rsCard.Stage = domain.StageDone
	rsCard.Budget.Envelope = 100
	if err := store.CreateFeature(context.Background(), &rsCard); err != nil {
		t.Fatal(err)
	}
	// 70 spent of a 100 envelope: 30 remaining (the full reserve).
	if err := store.AddSpend(context.Background(), rsCard.ID, 70, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	writeResearchArtifact(t, ws.Root, rsCard, oneRowDoc)

	if _, err := e.DecomposeForCard(context.Background(), rsCard.ID, ""); err == nil {
		t.Fatal("first DecomposeForCard call = nil error, want the fake's transient failure")
	}
	if _, err := e.DecomposeForCard(context.Background(), rsCard.ID, ""); err != nil {
		t.Fatalf("second DecomposeForCard call: %v", err)
	}
	if len(caps) != 2 || caps[0] != 30 || caps[1] != 10 {
		t.Fatalf("admitted MaxCredits across re-runs = %v, want [30 10] (30 remaining, then 30-20=10 after the first call's metered spend)", caps)
	}
}
