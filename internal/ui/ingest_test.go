package ui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/morphia/gummi/internal/agent"
	"github.com/morphia/gummi/internal/domain"
)

const uiProposalJSON = `{
  "features": [
    {"title": "Payment webhooks", "one_liner": "receive callbacks",
     "source_refs": ["Payments"], "skip": {"brainstorm": true},
     "problem": "We miss async state.", "open_questions": ["which providers?"]},
    {"title": "Webhook retries", "one_liner": "retry failed",
     "depends_on": ["Payment webhooks"], "problem": "Deliveries dropped."}
  ],
  "coverage": [
    {"requirement": "callbacks", "feature": "Payment webhooks", "status": "mapped"},
    {"requirement": "gdpr export", "status": "unmapped", "note": "unclear owner"}
  ]
}`

// fakeProposer is a Fake agent that answers every ingest pass with the
// same two-feature decomposition.
func fakeProposer() *agent.Fake {
	return &agent.Fake{
		Caps: agent.Capabilities{ClientTools: true},
		Responder: func(agent.SessionOpts, string) []agent.Event {
			return []agent.Event{
				{Kind: agent.EventClientToolCall, ToolCall: &agent.ToolCall{ID: "c1", Name: "propose_features", Args: json.RawMessage(uiProposalJSON)}},
				{Kind: agent.EventIdle},
			}
		},
	}
}

