package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// briefCounter is a fake whose turns write a file when told to, whose
// brief turn answers briefReply, and which counts the brief sessions it
// was asked to open and the options each was opened with.
type briefCounter struct {
	*agent.Fake
	mu     sync.Mutex
	briefs []agent.SessionOpts
}

func newBriefCounter(t *testing.T, caps agent.Capabilities) *briefCounter {
	t.Helper()
	b := &briefCounter{}
	b.Fake = &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		if strings.Contains(msg, "handoff brief") {
			return []agent.Event{
				{Kind: agent.EventMessage, Text: briefReply},
				{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 1, OutputTokens: 10, Model: opts.Model}},
				{Kind: agent.EventIdle},
			}
		}
		if strings.Contains(msg, "write") {
			if err := os.WriteFile(filepath.Join(opts.WorkDir, "retry.go"), []byte("package sync\n"), 0o600); err != nil {
				t.Error(err)
			}
		}
		return []agent.Event{{Kind: agent.EventMessage, Text: "done"}, {Kind: agent.EventIdle}}
	}}
	b.Caps = caps
	b.OnNewSession = func(opts agent.SessionOpts) {
		if !strings.Contains(strings.Join(opts.SystemHints, "\n"), "handoff summary") {
			return
		}
		b.mu.Lock()
		b.briefs = append(b.briefs, opts)
		b.mu.Unlock()
	}
	return b
}

func (b *briefCounter) briefTurns() []agent.SessionOpts {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]agent.SessionOpts(nil), b.briefs...)
}

