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

// door builds a new-card dialog with no repositories and no seams: the
// shape every unit test below starts from.
func door(kind domain.Kind, onSubmit func(formResult) tea.Cmd) *cardForm {
	return newCardForm(kind, []string{"thrifty", "premium"}, nil, false, "", nil, 2400, onSubmit)
}

func doorKey(code rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code, Text: string(code)} }

var (
	keyEnter = tea.KeyPressMsg{Code: tea.KeyEnter}
	keyTab   = tea.KeyPressMsg{Code: tea.KeyTab}
	keyRight = tea.KeyPressMsg{Code: tea.KeyRight}
	altG     = tea.KeyPressMsg{Code: 'g', Mod: tea.ModAlt}
	altO     = tea.KeyPressMsg{Code: 'o', Mod: tea.ModAlt}
)

// TestCardFormSizing renders the door at a small and a large draw area
// and asserts the text box fills what its own static rows leave, and
// the whole dialog fits the area with the status bar's row spared.
func TestCardFormSizing(t *testing.T) {
	styles := theme.New(theme.GummiDark())
	for _, area := range []struct{ w, h int }{{60, 20}, {100, 30}, {200, 60}} {
		form := door(domain.KindFeature, nil)
		view := form.View(styles, area.w, area.h)
		lines := strings.Split(form.text.View(), "\n")
		wantW, _ := dialogDescSize(area.w, area.h, 0)
		for i, l := range lines {
			if gotW := lipgloss.Width(l); gotW != wantW {
				t.Errorf("at %dx%d: text line %d width = %d, want %d", area.w, area.h, i, gotW, wantW)
			}
		}
		if len(lines) < descHeightMin {
			t.Errorf("at %dx%d: text box has %d rows, below the floor", area.w, area.h, len(lines))
		}
		if h := lipgloss.Height(view); h > area.h-dialogStatusBarMargin {
			t.Errorf("at %dx%d: dialog is %d rows tall, paints over the status bar", area.w, area.h, h)
		}
	}
}

// TestCardFormCharLimit: the text accepts up to 4096 characters.
func TestCardFormCharLimit(t *testing.T) {
	form := door(domain.KindFeature, nil)
	long := strings.Repeat("a", 4096)
	form.SetText(long)
	if got := len(form.Text()); got != 4096 {
		t.Fatalf("text length = %d, want 4096 (not truncated)", got)
	}
}

// TestCardFormOpensInTextWithKindPreset: with nothing to choose, focus
// starts in the text, and the preset kind is the row's choice.
func TestCardFormOpensInTextWithKindPreset(t *testing.T) {
	for _, kind := range cardKinds {
		form := door(kind, nil)
		if form.focus != cardStopText || !form.text.Focused() {
			t.Errorf("%s: focus = %d, want the text", kind, form.focus)
		}
		if form.Kind() != kind {
			t.Errorf("kind = %q, want %q", form.Kind(), kind)
		}
	}
	if form := door("bogus", nil); form.Kind() != domain.KindFeature {
		t.Errorf("an invalid preset should read as feature, got %q", form.Kind())
	}
}

// TestCardFormKindRowCycles: ←/→ on the kind row move it; a printable
// key there falls through to the text instead of doing nothing.
func TestCardFormKindRowCycles(t *testing.T) {
	form := door(domain.KindFeature, nil)
	form.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}) // buttons
	form.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}) // after (expands)
	form.setFocus(cardStopKind)
	form.HandleKey(keyRight)
	if form.Kind() != domain.KindBug {
		t.Fatalf("→ on kind = %q, want bug", form.Kind())
	}
	form.HandleKey(keyRight)
	form.HandleKey(keyRight)
	if form.Kind() != domain.KindFeature {
		t.Fatalf("kind should wrap back to feature, got %q", form.Kind())
	}
	form.HandleKey(doorKey('d'))
	if form.focus != cardStopText || form.Text() != "d" {
		t.Errorf("typing on the kind row should type into the text: focus=%d text=%q", form.focus, form.Text())
	}
}

