package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/worktree"
)

func newRepo(t *testing.T) (state.Workspace, *state.Store, *worktree.Manager) {
	t.Helper()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.CommandContext(context.Background(), "git",
			append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.name", "t")
	git("config", "user.email", "t@e.invalid")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-q", "-m", "init")

	ws, err := state.Init(root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	wt, err := worktree.NewManager(context.Background(), root, store)
	if err != nil {
		t.Fatal(err)
	}
	return ws, store, wt
}

func feature(num int, title string, stage domain.Stage) domain.Feature {
	id, _ := domain.NewFeatureID(num)
	slug, _ := domain.Slugify(title)
	now := time.Now()
	return domain.Feature{
		ID: id, Num: num, Title: title, Slug: slug, Stage: stage,
		CreatedAt: now, UpdatedAt: now,
	}
}

func waitFor(t *testing.T, e *Engine, kind EventKind) Event {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-e.Events():
			if ev.Kind == kind {
				return ev
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s", kind)
		}
	}
}

// waitState polls a feature's session until it reaches want.
func waitState(t *testing.T, e *Engine, id domain.FeatureID, want SessionState) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		if s := e.Get(id); s != nil && s.State() == want {
			return
		}
		select {
		case <-deadline:
			cur := "<nil>"
			if s := e.Get(id); s != nil {
				cur = string(s.State())
			}
			t.Fatalf("%s did not reach %s (at %s)", id, want, cur)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// waitActivity waits until the session's activity feed contains one of
// the wanted substrings. Used to wait out the git epilogue: the state
// flips Done before settle's checkpoint commit, so a test that tears
// its repo down right at Done races the commit.
func waitActivity(t *testing.T, e *Engine, id domain.FeatureID, wants ...string) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		acts := strings.Join(e.Get(id).Snapshot().Activity, "\n")
		for _, w := range wants {
			if strings.Contains(acts, w) {
				return
			}
		}
		select {
		case <-deadline:
			t.Fatalf("%s activity never showed %q:\n%s", id, wants, acts)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// newEngine builds a single-slot engine (MaxActive 1); multi-slot tests
// construct New directly.
func newEngine(t *testing.T, ag agent.Agent) *Engine {
	t.Helper()
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "fake-model", MaxActive: 1})
	t.Cleanup(func() { e.Close() })
	return e
}

// withWorktree creates a feature's worktree so autonomous stages locate it.
func withWorktree(t *testing.T, wt *worktree.Manager, f domain.Feature) {
	t.Helper()
	if _, err := wt.Create(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
}

func TestRejectsNonAgentStages(t *testing.T) {
	e := newEngine(t, agent.NewFake("hi"))
	for _, st := range []domain.Stage{domain.StageTodo, domain.StageDone} {
		if _, err := e.Attach(context.Background(), feature(1, "x", st)); err == nil {
			t.Errorf("Attach: stage %s should have no agent action", st)
		}
		if err := e.Run(feature(1, "x", st)); err == nil {
			t.Errorf("Run: stage %s should have no agent action", st)
		}
	}
}

func TestInteractiveRoundTrip(t *testing.T) {
	e := newEngine(t, agent.NewFake("Here are two approaches."))
	ctx := context.Background()

	s, err := e.Attach(ctx, feature(1, "Dark mode", domain.StageBrainstorm))
	if err != nil {
		t.Fatal(err)
	}
	if s.Role != agent.RoleArchitect || !s.Interactive || s.State() != StateInteractive {
		t.Fatalf("brainstorm should be an interactive architect session: %+v", s.Snapshot())
	}
	waitFor(t, e, EventStarted)
	waitFor(t, e, EventIdle) // the kickoff turn (agent speaks first) completes

	if err := e.Send(ctx, "FD-001", "how should dark mode persist?"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventIdle)

	// kickoff (system) + its reply, then the user turn + its reply
	snap := s.Snapshot()
	if len(snap.Transcript) != 4 || snap.Transcript[0].Author != AuthorSystem ||
		snap.Transcript[3].Content != "Here are two approaches." {
		t.Fatalf("transcript = %+v", snap.Transcript)
	}
	if snap.Busy || snap.Spend.Credits != 2 {
		t.Errorf("post-turn state wrong: busy=%v spend=%+v", snap.Busy, snap.Spend)
	}
	// interactive sessions do not consume a slot
	if s := e.Get("FD-001"); s.State() != StateInteractive {
		t.Errorf("interactive session changed state to %s", s.State())
	}
}

func TestAutonomousRunKicksOff(t *testing.T) {
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{
			{Kind: agent.EventToolCall, Tool: "edit", Detail: "theme.go"},
			{Kind: agent.EventMessage, Text: "Implemented."},
			{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 2, OutputTokens: 40}},
			{Kind: agent.EventIdle},
		}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "impl", domain.StageImplement)
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	// the engine kicks it off automatically and it finishes → done
	waitState(t, e, "FD-001", StateDone)

	snap := e.Get("FD-001").Snapshot()
	// tool name and detail compose double-space separated (the UI's
	// split point for styling the two parts)
	if len(snap.Activity) != 1 || snap.Activity[0] != "edit  theme.go" {
		t.Errorf("activity = %+v", snap.Activity)
	}
	if snap.Spend.Credits != 2 {
		t.Errorf("spend = %+v", snap.Spend)
	}
	// the kickoff turn is recorded as the first user message
	if len(snap.Transcript) < 1 || snap.Transcript[0].Author != AuthorUser {
		t.Errorf("kickoff not recorded: %+v", snap.Transcript)
	}
}

