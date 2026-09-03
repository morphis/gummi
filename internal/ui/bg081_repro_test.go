package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/ui/theme"
)

// TestBG081ResearchCardNamesItsDocumentEverywhere is BG-081's regression
// test, and the third kind's half of BG-079.
//
// artifactNoun is the single place the UI names a card's design
// artifact, so the thread's pinned line, the action inventory's row and
// the header of the view that row opens all agree. It knew about two
// kinds: bugs got "bug report" and everything else got "spec". A
// research card's document is a research document — that is what
// internal/domain/research.go calls the thing the workflow delivers,
// what spec.blankTemplate calls the template it writes, and what the
// stage hint tells the agent to go read — so a research card was the
// "everything else" that got named after an artifact it is not, with a
// different set of sections.
func TestBG081ResearchCardNamesItsDocumentEverywhere(t *testing.T) {
	m := populatedShell(120, 34)
	ws, store, wt := uiRepo(t)
	m.Attach(store, wt, ws)

	f := mkFeature(t, store, 5, "snapshot expiry across tools", domain.StageShape)
	f.Kind = domain.KindResearch
	r := featureRow{F: f}
	m.rows = []featureRow{r}
	m.sel = 0

	const want = "research document"

	// the row a reader picks to open the document
	var row *cardAction
	acts := cardActionsFor(m.nextInputFor(r), r)
	for i := range acts {
		if acts[i].id == "spec" {
			row = &acts[i]
			break
		}
	}
	if row == nil {
		t.Fatal("precondition: a research card offers no way to open its document")
	}
	if row.label != want {
		t.Errorf("the action inventory calls a research card's document %q, not %q", row.label, want)
	}
	if strings.Contains(row.why, "spec") {
		t.Errorf("the row's explanation calls a research card's document a spec: %q", row.why)
	}

	// the header of what it opens
	m.spec = &specView{f: f}
	head := ansi.Strip(m.specViewRender(90, 30))
	if !strings.Contains(head, "· "+want) {
		t.Errorf("the artifact view heads a research document with the wrong noun:\n%s", firstLines(head, 3))
	}

	// and the line in the thread that points at it, which is the one a
	// reader sees without opening anything
	pinned := ansi.Strip(pinnedSpecLine(theme.New(theme.GummiDark()), r, 110))
	if !strings.HasPrefix(pinned, "⌄ "+want+" · ") {
		t.Errorf("the thread's pinned line names a research card's document wrong: %q", pinned)
	}

	// the other two kinds keep their own words
	if got := artifactNoun(domain.KindBug); got != "bug report" {
		t.Errorf("artifactNoun(bug) = %q, want %q", got, "bug report")
	}
	if got := artifactNoun(domain.KindFeature); got != "spec" {
		t.Errorf("artifactNoun(feature) = %q, want %q", got, "spec")
	}
}