// TestCardFormTabOrder: tab walks kind, text, then expands the options —
// envelope, profile, (severity for a bug), after — then the buttons, and
// wraps. The repo stop is absent when there is nothing to choose.
func TestCardFormTabOrder(t *testing.T) {
	form := door(domain.KindFeature, nil)
	if form.expanded {
		t.Fatal("options should open collapsed")
	}
	for _, want := range []int{cardStopText, cardStopEnvelope, cardStopProfile, cardStopAfter, cardStopButtons, cardStopKind, cardStopText} {
		if form.focus != want {
			t.Fatalf("focus = %d, want %d", form.focus, want)
		}
		form.HandleKey(keyTab)
	}
	if !form.expanded {
		t.Error("tabbing onto the runs line should have expanded the options")
	}
	bug := door(domain.KindBug, nil)
	for _, want := range []int{cardStopText, cardStopEnvelope, cardStopProfile, cardStopSeverity, cardStopAfter} {
		if bug.focus != want {
			t.Fatalf("bug focus = %d, want %d", bug.focus, want)
		}
		bug.HandleKey(keyTab)
	}
	// a severity stop that stops existing hands focus on
	bug.setFocus(cardStopSeverity)
	bug.setKind(domain.KindFeature)
	if bug.focus != cardStopAfter {
		t.Errorf("focus after the severity row vanished = %d, want after", bug.focus)
	}
}

// TestCardFormAltOTogglesOptions: alt+o expands and collapses in place;
// collapsing from an option row returns focus to the text.
func TestCardFormAltOTogglesOptions(t *testing.T) {
	s := theme.New(theme.GummiDark())
	form := door(domain.KindBug, nil)
	form.HandleKey(altO)
	if !form.expanded || form.focus != cardStopText {
		t.Fatalf("alt+o from the text: expanded=%v focus=%d", form.expanded, form.focus)
	}
	view := ansi.Strip(form.View(s, 100, 30))
	for _, want := range []string{"envelope", "profile", "severity", "after"} {
		if !strings.Contains(view, want) {
			t.Errorf("expanded options missing %q:\n%s", want, view)
		}
	}
	form.setFocus(cardStopProfile)
	form.HandleKey(altO)
	if form.expanded || form.focus != cardStopText {
		t.Errorf("alt+o from an option row: expanded=%v focus=%d", form.expanded, form.focus)
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
		form := door(domain.KindFeature, func(res formResult) tea.Cmd { got, submitted = res, true; return nil })
		form.SetText("dark mode")
		return form
	}
	form := mk()
	if form.env.Value() != "2400" {
		t.Fatalf("envelope prefill = %q, want 2400", form.env.Value())
	}
	form.HandleKey(keyEnter)
	if !submitted || got.Envelope == nil || *got.Envelope != 2400 {
		t.Fatalf("prefill submit: %v %v", submitted, got.Envelope)
	}

	form = mk()
	form.env.SetValue("7500")
	form.HandleKey(keyEnter)
	if got.Envelope == nil || *got.Envelope != 7500 {
		t.Fatalf("Envelope = %v, want explicit 7500", got.Envelope)
	}

	for _, bad := range []string{"-100", "abc"} {
		form = mk()
		form.env.SetValue(bad)
		if done, _ := form.HandleKey(keyEnter); done || submitted || form.errText == "" {
			t.Errorf("%q: done=%v submitted=%v err=%q", bad, done, submitted, form.errText)
		}
	}

	form = mk()
	form.env.SetValue("")
	form.HandleKey(keyEnter)
	if !submitted || got.Envelope != nil {
		t.Fatalf("empty envelope: submitted=%v Envelope=%v, want nil (use default)", submitted, got.Envelope)
	}
}

