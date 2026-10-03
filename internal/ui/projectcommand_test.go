package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/agent"
)

// TestAProjectCommandOnAFreeformCardIsATurn: "/review" names one of the
// repository's own command files, so on a freeform card it is a turn for
// the session to expand — not a word for the "/" menu to filter by — and
// the menu offers it too. A word that names nothing still opens the menu.
func TestAProjectCommandOnAFreeformCardIsATurn(t *testing.T) {
	m, eng := agentWorkspace(t, &agent.Fake{})
	r := freeformRow(7, "tidy the parser", true)
	if err := m.store.CreateFeature(context.Background(), &r.F); err != nil {
		t.Fatal(err)
	}
	ff, err := eng.OpenFreeform(context.Background(), r.F)
	if err != nil {
		t.Fatal(err)
	}
	dir := ff.WorkDir()
	if dir == "" {
		t.Fatal("the freeform session reports no worktree")
	}
	p := filepath.Join(dir, ".claude", "commands", "review.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("---\ndescription: review the diff\n---\nReview $ARGUMENTS."), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := m.classifyThreadLine(r, "/review the parser", func() *threadDecision { return nil }).route; got != lineFreeformTurn {
		t.Errorf("/review routes to %v, want a freeform turn", got)
	}
	if got := m.classifyThreadLine(r, "/nosuch", func() *threadDecision { return nil }).route; got == lineFreeformTurn {
		t.Error("a word naming no command was sent as a turn")
	}
	m.rows = append(m.rows, r)
	m.sel = len(m.rows) - 1
	var labels []string
	for _, c := range m.cardCommands(nil) {
		labels = append(labels, c.label)
	}
	if !strings.Contains(strings.Join(labels, "\n"), "/review — review the diff") {
		t.Errorf("the / menu does not offer the project command:\n%s", strings.Join(labels, "\n"))
	}
	if got := m.webComposer(r, "/review the parser").Says; !strings.Contains(got, "/review (.claude/commands/review.md)") {
		t.Errorf("the web composer says %q for a project command", got)
	}
	if got := m.webComposer(r, "/rev").Says; !strings.Contains(got, "/review") {
		t.Errorf("the web composer says %q for a partly typed project command", got)
	}
	if got := m.webComposer(r, "/rev").Completions; len(got) != 1 || got[0].Text != "/review " || got[0].Detail != "review the diff" {
		t.Errorf("the web composer offers %+v for /rev, want /review with its description", got)
	}
	if got := m.webComposer(r, "/review ").Completions; len(got) != 0 {
		t.Errorf("a completed command still offers completions: %+v", got)
	}
	if got := m.webComposer(r, "/review the parser").Completions; len(got) != 0 {
		t.Errorf("a line already past its command word still offers completions: %+v", got)
	}
}