func writeRepoFile(t *testing.T, m *Shell, name, body string) string {
	t.Helper()
	p := filepath.Join(m.ws.Root, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestIngestReviewFlowMaterializes(t *testing.T) {
	m, _ := chatWorkspace(t, fakeProposer()) // also creates FD-001
	src := writeRepoFile(t, m, "prd.md", "# PRD\nrequirements\n")

	// run the pass and open the review surface
	m = pump(t, m, m.startIngest(src, "premium"))
	if m.ingest == nil {
		t.Fatal("ingest review surface did not open")
	}
	if got := m.ingest.keptCount(); got != 2 {
		t.Fatalf("keptCount = %d, want 2", got)
	}

	// the surface renders the proposals and flags the unmapped requirement
	view := m.ingestViewRender(100, 30)
	for _, want := range []string{"Payment webhooks", "Webhook retries", "gdpr export"} {
		if !strings.Contains(view, want) {
			t.Errorf("render missing %q\n%s", want, view)
		}
	}

	// drop the first proposal, then undo
	m = press(t, m, tea.KeyPressMsg{Code: 'x', Text: "x"})
	if m.ingest.keptCount() != 1 {
		t.Fatalf("after drop keptCount = %d, want 1", m.ingest.keptCount())
	}
	m = press(t, m, tea.KeyPressMsg{Code: 'x', Text: "x"})
	if m.ingest.keptCount() != 2 {
		t.Fatalf("after undo keptCount = %d, want 2", m.ingest.keptCount())
	}

	// approve → confirmation dialog
	m = press(t, m, tea.KeyPressMsg{Code: 'A', Text: "A"})
	if !m.Overlay.Contains("confirm-ingest") {
		t.Fatal("approve did not raise the confirmation dialog")
	}
	// confirm → materialize
	m = press(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	if m.ingest != nil {
		t.Error("review surface should close after materialization")
	}

	// two features minted into todo, alongside the pre-existing FD-001
	all, err := m.store.ListFeatures(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ingested int
	for _, f := range all {
		if (f.Title == "Payment webhooks" || f.Title == "Webhook retries") && f.Stage == domain.StageTodo && f.Profile == "premium" {
			ingested++
		}
	}
	if ingested != 2 {
		t.Errorf("materialized %d ingested features, want 2 (of %d total)", ingested, len(all))
	}
}

func TestIngestSingleInFlight(t *testing.T) {
	m, _ := chatWorkspace(t, fakeProposer())
	src := writeRepoFile(t, m, "prd.md", "# PRD\nx\n")

	// mark a pass as in flight (as startIngest does) without delivering its
	// result yet, then confirm a second start is refused and 'I' won't open
	// the form.
	m.ingesting = true
	if cmd := m.startIngest(src, ""); cmd != nil {
		t.Error("a second ingest should be refused while one is in flight")
	}
	m = press(t, m, tea.KeyPressMsg{Code: 'I', Text: "I"})
	if m.Overlay.Contains("ingest-spec") {
		t.Error("I should not open the form while an ingest is in flight")
	}

	// delivering the result clears the in-flight flag and opens the surface
	m, _ = update(m, ingestLoadedMsg{res: mustDecode(t), profile: "premium"})
	if m.ingesting {
		t.Error("in-flight flag should clear when the result arrives")
	}
	if m.ingest == nil {
		t.Error("review surface should open on result")
	}
}

// update is a thin Update helper returning the concrete Shell.
func update(m *Shell, msg tea.Msg) (*Shell, tea.Cmd) {
	model, cmd := m.Update(msg)
	return model.(*Shell), cmd
}

func mustDecode(t *testing.T) domain.IngestResult {
	t.Helper()
	return domain.IngestResult{Proposals: []domain.FeatureProposal{{Title: "A"}, {Title: "B"}}}
}

func TestIngestResultTakesForegroundOverSpecPane(t *testing.T) {
	m, _ := chatWorkspace(t, fakeProposer())
	// a spec pane is open when the ingest result lands
	m.spec = &specView{}
	m, _ = update(m, ingestLoadedMsg{res: mustDecode(t), profile: "premium"})
	if m.spec != nil {
		t.Error("spec pane should be cleared so the review surface isn't hidden")
	}
	if m.ingest == nil {
		t.Error("review surface should be installed and visible")
	}
}

func TestIngestKeyOpensForm(t *testing.T) {
	m, _ := chatWorkspace(t, fakeProposer())
	m = press(t, m, tea.KeyPressMsg{Code: 'I', Text: "I"})
	if !m.Overlay.Contains("ingest-spec") {
		t.Fatal("I did not open the ingest form")
	}
}

func TestIngestViewMerge(t *testing.T) {
	iv := newIngestView(domain.IngestResult{Proposals: []domain.FeatureProposal{
		{Title: "A", SourceRefs: []string{"s1"}, Draft: domain.DraftSeed{Problem: "pa", OpenQuestions: []string{"qa"}}},
		{Title: "B", SourceRefs: []string{"s2"}, DependsOn: []string{"A", "C"}, Draft: domain.DraftSeed{Problem: "pb", OpenQuestions: []string{"qb"}}},
	}}, "premium", 0)
	iv.setCursor(1)
	if !iv.mergeIntoPrev() {
		t.Fatal("merge should succeed for the second proposal")
	}
	if len(iv.props) != 1 || iv.cursor != 0 {
		t.Fatalf("after merge: %d proposals, cursor %d", len(iv.props), iv.cursor)
	}
	merged := iv.props[0].p
	if len(merged.SourceRefs) != 2 {
		t.Errorf("merged refs = %v, want both", merged.SourceRefs)
	}
	if merged.Draft.Problem != "pa\n\npb" {
		t.Errorf("merged problem = %q", merged.Draft.Problem)
	}
	if len(merged.Draft.OpenQuestions) != 2 {
		t.Errorf("merged open questions = %v", merged.Draft.OpenQuestions)
	}
	// the dependency on "A" (now the survivor itself) is dropped; "C" stays
	if len(merged.DependsOn) != 1 || merged.DependsOn[0] != "C" {
		t.Errorf("merged deps = %v, want [C] (self-dep removed)", merged.DependsOn)
	}
	// merge at the top is a no-op
	iv.setCursor(0)
	if iv.mergeIntoPrev() {
		t.Error("merge at the top of the list should be a no-op")
	}
}

func TestIngestFormRejectsMissingFile(t *testing.T) {
	var called bool
	f := newIngestForm([]string{"premium"}, func(string, string) tea.Cmd { called = true; return nil })
	// type a path that doesn't exist, then submit
	for _, r := range "/no/such/spec.md" {
		f.HandleKey(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	consumed, _ := f.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if consumed {
		t.Error("submit with a missing file should not close the form")
	}
	if called {
		t.Error("onSubmit should not fire for a missing file")
	}
	if f.errText == "" {
		t.Error("missing file should set an error message")
	}
}