// TestErrorFreesSlotAndUnwedgesQueue covers the scheduler-wedge fix: an
// autonomous run that ends in a terminal EventError (no trailing idle) must
// free its attention slot and move to a re-runnable state, so a second
// queued feature starts rather than starving behind it forever.
func TestErrorFreesSlotAndUnwedgesQueue(t *testing.T) {
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		if strings.Contains(opts.WorkDir, "FD-001") {
			return []agent.Event{{Kind: agent.EventError, Err: errorString("backend blew up")}}
		}
		return []agent.Event{{Kind: agent.EventMessage, Text: "done"}, {Kind: agent.EventIdle}}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	f1 := feature(1, "boom", domain.StageImplement)
	f2 := feature(2, "fine", domain.StageImplement)
	withWorktree(t, wt, f1)
	withWorktree(t, wt, f2)

	if err := e.Run(f1); err != nil {
		t.Fatal(err)
	}
	// f1 fails: it must not stay Running holding the only slot.
	waitState(t, e, "FD-001", StatePaused)
	if s := e.Get("FD-001"); s.Snapshot().Err == nil {
		t.Error("failed run recorded no error")
	}

	// with the slot freed, a newly queued feature runs to completion.
	if err := e.Run(f2); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-002", StateDone)

	// and the failed feature can be retried (Run no longer no-ops on it).
	if err := e.Run(f1); err != nil {
		t.Fatalf("re-running a failed feature should be allowed: %v", err)
	}
}

type errorString string

func (e errorString) Error() string { return string(e) }

func TestRunWithAppendsCommentsToKickoff(t *testing.T) {
	var mu sync.Mutex
	var got string
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		mu.Lock()
		got = msg
		mu.Unlock()
		return []agent.Event{{Kind: agent.EventIdle}}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "planned", domain.StagePlan)
	withWorktree(t, wt, f)
	note := "Please address these review comments in the spec.\n\n- L12: split the migration"
	if err := e.RunWith(f, note); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-001", StateDone)

	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(got, kickoff) || !strings.Contains(got, "split the migration") {
		t.Errorf("kickoff missing the review comments:\n%s", got)
	}
	// the combined kickoff is what the transcript records
	snap := e.Get("FD-001").Snapshot()
	if len(snap.Transcript) == 0 || snap.Transcript[0].Content != got {
		t.Errorf("transcript kickoff differs from the sent one: %+v", snap.Transcript)
	}
}

