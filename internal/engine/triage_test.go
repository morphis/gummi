package engine

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// triageSession opens a freeform session, with triage on or off, whose
// triage one-shot answers verdict, recording every line the working
// agent was sent and every prompt triage got.
func triageSession(t *testing.T, on bool, verdict string) (ff *FreeformSession, worked, triaged func() []string) {
	t.Helper()
	ff, _, worked, triaged = triageSessionTitled(t, on, true, verdict, "add spec states")
	return ff, worked, triaged
}

// triageSessionTitled is triageSession on a card titled title, handing
// back its engine too.
func triageSessionTitled(t *testing.T, on, workflows bool, verdict, title string) (ff *FreeformSession, e *Engine, worked, triaged func() []string) {
	t.Helper()
	var mu sync.Mutex
	var work, asks []string
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		mu.Lock()
		defer mu.Unlock()
		if opts.Role == agent.RoleTriage {
			asks = append(asks, msg)
			return []agent.Event{{Kind: agent.EventMessage, Text: verdict}, {Kind: agent.EventIdle}}
		}
		work = append(work, msg)
		return []agent.Event{{Kind: agent.EventMessage, Text: "on it"}, {Kind: agent.EventIdle}}
	}}
	ws, store, wt := newRepo(t)
	e = New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", Triage: on, Workflows: workflows})
	t.Cleanup(func() { e.Close() })
	f := freeformCard(1, title)
	createFeature(t, store, f)
	ff, err := e.OpenFreeform(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	snap := func(p *[]string) func() []string {
		return func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), *p...) }
	}
	return ff, e, snap(&work), snap(&asks)
}

const bigAsk = "add a state to every session spec, store it, and show it on the board, the web page and the CLI"

// TestTriageNamesARecipeAheadOfTheFirstRequest: the first request a
// person sends is read against the recipes before the agent hears it; a
// recipe named reaches the agent ahead of the request and the person as a
// line on the thread, and the transcript keeps the request as typed. The
// second request is not triaged.
func TestTriageNamesARecipeAheadOfTheFirstRequest(t *testing.T) {
	ff, worked, triaged := triageSession(t, true, "TRIAGE: deliver — a change across the store and three faces")
	say(t, ff, bigAsk)
	say(t, ff, "and make the state sortable on the board, please, when you get to it")
	asks := triaged()
	if len(asks) != 1 || !strings.Contains(asks[0], bigAsk) || !strings.Contains(asks[0], "- deliver:") {
		t.Fatalf("triage was asked %q: want the first request once, with the recipes", asks)
	}
	w := worked()
	if len(w) != 2 || !strings.HasPrefix(w[0], "(gummi's triage read this request as work for the deliver workflow recipe: "+
		"a change across the store and three faces.") || !strings.HasSuffix(w[0], bigAsk) {
		t.Fatalf("the agent was sent %q", w)
	}
	if strings.Contains(w[1], "triage") {
		t.Fatalf("the second request was triaged: %q", w[1])
	}
	tr := transcriptOf(ff)
	if !strings.Contains(tr, "|tool:gummi triage: work for the deliver workflow|") ||
		!strings.Contains(tr, "user:"+bigAsk+"|") {
		t.Fatalf("the thread reads %s", tr)
	}
}

// TestTriageHereSendsTheRequestAsIs: work for the session itself, an
// answer that names no recipe, and triage switched off all send the
// request as it was, with nothing on the thread.
func TestTriageHereSendsTheRequestAsIs(t *testing.T) {
	for name, tc := range map[string]struct {
		on      bool
		verdict string
		asked   int
	}{
		"here":      {true, "TRIAGE: here — a small change", 1},
		"no recipe": {true, "TRIAGE: rewrite-everything — big", 1},
		"no line":   {true, "I think this is big", 1},
		"off":       {false, "TRIAGE: deliver — big", 0},
	} {
		t.Run(name, func(t *testing.T) {
			ff, worked, triaged := triageSession(t, tc.on, tc.verdict)
			say(t, ff, bigAsk)
			if got := len(triaged()); got != tc.asked {
				t.Fatalf("triage asked %d times, want %d", got, tc.asked)
			}
			if w := worked(); len(w) != 1 || w[0] != bigAsk {
				t.Fatalf("the agent was sent %q, want the request as it was", w)
			}
			if strings.Contains(transcriptOf(ff), "gummi triage") {
				t.Fatalf("the thread has a triage line: %s", transcriptOf(ff))
			}
		})
	}
}

// TestTriageSkipsShortLinesAndCommands: a short line and a command are
// never triaged, and do not use up the first request.
func TestTriageSkipsShortLinesAndCommands(t *testing.T) {
	ff, _, triaged := triageSession(t, true, "TRIAGE: deliver — big")
	say(t, ff, "hi there")
	if n := len(triaged()); n != 0 {
		t.Fatalf("a short line was triaged %d times", n)
	}
	say(t, ff, "/workflow deliver on spec 1 for the whole of the spec state work")
	if n := len(triaged()); n != 0 {
		t.Fatalf("a command was triaged %d times", n)
	}
}

