package ui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/ui/theme"
)

func sampleBugImport() engine.BugIngestResult {
	return engine.BugIngestResult{
		Source: "acme/app",
		Proposals: []domain.BugProposal{
			{Title: "Login loops", ExternalRef: "https://x/1"},
			{Title: "Logout crash", ExternalRef: "https://x/2"},
			{Title: "Footer typo", ExternalRef: "https://x/3"},
		},
		Skipped: []engine.SkippedBug{{Proposal: domain.BugProposal{Title: "old", ExternalRef: "https://x/0"}, LocalID: "BG-041"}},
	}
}

// TestBugImportOpensFilterFocused proves the picker opens ready to type:
// the filter has focus and the surface starts in the filtering state.
func TestBugImportOpensFilterFocused(t *testing.T) {
	bv := newBugIngestView(sampleBugImport(), "thrifty", 0)
	if !bv.filtering {
		t.Fatal("bug import should open with the filter focused")
	}
	if !bv.filter.Focused() {
		t.Error("filter input should be focused on open")
	}
}

func TestBugImportFilterNarrowsVisible(t *testing.T) {
	bv := newBugIngestView(sampleBugImport(), "thrifty", 0)
	if len(bv.visible()) != 3 {
		t.Fatalf("unfiltered visible = %d, want 3", len(bv.visible()))
	}
	if len(bv.skipped) != 1 || bv.skipped[0] != "BG-041" {
		t.Errorf("skipped IDs = %v, want [BG-041]", bv.skipped)
	}

	bv.filter.SetValue("log") // matches "Login loops" and "Logout crash"
	vis := bv.visible()
	if len(vis) != 2 {
		t.Fatalf("filter 'log' visible = %d, want 2", len(vis))
	}
	if bv.props[vis[0]].Title != "Login loops" || bv.props[vis[1]].Title != "Logout crash" {
		t.Errorf("filtered visible = %+v", vis)
	}

	// a filter matching nothing hides everything.
	bv.filter.SetValue("zzz")
	if len(bv.visible()) != 0 {
		t.Errorf("non-matching filter: visible=%d, want 0", len(bv.visible()))
	}
}