func TestSchedulerQueuesBeyondMaxActive(t *testing.T) {
	// a responder that blocks until released, so slot 1 stays occupied
	release := make(chan struct{})
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		<-release
		return []agent.Event{{Kind: agent.EventIdle}}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() {
		close(release)
		e.Close()
	})

	f1 := feature(1, "one", domain.StageImplement)
	f2 := feature(2, "two", domain.StageImplement)
	withWorktree(t, wt, f1)
	withWorktree(t, wt, f2)

	if err := e.Run(f1); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-001", StateRunning)
	if err := e.Run(f2); err != nil {
		t.Fatal(err)
	}
	// slot is full → f2 queues
	waitState(t, e, "FD-002", StateQueued)

	// release f1's turn → it goes done, frees the slot, f2 starts
	release <- struct{}{}
	waitState(t, e, "FD-001", StateDone)
	waitState(t, e, "FD-002", StateRunning)
}

func TestPauseFreesSlotAndPromotes(t *testing.T) {
	release := make(chan struct{})
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		<-release
		return []agent.Event{{Kind: agent.EventIdle}}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() {
		close(release)
		e.Close()
	})

	f1 := feature(1, "one", domain.StageImplement)
	f2 := feature(2, "two", domain.StageImplement)
	withWorktree(t, wt, f1)
	withWorktree(t, wt, f2)
	if err := e.Run(f1); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-001", StateRunning)
	if err := e.Run(f2); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-002", StateQueued)

	// pause f1 → slot frees, f2 promoted to running
	if err := e.Pause(context.Background(), "FD-001"); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-001", StatePaused)
	waitState(t, e, "FD-002", StateRunning)
}

func TestMaxActiveTwo(t *testing.T) {
	release := make(chan struct{})
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		<-release
		return []agent.Event{{Kind: agent.EventIdle}}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 2})
	t.Cleanup(func() {
		close(release)
		e.Close()
	})

	for i := 1; i <= 3; i++ {
		f := feature(i, "f", domain.StageImplement)
		withWorktree(t, wt, f)
		if err := e.Run(f); err != nil {
			t.Fatal(err)
		}
	}
	waitState(t, e, "FD-001", StateRunning)
	waitState(t, e, "FD-002", StateRunning)
	waitState(t, e, "FD-003", StateQueued) // third waits behind two slots
}

func TestRunRequiresWorktree(t *testing.T) {
	e := newEngine(t, agent.NewFake("ok"))
	f := feature(1, "no wt", domain.StageImplement)
	if err := e.Run(f); err != nil {
		t.Fatal(err) // Run enqueues fine
	}
	// the failure surfaces asynchronously as the scheduler tries to start
	ev := waitFor(t, e, EventError)
	if ev.Err == nil {
		t.Error("missing worktree produced no error")
	}
	waitState(t, e, "FD-001", StatePaused)
}

func TestDroppingQueuedDoesNotOverfreeSlot(t *testing.T) {
	// Regression: dropping/pausing a QUEUED session must not decrement
	// the running count (it never held a slot), which would let an extra
	// autonomous session start beyond MaxActive.
	release := make(chan struct{})
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		<-release
		return []agent.Event{{Kind: agent.EventIdle}}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() {
		close(release)
		e.Close()
	})

	f1 := feature(1, "one", domain.StageImplement)
	f2 := feature(2, "two", domain.StageImplement)
	f3 := feature(3, "three", domain.StageImplement)
	for _, f := range []domain.Feature{f1, f2, f3} {
		withWorktree(t, wt, f)
	}
	if err := e.Run(f1); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-001", StateRunning)
	if err := e.Run(f2); err != nil {
		t.Fatal(err)
	}
	if err := e.Run(f3); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-002", StateQueued)
	waitState(t, e, "FD-003", StateQueued)

	// drop the queued FD-002 — the slot is still held by FD-001, so
	// FD-003 must NOT start (that would be 2 running with MaxActive 1).
	e.Drop("FD-002")
	// give any erroneous scheduling a moment to happen
	time.Sleep(50 * time.Millisecond)
	if s := e.Get("FD-003"); s == nil || s.State() != StateQueued {
		t.Fatalf("FD-003 wrongly promoted past MaxActive after dropping a queued session: %v", s)
	}
	if e.Get("FD-001").State() != StateRunning {
		t.Error("FD-001 stopped running unexpectedly")
	}
}

