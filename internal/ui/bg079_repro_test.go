package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
)

// TestBG079BugCardNamesItsReportEverywhere is BG-079's regression test.
// artifactNoun exists so a bug card calls its document a bug report and
// a feature card calls it a spec, and the thread's pinned line, the gate
// wording and the verify notices all went through it. The two surfaces
// that actually open the document did not: the action inventory's row
// was labelled "spec" while its own explanation underneath said "bug
// report", and the artifact view's header said "spec" over a bug's
// report. Those are the surface a reader opens the document from and the
// one they land on, so the document was renamed across a single
// keystroke.
func TestBG079BugCardNamesItsReportEverywhere(t *testing.T) {
	m := populatedShell(120, 34)
	ws, store, wt := uiRepo(t)
	m.Attach(store, wt, ws)

	f := mkFeature(t, store, 4, "file pull truncation", domain.StageDiagnose)
	f.Kind = domain.KindBug
	r := featureRow{F: f}
	m.rows = []featureRow{r}
	m.sel = 0

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
		t.Fatal("precondition: a bug card offers no way to open its document")
	}
	if row.label != "bug report" {
		t.Errorf("the action inventory calls a bug card's document %q, not %q", row.label, "bug report")
	}
	// the explanation under it is stage-specific prose, so it is not
	// required to name the document — only never to name it as a spec.
	if strings.Contains(row.why, "spec") {
		t.Errorf("the row's explanation calls a bug card's document a spec: %q", row.why)
	}

	// and the header of what it opens
	m.spec = &specView{f: f}
	head := ansi.Strip(m.specViewRender(90, 30))
	if !strings.Contains(head, "· bug report") {
		t.Errorf("the artifact view heads a bug's report with the wrong noun:\n%s", firstLines(head, 3))
	}
}

// firstLines keeps a failure's log to the part that carries the header.
func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
