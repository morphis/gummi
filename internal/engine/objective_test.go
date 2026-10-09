package engine

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// objectiveSessionWith opens a freeform session whose auditor answers
// from verdicts in order (the last one repeating), recording every line
// the working agent was sent and every prompt the auditor got.
func objectiveSessionWith(t *testing.T, verdicts ...string) (e *Engine, ff *FreeformSession, worked, audited func() []string) {
	t.Helper()
	var mu sync.Mutex
	var work, audits []string
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		mu.Lock()
		defer mu.Unlock()
		if opts.Role == agent.RoleAuditor {
			v := verdicts[min(len(audits), len(verdicts)-1)]
			audits = append(audits, msg)
			return []agent.Event{{Kind: agent.EventMessage, Text: v}, {Kind: agent.EventIdle}}
		}
		work = append(work, msg)
		return []agent.Event{{Kind: agent.EventMessage, Text: "worked on it"}, {Kind: agent.EventIdle}}
	}}
	ws, store, wt := newRepo(t)
	e = New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", Persist: true})
	t.Cleanup(func() { e.Close() })
	f := freeformCard(1, "tidy the parser")
	createFeature(t, store, f)
	ff, err := e.OpenFreeform(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	snap := func(p *[]string) func() []string {
		return func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), *p...) }
	}
	return e, ff, snap(&work), snap(&audits)
}