// TestTheWorkflowCommandAsksForAProposal: /workflow is the person asking
// for a run: the agent is told to write the spec and propose, not to do
// the work, and the transcript keeps the line as typed.
func TestTheWorkflowCommandAsksForAProposal(t *testing.T) {
	ff, sent, _ := recordingSession(t)
	say(t, ff, WorkflowAskLine(2))
	say(t, ff, "/workflow")
	got := sent()
	if len(got) != 2 ||
		!strings.HasPrefix(got[0], `The person asks for a workflow run: "on spec 2". Do not do the work yourself`) ||
		!strings.Contains(got[0], "workflow_run") ||
		!strings.HasPrefix(got[1], "The person asks for a workflow run for the work in this conversation.") {
		t.Fatalf("the agent was sent %q", got)
	}
	if tr := transcriptOf(ff); !strings.Contains(tr, "user:/workflow on spec 2|") {
		t.Fatalf("the thread reads %s", tr)
	}
}

// TestTriageNamesTheSession: a session titled with its first request is
// renamed after triage's NAME line before its agent hears that request,
// and its branch moves to the name's words; the agent is told its new
// branch and the thread says so. A title anyone chose stays.
func TestTriageNamesTheSession(t *testing.T) {
	ctx := context.Background()
	ff, e, worked, _ := triageSessionTitled(t, true, true, "TRIAGE: here — small\nNAME: Session spec states", domain.DeriveTitle(bigAsk))
	say(t, ff, bigAsk)
	f, err := e.feature(ctx, ff.id)
	if err != nil {
		t.Fatal(err)
	}
	if f.Title != "Session spec states" || f.BranchName() != "ff/session-spec-states" || f.OneLiner != bigAsk {
		t.Fatalf("the session is %q on %s (one-liner %q)", f.Title, f.BranchName(), f.OneLiner)
	}
	was := freeformCard(1, domain.DeriveTitle(bigAsk))
	wt, _ := e.mgr(ctx, &f)
	if !wt.HasBranch(ctx, "ff/session-spec-states") || wt.HasBranch(ctx, was.BranchName()) {
		t.Fatal("the branch did not move")
	}
	if w := worked(); len(w) != 1 || !strings.HasPrefix(w[0], `(gummi named this session "Session spec states": its branch is now ff/session-spec-states`) {
		t.Fatalf("the agent was sent %q", w)
	}
	if tr := transcriptOf(ff); !strings.Contains(tr, "|tool:gummi named this session|") {
		t.Fatalf("the thread reads %s", tr)
	}

	ff2, e2, worked2, _ := triageSessionTitled(t, true, true, "TRIAGE: here — small\nNAME: Session spec states", "add spec states")
	say(t, ff2, bigAsk)
	if f, _ := e2.feature(ctx, ff2.id); f.Title != "add spec states" {
		t.Fatalf("a chosen title was replaced by %q", f.Title)
	}
	if w := worked2(); len(w) != 1 || w[0] != bigAsk {
		t.Fatalf("the agent was sent %q", w)
	}
}

func TestParseTriageName(t *testing.T) {
	for reply, want := range map[string]string{
		"TRIAGE: here — x\nNAME: Better session names": "Better session names",
		"**NAME: \"Better  session names.\"**":         "Better session names",
		"TRIAGE: here — x":                             "",
		"NAME: ":                                       "",
		"NAME: " + strings.Repeat("word ", 20):         "",
	} {
		if got := parseTriageName(reply); got != want {
			t.Errorf("parseTriageName(%q) = %q, want %q", reply, got, want)
		}
	}
}

func TestParseTriage(t *testing.T) {
	recipes := []WorkflowRecipe{{Name: "deliver"}, {Name: "fix-until-green"}}
	for _, tc := range []struct{ reply, name, why string }{
		{"TRIAGE: deliver — across packages", "deliver", "across packages"},
		{"thinking…\n**TRIAGE: `fix-until-green` - the tests fail**", "fix-until-green", "the tests fail"},
		{"TRIAGE: here—a question", "here", "a question"},
		{"TRIAGE: Deliver", "deliver", "no reason given"},
	} {
		name, why, ok := parseTriage(tc.reply, recipes)
		if !ok || name != tc.name || why != tc.why {
			t.Errorf("parseTriage(%q) = %q, %q, %v; want %q, %q", tc.reply, name, why, ok, tc.name, tc.why)
		}
	}
	for _, reply := range []string{"", "deliver", "TRIAGE: investigate — not here"} {
		if _, _, ok := parseTriage(reply, recipes); ok {
			t.Errorf("parseTriage(%q) answered", reply)
		}
	}
}
