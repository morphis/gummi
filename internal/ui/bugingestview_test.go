package ui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
)

func sampleBugImport() engine.BugIngestResult {
	return engine.BugIngestResult{
		Source: "acme/app",
		Proposals: []domain.BugProposal{
			{Title: "Login loops", ExternalRef: "https://x/1", Body: "SSO users bounce back"},
			{Title: "Logout crash", ExternalRef: "https://x/2", Number: 2, Body: "panic on logout"},
			{Title: "Footer typo", ExternalRef: "https://x/3"},
		},
		Skipped: []engine.SkippedBug{{Proposal: domain.BugProposal{Title: "old", ExternalRef: "https://x/0"}, LocalID: "BG-041"}},
	}
}

func samplePicker() *bugIngestView {
	return newBugIngestView(sampleBugImport(), bugIngestParams{ownerRepo: "acme/app", label: "bug", state: "open"})
}

// TestBugImportOpensFilterFocused proves the picker opens ready to type:
// the filter has focus and the surface starts in the filtering state.
func TestBugImportOpensFilterFocused(t *testing.T) {
	bv := samplePicker()
	if !bv.filtering {
		t.Fatal("bug import should open with the filter focused")
	}
	if !bv.filter.Focused() {
		t.Error("filter input should be focused on open")
	}
}