// awaitObjective waits for the card's objective to reach state with its
// audit finished.
func awaitObjective(t *testing.T, ff *FreeformSession, state domain.ObjectiveState) domain.Objective {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		o, auditing := ff.objectiveView()
		if o != nil && o.State == state && !auditing && !ff.Busy() {
			return *o
		}
		if time.Now().After(deadline) {
			t.Fatalf("objective never reached %s: %+v (auditing %v)\n%s", state, o, auditing, transcriptOf(ff))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestAnObjectiveLoopsUntilMet: gummi sends the objective, then a turn of
// its own for every CONTINUE, and stops at MET — each continuation
// carrying the auditor's note, and the auditor never shown the transcript.
func TestAnObjectiveLoopsUntilMet(t *testing.T) {
	e, ff, worked, audited := objectiveSessionWith(t,
		"VERDICT: CONTINUE — write the tests", "VERDICT: CONTINUE — fix the lint", "VERDICT: MET — all green")
	if err := e.SetObjective(context.Background(), ff.id, "the parser is tidy", ""); err != nil {
		t.Fatal(err)
	}
	o := awaitObjective(t, ff, domain.ObjectiveMet)
	if o.Turns != 2 || o.Note != "all green" {
		t.Fatalf("settled objective = %+v, want 2 turns and the MET note", o)
	}
	w := worked()
	if len(w) != 3 || !strings.HasPrefix(w[0], "Objective: the parser is tidy") ||
		!strings.Contains(w[1], "write the tests") || !strings.Contains(w[2], "fix the lint") {
		t.Fatalf("the agent was sent %q", w)
	}
	for _, p := range audited() {
		if !strings.Contains(p, "worked on it") || strings.Contains(p, "Objective: the parser is tidy\n\nWork toward it") {
			t.Fatalf("the auditor saw %q: want the last reply and not the transcript", p)
		}
	}
	// the continuation turns are gummi's, never the person's
	for _, m := range ff.Snapshot().Transcript {
		if m.Author == AuthorUser && m.By != "objective" {
			t.Fatalf("a turn %q went in as %q", m.Content, m.By)
		}
	}
}

// TestThreeStuckVerdictsSettleStuck: STUCK is tolerated twice in a row
// and settles the third time; an answer with no verdict counts as STUCK.
func TestThreeStuckVerdictsSettleStuck(t *testing.T) {
	e, ff, worked, _ := objectiveSessionWith(t, "VERDICT: STUCK — needs a key", "no idea", "VERDICT: STUCK — still")
	if err := e.SetObjective(context.Background(), ff.id, "ship it", ""); err != nil {
		t.Fatal(err)
	}
	o := awaitObjective(t, ff, domain.ObjectiveStuck)
	if o.StuckStreak != 3 || len(worked()) != 3 {
		t.Fatalf("objective = %+v after %d turns, want a streak of 3 after 3 turns", o, len(worked()))
	}
}

// TestAFailingCheckKeepsTheObjectiveGoing: MET counts only once the check
// passes; a failing check is a CONTINUE until the cap settles it.
func TestAFailingCheckKeepsTheObjectiveGoing(t *testing.T) {
	e, ff, worked, _ := objectiveSessionWith(t, "VERDICT: MET — done, honest")
	if err := e.SetObjective(context.Background(), ff.id, "tests pass", "echo nope; exit 1"); err != nil {
		t.Fatal(err)
	}
	o := awaitObjective(t, ff, domain.ObjectiveCapped)
	if o.Turns != domain.ObjectiveTurnCap || !strings.Contains(o.Note, "nope") {
		t.Fatalf("objective = %+v, want the cap with the check's tail as its note", o)
	}
	if n := len(worked()); n != domain.ObjectiveTurnCap+1 {
		t.Fatalf("the agent got %d turns, want the opening and %d continuations", n, domain.ObjectiveTurnCap)
	}
}

// TestAPassingCheckMeetsTheObjective: the check is the judge.
func TestAPassingCheckMeetsTheObjective(t *testing.T) {
	e, ff, _, _ := objectiveSessionWith(t, "VERDICT: MET — done")
	if err := e.SetObjective(context.Background(), ff.id, "tests pass", "true"); err != nil {
		t.Fatal(err)
	}
	if o := awaitObjective(t, ff, domain.ObjectiveMet); o.Turns != 0 {
		t.Fatalf("objective = %+v, want met on the first audit", o)
	}
}

// TestTheObjectiveCommand drives the whole surface through /objective, the
// way both faces' composers do: set with a check, pause, clear.
func TestTheObjectiveCommand(t *testing.T) {
	_, ff, worked, _ := objectiveSessionWith(t, "VERDICT: STUCK — waiting")
	if err := ff.Send(context.Background(), "/objective pause"); err == nil {
		t.Fatal("pausing with no objective succeeded")
	}
	if err := ff.Send(context.Background(), "/objective --check 'go vet ./...' vet is clean"); err != nil {
		t.Fatal(err)
	}
	o := awaitObjective(t, ff, domain.ObjectiveStuck)
	if o.Text != "vet is clean" || o.Check != "go vet ./..." {
		t.Fatalf("objective = %+v", o)
	}
	if err := ff.Send(context.Background(), "/objective resume"); err == nil {
		t.Fatal("a settled objective resumed")
	}
	if err := ff.Send(context.Background(), "/objective clear"); err != nil {
		t.Fatal(err)
	}
	if o, _ := ff.objectiveView(); o != nil {
		t.Fatalf("a cleared objective is still there: %+v", o)
	}
	n := len(worked())
	say(t, ff, "thanks")
	time.Sleep(50 * time.Millisecond)
	if got := len(worked()); got != n+1 {
		t.Fatalf("a session with its objective cleared sent %d turns for one line", got-n)
	}
}

// TestPauseAndResume: a paused objective sends nothing at a turn's end,
// and resume audits at once.
func TestPauseAndResume(t *testing.T) {
	e, ff, worked, audited := objectiveSessionWith(t, "VERDICT: MET — fine")
	ctx := context.Background()
	if err := e.SetObjective(ctx, ff.id, "x", ""); err != nil {
		t.Fatal(err)
	}
	awaitObjective(t, ff, domain.ObjectiveMet)
	// set again, paused straight away: the opening turn's end is not audited
	before := len(audited())
	if err := e.SetObjective(ctx, ff.id, "y", ""); err != nil {
		t.Fatal(err)
	}
	if err := e.PauseObjective(ctx, ff.id); err != nil {
		t.Fatal(err)
	}
	waitFreeformIdle(t, ff)
	time.Sleep(50 * time.Millisecond)
	if o, _ := ff.objectiveView(); o.State != domain.ObjectivePaused || len(audited()) != before {
		t.Fatalf("a paused objective was audited: %+v", o)
	}
	if err := e.ResumeObjective(ctx, ff.id); err != nil {
		t.Fatal(err)
	}
	awaitObjective(t, ff, domain.ObjectiveMet)
	if len(worked()) != 2 {
		t.Fatalf("the agent was sent %q", worked())
	}
}

// TestAWorkflowCardRefusesAnObjectiveAtTheEngine: its stages already loop.
func TestAWorkflowCardRefusesAnObjectiveAtTheEngine(t *testing.T) {
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(&agent.Fake{}), Store: store, Worktrees: wt, Workspace: ws, Model: "m", Persist: true})
	t.Cleanup(func() { e.Close() })
	f := freeformCard(1, "a feature")
	f.ID, _ = domain.NewID(domain.KindFeature, 1)
	f.Kind, f.Stage = domain.KindFeature, domain.StageTodo
	createFeature(t, store, f)
	if err := e.SetObjective(context.Background(), f.ID, "x", ""); err == nil {
		t.Fatal("a feature card took an objective")
	}
}