// TestCardFormSubmitCarriesKindSeverityAndButtons: the result names the
// kind, a bug's severity, the profile, and whether Create & start was the
// button pressed.
func TestCardFormSubmitCarriesKindSeverityAndButtons(t *testing.T) {
	var got formResult
	form := door(domain.KindBug, func(res formResult) tea.Cmd { got = res; return nil })
	form.SetText("Login loops\n\nSSO users bounce back")
	form.HandleKey(altO)
	form.setFocus(cardStopSeverity)
	form.HandleKey(keyRight) // critical
	form.HandleKey(keyRight) // high
	form.setFocus(cardStopProfile)
	form.HandleKey(keyRight) // premium
	form.setFocus(cardStopButtons)
	form.buttons.SetCursor(2)
	if done, _ := form.HandleKey(keyEnter); !done {
		t.Fatal("Create & start did not submit")
	}
	if got.Kind != domain.KindBug || got.Severity != domain.SeverityHigh || got.Profile != "premium" || !got.Start {
		t.Errorf("result = %+v", got)
	}
	if got.Desc != "Login loops\n\nSSO users bounce back" || got.Source != "manual" {
		t.Errorf("desc/source = %q %q", got.Desc, got.Source)
	}
	// Cancel is the first button
	form = door(domain.KindFeature, func(formResult) tea.Cmd { t.Fatal("cancel submitted"); return nil })
	form.setFocus(cardStopButtons)
	form.buttons.SetCursor(0)
	if done, _ := form.HandleKey(keyEnter); !done {
		t.Error("Cancel did not close the form")
	}
}

// TestCardFormBecomesLine reads back prefix and slug live, and the slug
// refusal before enter.
func TestCardFormBecomesLine(t *testing.T) {
	s := theme.New(theme.GummiDark())
	form := door(domain.KindBug, nil)
	form.SetText("Login loops")
	if v := ansi.Strip(form.View(s, 100, 30)); !strings.Contains(v, "becomes  BG · login-loops") {
		t.Errorf("becomes line missing:\n%s", v)
	}
	form.SetText("???")
	if v := ansi.Strip(form.View(s, 100, 30)); !strings.Contains(v, "letter or digit") {
		t.Errorf("slug refusal not shown before enter:\n%s", v)
	}
	if done, _ := form.HandleKey(keyEnter); done {
		t.Error("a title with no letter or digit submitted")
	}
}

// TestCardFormTextEditing: alt+enter and ctrl+j insert newlines, a
// multiline paste keeps them, and down moves the cursor rather than focus.
func TestCardFormTextEditing(t *testing.T) {
	form := door(domain.KindBug, nil)
	form.SetText("Crash on empty diff")
	form.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt})
	form.HandleKey(tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl})
	if got := form.Text(); got != "Crash on empty diff\n\n" {
		t.Fatalf("text after alt+enter, ctrl+j = %q", got)
	}
	form = door(domain.KindBug, nil)
	form.HandlePaste(tea.PasteMsg{Content: "Crash on empty diff\n\nRepro: stage nothing, hit c."})
	if got := form.Text(); got != "Crash on empty diff\n\nRepro: stage nothing, hit c." {
		t.Fatalf("text after paste = %q", got)
	}
	form.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if form.focus != cardStopText {
		t.Error("down moved focus instead of the cursor")
	}
}

// --- the offer ---

// TestCardFormReferenceIsAnOffer: a bare issue reference on the first line
// puts an offer on the from line and refuses enter; nothing is fetched
// until alt+g, and alt+g with no repository to resolve against moves
// focus to the repo row.
func TestCardFormReferenceIsAnOffer(t *testing.T) {
	s := theme.New(theme.GummiDark())
	fetched := false
	form := newCardForm(domain.KindBug, nil, []string{"lxd", "incus"}, false, "", nil, 2400, nil)
	form.onImport = func(domain.IssueRef, string) tea.Cmd { fetched = true; return nil }
	form.setFocus(cardStopText)
	form.SetText("#18409")

	view := ansi.Strip(form.View(s, 100, 30))
	if !strings.Contains(view, "choose a repository to resolve #18409") {
		t.Errorf("offer line missing:\n%s", view)
	}
	if done, _ := form.HandleKey(keyEnter); done {
		t.Error("enter created a card from a bare reference")
	}
	if fetched {
		t.Fatal("something fetched without alt+g")
	}
	form.HandleKey(altG)
	if fetched || form.focus != cardStopRepo || form.errText == "" {
		t.Errorf("alt+g with no repo: fetched=%v focus=%d err=%q", fetched, form.focus, form.errText)
	}

	// a repo whose origin is not GitHub cannot resolve #N either
	form.originFor = func(string) repoOrigin { return repoOrigin{host: "gitlab.example.com", ownerRepo: "t/p"} }
	form.HandleKey(doorKey('1'))
	form.HandleKey(altG)
	if fetched || !strings.Contains(form.errText, "no GitHub origin") {
		t.Errorf("non-github origin: fetched=%v err=%q", fetched, form.errText)
	}
}

