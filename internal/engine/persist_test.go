package engine

import (
	"context"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/worktree"
)

// persistEngine builds a persisting engine sharing a store/repo so a
// second engine can Restore from it.
func persistEngine(t *testing.T, ag agent.Agent, ws state.Workspace, store *state.Store, wt *worktree.Manager) *Engine {
	t.Helper()
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1, Persist: true})
	t.Cleanup(func() { e.Close() })
	return e
}

// createFeature persists a feature so session FKs resolve.
func createFeature(t *testing.T, store *state.Store, f domain.Feature) {
	t.Helper()
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
}

func TestSessionPersistAndRestore(t *testing.T) {
	ws, store, wt := newRepo(t)
	ctx := context.Background()

	// an interactive feature with a persisted conversation
	f := feature(1, "Dark mode", domain.StageBrainstorm)
	createFeature(t, store, f)

	e1 := persistEngine(t, agent.NewFake("Two approaches, per-device vs synced."), ws, store, wt)
	if _, err := e1.Attach(ctx, f); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e1, EventIdle) // kickoff turn completes
	if err := e1.Send(ctx, f.ID, "how should it persist?"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e1, EventIdle)
	e1.Close()

	// a fresh engine restores the session from the store
	e2 := persistEngine(t, agent.NewFake("x"), ws, store, wt)
	if err := e2.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	s := e2.Get(f.ID)
	if s == nil {
		t.Fatal("session not restored")
	}
	// kickoff (system) + reply, then the user turn + reply
	snap := s.Snapshot()
	if len(snap.Transcript) != 4 {
		t.Fatalf("restored transcript = %+v", snap.Transcript)
	}
	if snap.Transcript[0].Author != AuthorSystem ||
		snap.Transcript[2].Content != "how should it persist?" ||
		snap.Transcript[3].Content != "Two approaches, per-device vs synced." {
		t.Errorf("restored transcript wrong: %+v", snap.Transcript)
	}
	if snap.Spend.Credits != 2 {
		t.Errorf("restored spend = %+v", snap.Spend)
	}
	if s.State() != StateInteractive {
		t.Errorf("restored interactive session state = %s", s.State())
	}
}

// idAgent wraps an Agent so its sessions implement agent.Identified,
// standing in for backends (copilot) that assign durable session ids.
type idAgent struct {
	agent.Agent
	id string
}

func (a idAgent) NewSession(ctx context.Context, opts agent.SessionOpts) (agent.Session, error) {
	s, err := a.Agent.NewSession(ctx, opts)
	if err != nil {
		return nil, err
	}
	return idSession{Session: s, id: a.id}, nil
}

type idSession struct {
	agent.Session
	id string
}

func (s idSession) SessionID() string { return s.id }

func TestToolResultsAndSessionIDSurviveRestart(t *testing.T) {
	ws, store, wt := newRepo(t)
	ctx := context.Background()

	f := feature(1, "impl", domain.StageImplement)
	createFeature(t, store, f)
	withWorktree(t, wt, f)

	ag := idAgent{id: "cli-session-42", Agent: &agent.Fake{
		Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
			return []agent.Event{
				{Kind: agent.EventToolCall, Tool: "bash", Detail: "rockcraft pack", CallID: "c1"},
				{
					Kind: agent.EventToolResult, CallID: "c1",
					Result: &agent.ToolResult{OK: false, Output: "Error: device eth0 already exists"},
				},
				{Kind: agent.EventMessage, Text: "build failed"},
				{Kind: agent.EventIdle},
			}
		},
	}}
	e1 := persistEngine(t, ag, ws, store, wt)
	if err := e1.Run(f); err != nil {
		t.Fatal(err)
	}
	// EventIdle is sent after the turn's persist, so the snapshot below
	// is durably written before the restart (waitState would race it).
	waitFor(t, e1, EventIdle)
	if got := e1.Get(f.ID).Snapshot().AgentSessionID; got != "cli-session-42" {
		t.Errorf("live AgentSessionID = %q", got)
	}
	e1.Close()

	e2 := persistEngine(t, agent.NewFake("x"), ws, store, wt)
	if err := e2.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	s := e2.Get(f.ID)
	if s == nil {
		t.Fatal("session not restored")
	}
	snap := s.Snapshot()
	if snap.AgentSessionID != "cli-session-42" {
		t.Errorf("restored AgentSessionID = %q", snap.AgentSessionID)
	}
	var tool *Message
	for i := range snap.Transcript {
		if snap.Transcript[i].Author == AuthorTool {
			tool = &snap.Transcript[i]
			break
		}
	}
	if tool == nil {
		t.Fatalf("no tool entry restored: %+v", snap.Transcript)
	}
	if tool.ToolStatus != ToolFail || tool.ToolOutput != "Error: device eth0 already exists" {
		t.Errorf("restored tool entry = %+v, want the failure + output", tool)
	}
}

func TestRestoreInterruptedAutonomousComesBackPaused(t *testing.T) {
	ws, store, wt := newRepo(t)
	ctx := context.Background()

	f := feature(1, "impl", domain.StageImplement)
	createFeature(t, store, f)
	withWorktree(t, wt, f)

	// a blocking responder keeps the session Running when we Close
	release := make(chan struct{})
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		<-release
		return []agent.Event{{Kind: agent.EventIdle}}
	}}
	e1 := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1, Persist: true})
	if err := e1.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e1, f.ID, StateRunning)
	// persisted as running; simulate a crash/restart
	close(release)
	e1.Close()

	e2 := persistEngine(t, agent.NewFake("x"), ws, store, wt)
	if err := e2.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	s := e2.Get(f.ID)
	if s == nil {
		t.Fatal("autonomous session not restored")
	}
	if s.State() != StatePaused {
		t.Errorf("interrupted running session restored as %s, want paused", s.State())
	}
}

func TestRestoreSkipsStaleStage(t *testing.T) {
	ws, store, wt := newRepo(t)
	ctx := context.Background()

	f := feature(1, "x", domain.StageBrainstorm)
	createFeature(t, store, f)
	e1 := persistEngine(t, agent.NewFake("hi"), ws, store, wt)
	if _, err := e1.Attach(ctx, f); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e1, EventStarted)
	e1.Close()

	// the feature advanced past brainstorm since the session was saved
	if _, err := store.Transition(ctx, f.ID, domain.StageSpec, "user"); err != nil {
		t.Fatal(err)
	}

	e2 := persistEngine(t, agent.NewFake("x"), ws, store, wt)
	if err := e2.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	if e2.Get(f.ID) != nil {
		t.Error("restored a session whose stage no longer matches the feature")
	}
}

func TestDropDeletesPersistedSession(t *testing.T) {
	ws, store, wt := newRepo(t)
	ctx := context.Background()
	f := feature(1, "x", domain.StageBrainstorm)
	createFeature(t, store, f)

	e := persistEngine(t, agent.NewFake("hi"), ws, store, wt)
	if _, err := e.Attach(ctx, f); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventStarted)
	e.Drop(f.ID)

	snaps, err := store.LoadSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 0 {
		t.Errorf("Drop left a persisted session: %+v", snaps)
	}
}
