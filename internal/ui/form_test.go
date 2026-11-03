package ui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/ui/theme"
)

// TestDialogDescSize covers the sizing contract's two goldens plus a
// boundary case at each clamp edge.
func TestDialogDescSize(t *testing.T) {
	const staticRows = 8 // matches the old shared dialogStaticRows constant this test was written against
	cases := []struct {
		name  string
		w, h  int
		wantW int
		wantH int
	}{
		{"golden small area", 60, 20, 54, 7},
		{"golden large area", 200, 60, 104, 20},
		{"width just under the floor's trigger point stays at the floor", 51, 20, descWidthMin, 7},
		{"width past the ceiling's trigger point clamps to the ceiling", 111, 20, descWidthMax, 7},
		{"height just under the floor's trigger point stays at the floor", 60, 16, 54, descHeightMin},
		{"height past the ceiling's trigger point clamps to the ceiling", 60, 100, 54, descHeightMax},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotW, gotH := dialogDescSize(c.w, c.h, staticRows)
			if gotW != c.wantW || gotH != c.wantH {
				t.Errorf("dialogDescSize(%d, %d, %d) = (%d, %d), want (%d, %d)", c.w, c.h, staticRows, gotW, gotH, c.wantW, c.wantH)
			}
		})
	}
}

// door builds a new session's model with no repositories and no seams:
// the shape every unit test below starts from.
func door(onSubmit func(formResult) tea.Cmd) *cardForm {
	return newCardForm([]string{"thrifty", "premium"}, nil, false, "", 2400, onSubmit)
}

var keyEnter = tea.KeyPressMsg{Code: tea.KeyEnter}

// TestDialogsFitTheDrawArea: a dialog frame is as wide and as tall as
// its widest and tallest content, and the overlay centres it in the
// whole terminal — so a row that overflows is not clipped neatly, it
// carries the frame off both edges of the screen. The repository picker
// lays out one cell per `repos:` name, and has to fit the area it is
// handed, whatever the workspace names.
func TestDialogsFitTheDrawArea(t *testing.T) {
	s := theme.New(theme.GummiDark())
	repos := []string{
		"platform-core", "mobile-android", "mobile-ios", "web-console",
		"infra-terraform", "docs-site", "sdk-python", "sdk-go",
		"billing-service", "identity-service", "telemetry-pipeline",
		"design-tokens", "edge-proxy", "batch-jobs",
	}
	areas := []struct{ w, h int }{{60, 20}, {80, 24}, {100, 30}, {200, 60}}
	for _, area := range areas {
		picker := newRepoPickerDialog(domain.Feature{ID: "FD-001"}, repos, nil)
		checkFits(t, "repo picker", picker.View(s, area.w, area.h), area.w, area.h)
	}
}

// checkFits asserts no line of a dialog is wider than the draw area and
// that the whole frame leaves the status bar its row.
func checkFits(t *testing.T, name, view string, w, h int) {
	t.Helper()
	for i, line := range strings.Split(view, "\n") {
		if got := ansi.StringWidth(line); got > w {
			t.Errorf("%s at %dx%d: line %d is %d columns wide, past the terminal's edge:\n%s", name, w, h, i, got, ansi.Strip(view))
			break
		}
	}
	if got := lipgloss.Height(view); h >= 24 && got > h-dialogStatusBarMargin {
		t.Errorf("%s at %dx%d: dialog is %d rows tall:\n%s", name, w, h, got, ansi.Strip(view))
	}
}

// TestWrapHintBreaksOnSeparators: a key and what it does stay on one
// line — the hint folds at " · " or not at all.
func TestWrapHintBreaksOnSeparators(t *testing.T) {
	hint := "tab/shift+tab rows · alt+g browse issues · alt+o options · alt+enter newline · enter create · esc cancel"
	rows := wrapHint(hint, 46)
	if len(rows) < 2 {
		t.Fatalf("a 103-column hint did not fold at 46 columns: %q", rows)
	}
	for _, row := range rows {
		if ansi.StringWidth(row) > 46 {
			t.Errorf("folded hint row is %d columns: %q", ansi.StringWidth(row), row)
		}
		if strings.HasPrefix(row, "·") || strings.HasSuffix(row, "·") {
			t.Errorf("hint folded mid-separator: %q", row)
		}
	}
	if got := strings.Join(rows, " · "); got != hint {
		t.Errorf("folding changed the hint:\n got %q\nwant %q", got, hint)
	}
	if rows := wrapHint(hint, 200); len(rows) != 1 {
		t.Errorf("a hint that fits should stay on one line, got %d", len(rows))
	}
}