// TestCardFormImportReplacesOnlyTheReference: alt+g resolves #N against
// the repo's origin, and the landing replaces the reference line with the
// issue's title and body, keeps everything else typed, reports labels on
// the from line without moving the kind row, and reads the severity.
func TestCardFormImportReplacesOnlyTheReference(t *testing.T) {
	s := theme.New(theme.GummiDark())
	var asked domain.IssueRef
	var got formResult
	form := newCardForm(domain.KindFeature, nil, []string{"lxd", "incus"}, false, "lxd", nil, 2400, func(res formResult) tea.Cmd { got = res; return nil })
	form.originFor = func(string) repoOrigin { return repoOrigin{host: "github.com", ownerRepo: "canonical/lxd"} }
	form.onImport = func(ref domain.IssueRef, _ string) tea.Cmd { asked = ref; return nil }
	form.SetText("#18409\n\nmy own note under it")

	view := ansi.Strip(form.View(s, 100, 30))
	if !strings.Contains(view, "looks like canonical/lxd#18409") || !strings.Contains(view, "github.com/canonical/lxd") {
		t.Errorf("offer or origin readout missing:\n%s", view)
	}
	form.HandleKey(altG)
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
	if form.Kind() != domain.KindFeature {
		t.Error("the kind row moved on import")
	}
	view = ansi.Strip(form.View(s, 100, 30))
	for _, w := range []string{"github · canonical/lxd#18409", "open", "labelled bug, storage", "1 comments"} {
		if !strings.Contains(view, w) {
			t.Errorf("provenance line missing %q:\n%s", w, view)
		}
	}
	form.HandleKey(keyEnter)
	if got.ExternalRef != prop.ExternalRef || got.Source != "github" || got.Discussion != "**b:** same here" {
		t.Errorf("result did not carry the import: %+v", got)
	}
	// the severity was read for a bug: flip the kind and check
	form.setKind(domain.KindBug)
	form.HandleKey(keyEnter)
	if got.Severity != domain.SeverityHigh {
		t.Errorf("severity = %q, want high from the label", got.Severity)
	}

	// a refetch after the imported text was edited is refused
	form.SetText(strings.Replace(form.Text(), "refuses", "fails", 1))
	asked = domain.IssueRef{}
	form.HandleKey(altG)
	if asked.Number != 0 || form.errText == "" {
		t.Errorf("refetch after edit: asked=%+v err=%q", asked, form.errText)
	}
}

// TestCardFormFullReferenceNeedsNoRepoOrigin: "owner/repo#N" and an issue
// URL name their repository, so they import even where the chosen repo's
// origin is unknown.
func TestCardFormFullReferenceNeedsNoRepoOrigin(t *testing.T) {
	var asked domain.IssueRef
	form := door(domain.KindBug, nil)
	form.onImport = func(ref domain.IssueRef, _ string) tea.Cmd { asked = ref; return nil }
	form.SetText("https://github.com/canonical/lxd/issues/7")
	form.HandleKey(altG)
	if asked != (domain.IssueRef{Owner: "canonical", Repo: "lxd", Number: 7}) {
		t.Errorf("asked = %+v", asked)
	}
	// and a failed fetch lands as an error, not a hang
	form.failImport(errors.New("gh: not found"))
	if form.fetching || !strings.Contains(form.errText, "not found") {
		t.Errorf("failImport: fetching=%v err=%q", form.fetching, form.errText)
	}
}