// TestBugImportFilterNarrowsVisible: the filter narrows the list live,
// and an issue already on the board is listed too, greyed with its id,
// rather than dropped into a footnote.
func TestBugImportFilterNarrowsVisible(t *testing.T) {
	bv := samplePicker()
	if len(bv.visible()) != 4 {
		t.Fatalf("unfiltered visible = %d, want 4 (three fresh, one on the board)", len(bv.visible()))
	}
	if id, ok := bv.isOnBoard(bv.props[3]); !ok || id != "BG-041" {
		t.Errorf("the skipped issue should read as on the board as BG-041: %v %v", id, ok)
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

// TestBugImportEnterFillsTheForm proves enter mints nothing: it fills the
// new-card form with exactly the highlighted issue and brings the form
// back, and Create from that form returns to the picker with the issue's
// row greyed as on the board.
func TestBugImportEnterFillsTheForm(t *testing.T) {
	m, _ := chatWorkspace(t, agent.NewFake("hi"))
	m = pump(t, m, m.Init())
	m.bugIngest = samplePicker()
	// move focus off the filter and select the second row ("Logout crash").
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.Overlay.Contains("confirm-bug-ingest") {
		t.Fatal("enter raised the old materialize confirmation")
	}
	form, ok := m.Overlay.Top().(*cardForm)
	if !ok {
		t.Fatalf("enter did not bring up the new-card form (top=%T)", m.Overlay.Top())
	}
	if form.Kind() != domain.KindBug || !form.fromPicker || !strings.HasPrefix(form.Text(), "Logout crash\n\npanic on logout") {
		t.Errorf("form = kind %q fromPicker %v text %q", form.Kind(), form.fromPicker, form.Text())
	}
	if m.bugIngest == nil {
		t.Fatal("the picker should stay open beneath the form")
	}

	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.Overlay.HasDialogs() {
		t.Fatalf("form did not close on Create: %q", m.notice.text)
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
	if len(bugs) != 1 || bugs[0].Title != "Logout crash" || bugs[0].ExternalRef != "https://x/2" {
		t.Fatalf("bugs created = %+v, want exactly the highlighted row with its ref", bugs)
	}
	if m.bugIngest == nil {
		t.Fatal("Create from a picked issue should return to the picker")
	}
	if id, ok := m.bugIngest.isOnBoard(m.bugIngest.props[1]); !ok || id != bugs[0].ID {
		t.Errorf("the created issue's row is not greyed as on the board: %v %v", id, ok)
	}
	// enter on it again says so instead of filling the form
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.Overlay.HasDialogs() || !strings.Contains(m.notice.text, "already on the board") {
		t.Errorf("re-picking an imported issue: dialogs=%v notice=%q", m.Overlay.HasDialogs(), m.notice.text)
	}
}

// TestBugImportNoBulkMaterializePath proves the surface has no reachable
// key path — filtering or not — that creates more than one card, and no
// key that creates any: only the form does.
func TestBugImportNoBulkMaterializePath(t *testing.T) {
	bv := samplePicker()
	for _, b := range bv.bindings() {
		if b.key == "A" {
			t.Errorf("bulk-approve binding %q should not exist on the single-select picker", b.key)
		}
	}
	m := &Shell{}
	m.bugIngest = samplePicker()
	m.handleBugIngestKey(tea.KeyPressMsg{Code: 'A', Text: "A"})
	m.handleBugIngestKey(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if m.Overlay.HasDialogs() {
		t.Error("'A' and 'x' should open nothing on the single-select picker")
	}
}

// TestBugImportEscUnwindsOneLevelAtATime proves esc means "back one
// level" here like it does everywhere else: from the filter it drops
// focus to the list, and only from the list does it leave — and leaving
// brings back the form the picker was opened from.
func TestBugImportEscUnwindsOneLevelAtATime(t *testing.T) {
	m := &Shell{}
	m.bugIngest = samplePicker()
	if !m.bugIngest.filtering {
		t.Fatal("expected the picker to open filtering")
	}
	m.handleBugIngestKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.bugIngest == nil {
		t.Fatal("esc while filtering should leave the filter, not the picker")
	}
	if m.bugIngest.filtering {
		t.Error("esc while filtering should move focus to the list")
	}
	parked := door(domain.KindBug, nil)
	m.pendingCard = parked
	m.handleBugIngestKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.bugIngest != nil {
		t.Error("a second esc, with list focus, should leave the picker")
	}
	if m.Overlay.Top() != parked || m.pendingCard != nil {
		t.Error("leaving the picker should bring the parked form back")
	}

	// / puts focus back on the filter, keeping the query.
	m.bugIngest = samplePicker()
	m.handleBugIngestKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	m.handleBugIngestKey(tea.KeyPressMsg{Code: '/', Text: "/"})
	if !m.bugIngest.filtering {
		t.Error("/ should focus the filter")
	}

	m.bugIngest = samplePicker()
	m.handleBugIngestKey(tea.KeyPressMsg{Code: tea.KeyEscape}) // move focus off the filter
	m.handleBugIngestKey(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if m.bugIngest != nil {
		t.Error("q with list focus should leave the picker")
	}

	m.bugIngest = samplePicker()
	m.handleBugIngestKey(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if m.bugIngest == nil || !m.bugIngest.filtering {
		t.Fatal("q while filtering should type into the query")
	}
	if got := m.bugIngest.filter.Value(); got != "q" {
		t.Errorf("q while filtering should type into the query, got %q", got)
	}
}

// TestBugImportEditKeysOffTheFilter proves r/c and e still edit the
// highlighted proposal once focus has left the filter, and l/o open the
// prompts that refetch under a changed label or owner/repo.
func TestBugImportEditKeysOffTheFilter(t *testing.T) {
	m := &Shell{}
	m.bugIngest = samplePicker()
	bv := m.bugIngest
	m.handleBugIngestKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if bv.filtering {
		t.Fatal("esc should move focus off the filter")
	}
	for _, k := range []rune{'r', 'e', 'l', 'o'} {
		m.handleBugIngestKey(tea.KeyPressMsg{Code: k, Text: string(k)})
		if !m.Overlay.Contains("text-prompt") {
			t.Errorf("%q off the filter should open a prompt", k)
		}
		m.Overlay.Pop()
	}
}

// TestBugImportStateCyclesAndRefetches: s cycles open → closed → all and
// fetches again under the new state, in the chosen repo's checkout with
// the explicit owner/repo.
func TestBugImportStateCyclesAndRefetches(t *testing.T) {
	m, _ := chatWorkspace(t, agent.NewFake("hi"))
	m = pump(t, m, m.Init())
	var got engine.GitHubSource
	m.ghIssues = func(_ context.Context, src engine.GitHubSource) (engine.BugIngestResult, error) {
		got = src
		return sampleBugImport(), nil
	}
	m.bugIngest = samplePicker()
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	m = press(t, m, tea.KeyPressMsg{Code: 's', Text: "s"})
	if got.State != "closed" || got.Repo != "acme/app" || got.Label != "bug" {
		t.Errorf("refetch source = %+v, want state closed under the same repo and label", got)
	}
	if m.bugIngest == nil || m.bugIngest.params.state != "closed" {
		t.Errorf("picker after refetch: %+v", m.bugIngest)
	}
}

// TestBoardGBrowsesIntoTheForm: G opens the door with bug preset and goes
// straight to the picker, parking the form; a picked issue comes back in
// that same form.
func TestBoardGBrowsesIntoTheForm(t *testing.T) {
	m, _ := chatWorkspace(t, agent.NewFake("hi"))
	m = pump(t, m, m.Init())
	m.remoteOrigin = func(string) string { return "git@github.com:acme/app.git" }
	var got engine.GitHubSource
	m.ghIssues = func(_ context.Context, src engine.GitHubSource) (engine.BugIngestResult, error) {
		got = src
		return sampleBugImport(), nil
	}
	m = press(t, m, tea.KeyPressMsg{Code: 'G', Text: "G"})
	if m.Overlay.HasDialogs() {
		t.Fatalf("G should park the form and open the picker, but a dialog is up: %T", m.Overlay.Top())
	}
	if m.bugIngest == nil {
		t.Fatalf("G did not open the picker: %q", m.notice.text)
	}
	if got.Repo != "acme/app" || got.Dir == "" {
		t.Errorf("fetch source = %+v, want the origin's owner/repo in the checkout", got)
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	form, ok := m.Overlay.Top().(*cardForm)
	if !ok || form.Kind() != domain.KindBug || !strings.HasPrefix(form.Text(), "Login loops") {
		t.Fatalf("enter did not fill the parked form: %T", m.Overlay.Top())
	}
}