// TestCardFormCharLimit: the text takes as much as the thread composer
// does, and no more.
func TestCardFormCharLimit(t *testing.T) {
	form := door(nil)
	form.text.SetValue(strings.Repeat("a", threadInputCharLimit))
	if got := len(form.Text()); got != threadInputCharLimit {
		t.Fatalf("text length = %d, want %d (not truncated)", got, threadInputCharLimit)
	}
	form.text.SetValue(strings.Repeat("a", threadInputCharLimit+1))
	if got := len(form.Text()); got != threadInputCharLimit {
		t.Fatalf("text length = %d, want %d (cut at the limit)", got, threadInputCharLimit)
	}
}

// TestCardFormOpensInText: with nothing to choose, focus starts in the
// text.
func TestCardFormOpensInText(t *testing.T) {
	form := door(nil)
	if form.focus != cardStopText {
		t.Errorf("focus = %d, want the text", form.focus)
	}
}

// TestCardFormEnvelope covers the envelope's four outcomes on submit: the
// prefill, a custom value, a refusal for negative or non-numeric input,
// and an empty field as the "use the default" signal.
func TestCardFormEnvelope(t *testing.T) {
	var got formResult
	var submitted bool
	mk := func() *cardForm {
		submitted = false
		form := door(func(res formResult) tea.Cmd { got, submitted = res, true; return nil })
		form.text.SetValue("dark mode")
		return form
	}
	form := mk()
	if form.env.Value() != "24" {
		t.Fatalf("envelope prefill = %q, want 24", form.env.Value())
	}
	form.submit()
	if !submitted || got.Envelope == nil || *got.Envelope != 2400 {
		t.Fatalf("prefill submit: %v %v", submitted, got.Envelope)
	}

	form = mk()
	form.env.SetValue("75")
	form.submit()
	if got.Envelope == nil || *got.Envelope != 7500 {
		t.Fatalf("Envelope = %v, want explicit 7500", got.Envelope)
	}

	for _, bad := range []string{"-100", "abc"} {
		form = mk()
		form.env.SetValue(bad)
		if done, _ := form.submit(); done || submitted || form.errText == "" {
			t.Errorf("%q: done=%v submitted=%v err=%q", bad, done, submitted, form.errText)
		}
	}

	form = mk()
	form.env.SetValue("")
	form.submit()
	if !submitted || got.Envelope != nil {
		t.Fatalf("empty envelope: submitted=%v Envelope=%v, want nil (use default)", submitted, got.Envelope)
	}
}

// TestCardFormSubmitCarriesTheProfile: the result names the description
// and the profile chosen.
func TestCardFormSubmitCarriesTheProfile(t *testing.T) {
	var got formResult
	form := door(func(res formResult) tea.Cmd { got = res; return nil })
	form.text.SetValue("Login loops\n\nSSO users bounce back")
	form.profile = 1 // premium
	if done, _ := form.submit(); !done {
		t.Fatal("submit refused")
	}
	if got.Profile != "premium" || got.Desc != "Login loops\n\nSSO users bounce back" {
		t.Errorf("result = %+v", got)
	}
}

// TestCardFormTitlesFromTheFirstLine: the title is the text's first
// line, cut by DeriveTitle with its ellipsis where it runs long, and a
// first line with no letter or digit is refused before anything exists.
func TestCardFormTitlesFromTheFirstLine(t *testing.T) {
	form := door(nil)
	form.text.SetValue("Login loops\n\nSSO users bounce back")
	if got := form.title(); got != "Login loops" {
		t.Errorf("title = %q", got)
	}
	form.text.SetValue("tally count reports one character too many when a file has no trailing newline and the operator was hoping for a number they could actually reconcile against the ledger")
	if got := form.title(); !strings.HasSuffix(got, "…") {
		t.Errorf("a long first line is not cut with an ellipsis: %q", got)
	}
	form.text.SetValue("???")
	if done, _ := form.submit(); done || !strings.Contains(form.errText, "letter or digit") {
		t.Errorf("done=%v err=%q, want the slug refusal", done, form.errText)
	}
}

// --- the offer ---