// TestCardFormAltGBrowsesWithoutAReference: alt+g on ordinary text opens
// the picker, popping the form; with no repo chosen it asks for one.
func TestCardFormAltGBrowsesWithoutAReference(t *testing.T) {
	browsed := ""
	form := newCardForm(domain.KindBug, nil, []string{"lxd", "incus"}, false, "", nil, 2400, nil)
	form.onBrowse = func(repo string) tea.Cmd { browsed = repo; return nil }
	form.setFocus(cardStopText)
	form.HandleKey(altG)
	if browsed != "" || form.focus != cardStopRepo {
		t.Fatalf("browse with no repo: browsed=%q focus=%d", browsed, form.focus)
	}
	form.HandleKey(doorKey('2'))
	if done, _ := form.HandleKey(altG); !done || browsed != "incus" {
		t.Errorf("browse: done=%v repo=%q", done, browsed)
	}
}

// --- the after row ---

func sampleCands() []afterCand {
	return []afterCand{
		{ID: "FD-097", Title: "shared storage driver interface", Repo: "incus", Stage: domain.StagePlan},
		{ID: "FD-112", Title: "storage pool quotas on zfs", Repo: "lxd", Stage: domain.StageTodo},
		{ID: "BG-104", Title: "snapshot restore loses quota", Repo: "lxd", Stage: domain.StagePlan},
	}
}

// TestCardFormAfterRow: the row lists the chosen repo's cards first, the
// filter narrows by id or title, enter adds a chip, backspace removes the
// last, and the result carries the edges.
func TestCardFormAfterRow(t *testing.T) {
	s := theme.New(theme.GummiDark())
	var got formResult
	form := newCardForm(domain.KindFeature, nil, []string{"lxd", "incus"}, false, "lxd", sampleCands(), 2400, func(res formResult) tea.Cmd { got = res; return nil })
	form.SetText("storage quotas")
	form.HandleKey(altO)
	form.setFocus(cardStopAfter)

	vis := form.afterVisible()
	if len(vis) != 3 || vis[0].Repo != "lxd" || vis[1].Repo != "lxd" || vis[2].ID != "FD-097" {
		t.Fatalf("visible order = %+v, want lxd's cards first", vis)
	}
	view := ansi.Strip(form.View(s, 100, 30))
	if !strings.Contains(view, "── other repos ──") || !strings.Contains(view, "FD-097") {
		t.Errorf("list missing the divider or the other repo's card:\n%s", view)
	}

	for _, r := range "quota" {
		form.HandleKey(doorKey(r))
	}
	vis = form.afterVisible()
	if len(vis) != 2 {
		t.Fatalf("filter 'quota' left %d, want 2", len(vis))
	}
	form.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	form.HandleKey(keyEnter)
	if len(form.after) != 1 || form.after[0] != "BG-104" || form.afterFilter.Value() != "" {
		t.Fatalf("after enter: chips=%v filter=%q", form.after, form.afterFilter.Value())
	}
	for _, r := range "fd-1" {
		form.HandleKey(doorKey(r))
	}
	form.HandleKey(keyEnter)
	if len(form.after) != 2 || form.after[1] != "FD-112" {
		t.Fatalf("second chip: %v", form.after)
	}
	// a chosen card leaves the list; a duplicate enter adds nothing
	if len(form.afterVisible()) != 1 {
		t.Errorf("chosen cards still offered: %+v", form.afterVisible())
	}
	form.HandleKey(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if len(form.after) != 1 {
		t.Errorf("backspace with an empty filter should drop the last chip: %v", form.after)
	}
	form.HandleKey(altO)
	if v := ansi.Strip(form.View(s, 100, 30)); !strings.Contains(v, "after BG-104") {
		t.Errorf("collapsed readout missing the chip:\n%s", v)
	}
	form.setFocus(cardStopText)
	form.HandleKey(keyEnter)
	if len(got.After) != 1 || got.After[0] != "BG-104" {
		t.Errorf("result After = %v", got.After)
	}
}

// --- the shell's create path ---

// TestCreateCardEnvelope: the parsed envelope stamps the persisted card's
// Budget; nil falls back to the global default and an explicit 0 stays
// uncapped. A bug's severity persists the same way.
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
	f, err := store.GetFeature(ctx, "FD-001")
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

	threeK := 3000
	if msg := m.createCard(formResult{Kind: domain.KindBug, Desc: "Crash on empty diff", Severity: domain.SeverityLow, Envelope: &threeK})(); msg != nil {
		if nm, ok := msg.(noticeMsg); ok && nm.isErr {
			t.Fatalf("create bug failed: %s", nm.text)
		}
	}
	f, err = store.GetFeature(ctx, "BG-003")
	if err != nil {
		t.Fatal(err)
	}
	if f.Budget.Envelope != 3000 || f.Severity != domain.SeverityLow {
		t.Errorf("bug envelope/severity = %d %q", f.Budget.Envelope, f.Severity)
	}
}

