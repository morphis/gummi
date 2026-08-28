package ui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/ui/theme"
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
	m = pump(t, m, m.startIngest(src, "premium", ""))
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

	// the board rows reflect the new features: the materialize notice
	// carries reload, so the freshly minted rows appear without a second,
	// unrelated notice (regression: ingestion used to go stale on screen).
	var seen int
	for _, r := range m.rows {
		if (r.F.Title == "Payment webhooks" || r.F.Title == "Webhook retries") && r.F.Stage == domain.StageTodo {
			seen++
		}
	}
	if seen != 2 {
		t.Errorf("board shows %d of the ingested features, want 2 (rows %d)", seen, len(m.rows))
	}
}

func TestIngestSingleInFlight(t *testing.T) {
	m, _ := chatWorkspace(t, fakeProposer())
	src := writeRepoFile(t, m, "prd.md", "# PRD\nx\n")

	// mark a pass as in flight (as startIngest does) without delivering its
	// result yet, then confirm a second start is refused and 'I' won't open
	// the form.
	m.ingestRun = newIngestRunView(src)
	if cmd := m.startIngest(src, "", ""); cmd != nil {
		t.Error("a second ingest should be refused while one is in flight")
	}
	m = press(t, m, tea.KeyPressMsg{Code: 'I', Text: "I"})
	if m.Overlay.Contains("ingest-spec") {
		t.Error("I should not open the form while an ingest is in flight")
	}

	// delivering the result clears the in-flight run and opens the surface
	m = update(m, ingestLoadedMsg{res: mustDecode(t), profile: "premium", repo: ""})
	if m.ingestRun != nil {
		t.Error("in-flight run should clear when the result arrives")
	}
	if m.ingest == nil {
		t.Error("review surface should open on result")
	}
}

func TestIngestRunFeedShowsProgress(t *testing.T) {
	m, _ := chatWorkspace(t, fakeProposer())
	src := writeRepoFile(t, m, "prd.md", "# PRD\nx\n")

	// starting a pass installs the live feed in the main pane
	cmd := m.startIngest(src, "premium", "")
	if m.ingestRun == nil {
		t.Fatal("startIngest did not install the live feed")
	}
	if !strings.Contains(m.mainView(100, 30), "decomposing") {
		t.Error("main pane should show the decomposing feed")
	}

	// steps stream into the feed: milestones, tool calls, commentary
	m.ingestRun.apply(engine.IngestStep{Kind: engine.IngestStepNote, Text: "architect reading prd.md"})
	m.ingestRun.apply(engine.IngestStep{Kind: engine.IngestStepTool, Text: "read prd.md"})
	m.ingestRun.apply(engine.IngestStep{Kind: engine.IngestStepDelta, Text: "splitting the doc "})
	m.ingestRun.apply(engine.IngestStep{Kind: engine.IngestStepDelta, Text: "into slices"})
	view := m.mainView(100, 30)
	for _, want := range []string{"architect reading prd.md", "read prd.md", "splitting the doc into slices"} {
		if !strings.Contains(view, want) {
			t.Errorf("feed missing %q\n%s", want, view)
		}
	}
	// a completed message replaces the streamed tail rather than doubling it
	m.ingestRun.apply(engine.IngestStep{Kind: engine.IngestStepMessage, Text: "splitting the doc into slices"})
	if got := m.ingestRun.tail; got != "splitting the doc into slices" {
		t.Errorf("tail after message = %q", got)
	}

	// esc backgrounds the feed (the pass keeps running); I brings it back
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.ingestRun == nil || !m.ingestRun.hidden {
		t.Fatal("esc should background the feed, not discard the run")
	}
	if strings.Contains(m.mainView(100, 30), "decomposing") {
		t.Error("hidden feed should yield the main pane")
	}
	m = press(t, m, tea.KeyPressMsg{Code: 'I', Text: "I"})
	if m.ingestRun.hidden {
		t.Error("I should bring the backgrounded feed forward")
	}
	if m.Overlay.Contains("ingest-spec") {
		t.Error("I must not open a second ingest form while a pass runs")
	}

	// the finished pass clears the feed and opens the review surface
	m = pump(t, m, cmd)
	if m.ingestRun != nil {
		t.Error("feed should clear when the result arrives")
	}
	if m.ingest == nil {
		t.Error("review surface should open on result")
	}
}