// TestTheBriefTurnIsReadOnly: the brief turn runs in the card's worktree,
// so on a backend that can strip its own writing tools it is asked to —
// exactly as a consult is. Granting it none of gummi's tools was never
// the whole of it: a backend's own surface (edits auto-accepted, a shell
// pre-approved) stayed in reach, held off the files by a hint alone.
func TestTheBriefTurnIsReadOnly(t *testing.T) {
	ws, store, wt := newRepo(t)
	ctx := context.Background()
	f := freeformCard(21, "read only brief")
	createFeature(t, store, f)
	ag := newBriefCounter(t, agent.Capabilities{UsageEvents: true, Interrupt: true, ReadOnlyEnforce: true})
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", Persist: true})
	t.Cleanup(func() { e.Close() })

	ff, err := e.OpenFreeform(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := ff.Send(ctx, "look at the retry loop"); err != nil {
		t.Fatal(err)
	}
	waitFreeformIdle(t, ff)
	if _, _, err := awaitBrief(e, f.ID); err != nil {
		t.Fatal(err)
	}
	turns := ag.briefTurns()
	if len(turns) != 1 {
		t.Fatalf("the brief opened %d sessions, want 1", len(turns))
	}
	if !turns[0].ReadOnly {
		t.Error("the brief turn ran read-write on a backend that can enforce read-only")
	}
	if turns[0].Role != agent.RoleConsult {
		t.Errorf("the brief turn ran as %q, want the consult role a read-only conversation takes", turns[0].Role)
	}
	if len(turns[0].Tools) != 0 {
		t.Errorf("the brief turn was granted %d of gummi's tools, want none", len(turns[0].Tools))
	}
}

// TestAnAnsweredBriefIsNotWrittenTwice: closing the dialog and opening it
// again with nothing said in between hands back the brief already on the
// record — the brief turn is a fresh, uncached session, and paying for it
// twice buys the same words. Anything said since makes the conversation
// newer than the brief, and it is written again.
func TestAnAnsweredBriefIsNotWrittenTwice(t *testing.T) {
	ws, store, wt := newRepo(t)
	ctx := context.Background()
	f := freeformCard(22, "brief once")
	createFeature(t, store, f)
	ag := newBriefCounter(t, agent.Capabilities{UsageEvents: true, Interrupt: true})
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", Persist: true})
	t.Cleanup(func() { e.Close() })

	ff, err := e.OpenFreeform(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := ff.Send(ctx, "look at the retry loop"); err != nil {
		t.Fatal(err)
	}
	waitFreeformIdle(t, ff)

	first, src, err := awaitBrief(e, f.ID)
	if err != nil || src != BriefLive {
		t.Fatalf("first brief: %v (%s)", err, src)
	}
	lines := len(ff.Snapshot().Transcript)
	again, src, err := awaitBrief(e, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(ag.briefTurns()); n != 1 {
		t.Errorf("reopening the dialog ran %d brief turns, want the one already answered", n)
	}
	if again != first || src != BriefLive {
		t.Errorf("the reopened dialog got %q (%s), want the brief on the record, as the session's own", again, src)
	}
	if n := len(ff.Snapshot().Transcript); n != lines {
		t.Errorf("reopening the dialog appended %d line(s) to the conversation", n-lines)
	}
	got, err := store.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Spend.Credits != 1 {
		t.Errorf("the card booked %.0f credits for two opens, want the one brief turn's 1", got.Spend.Credits)
	}

	// a turn since: the brief no longer describes the conversation
	if err := ff.Send(ctx, "and the timeout too"); err != nil {
		t.Fatal(err)
	}
	waitFreeformIdle(t, ff)
	if _, _, err := awaitBrief(e, f.ID); err != nil {
		t.Fatal(err)
	}
	if n := len(ag.briefTurns()); n != 2 {
		t.Errorf("after a new turn the brief ran %d times in all, want it written again (2)", n)
	}
}

// TestWritingASpecRefusesUncommittedWork: the spec's branch is cut from
// the session's last commit and nothing commits a session's work for it,
// so loose work would be left behind on a branch the spec does not
// continue. The refusal fires before the brief turn spends anything, and
// lifts once the work is committed on purpose.
func TestWritingASpecRefusesUncommittedWork(t *testing.T) {
	ws, store, wt := newRepo(t)
	ctx := context.Background()
	f := freeformCard(23, "dirty session")
	createFeature(t, store, f)
	ag := newBriefCounter(t, agent.Capabilities{UsageEvents: true, Interrupt: true})
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", Persist: true})
	t.Cleanup(func() { e.Close() })

	ff, err := e.OpenFreeform(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := ff.Send(ctx, "write the retry loop"); err != nil {
		t.Fatal(err)
	}
	waitFreeformIdle(t, ff)

	_, _, err = awaitBrief(e, f.ID)
	if !errors.Is(err, ErrSessionDirty) {
		t.Fatalf("the brief of a session with loose work = %v, want ErrSessionDirty", err)
	}
	if !strings.Contains(err.Error(), "commit it") {
		t.Errorf("the refusal does not say what to do: %v", err)
	}
	if n := len(ag.briefTurns()); n != 0 {
		t.Errorf("a refused brief still ran %d brief turn(s)", n)
	}

	if _, err := e.CommitFreeform(ctx, f.ID, "sync: retry loop"); err != nil {
		t.Fatal(err)
	}
	if err := e.WritespecRefusal(ctx, f.ID); err != nil {
		t.Errorf("a committed session is still refused: %v", err)
	}
}

// TestRefusalsDoNotBorrowTheBackendsWords: a brief refused mid-turn still
// maps to the busy conflict both faces answer with, but it reads as its
// own sentence — not as one with "a turn is already in progress" tacked on
// to a refusal about something else.
func TestRefusalsDoNotBorrowTheBackendsWords(t *testing.T) {
	for _, err := range []error{
		busyRefusal("FF-001 is waiting on your answer — answer the question before writing a spec from it"),
	} {
		if !errors.Is(err, agent.ErrBusy) {
			t.Errorf("%v does not map to the busy conflict", err)
		}
		if strings.Contains(err.Error(), agent.ErrBusy.Error()) {
			t.Errorf("%q carries the backend's own wording", err)
		}
	}
}

// TestAFreeformHandOffCommitsNothing: gummi never commits a session's
// work (DESIGN §19.3), and ending one is not an exception. What the last
// turn left loose stays in the worktree the hand-off keeps standing.
func TestAFreeformHandOffCommitsNothing(t *testing.T) {
	ws, store, wt := newRepo(t)
	ctx := context.Background()
	f := freeformCard(24, "hand off loose")
	createFeature(t, store, f)
	ag := newBriefCounter(t, agent.Capabilities{UsageEvents: true, Interrupt: true})
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
	t.Cleanup(func() { e.Close() })

	ff, err := e.OpenFreeform(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := ff.Send(ctx, "write the retry loop"); err != nil {
		t.Fatal(err)
	}
	waitFreeformIdle(t, ff)
	_ = ff.Close()

	res, err := e.HandOff(ctx, f.ID, "user")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusAdvanced || res.Feature.Stage != domain.StageDone {
		t.Fatalf("hand-off = %v at %s, want the session closed", res.Status, res.Feature.Stage)
	}
	tree := filepath.Join(ws.Root, f.WorktreePath())
	if log := gitOut(t, tree, "log", "--oneline", "--no-decorate", "--grep=checkpoint"); strings.TrimSpace(log) != "" {
		t.Errorf("the hand-off committed the session's work:\n%s", log)
	}
	if out := gitOut(t, tree, "status", "--porcelain"); !strings.Contains(out, "retry.go") {
		t.Errorf("the loose work is not where the session left it:\n%s", out)
	}
}

// TestAHeldFreeformHandOffIsNotStamped: a session's one floor — its open
// diff comments — holds the hand-off, and a held hand-off leaves the card
// as it was. Stamping it first left a card that was still open badged as
// handed off.
func TestAHeldFreeformHandOffIsNotStamped(t *testing.T) {
	ws, store, wt := newRepo(t)
	ctx := context.Background()
	f := freeformCard(25, "held hand off")
	createFeature(t, store, f)
	e := New(Config{Agents: singleAgent(&agent.Fake{}), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
	t.Cleanup(func() { e.Close() })
	if _, err := wt.Create(ctx, &f); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddDiffAnnotation(ctx, domain.DiffAnnotation{
		Feature: f.ID, File: "a.go", Anchor: "h", Excerpt: "x", Comment: "fix this",
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	res, err := e.HandOff(ctx, f.ID, "user")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusBlockedDiff {
		t.Fatalf("hand-off = %v, want held by the open comment", res.Status)
	}
	got, err := store.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.HandedOff() || got.Stage != domain.StageOpen {
		t.Errorf("a held hand-off left the card at %s, handed off %v — want it open and unstamped", got.Stage, got.HandedOff())
	}
}