// TestCreateCardWritesDependencies: the After edges land in the store,
// and an edge onto an unknown card is reported without losing the card.
func TestCreateCardWritesDependencies(t *testing.T) {
	ws, store, wt := uiRepo(t)
	ctx := context.Background()
	m := NewShell(theme.GummiDark(), "v0-test")
	m.Attach(store, wt, ws)
	if _, ok := m.createCard(formResult{Desc: "first"})().(cardCreatedMsg); !ok {
		t.Fatal("first create failed")
	}
	msg := m.createCard(formResult{Desc: "second", After: []domain.FeatureID{"FD-001", "FD-999"}})()
	created, ok := msg.(cardCreatedMsg)
	if !ok {
		t.Fatalf("createCard returned %#v", msg)
	}
	deps, err := store.ListDependencies(ctx, created.f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) != 1 || deps[0] != "FD-001" {
		t.Errorf("deps = %v, want [FD-001]", deps)
	}
	if !strings.Contains(created.warn, "FD-999") {
		t.Errorf("unknown dependency not reported: %q", created.warn)
	}
}

// TestBoardNOpensTheDoorAndRemembersTheRepo: n opens the door; a created
// card's repository preselects the next one; B and R preset the kind.
func TestBoardNOpensTheDoorAndRemembersTheRepo(t *testing.T) {
	m, _ := newWorkspace(t)
	m = pump(t, m, m.Init())
	m = press(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	form, ok := m.Overlay.Top().(*cardForm)
	if !ok || form.Kind() != domain.KindFeature {
		t.Fatal("n did not open the door on feature")
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	for key, kind := range map[rune]domain.Kind{'B': domain.KindBug, 'R': domain.KindResearch} {
		m = press(t, m, tea.KeyPressMsg{Code: key, Text: string(key)})
		form, ok := m.Overlay.Top().(*cardForm)
		if !ok || form.Kind() != kind {
			t.Errorf("%c did not preset %s", key, kind)
		}
		m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	}
	m.lastRepo = ""
	m = press(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	m = typeString(t, m, "Dark mode")
	m = press(t, m, keyEnter)
	if m.Overlay.HasDialogs() || len(m.rows) != 1 {
		t.Fatalf("create: dialogs=%v rows=%d notice=%q", m.Overlay.HasDialogs(), len(m.rows), m.notice.text)
	}
	if !strings.Contains(m.notice.text, "FD-001 created") {
		t.Errorf("notice = %q", m.notice.text)
	}
}

// TestCreateAndStartOpensAutopilot: the third button mints, then opens
// the autopilot dialog on the new card instead of running anything.
func TestCreateAndStartOpensAutopilot(t *testing.T) {
	m, _ := newWorkspace(t)
	m = pump(t, m, m.Init())
	m = press(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	m = typeString(t, m, "Dark mode")
	form := m.Overlay.Top().(*cardForm)
	form.setFocus(cardStopButtons)
	form.buttons.SetCursor(2)
	m = press(t, m, keyEnter)
	if len(m.rows) != 1 {
		t.Fatalf("card not created: %q", m.notice.text)
	}
	if _, ok := m.Overlay.Top().(*autopilotDialog); !ok {
		t.Errorf("Create & start did not open the autopilot dialog (top=%T notice=%q)", m.Overlay.Top(), m.notice.text)
	}
}