// update is a thin Update helper returning the concrete Shell.
func update(m *Shell, msg tea.Msg) *Shell {
	model, _ := m.Update(msg)
	return model.(*Shell)
}

func mustDecode(t *testing.T) domain.IngestResult {
	t.Helper()
	return domain.IngestResult{Proposals: []domain.FeatureProposal{{Title: "A"}, {Title: "B"}}}
}

func TestIngestResultTakesForegroundOverSpecPane(t *testing.T) {
	m, _ := chatWorkspace(t, fakeProposer())
	// a spec pane is open when the ingest result lands
	m.spec = &specView{}
	m = update(m, ingestLoadedMsg{res: mustDecode(t), profile: "premium", repo: ""})
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
	}}, "premium", 0, "")
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
	f := newIngestForm([]string{"premium"}, nil, false, func(string, string, string) tea.Cmd { called = true; return nil })
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

// TestIngestFormRepo: the ingest form's repo field is its own tab stop,
// cycled with ←/→, and forwards the selection through onSubmit; it's
// hidden entirely when there's nothing to choose.
func TestIngestFormRepo(t *testing.T) {
	t.Run("cycles and forwards", func(t *testing.T) {
		prd := filepath.Join(t.TempDir(), "prd.md")
		if err := os.WriteFile(prd, []byte("# PRD\nx\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		var gotPath, gotProfile, gotRepo string
		f := newIngestForm([]string{"premium"}, []string{"a", "b"}, true, func(path, profile, repo string) tea.Cmd {
			gotPath, gotProfile, gotRepo = path, profile, repo
			return nil
		})
		f.path.SetValue(prd)
		// tab order is repo -> path -> profile -> buttons; from the initial
		// path focus, three forward tabs (profile -> buttons -> repo) reach
		// it, then ←/→ cycles: default -> a -> b
		f.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab})
		f.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab})
		f.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab})
		f.HandleKey(tea.KeyPressMsg{Code: tea.KeyRight})
		f.HandleKey(tea.KeyPressMsg{Code: tea.KeyRight})
		if got := f.repo.name(); got != "b" {
			t.Fatalf("repo name after two cycles = %q, want b", got)
		}
		if done, _ := f.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}); !done {
			t.Fatal("form did not submit")
		}
		if gotPath != prd || gotProfile != "premium" || gotRepo != "b" {
			t.Fatalf("onSubmit = (%q, %q, %q), want (%s, premium, b)", gotPath, gotProfile, gotRepo, prd)
		}
	})

	t.Run("field shown when repos configured", func(t *testing.T) {
		f := newIngestForm(nil, []string{"b"}, true, func(string, string, string) tea.Cmd { return nil })
		s := theme.New(theme.GummiDark())
		view := f.View(s, 60, 12)
		if !strings.Contains(view, "repo: default") {
			t.Errorf("repo field missing when repos configured:\n%s", view)
		}
	})

	t.Run("hidden when none configured", func(t *testing.T) {
		f := newIngestForm(nil, nil, false, func(string, string, string) tea.Cmd { return nil })
		s := theme.New(theme.GummiDark())
		view := f.View(s, 60, 12)
		if strings.Contains(view, "repo:") {
			t.Errorf("repo field should be hidden when no repos configured:\n%s", view)
		}
	})

	t.Run("tab skips the repo stop with none configured", func(t *testing.T) {
		f := newIngestForm(nil, nil, false, func(string, string, string) tea.Cmd { return nil })
		f.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab})
		if f.focus == ingestFieldRepo {
			t.Fatal("focus should skip the repo field when nothing is configured")
		}
		if got := f.repo.name(); got != "" {
			t.Fatalf("repo name = %q, want empty default", got)
		}
	})
}