// TestBugImportEnterImportsExactlyHighlighted proves enter materializes the
// one row under the cursor regardless of how many issues were fetched, and
// that no other proposal is created alongside it.
func TestBugImportEnterImportsExactlyHighlighted(t *testing.T) {
	m, _ := chatWorkspace(t, agent.NewFake("hi"))
	m = pump(t, m, m.Init())
	m.bugIngest = newBugIngestView(sampleBugImport(), "thrifty", 0)
	// move focus off the filter and select the second row ("Logout crash").
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyDown})

	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.Overlay.Contains("confirm-bug-ingest") {
		t.Fatal("enter did not raise the import confirmation dialog")
	}
	m = press(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	if m.bugIngest != nil {
		t.Error("review surface should close after materialization")
	}

	all, err := m.store.ListFeatures(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var bugs []domain.Feature
	for _, f := range all {
		if f.Kind == domain.KindBug {
			bugs = append(bugs, f)
		}
	}
	if len(bugs) != 1 {
		t.Fatalf("bugs created = %d, want exactly 1: %+v", len(bugs), bugs)
	}
	if bugs[0].Title != "Logout crash" {
		t.Errorf("materialized bug = %q, want %q (the highlighted row)", bugs[0].Title, "Logout crash")
	}
}

// TestBugImportNoBulkMaterializePath proves the surface has no reachable
// key path — filtering or not — that materializes more than the single
// highlighted proposal: there is no bulk approve binding left at all.
func TestBugImportNoBulkMaterializePath(t *testing.T) {
	bv := newBugIngestView(sampleBugImport(), "thrifty", 0)
	for _, b := range bv.bindings() {
		if b.key == "A" {
			t.Errorf("bulk-approve binding %q should not exist on the single-select picker", b.key)
		}
	}
	m := &Shell{}
	m.bugIngest = newBugIngestView(sampleBugImport(), "thrifty", 0)
	m.handleBugIngestKey(tea.KeyPressMsg{Code: 'A', Text: "A"})
	if m.Overlay.Contains("confirm-bug-ingest") {
		t.Error("'A' should not raise the materialize confirmation on the single-select picker")
	}
	m.handleBugIngestKey(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if m.Overlay.Contains("confirm-bug-ingest") {
		t.Error("'x' should not raise the materialize confirmation on the single-select picker")
	}
}

// TestBugImportEscDiscardsFromEitherFocusStateQOnlyWhenListFocused proves
// esc discards the whole pass and creates nothing regardless of which
// element has focus, while q only discards once the list has focus — while
// the filter is focused, q is an ordinary character that types into the
// query (a query containing "q", e.g. "query" or "quota", is unremarkable).
func TestBugImportEscDiscardsFromEitherFocusStateQOnlyWhenListFocused(t *testing.T) {
	m := &Shell{}
	m.bugIngest = newBugIngestView(sampleBugImport(), "thrifty", 0)
	if !m.bugIngest.filtering {
		t.Fatal("expected the picker to open filtering")
	}
	m.handleBugIngestKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.bugIngest != nil {
		t.Error("esc while filtering should discard the pass immediately")
	}

	m.bugIngest = newBugIngestView(sampleBugImport(), "thrifty", 0)
	m.handleBugIngestKey(tea.KeyPressMsg{Code: tea.KeyTab}) // move focus off the filter
	m.handleBugIngestKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.bugIngest != nil {
		t.Error("esc with list focus should discard the pass")
	}

	m.bugIngest = newBugIngestView(sampleBugImport(), "thrifty", 0)
	m.handleBugIngestKey(tea.KeyPressMsg{Code: tea.KeyTab}) // move focus off the filter
	m.handleBugIngestKey(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if m.bugIngest != nil {
		t.Error("q with list focus should discard the pass")
	}

	m.bugIngest = newBugIngestView(sampleBugImport(), "thrifty", 0)
	if !m.bugIngest.filtering {
		t.Fatal("expected the picker to open filtering")
	}
	m.handleBugIngestKey(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if m.bugIngest == nil {
		t.Fatal("q while filtering should not discard the pass")
	}
	if !m.bugIngest.filtering {
		t.Error("q while filtering should leave the filter focused")
	}
	if got := m.bugIngest.filter.Value(); got != "q" {
		t.Errorf("q while filtering should type into the query, got %q", got)
	}
}

// TestBugImportRenameAndOneLinerAfterTab proves r/c and o still edit the
// highlighted proposal once Tab has moved focus off the filter.
func TestBugImportRenameAndOneLinerAfterTab(t *testing.T) {
	m := &Shell{}
	m.bugIngest = newBugIngestView(sampleBugImport(), "thrifty", 0)
	bv := m.bugIngest
	m.handleBugIngestKey(tea.KeyPressMsg{Code: tea.KeyTab})
	if bv.filtering {
		t.Fatal("tab should move focus off the filter")
	}

	m.handleBugIngestKey(tea.KeyPressMsg{Code: 'r', Text: "r"})
	if !m.Overlay.Contains("text-prompt") {
		t.Fatal("'r' after tab should open the rename prompt")
	}
	m.Overlay.Pop()

	m.handleBugIngestKey(tea.KeyPressMsg{Code: 'o', Text: "o"})
	if !m.Overlay.Contains("text-prompt") {
		t.Fatal("'o' after tab should open the one-liner prompt")
	}
}

// TestBugImportMaterializeUpdatesBoard proves the import's materialize
// notice carries reload, so the freshly minted bug row appears on the
// board without a second, unrelated notice (regression: import used to go
// stale on screen until some other reload happened to fire).
func TestBugImportMaterializeUpdatesBoard(t *testing.T) {
	m, _ := chatWorkspace(t, agent.NewFake("hi"))
	m = pump(t, m, m.Init())
	m.bugIngest = newBugIngestView(sampleBugImport(), "thrifty", 0)
	m = pump(t, m, m.materializeBugIngest())
	if m.bugIngest != nil {
		t.Error("review surface should close after materialization")
	}
	var seen int
	for _, r := range m.rows {
		if r.F.Kind == domain.KindBug && r.F.Stage == domain.StageTodo {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("board shows %d imported bugs, want 1 (rows %d)", seen, len(m.rows))
	}
}

func TestBugImportFormCommentsCheckbox(t *testing.T) {
	var submitted *bool
	form := newBugIngestForm([]string{"thrifty", "fast"},
		func(_ string, _ string, _ string, comments bool) tea.Cmd {
			submitted = &comments
			return nil
		})

	// default off; renders an unchecked box.
	if form.comments {
		t.Fatal("comments should default off")
	}
	if !strings.Contains(form.View(theme.New(theme.GummiDark()), 60, 12), "[ ] Fetch comments") {
		t.Error("view should render the unchecked comments box")
	}

	// tab to the comments field and press space to check it.
	form.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab})
	form.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab})
	form.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab})
	form.HandleKey(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	if !form.comments {
		t.Fatal("space on the comments field should check it")
	}

	// submitting passes the checked flag through.
	form.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if submitted == nil || !*submitted {
		t.Errorf("onSubmit should receive comments=true, got %v", submitted)
	}
}