func TestDropStopsSession(t *testing.T) {
	e := newEngine(t, agent.NewFake("hi"))
	if _, err := e.Attach(context.Background(), feature(1, "x", domain.StageBrainstorm)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventStarted)
	e.Drop("FD-001")
	if e.Get("FD-001") != nil {
		t.Error("Drop did not remove the session")
	}
}

func TestSendUnknownFeature(t *testing.T) {
	e := newEngine(t, agent.NewFake("x"))
	if err := e.Send(context.Background(), "FD-999", "hi"); err == nil {
		t.Error("send to unknown feature should error")
	}
	if err := e.Interrupt(context.Background(), "FD-999"); err == nil {
		t.Error("interrupt of unknown feature should error")
	}
}

func TestWorktreeStageLocatesWorktree(t *testing.T) {
	ws, store, wt := newRepo(t)
	rec := recordingAgent()
	e := New(Config{Agents: singleAgent(rec), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "impl me", domain.StageImplement)
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-001", StateDone)

	wantDir := filepath.Join(wt.Root(), f.WorktreePath())
	if rec.opts().WorkDir != wantDir {
		t.Errorf("workdir = %s, want worktree %s", rec.opts().WorkDir, wantDir)
	}
	if rec.opts().Role != agent.RoleImplementer {
		t.Errorf("role = %s, want implementer", rec.opts().Role)
	}
}

func TestAttachMaterializesDraftAndKicksOff(t *testing.T) {
	ws, store, wt := newRepo(t)
	rec := recordingAgent()
	e := New(Config{Agents: singleAgent(rec), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "Dark mode", domain.StageBrainstorm)
	if _, err := e.Attach(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventIdle)

	// the draft exists before the agent's first turn
	draft := filepath.Join(ws.DraftsDir(), spec.DraftFilename(&f))
	raw, err := os.ReadFile(draft)
	if err != nil {
		t.Fatalf("draft not materialized: %v", err)
	}
	if !strings.Contains(string(raw), "## Problem") {
		t.Errorf("draft is not the template: %q", raw)
	}

	// the hints carry the compiled-in contract, pointing at the draft
	hints := strings.Join(rec.opts().SystemHints, "\n")
	for _, want := range []string{draft, "single source of truth", "nearest preceding non-marker line"} {
		if !strings.Contains(hints, want) {
			t.Errorf("contract hint missing %q", want)
		}
	}

	// the agent led: gummi's kickoff is the first transcript turn
	snap := e.Get("FD-001").Snapshot()
	if len(snap.Transcript) == 0 || snap.Transcript[0].Author != AuthorSystem {
		t.Errorf("kickoff not recorded as a system turn: %+v", snap.Transcript)
	}
}

// recorder is an Agent that captures the last SessionOpts and counts
// how many sessions it was asked to start — enough to assert that a
// profile's backend field routed to the right adapter. name overrides
// the underlying Fake's default "fake" identity when a test needs it to
// match a specific backend key ("copilot", "headless").
type recorder struct {
	*agent.Fake
	mu       sync.Mutex
	lastOpts agent.SessionOpts
	sessions int
	name     string
}

func recordingAgent() *recorder {
	return &recorder{Fake: agent.NewFake("ok")}
}

func (r *recorder) Name() string {
	if r.name != "" {
		return r.name
	}
	return r.Fake.Name()
}

func (r *recorder) NewSession(ctx context.Context, opts agent.SessionOpts) (agent.Session, error) {
	r.mu.Lock()
	r.lastOpts = opts
	r.sessions++
	r.mu.Unlock()
	return r.Fake.NewSession(ctx, opts)
}

func (r *recorder) opts() agent.SessionOpts {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastOpts
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessions
}