// TestCardFormReferenceIsAnOffer: a bare issue reference on the first line
// is an offer on the from line and refuses to create; nothing is fetched
// until alt+g (github), and alt+g with no repository to resolve against
// names the repository.
func TestCardFormReferenceIsAnOffer(t *testing.T) {
	s := theme.New(theme.GummiDark())
	fetched := false
	form := newCardForm(nil, []string{"lxd", "incus"}, false, "", 2400, nil)
	form.onImport = func(domain.IssueRef, string) tea.Cmd { fetched = true; return nil }
	form.text.SetValue("#18409")

	if line := ansi.Strip(form.fromLine(s)); !strings.Contains(line, "choose a repository to resolve #18409") {
		t.Errorf("offer line = %q", line)
	}
	if done, _ := form.submit(); done {
		t.Error("submit created a card from a bare reference")
	}
	if fetched {
		t.Fatal("something fetched without alt+g")
	}
	form.github()
	if fetched || form.focus != cardStopRepo || form.errText == "" {
		t.Errorf("alt+g with no repo: fetched=%v focus=%d err=%q", fetched, form.focus, form.errText)
	}

	// a repo whose origin is not GitHub cannot resolve #N either
	form.originFor = func(string) repoOrigin { return repoOrigin{host: "gitlab.example.com", ownerRepo: "t/p"} }
	form.repo.selectName("lxd")
	form.github()
	if fetched || !strings.Contains(form.errText, "no GitHub origin") {
		t.Errorf("non-github origin: fetched=%v err=%q", fetched, form.errText)
	}
}

// TestCardFormImportReplacesOnlyTheReference: alt+g resolves #N against
// the repo's origin, and the landing replaces the reference line with the
// issue's title and body, keeps everything else typed, reports labels on
// the from line, and carries the issue's comments into the session.
func TestCardFormImportReplacesOnlyTheReference(t *testing.T) {
	s := theme.New(theme.GummiDark())
	var asked domain.IssueRef
	var got formResult
	form := newCardForm(nil, []string{"lxd", "incus"}, false, "lxd", 2400, func(res formResult) tea.Cmd { got = res; return nil })
	form.originFor = func(string) repoOrigin { return repoOrigin{host: "github.com", ownerRepo: "canonical/lxd"} }
	form.onImport = func(ref domain.IssueRef, _ string) tea.Cmd { asked = ref; return nil }
	form.text.SetValue("#18409\n\nmy own note under it")

	if line := ansi.Strip(form.fromLine(s)); !strings.Contains(line, "looks like canonical/lxd#18409") {
		t.Errorf("offer readout = %q", line)
	}
	form.github()
	if asked != (domain.IssueRef{Owner: "canonical", Repo: "lxd", Number: 18409}) || !form.fetching {
		t.Fatalf("alt+g asked for %+v fetching=%v", asked, form.fetching)
	}
	prop := domain.BugProposal{
		Title: "ZFS pool creation fails", Source: "github", ExternalRef: "https://github.com/canonical/lxd/issues/18409",
		Number: 18409, Severity: domain.SeverityHigh, State: "open", Labels: []string{"bug", "storage"},
		Body:   "Creating a pool refuses.\n\n## Steps to reproduce\n1. zfs create",
		Report: domain.BugReport{Discussion: "**b:** same here"},
	}
	form.applyIssue(prop, asked)
	if form.fetching {
		t.Error("fetching should clear on landing")
	}
	want := "ZFS pool creation fails\n\nCreating a pool refuses.\n\n## Steps to reproduce\n1. zfs create\n\nmy own note under it"
	if got := form.Text(); got != want {
		t.Errorf("text after import = %q\nwant %q", got, want)
	}
	view := ansi.Strip(form.fromLine(s))
	for _, w := range []string{"github · canonical/lxd#18409", "open", "labelled bug, storage", "1 comments"} {
		if !strings.Contains(view, w) {
			t.Errorf("provenance line missing %q:\n%s", w, view)
		}
	}
	form.submit()
	if got.ExternalRef != prop.ExternalRef || !strings.HasSuffix(got.Desc, "## Discussion\n\n**b:** same here") {
		t.Errorf("result did not carry the import: %+v", got)
	}

	// a refetch after the imported text was edited is refused
	form.text.SetValue(strings.Replace(form.Text(), "refuses", "fails", 1))
	asked = domain.IssueRef{}
	form.github()
	if asked.Number != 0 || form.errText == "" {
		t.Errorf("refetch after edit: asked=%+v err=%q", asked, form.errText)
	}
}

