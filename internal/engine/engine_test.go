package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/morphia/gummi/internal/agent"
	"github.com/morphia/gummi/internal/domain"
	"github.com/morphia/gummi/internal/state"
	"github.com/morphia/gummi/internal/worktree"
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
	wt, err := worktree.NewManager(context.Background(), root)
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

// waitFor reads engine events until one matching kind arrives or the
// deadline passes.
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

func newEngine(t *testing.T, ag agent.Agent) *Engine {
	t.Helper()
	ws, store, wt := newRepo(t)
	e := New(Config{Agent: ag, Store: store, Worktrees: wt, Workspace: ws, Model: "fake-model"})
	t.Cleanup(func() { e.Close() })
	return e
}

func TestStartRejectsNonAgentStages(t *testing.T) {
	e := newEngine(t, agent.NewFake("hi"))
	for _, st := range []domain.Stage{domain.StageTodo, domain.StageDone} {
		if _, err := e.Start(context.Background(), feature(1, "x", st)); err == nil {
			t.Errorf("stage %s should have no agent action", st)
		}
	}
}

func TestInteractiveRoundTrip(t *testing.T) {
	e := newEngine(t, agent.NewFake("Here are two approaches."))
	ctx := context.Background()

	s, err := e.Start(ctx, feature(1, "Dark mode", domain.StageBrainstorm))
	if err != nil {
		t.Fatal(err)
	}
	if s.Role != agent.RoleArchitect || !s.Interactive {
		t.Fatalf("brainstorm should be interactive architect: %+v", s)
	}
	waitFor(t, e, EventStarted)

	if err := e.Send(ctx, "how should dark mode persist?"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventIdle)

	snap := s.Snapshot()
	if len(snap.Transcript) != 2 {
		t.Fatalf("transcript = %+v, want user+assistant", snap.Transcript)
	}
	if snap.Transcript[0].Author != AuthorUser || snap.Transcript[0].Content != "how should dark mode persist?" {
		t.Errorf("user turn wrong: %+v", snap.Transcript[0])
	}
	if snap.Transcript[1].Author != AuthorAssistant || snap.Transcript[1].Content != "Here are two approaches." {
		t.Errorf("assistant turn wrong: %+v", snap.Transcript[1])
	}
	if snap.Transcript[1].Streaming {
		t.Error("assistant turn should be finalized")
	}
	if snap.Busy {
		t.Error("session should be idle after the turn")
	}
	if snap.Spend.Credits != 1 {
		t.Errorf("spend not metered: %+v", snap.Spend)
	}
}

func TestStreamingDeltasAndActivity(t *testing.T) {
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{
			{Kind: agent.EventTextDelta, Text: "Look"},
			{Kind: agent.EventTextDelta, Text: "ing…"},
			{Kind: agent.EventToolCall, Tool: "grep internal/"},
			{Kind: agent.EventToolCall, Tool: "edit theme.go"},
			{Kind: agent.EventMessage, Text: "Looking… done."},
			{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 2, OutputTokens: 40}},
			{Kind: agent.EventIdle},
		}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{Agent: ag, Store: store, Worktrees: wt, Workspace: ws, Model: "fake-model"})
	t.Cleanup(func() { e.Close() })
	ctx := context.Background()

	f := feature(2, "search", domain.StageImplement)
	if _, err := wt.Create(ctx, &f); err != nil {
		t.Fatal(err)
	}
	s, err := e.Start(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if s.Interactive {
		t.Error("implement is autonomous, not interactive")
	}
	waitFor(t, e, EventStarted)
	if err := e.Send(ctx, "go"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventIdle)

	snap := s.Snapshot()
	last := snap.Transcript[len(snap.Transcript)-1]
	if last.Content != "Looking… done." || last.Streaming {
		t.Errorf("streamed message not finalized: %+v", last)
	}
	if len(snap.Activity) != 2 || snap.Activity[0] != "grep internal/" {
		t.Errorf("activity feed wrong: %+v", snap.Activity)
	}
	if snap.Spend.Credits != 2 || snap.Spend.OutputTokens != 40 {
		t.Errorf("spend wrong: %+v", snap.Spend)
	}
}

func TestStartReplacesActiveSession(t *testing.T) {
	e := newEngine(t, agent.NewFake("ok"))
	ctx := context.Background()
	s1, err := e.Start(ctx, feature(1, "one", domain.StageBrainstorm))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventStarted)
	s2, err := e.Start(ctx, feature(2, "two", domain.StageBrainstorm))
	if err != nil {
		t.Fatal(err)
	}
	// the first session is stopped; the engine's active is the second
	if e.Active() != s2 {
		t.Error("active session was not replaced")
	}
	// s1 eventually reports stopped on the event stream
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-e.Events():
			if ev.Feature == s1.Feature.ID && ev.Kind == EventStopped {
				return
			}
		case <-deadline:
			t.Fatal("first session never stopped")
		}
	}
}

func TestErrorEvent(t *testing.T) {
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{{Kind: agent.EventError, Err: context.DeadlineExceeded}}
	}}
	e := newEngine(t, ag)
	ctx := context.Background()
	s, err := e.Start(ctx, feature(1, "x", domain.StageBrainstorm))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventStarted)
	if err := e.Send(ctx, "go"); err != nil {
		t.Fatal(err)
	}
	ev := waitFor(t, e, EventError)
	if ev.Err == nil {
		t.Error("error event carried no error")
	}
	if s.Snapshot().Err == nil {
		t.Error("session did not record the error")
	}
}

func TestSendWithoutActiveSession(t *testing.T) {
	e := newEngine(t, agent.NewFake("x"))
	if err := e.Send(context.Background(), "hi"); err == nil {
		t.Error("send with no active session should error")
	}
	if err := e.Interrupt(context.Background()); err == nil {
		t.Error("interrupt with no active session should error")
	}
}

func TestWorktreeStageLocatesWorktree(t *testing.T) {
	ws, store, wt := newRepo(t)
	e := New(Config{Agent: recordingAgent(), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "impl me", domain.StageImplement)
	if _, err := wt.Create(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Start(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	ra := e.cfg.Agent.(*recorder)
	wantDir := filepath.Join(wt.Root(), f.WorktreePath())
	if ra.lastOpts.WorkDir != wantDir {
		t.Errorf("workdir = %s, want worktree %s", ra.lastOpts.WorkDir, wantDir)
	}
	if ra.lastOpts.Role != agent.RoleImplementer {
		t.Errorf("role = %s, want implementer", ra.lastOpts.Role)
	}
}

func TestWorktreeStageRequiresWorktree(t *testing.T) {
	e := newEngine(t, agent.NewFake("ok"))
	// implement with no worktree must error, not silently run in root
	_, err := e.Start(context.Background(), feature(1, "no wt", domain.StageImplement))
	if err == nil {
		t.Fatal("implement without a worktree should error")
	}
}

// recorder is an Agent that captures the last SessionOpts.
type recorder struct {
	*agent.Fake
	lastOpts agent.SessionOpts
}

func recordingAgent() *recorder {
	return &recorder{Fake: agent.NewFake("ok")}
}

func (r *recorder) NewSession(ctx context.Context, opts agent.SessionOpts) (agent.Session, error) {
	r.lastOpts = opts
	return r.Fake.NewSession(ctx, opts)
}
