package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/ui/theme"
	"github.com/morphis/gummi/internal/webapi"
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

// TestTheCardOffersItsOwnSlashWords: gummi's own vocabulary completes
// beside a project's command files — the words the TUI's "/" menu offers
// on a card page, each named for the action it runs or the menu row it
// lands on, and only while the word is still being typed.
func TestTheCardOffersItsOwnSlashWords(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0-test")
	m.now = func() time.Time { return fixedTime }
	r := row(42, "dark mode", domain.StageImplement, "thrifty", true)
	v := row(44, "search", domain.StageVerify, "thrifty", true)
	v.F.VerifiedAt = fixedTime
	m.rows = []featureRow{r, v}
	m.sel = 0
	m.cardOpen = true

	names := func(cs []webapi.Completion) []string {
		out := make([]string, len(cs))
		for i, c := range cs {
			out[i] = c.Text
		}
		return out
	}
	has := func(cs []webapi.Completion, text, detail string) bool {
		for _, c := range cs {
			if c.Text == text && strings.Contains(c.Detail, detail) {
				return true
			}
		}
		return false
	}

	// a partly typed word narrows to the words it could become
	if got := m.webComposer(r, "/re").Completions; !has(got, "/rebase ", "rebase branch onto") {
		t.Errorf("/re offers %v, want /rebase with what it does", names(got))
	}
	// a verb's word is claimed by the action the verb fires: at verify,
	// /land is the merge's word, never an advance row that also says
	// "land" — and before the branch is verified there is no land to
	// offer at all (the floor the menu reads)
	if got := m.webComposer(r, "/la").Completions; len(got) != 0 {
		t.Errorf("/la at implement offers %v, want none — nothing lands before verify", names(got))
	}
	if got := m.webComposer(v, "/la").Completions; !has(got, "/land ", "merge") {
		t.Errorf("/la at verify offers %v, want /land named for the merge", names(got))
	}
	// the bare slash offers the vocabulary itself
	if got := m.webComposer(r, "/").Completions; len(got) < 2 {
		t.Errorf("/ offers %v, want the card's own words", names(got))
	}
	// a word naming nothing offers nothing
	if got := m.webComposer(r, "/zzz").Completions; len(got) != 0 {
		t.Errorf("/zzz offers %v, want none", names(got))
	}
	// a completed word is a line, not a picker
	if got := m.webComposer(r, "/rebase ").Completions; len(got) != 0 {
		t.Errorf("/rebase␠ offers %v, want none", names(got))
	}
}

// TestCompactIsOfferedWhenTheBackendCompacts: "/compact" is the one
// command a freeform session offers that no file defines, and the
// composer completes it when the card's backend can compact — the same
// condition the turn itself is routed by.
func TestCompactIsOfferedWhenTheBackendCompacts(t *testing.T) {
	ag := &agent.Fake{Caps: agent.Capabilities{Compact: true}}
	m, eng := agentWorkspace(t, ag)
	r := freeformRow(7, "tidy the parser", true)
	if err := m.store.CreateFeature(context.Background(), &r.F); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.OpenFreeform(context.Background(), r.F); err != nil {
		t.Fatal(err)
	}
	m.rows = append(m.rows, r)
	m.sel = len(m.rows) - 1
	got := m.webComposer(r, "/comp").Completions
	if len(got) != 1 || got[0].Text != "/compact " || got[0].Detail == "" {
		t.Errorf("/comp offers %+v, want /compact with its description", got)
	}
}
