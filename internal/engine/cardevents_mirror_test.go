package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/worktree"
)

// TestMirrorEventsIdempotentAcrossRepeatedPersist is the regression that
// matters: SaveSession rewrites a card's whole transcript on every save,
// so a naive mirror that appended the snapshot's messages each time would
// duplicate the transcript once per save. The dedupe key on each mirrored
// event exists to stop exactly that.
func TestMirrorEventsIdempotentAcrossRepeatedPersist(t *testing.T) {
	ws, store, wt := newRepo(t)
	ctx := context.Background()
	f := feature(1, "impl", domain.StageImplement)
	createFeature(t, store, f)
	withWorktree(t, wt, f)
	e := persistEngine(t, agent.NewFake("hi"), ws, store, wt)

	s := &Session{Feature: f, Role: agent.RoleImplementer, state: StateRunning, startedAt: time.Now()}
	s.transcript = []Message{
		{Author: AuthorSystem, Content: "kickoff"},
		{Author: AuthorAssistant, Content: "on it"},
		{Author: AuthorTool, Content: "go test ./...", ToolStatus: ToolOK, ToolOutput: "PASS"},
	}

	var want int
	for i := 0; i < 5; i++ {
		e.persist(s)
		evs, err := store.Events(ctx, f.ID)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			want = len(evs)
			if want == 0 {
				t.Fatal("first persist mirrored no events")
			}
			continue
		}
		if len(evs) != want {
			t.Fatalf("persist #%d: event count = %d, want %d (duplicate mirror)", i+1, len(evs), want)
		}
	}
}

