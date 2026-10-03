package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// mainCheckoutCard builds a freeform card minted into the main checkout:
// no branch, no worktree, its turns loose where the person works.
func mainCheckoutCard(num int, title string) domain.Feature {
	f := freeformCard(num, title)
	f.MainCheckout = true
	return f
}

// TestAMainCheckoutSessionRunsInTheCheckout: the freeform kind's opt-out of
// the worktree. Opening the session starts no branch and no worktree — the
// agent's working directory IS the managed checkout — and what a turn
// writes is left there, uncommitted, beside everything else the person has.
func TestAMainCheckoutSessionRunsInTheCheckout(t *testing.T) {
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, _ string) []agent.Event {
		if err := os.WriteFile(filepath.Join(opts.WorkDir, "sketch.txt"), []byte("hand-rolled\n"), 0o600); err != nil {
			t.Error(err)
		}
		return []agent.Event{{Kind: agent.EventMessage, Text: "wrote it"}, {Kind: agent.EventIdle}}
	}}
	ag.Caps = agent.Capabilities{UsageEvents: true, Interrupt: true}
	var gotHints []string
	ag.OnNewSession = func(opts agent.SessionOpts) { gotHints = opts.SystemHints }
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
	t.Cleanup(func() { e.Close() })
	ctx := context.Background()

	f := mainCheckoutCard(1, "poke at the pty leak")
	createFeature(t, store, f)

	ff, err := e.OpenFreeform(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := ff.Send(ctx, "drop the leaked fd"); err != nil {
		t.Fatal(err)
	}
	waitFreeformIdle(t, ff)

	// The work is in the checkout, loose.
	if _, err := os.Stat(filepath.Join(ws.Root, "sketch.txt")); err != nil {
		t.Fatalf("the turn's file is not in the main checkout: %v", err)
	}
	// No worktree was cut for the card, and no branch either.
	if ok, err := wt.Exists(ctx, &f); err != nil || ok {
		t.Errorf("Exists = %v, %v; want no worktree", ok, err)
	}
	if out := gitOut(t, ws.Root, "branch", "--list", "ff/*"); strings.TrimSpace(out) != "" {
		t.Errorf("a branch was cut anyway:\n%s", out)
	}
	// The checkout's own branch is untouched.
	if got := strings.TrimSpace(gitOut(t, ws.Root, "rev-parse", "--abbrev-ref", "HEAD")); got != "main" {
		t.Errorf("the checkout sits on %s, want main", got)
	}
	// And nothing was committed for the turn.
	if out := gitOut(t, ws.Root, "status", "--porcelain"); !strings.Contains(out, "sketch.txt") {
		t.Errorf("the turn's file was committed, want it left loose:\n%s", out)
	}
	// The hint the session was given names where it actually is.
	joined := strings.Join(gotHints, "\n")
	if !strings.Contains(joined, "main checkout") {
		t.Errorf("the contract hint does not say where the session stands:\n%s", joined)
	}
	if strings.Contains(joined, "never write into the repository's main checkout") {
		t.Errorf("the worktree boundary hint reached a session standing in that checkout:\n%s", joined)
	}
}

// TestAMainCheckoutCardIsNotCommittedFor: the reader's own commit verb
// refuses, because there is no branch to commit to and sweeping the
// checkout's loose work into one is not the card's to do. A hand-off
// closes the card and keeps the work loose, which is the ending such a
// card has.
func TestAMainCheckoutCardIsNotCommittedFor(t *testing.T) {
	ag := agent.NewFake("ack")
	ag.Caps = agent.Capabilities{UsageEvents: true, Interrupt: true}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
	t.Cleanup(func() { e.Close() })
	ctx := context.Background()

	f := mainCheckoutCard(2, "tidy the cli help")
	createFeature(t, store, f)
	if err := os.WriteFile(filepath.Join(ws.Root, "notes.txt"), []byte("loose\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := e.CommitFreeform(ctx, f.ID, "ff-041: tidy"); err == nil {
		t.Fatal("CommitFreeform committed a main-checkout card")
	} else if !strings.Contains(err.Error(), "main checkout") {
		t.Errorf("the refusal does not say why: %v", err)
	}
	if out := gitOut(t, ws.Root, "status", "--porcelain"); !strings.Contains(out, "notes.txt") {
		t.Errorf("the refusal committed the checkout anyway:\n%s", out)
	}

	// The hand-off still closes it, and the work stays exactly where it is.
	out, err := e.HandOff(ctx, f.ID, "t")
	if err != nil {
		t.Fatalf("HandOff: %v", err)
	}
	if out.Status != StatusAdvanced {
		t.Errorf("status = %v, want advanced", out.Status)
	}
	stored, err := store.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Stage != domain.StageDone || stored.HandedOffAt.IsZero() {
		t.Errorf("stage %q, handedOff %v; want done and stamped", stored.Stage, stored.HandedOffAt)
	}
	if out2 := gitOut(t, ws.Root, "status", "--porcelain"); !strings.Contains(out2, "notes.txt") {
		t.Errorf("the hand-off committed the checkout:\n%s", out2)
	}
}
