package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/goalpolicy"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
)

// What a goal knows that no card owns reaches every card as an index, is
// the owner's where it is reference, and says which cards stand on an
// entry the moment that entry changes.
func TestAGoalsNotebookReachesItsCards(t *testing.T) {
	e, _, store, wt := advanceEngine(t)
	ctx := context.Background()
	g := goalAtPlan(t, store, wt, testGoalDoc, 4000)

	design := filepath.Join(t.TempDir(), "design.md")
	if err := os.WriteFile(design, []byte("three tiers"), 0o600); err != nil {
		t.Fatal(err)
	}
	nb := e.GoalNotebook(g.ID)
	if err := nb.AddReference(design); err != nil {
		t.Fatal(err)
	}
	if res, err := e.Advance(ctx, g.ID, "user"); err != nil || res.Status != StatusAdvanced {
		t.Fatalf("advance: %v %v %q", res.Status, err, res.Reason)
	}
	// pinned at the gate: a change afterwards is reported, not absorbed
	if err := os.WriteFile(filepath.Join(nb.ReferenceDir(), "design.md"), []byte("two tiers"), 0o600); err != nil {
		t.Fatal(err)
	}
	view, _ := e.GoalView(ctx, g.ID)
	lt := &leadTurn{e: e, view: view}
	call := func(name, args string) string {
		t.Helper()
		out, err := lt.dispatch(ctx, name, []byte(args))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return out
	}
	if _, err := lt.dispatch(ctx, "notebook_set", []byte(`{"key":"cache-path","value":"~/.cache/export"}`)); err == nil {
		t.Fatal("a decided constant is a decision for review: it needs its reason and the option not taken")
	}
	if out := call("notebook_set", `{"key":"cache-path","value":"~/.cache/export","reason":"XDG","alternative":"./cache"}`); !strings.Contains(out, "D-1") {
		t.Fatalf("recorded as a decision for review: %q", out)
	}
	call("notebook_finding", `{"finding":"the exporter opens its cache read-only","evidence":"RS-002 §2"}`)

	cards := goalCards(t, store, g.ID)
	card := cards[0]
	hint := e.notebookHint(card)
	for _, want := range []string{"design.md", "CHANGED since the plan was agreed", "cache-path = ~/.cache/export", "F-1 (holds)", string(g.ID)} {
		if !strings.Contains(hint, want) {
			t.Fatalf("a card's kickoff carries the index — missing %q:\n%s", want, hint)
		}
	}
	if e.notebookHint(domain.Feature{ID: "FD-099"}) != "" {
		t.Fatal("a card on the open board is told nothing")
	}

	// a card that stands on F-1 is named when F-1 stops holding
	art, _, _ := spec.ReplaceSection(spec.Template(&card), "Implementation notes", "Open the cache read-only, per F-1 and cache-path.\n")
	writeArtifact(t, wt.Root(), card, art)
	lt.view, _ = e.GoalView(ctx, g.ID)
	out := call("notebook_finding", `{"finding":"it opens the cache read-write when --refresh is given","evidence":"run 20260918T101500","supersedes":1}`)
	if !strings.Contains(out, "F-2") || !strings.Contains(out, string(card.ID)) || strings.Contains(out, string(cards[1].ID)) {
		t.Fatalf("it names the unfinished cards citing what was superseded, and only those: %q", out)
	}
	if out := call("notebook_set", `{"key":"cache-path","value":"$XDG_CACHE_HOME/export","reason":"honour the variable","alternative":"keep ~/.cache"}`); !strings.Contains(out, string(card.ID)) {
		t.Fatalf("deciding a constant again names who cites it: %q", out)
	}
	if out := call("notebook_read", `{"what":"findings"}`); !strings.Contains(out, "Superseded by F-2") {
		t.Fatalf("%q", out)
	}
	log, _ := store.GoalLog(ctx, g.ID)
	known, decisions := 0, 0
	for _, en := range log {
		switch en.Action {
		case state.GoalKnown:
			known++
		case state.GoalDecision:
			decisions++
		}
	}
	if known != 4 || decisions != 2 {
		t.Fatalf("every write is in the goal's log (%d), and every constant a decision for review (%d)", known, decisions)
	}
}

// A verified card that says it found something out about the system goes
// to the lead before it lands: only the lead can put it where the cards
// after it will see it.
func TestACardsDiscoveriesGoToTheLeadBeforeItLands(t *testing.T) {
	in := goalpolicy.Input{Stage: domain.StageImplement, Envelope: 4000, Reserve: 600, Lanes: 2, LeadAvailable: true,
		Cards: []goalpolicy.Card{{ID: "RS-002", State: goalpolicy.Verified, Envelope: 500, Discoveries: 2}}}
	got := goalpolicy.Decide(in)
	if len(got) != 1 || got[0].Kind != goalpolicy.Lead || !strings.Contains(got[0].Reasons[0], "notebook_finding") {
		t.Fatalf("%v", got)
	}
	in.Cards[0].LeadTries = 1
	if got = goalpolicy.Decide(in); len(got) == 0 || got[0].Kind != goalpolicy.Land {
		t.Fatalf("once, then it lands: %v", got)
	}

	e, _, store, wt := advanceEngine(t)
	g := goalAtPlan(t, store, wt, testGoalDoc, 4000)
	if res, err := e.Advance(context.Background(), g.ID, "user"); err != nil || res.Status != StatusAdvanced {
		t.Fatal(err, res.Reason)
	}
	card := goalCards(t, store, g.ID)[0]
	art, _, _ := spec.ReplaceSection(spec.Template(&card), "Implementation notes",
		"Built it.\n\nFINDING: the exporter ignores --cache-dir when HOME is unset — export.go:88\n- FINDING: a second one\nnot a FINDING: line\n")
	writeArtifact(t, wt.Root(), card, art)
	if n := e.reportedDiscoveries(&card); n != 2 {
		t.Fatalf("got %d", n)
	}
}