// TestCardFormFullReferenceNeedsNoRepoOrigin: "owner/repo#N" and an issue
// URL name their repository, so they import even where the chosen repo's
// origin is unknown.
func TestCardFormFullReferenceNeedsNoRepoOrigin(t *testing.T) {
	var asked domain.IssueRef
	form := door(nil)
	form.onImport = func(ref domain.IssueRef, _ string) tea.Cmd { asked = ref; return nil }
	form.text.SetValue("https://github.com/canonical/lxd/issues/7")
	form.github()
	if asked != (domain.IssueRef{Owner: "canonical", Repo: "lxd", Number: 7}) {
		t.Errorf("asked = %+v", asked)
	}
	// and a failed fetch lands as an error, not a hang
	form.failImport(errors.New("gh: not found"))
	if form.fetching || !strings.Contains(form.errText, "not found") {
		t.Errorf("failImport: fetching=%v err=%q", form.fetching, form.errText)
	}
}

// --- the after row ---

// --- the shell's create path ---

// TestCreateCardEnvelope: the parsed envelope stamps the persisted card's
// Budget; nil falls back to the global default and an explicit 0 stays
// uncapped.
func TestCreateCardEnvelope(t *testing.T) {
	ws, store, wt := uiRepo(t)
	ctx := context.Background()
	m := NewShell(theme.GummiDark(), "v0-test")
	m.now = func() time.Time { return time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC) }
	m.SetEnvelope(1000)
	m.Attach(store, wt, ws)

	zero := 0
	if msg := m.createCard(formResult{Desc: "Explicit uncapped", Envelope: &zero})(); msg != nil {
		if nm, ok := msg.(noticeMsg); ok && nm.isErr {
			t.Fatalf("create failed: %s", nm.text)
		}
	}
	f, err := store.GetFeature(ctx, "FF-001")
	if err != nil {
		t.Fatal(err)
	}
	if f.Budget.Envelope != 0 {
		t.Errorf("envelope = %d, want 0 (uncapped)", f.Budget.Envelope)
	}

	msg := m.createCard(formResult{Desc: "Default envelope"})()
	created, ok := msg.(cardCreatedMsg)
	if !ok {
		t.Fatalf("createCard returned %#v", msg)
	}
	f, err = store.GetFeature(ctx, created.f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if f.Budget.Envelope != 1000 {
		t.Errorf("envelope = %d, want the global default 1000", f.Budget.Envelope)
	}
}

// TestBoardNOpensTheDoorAndRemembersTheRepo: n opens a new session's
// draft, and its first message creates the session.
func TestBoardNOpensTheDoorAndRemembersTheRepo(t *testing.T) {
	m, _ := newWorkspace(t)
	m = pump(t, m, m.Init())
	m = press(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	if m.draft == nil || m.Overlay.HasDialogs() {
		t.Fatal("n did not open a new session's draft")
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.draft != nil || m.draftKept != nil {
		t.Fatal("esc on an empty draft should drop it")
	}
	m.lastRepo = ""
	m = press(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	m = typeString(t, m, "Dark mode")
	m = press(t, m, keyEnter)
	if m.draft != nil || len(m.rows) != 1 {
		t.Fatalf("create: draft=%v rows=%d notice=%q", m.draft != nil, len(m.rows), m.notice.text)
	}
	if !strings.Contains(m.notice.text, "“Dark mode” created") {
		t.Errorf("notice = %q", m.notice.text)
	}
}

// TestCardFormNamesTheSessionBeforeItStarts: a name given before it
// starts is the result's and its title, in place of the text's first
// line; left empty, the result carries none.
func TestCardFormNamesTheSessionBeforeItStarts(t *testing.T) {
	var got formResult
	form := door(func(res formResult) tea.Cmd { got = res; return nil })
	form.text.SetValue("the login page loops for SSO users")
	form.name.SetValue("SSO login loop")
	if form.title() != "SSO login loop" {
		t.Errorf("title = %q, want the name", form.title())
	}
	if done, _ := form.submit(); !done {
		t.Fatalf("submit refused: %s", form.errText)
	}
	if got.Name != "SSO login loop" || got.Desc != "the login page loops for SSO users" {
		t.Errorf("result = %+v", got)
	}

	form = door(func(res formResult) tea.Cmd { got = res; return nil })
	form.text.SetValue("Login loops")
	form.submit()
	if got.Name != "" {
		t.Errorf("an unnamed card carries the name %q", got.Name)
	}

	form = door(func(res formResult) tea.Cmd { t.Fatal("a name with no letter submitted"); return nil })
	form.text.SetValue("Login loops")
	form.name.SetValue("!!!")
	if done, _ := form.submit(); done || !strings.Contains(form.errText, "name") || form.focus != cardStopName {
		t.Errorf("done=%v err=%q focus=%d", done, form.errText, form.focus)
	}
}