func TestParseObjectiveArgs(t *testing.T) {
	cases := []struct{ in, check, text string }{
		{"make it fast", "", "make it fast"},
		{"--check 'go test ./...' tests pass", "go test ./...", "tests pass"},
		{"--check=make lint is clean", "make", "lint is clean"},
		{"--checklist done", "", "--checklist done"},
	}
	for _, c := range cases {
		check, text, err := ParseObjectiveArgs(c.in)
		if err != nil || check != c.check || text != c.text {
			t.Errorf("ParseObjectiveArgs(%q) = %q %q %v", c.in, check, text, err)
		}
	}
	for _, bad := range []string{"--check", "--check 'unclosed x", "--check 'cmd'"} {
		if _, _, err := ParseObjectiveArgs(bad); err == nil {
			t.Errorf("ParseObjectiveArgs(%q) took it", bad)
		}
	}
}

// TestARestoredObjectiveIsAuditedOnce: the turn that ended before the
// restart was never audited, so the board that comes back audits it
// before it sends anything.
func TestARestoredObjectiveIsAuditedOnce(t *testing.T) {
	ws, store, wt := newRepo(t)
	ctx := context.Background()
	f := freeformCard(21, "outlive the board")
	createFeature(t, store, f)
	e1 := New(Config{Agents: singleAgent(agent.NewFake("half done")), Store: store, Worktrees: wt, Workspace: ws, Model: "m", Persist: true})
	ff, err := e1.OpenFreeform(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	say(t, ff, "start on it")
	// the board died between the turn's end and its audit
	o := domain.Objective{Text: "finish it", State: domain.ObjectiveActive, SetAt: time.Now()}
	if err := store.SetObjective(ctx, f.ID, &o); err != nil {
		t.Fatal(err)
	}
	if err := e1.Close(); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var roles []agent.Role
	second := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		mu.Lock()
		roles = append(roles, opts.Role)
		mu.Unlock()
		return []agent.Event{{Kind: agent.EventMessage, Text: "VERDICT: MET — it is finished"}, {Kind: agent.EventIdle}}
	}}
	e2 := New(Config{Agents: singleAgent(second), Store: store, Worktrees: wt, Workspace: ws, Model: "m", Persist: true})
	t.Cleanup(func() { e2.Close() })
	if err := e2.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	awaitObjective(t, e2.Freeform(f.ID), domain.ObjectiveMet)
	mu.Lock()
	defer mu.Unlock()
	if len(roles) != 1 || roles[0] != agent.RoleAuditor {
		t.Fatalf("the restored board sent %v, want one audit and no turn", roles)
	}
}