// TestMirrorSkipsUnsettledUntilSettled: a streaming assistant message and
// an unresolved tool call have a stable ord while their content is still
// changing, so mirroring them early would freeze the truncated first
// version under a dedupe key that can never be overwritten. They must be
// skipped until they settle, then mirrored exactly once with their final
// content.
func TestMirrorSkipsUnsettledUntilSettled(t *testing.T) {
	ws, store, wt := newRepo(t)
	ctx := context.Background()
	f := feature(1, "impl", domain.StageImplement)
	createFeature(t, store, f)
	withWorktree(t, wt, f)
	e := persistEngine(t, agent.NewFake("hi"), ws, store, wt)

	s := &Session{Feature: f, Role: agent.RoleImplementer, state: StateRunning, startedAt: time.Now()}
	s.transcript = []Message{
		{Author: AuthorAssistant, Content: "partial thought...", Streaming: true},
		{Author: AuthorTool, Content: "go test ./...", ToolStatus: ToolPending},
	}
	e.persist(s)

	evs, err := store.Events(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs {
		if ev.Kind == state.EventMessage || ev.Kind == state.EventTool {
			t.Fatalf("unsettled entry mirrored early: %+v", ev)
		}
	}

	// both entries settle
	s.transcript[0].Streaming = false
	s.transcript[0].Content = "final thought"
	s.transcript[1].ToolStatus = ToolOK
	s.transcript[1].ToolOutput = "PASS"
	e.persist(s)

	msgEvs, toolEvs := splitByKind(t, store, f.ID)
	if len(msgEvs) != 1 {
		t.Fatalf("message events = %d, want 1: %+v", len(msgEvs), msgEvs)
	}
	if !strings.Contains(msgEvs[0].Payload, "final thought") {
		t.Errorf("message payload = %q, want the final content", msgEvs[0].Payload)
	}
	if len(toolEvs) != 1 {
		t.Fatalf("tool events = %d, want 1: %+v", len(toolEvs), toolEvs)
	}
	if toolEvs[0].Status != string(ToolOK) || toolEvs[0].Output != "PASS" {
		t.Errorf("tool event = %+v, want ok/PASS", toolEvs[0])
	}

	// a further persist with nothing new must not duplicate the now-settled entries
	e.persist(s)
	msgEvs, toolEvs = splitByKind(t, store, f.ID)
	if len(msgEvs) != 1 || len(toolEvs) != 1 {
		t.Fatalf("after re-persist: messages=%d tools=%d, want 1/1", len(msgEvs), len(toolEvs))
	}
}

// splitByKind reads a feature's mirrored events and buckets message/tool
// entries.
func splitByKind(t *testing.T, store *state.Store, id domain.FeatureID) (msgs, tools []state.CardEvent) {
	t.Helper()
	evs, err := store.Events(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs {
		switch ev.Kind {
		case state.EventMessage:
			msgs = append(msgs, ev)
		case state.EventTool:
			tools = append(tools, ev)
		}
	}
	return msgs, tools
}

// TestMirrorStageEnterOnce: stage_enter carries a dedupe key scoped to
// the session generation, so it lands once no matter how many times the
// session is persisted afterward.
func TestMirrorStageEnterOnce(t *testing.T) {
	ws, store, wt := newRepo(t)
	ctx := context.Background()
	f := feature(1, "impl", domain.StageImplement)
	createFeature(t, store, f)
	withWorktree(t, wt, f)
	e := persistEngine(t, agent.NewFake("hi"), ws, store, wt)

	s := &Session{Feature: f, Role: agent.RoleImplementer, state: StateRunning, startedAt: time.Now()}
	for i := 0; i < 4; i++ {
		s.transcript = append(s.transcript, Message{Author: AuthorAssistant, Content: fmt.Sprintf("turn %d", i)})
		e.persist(s)
	}

	evs, err := store.Events(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	var enters int
	for _, ev := range evs {
		if ev.Kind == state.EventStageEnter {
			enters++
		}
	}
	if enters != 1 {
		t.Fatalf("stage_enter events = %d, want 1", enters)
	}
}

// TestMirrorStageExitAndPruneOnDone: reaching StateDone writes a
// stage_exit event carrying the verdict and spend, and prunes the raw
// output of the stage's successful tool events while keeping it for any
// that failed. persist() early-returns on a finalized (stopped) session,
// but StateDone is a scheduling state reached well before stop() marks a
// session finalized, so a StateDone session still reaches persist and
// this mirror hook.
func TestMirrorStageExitAndPruneOnDone(t *testing.T) {
	ws, store, wt := newRepo(t)
	ctx := context.Background()
	f := feature(1, "impl", domain.StageImplement)
	createFeature(t, store, f)
	withWorktree(t, wt, f)
	e := persistEngine(t, agent.NewFake("hi"), ws, store, wt)

	s := &Session{Feature: f, Role: agent.RoleImplementer, startedAt: time.Now()}
	s.transcript = []Message{
		{Author: AuthorTool, Content: "go build ./...", ToolStatus: ToolOK, ToolOutput: "build ok output"},
		{Author: AuthorTool, Content: "go test ./...", ToolStatus: ToolFail, ToolOutput: "FAIL: boom"},
	}
	s.verdict = "pass"
	s.spend = agent.Usage{Credits: 3.5}
	s.state = StateDone
	e.persist(s)

	evs, err := store.Events(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}

	var exit *state.CardEvent
	toolByStatus := map[string]state.CardEvent{}
	for _, ev := range evs {
		ev := ev
		if ev.Kind == state.EventStageExit {
			exit = &ev
		}
		if ev.Kind == state.EventTool {
			toolByStatus[ev.Status] = ev
		}
	}
	if exit == nil {
		t.Fatal("no stage_exit event after StateDone")
	}
	if !strings.Contains(exit.Payload, "pass") || !strings.Contains(exit.Payload, "3.5") {
		t.Errorf("stage_exit payload = %q, want the verdict and spend", exit.Payload)
	}

	ok, hasOK := toolByStatus[string(ToolOK)]
	if !hasOK {
		t.Fatal("no successful tool event mirrored")
	}
	if ok.Output != "" {
		t.Errorf("successful tool output not pruned: %q", ok.Output)
	}
	fail, hasFail := toolByStatus[string(ToolFail)]
	if !hasFail {
		t.Fatal("no failed tool event mirrored")
	}
	if fail.Output != "FAIL: boom" {
		t.Errorf("failed tool output = %q, want it kept", fail.Output)
	}
}

// TestMirrorTwoGenerationsDoNotCollide: a second session generation on
// the same stage (a review bounce, a resumed card) is discriminated from
// the first by startedAt, so its events land alongside the first
// generation's rather than colliding with (and silently dropping under)
// its dedupe keys.
func TestMirrorTwoGenerationsDoNotCollide(t *testing.T) {
	ws, store, wt := newRepo(t)
	ctx := context.Background()
	f := feature(1, "impl", domain.StageImplement)
	createFeature(t, store, f)
	withWorktree(t, wt, f)
	e := persistEngine(t, agent.NewFake("hi"), ws, store, wt)

	t1 := time.Now()
	s1 := &Session{Feature: f, Role: agent.RoleImplementer, state: StateRunning, startedAt: t1}
	s1.transcript = []Message{{Author: AuthorAssistant, Content: "first attempt"}}
	e.persist(s1)

	t2 := t1.Add(time.Second)
	s2 := &Session{Feature: f, Role: agent.RoleImplementer, state: StateRunning, startedAt: t2}
	s2.transcript = []Message{{Author: AuthorAssistant, Content: "second attempt"}}
	e.persist(s2)

	evs, err := store.Events(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}

	var enters, msgs int
	var sawFirst, sawSecond bool
	for _, ev := range evs {
		if ev.Kind == state.EventStageEnter {
			enters++
		}
		if ev.Kind == state.EventMessage {
			msgs++
			if strings.Contains(ev.Payload, "first attempt") {
				sawFirst = true
			}
			if strings.Contains(ev.Payload, "second attempt") {
				sawSecond = true
			}
		}
	}
	if enters != 2 {
		t.Fatalf("stage_enter events across generations = %d, want 2", enters)
	}
	if msgs != 2 || !sawFirst || !sawSecond {
		t.Fatalf("message events across generations = %d (first=%v second=%v), want both distinct", msgs, sawFirst, sawSecond)
	}
}

// answeredAsk drives one real ask_user round trip: a client-tool fake
// raises the question on the kickoff turn, and AnswerAs resolves it with
// "Yes, move on" declared by by (the unattended loop's ActorAutopilot, or
// the composer's ActorUser). The persisting engine means the exchange is
// durably written by the time AnswerAs returns.
func answeredAsk(t *testing.T, by string) (e *Engine, store *state.Store, ws state.Workspace, wt *worktree.Manager, f domain.Feature) {
	t.Helper()
	args := askArgs(t, Ask{
		Question: "Proceed with the plan?",
		Options:  []AskOption{{Label: "Yes, move on"}, {Label: "revise"}},
	})
	ws, store, wt = newRepo(t)
	e = persistEngine(t, clientToolFake(args), ws, store, wt)
	f = feature(1, "Dark mode", domain.StagePlan)
	createFeature(t, store, f)
	if _, err := e.Attach(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventQuestion)
	if err := e.AnswerAs(context.Background(), f.ID, "Yes, move on", by); err != nil {
		t.Fatal(err)
	}
	return e, store, ws, wt, f
}

// userEchoRows returns the content of every mirrored user-authored
// message row in the card's event log.
func userEchoRows(t *testing.T, store *state.Store, id domain.FeatureID) []string {
	t.Helper()
	evs, err := store.Events(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, ev := range evs {
		if ev.Kind != state.EventMessage {
			continue
		}
		var p struct {
			Author  string `json:"author"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal([]byte(ev.Payload), &p); err != nil {
			t.Fatal(err)
		}
		if p.Author == string(AuthorUser) {
			out = append(out, p.Content)
		}
	}
	return out
}

// findEcho returns the user-authored transcript entry carrying text, or
// nil when there is none.
func findEcho(snap Snapshot, text string) *Message {
	for i := range snap.Transcript {
		m := &snap.Transcript[i]
		if m.Author == AuthorUser && m.Content == text {
			return m
		}
	}
	return nil
}

// TestMirrorSkipsAutopilotAnswerEcho pins the mirror contract the stretch
// derivation depends on. AnswerAs records a machine-taken ask_user answer
// in the transcript as a user-authored echo — deliberately, so a restored
// session reads as a conversation — stamped with who answered. Mirrored
// verbatim, that echo would land in the card's event log as an ordinary
// user message, and a user message is exactly what closes an autopilot
// period as "you took back control"; the ask event beside it already
// carries the true answerer. So the echo must stay in the transcript and
// out of the log, and the stamp must survive the full persistence round
// trip: an echo skipped live but unstamped after restore would be
// mirrored by the first post-restore save — reintroducing the bug on
// exactly the run→resume path unattended runs take.
func TestMirrorSkipsAutopilotAnswerEcho(t *testing.T) {
	e, store, ws, wt, f := answeredAsk(t, state.ActorAutopilot)
	ctx := context.Background()

	// the echo is in the live transcript, stamped with the answerer
	echo := findEcho(e.Get(f.ID).Snapshot(), "Yes, move on")
	if echo == nil {
		t.Fatal("answer echo not found in the live transcript")
	}
	if echo.AnsweredBy != state.ActorAutopilot {
		t.Fatalf("echo AnsweredBy = %q, want %q", echo.AnsweredBy, state.ActorAutopilot)
	}
	// and it is not in the log
	if rows := userEchoRows(t, store, f.ID); len(rows) != 0 {
		t.Fatalf("mirrored user messages = %q, want none — the echo of autopilot's own answer is not a person taking the card back", rows)
	}
	e.Close()

	// the restart: a fresh engine restores the session, stamp intact.
	e2 := persistEngine(t, agent.NewFake("x"), ws, store, wt)
	if err := e2.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	s := e2.Get(f.ID)
	if s == nil {
		t.Fatal("session not restored")
	}
	restored := findEcho(s.Snapshot(), "Yes, move on")
	if restored == nil {
		t.Fatal("answer echo lost across the restart")
	}
	if restored.AnsweredBy != state.ActorAutopilot {
		t.Fatalf("restored echo AnsweredBy = %q, want %q — unstamped, the next save mirrors the echo as a user message and closes the stretch",
			restored.AnsweredBy, state.ActorAutopilot)
	}
	// the first save after the restart: the skip still holds.
	e2.persist(s)
	if rows := userEchoRows(t, store, f.ID); len(rows) != 0 {
		t.Fatalf("after restore + resave, mirrored user messages = %q, want none", rows)
	}
}

// TestMirrorKeepsHumanAnswerEcho: the skip is scoped to the autopilot
// stamp. A person answering the open ask through the composer produces
// the same transcript echo, and it still mirrors: its ask row says the
// user answered and already closed the period, so the echo's mirror row
// is harmless — and the replayed history should read as a conversation.
func TestMirrorKeepsHumanAnswerEcho(t *testing.T) {
	e, store, _, _, f := answeredAsk(t, state.ActorUser)
	defer e.Close()
	rows := userEchoRows(t, store, f.ID)
	if len(rows) != 1 || rows[0] != "Yes, move on" {
		t.Fatalf("mirrored user messages = %q, want [Yes, move on]", rows)
	}
}
