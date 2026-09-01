package engine

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/config"
)

// waitBoardIdle polls the board session until it's no longer busy — the
// board-session counterpart to waitState (a card session polls State();
// a board session has no scheduling state, just Busy).
func waitBoardIdle(t *testing.T, b *BoardSession) {
	t.Helper()
	deadline := time.After(testWaitTimeout)
	for {
		if !b.Snapshot().Busy {
			return
		}
		select {
		case <-deadline:
			t.Fatal("board session never went idle")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// TestBoardOpenSendReceivesEvents: open→send→the turn lands in the
// snapshot's transcript, on a backend with neither ClientTools nor
// MCPTools — the "no tools" case still has to hold an ordinary
// conversation.
func TestBoardOpenSendReceivesEvents(t *testing.T) {
	ag := agent.NewFake("hello from the board")
	e := newEngine(t, ag)
	ctx := context.Background()

	b, err := e.OpenBoard(ctx, BoardOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Send(ctx, "what's on the board?"); err != nil {
		t.Fatal(err)
	}
	waitBoardIdle(t, b)

	snap := b.Snapshot()
	if snap.Role != agent.RoleBoard {
		t.Errorf("Role = %q, want %q", snap.Role, agent.RoleBoard)
	}
	if !snap.Interactive {
		t.Error("a board session must be Interactive (no attention slot)")
	}
	var sawUser, sawAssistant bool
	for _, m := range snap.Transcript {
		if m.Author == AuthorUser && m.Content == "what's on the board?" {
			sawUser = true
		}
		if m.Author == AuthorAssistant && strings.Contains(m.Content, "hello from the board") {
			sawAssistant = true
		}
	}
	if !sawUser || !sawAssistant {
		t.Fatalf("transcript missing turns: %+v", snap.Transcript)
	}
}

// TestBoardOpenNoToolsBackend: a backend advertising neither ClientTools
// nor MCPTools still opens fine — it just gets no board tools wired in
// (no prompt-convention fallback the way ask_user has one for card
// sessions; see OpenBoard's doc comment on the tools block).
func TestBoardOpenNoToolsBackend(t *testing.T) {
	r := &recorder{Fake: agent.NewFake("ok")}
	e := newEngine(t, r)
	ctx := context.Background()

	b, err := e.OpenBoard(ctx, BoardOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if b == nil {
		t.Fatal("nil board session")
	}
	opts := r.opts()
	if len(opts.Tools) != 0 {
		t.Errorf("Tools = %+v, want none", opts.Tools)
	}
	if opts.MCPSockPath != "" || opts.Workspace {
		t.Errorf("MCPSockPath/Workspace set for a backend with no MCPTools: %+v", opts)
	}
	if opts.Role != agent.RoleBoard {
		t.Errorf("Role = %q, want %q", opts.Role, agent.RoleBoard)
	}
	if opts.Permission != agent.PermissionAllowAll {
		t.Errorf("Permission = %q, want %q", opts.Permission, agent.PermissionAllowAll)
	}
	if opts.WorkDir != e.cfg.Workspace.Root {
		t.Errorf("WorkDir = %q, want the workspace root %q (not a worktree)", opts.WorkDir, e.cfg.Workspace.Root)
	}
	if opts.ArtifactPath != "" || opts.FeatureID != "" || opts.MaxCredits != 0 {
		t.Errorf("board session carried card-scoped opts it must not: %+v", opts)
	}
}

// TestBoardClientToolsWiring: a ClientTools backend gets the seven
// workspace tools on SessionOpts.Tools and no MCP endpoint.
func TestBoardClientToolsWiring(t *testing.T) {
	r := &recorder{Fake: agent.NewFake("ok")}
	r.Fake.Caps = agent.Capabilities{ClientTools: true, UsageEvents: true, Interrupt: true}
	e := newEngine(t, r)

	if _, err := e.OpenBoard(context.Background(), BoardOpts{}); err != nil {
		t.Fatal(err)
	}
	opts := r.opts()
	if len(opts.Tools) != len(workspaceTools()) {
		t.Fatalf("Tools = %d, want %d (workspaceTools)", len(opts.Tools), len(workspaceTools()))
	}
	if opts.MCPSockPath != "" || opts.Workspace {
		t.Errorf("a ClientTools backend must not also get an MCP endpoint: %+v", opts)
	}
}

// TestBoardMCPToolsWiring: an MCPTools backend gets an inbound workspace
// MCP endpoint (MCPSockPath + Workspace: true) and no opts.Tools — it
// reaches gummi's board tools by dialing back in, the same as a hosted
// agent using the socket StartWorkspaceMCPEndpoint hands the TUI.
func TestBoardMCPToolsWiring(t *testing.T) {
	r := &recorder{Fake: agent.NewFake("ok")}
	r.Fake.Caps = agent.Capabilities{MCPTools: true, UsageEvents: true, Interrupt: true}
	e := newEngine(t, r)

	b, err := e.OpenBoard(context.Background(), BoardOpts{})
	if err != nil {
		t.Fatal(err)
	}
	opts := r.opts()
	if len(opts.Tools) != 0 {
		t.Errorf("an MCPTools backend must not get opts.Tools: %+v", opts.Tools)
	}
	if opts.MCPSockPath == "" || !opts.Workspace {
		t.Errorf("an MCPTools backend must get an MCP endpoint in workspace mode: %+v", opts)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestBoardUsageAccumulatesNoStoreWrites: a usage event folds into the
// live snapshot's spend, but a board session belongs to no feature row —
// nothing lands in the store (no stage_spend row, no card).
func TestBoardUsageAccumulatesNoStoreWrites(t *testing.T) {
	ag := agent.NewFake("")
	ag.Responder = func(_ agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{
			{Kind: agent.EventMessage, Text: "ack: " + msg},
			{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 3, InputTokens: 10, OutputTokens: 20, Model: "m"}},
			{Kind: agent.EventIdle},
		}
	}
	ws, store, wt := newRepo(t)
	// Persist: true so this exercises the same "is persistence enabled"
	// condition the card path guards its store writes on — if a board
	// write ever snuck in, this is the config that would let it through.
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", Persist: true})
	t.Cleanup(func() { e.Close() })
	ctx := context.Background()

	b, err := e.OpenBoard(ctx, BoardOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Send(ctx, "status?"); err != nil {
		t.Fatal(err)
	}
	waitBoardIdle(t, b)

	snap := b.Snapshot()
	if snap.Spend.Credits != 3 {
		t.Errorf("Spend.Credits = %v, want 3", snap.Spend.Credits)
	}
	if snap.SpentCredits != 3 {
		t.Errorf("SpentCredits = %v, want 3", snap.SpentCredits)
	}

	rows, err := store.StageBreakdown(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("store.StageBreakdown(\"\") = %+v, want no rows — a board session must never write spend to the store", rows)
	}
	feats, err := store.ListFeatures(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(feats) != 0 {
		t.Errorf("ListFeatures = %+v, want none — a usage event must not conjure a card", feats)
	}
}

// TestBoardEventReachesEngineEventsWithEmptyFeature: EventBoard arrives
// on the same Events() stream a UI reads card events from, and carries
// an empty Feature/Stage — the one kind that legitimately does, per
// Event.Feature's doc comment.
func TestBoardEventReachesEngineEventsWithEmptyFeature(t *testing.T) {
	ag := agent.NewFake("hi")
	e := newEngine(t, ag)

	if _, err := e.OpenBoard(context.Background(), BoardOpts{}); err != nil {
		t.Fatal(err)
	}
	ev := waitFor(t, e, EventBoard)
	if ev.Feature != "" {
		t.Errorf("EventBoard.Feature = %q, want empty", ev.Feature)
	}
	if ev.Stage != "" {
		t.Errorf("EventBoard.Stage = %q, want empty", ev.Stage)
	}
}

// TestBoardEventFeatureLookupToleratesEmpty documents that the two
// engine-side lookups a consumer would naturally call with an event's
// Feature — Get and Sessions — behave safely on the empty id EventBoard
// carries: Get returns nil (a plain "no session", not a panic), and
// Sessions never lists it, since a board session is deliberately never
// installed into e.live. Whether every UI surface that keys off
// Event.Feature also tolerates an empty one is internal/ui's own
// question — this is the part of it that lives in this package.
func TestBoardEventFeatureLookupToleratesEmpty(t *testing.T) {
	ag := agent.NewFake("hi")
	e := newEngine(t, ag)

	if _, err := e.OpenBoard(context.Background(), BoardOpts{}); err != nil {
		t.Fatal(err)
	}
	ev := waitFor(t, e, EventBoard)
	if s := e.Get(ev.Feature); s != nil {
		t.Errorf("Get(%q) = %+v, want nil for the board's empty feature id", ev.Feature, s)
	}
	if _, ok := e.Sessions()[ev.Feature]; ok {
		t.Errorf("Sessions()[%q] exists; a board session must never be indexed under e.live", ev.Feature)
	}
}

// TestBoardClientToolDispatchAnswersBoardList exercises the copilot-style
// EventClientToolCall path end to end: the model calls board_list, the
// engine routes it through dispatchBoardTool (shared with the workspace
// MCP endpoint) on its own goroutine, and the result comes back through
// the fake's ToolResolver — the same contract handleClientTool's
// resolveNow uses for a card session.
func TestBoardClientToolDispatchAnswersBoardList(t *testing.T) {
	first := true
	ag := agent.NewFake("")
	ag.Caps = agent.Capabilities{ClientTools: true, UsageEvents: true, Interrupt: true}
	ag.Responder = func(_ agent.SessionOpts, msg string) []agent.Event {
		if first {
			first = false
			return []agent.Event{
				{Kind: agent.EventClientToolCall, ToolCall: &agent.ToolCall{
					ID: "call-1", Name: boardListToolName, Args: json.RawMessage(`{}`),
				}},
			}
		}
		return []agent.Event{{Kind: agent.EventMessage, Text: "done"}, {Kind: agent.EventIdle}}
	}
	e := newEngine(t, ag)
	ctx := context.Background()

	b, err := e.OpenBoard(ctx, BoardOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Send(ctx, "what's on the board?"); err != nil {
		t.Fatal(err)
	}

	type resolver interface {
		Resolved(string) (string, bool)
	}
	r, ok := b.sess.agent().(resolver)
	if !ok {
		t.Fatal("fake session does not implement Resolved")
	}
	deadline := time.After(testWaitTimeout)
	for {
		if got, done := r.Resolved("call-1"); done {
			if got != "[]" {
				t.Errorf("board_list resolved with %q, want [] (an empty backlog)", got)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("call-1 (board_list) never resolved")
		case <-time.After(5 * time.Millisecond):
		}
	}

	// the dispatch also recorded (and settled) an AuthorTool transcript
	// entry — the same bookkeeping a card session's tool calls get.
	var sawToolOK bool
	for _, m := range b.Snapshot().Transcript {
		if m.Author == AuthorTool && m.Content == boardListToolName && m.ToolStatus == ToolOK {
			sawToolOK = true
		}
	}
	if !sawToolOK {
		t.Errorf("transcript missing a resolved %s tool entry: %+v", boardListToolName, b.Snapshot().Transcript)
	}
}

// TestBoardCloseCleanNoDeadlock: BoardSession.Close terminates promptly
// (no deadlock between the pump goroutine's stop path and Close's join),
// actually closes the backing agent session (a further Send fails rather
// than leaking a live process), and a second Close — of the BoardSession
// or the Engine — is safe.
func TestBoardCloseCleanNoDeadlock(t *testing.T) {
	ag := agent.NewFake("hi")
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
	t.Cleanup(func() { e.Close() })
	ctx := context.Background()

	b, err := e.OpenBoard(ctx, BoardOpts{})
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := b.Close(); err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-done:
	case <-time.After(testWaitTimeout):
		t.Fatal("BoardSession.Close deadlocked")
	}

	if err := b.Send(ctx, "still there?"); err == nil {
		t.Error("Send after Close succeeded; the backend session should have been closed")
	}

	// a second BoardSession.Close is safe (sess.stop's sync.Once).
	if err := b.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}

	// a fresh OpenBoard after Close starts a new session rather than
	// resurrecting the closed one.
	b2, err := e.OpenBoard(ctx, BoardOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if b2 == b {
		t.Error("OpenBoard after Close returned the same (closed) BoardSession")
	}

	closeDone := make(chan struct{})
	go func() {
		defer close(closeDone)
		if err := e.Close(); err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-closeDone:
	case <-time.After(testWaitTimeout):
		t.Fatal("Engine.Close deadlocked with a board session live")
	}

	// Engine.Close is itself idempotent.
	if err := e.Close(); err != nil {
		t.Errorf("second Engine.Close: %v", err)
	}
}

// TestBoardOpenReusesLiveSession: a second OpenBoard while one is already
// live returns the same session rather than spawning a second backend —
// the board counterpart to Attach's "a live agent session ... is reused".
func TestBoardOpenReusesLiveSession(t *testing.T) {
	r := &recorder{Fake: agent.NewFake("hi")}
	e := newEngine(t, r)
	ctx := context.Background()

	b1, err := e.OpenBoard(ctx, BoardOpts{})
	if err != nil {
		t.Fatal(err)
	}
	b2, err := e.OpenBoard(ctx, BoardOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if b1 != b2 {
		t.Error("a second OpenBoard did not reuse the live board session")
	}
	if n := r.count(); n != 1 {
		t.Errorf("agent NewSession called %d times, want 1", n)
	}
}

// gatedOpener is an Agent whose NewSession blocks until the test
// releases it, and which counts how many times it was entered. It exists
// because the fake agent spawns instantly: a concurrency test built on
// it never actually overlaps, so it passes with or without the fix it is
// meant to guard. Holding every caller inside NewSession is what forces
// the interleaving the bug needs.
type gatedOpener struct {
	*agent.Fake
	entered chan struct{} // one send per NewSession entry
	release chan struct{} // closed by the test to let them all return
}

func newGatedOpener() *gatedOpener {
	return &gatedOpener{
		Fake:    agent.NewFake("hi"),
		entered: make(chan struct{}, 64),
		release: make(chan struct{}),
	}
}

func (g *gatedOpener) NewSession(ctx context.Context, opts agent.SessionOpts) (agent.Session, error) {
	g.entered <- struct{}{}
	<-g.release
	return g.Fake.NewSession(ctx, opts)
}

// TestBoardOpenIsSerializedUnderConcurrency pins the fix for a
// check-then-act race in OpenBoard. It used to take e.mu only to test
// e.board for nil, release it, and then do the whole expensive spawn —
// resolving the role, starting a real backend process, possibly binding
// an MCP endpoint — before re-taking the lock to install itself. Two
// callers arriving together therefore both saw "no board yet" and both
// spawned one; the second to finish installed itself and stopped the
// first, whose caller had already been handed it. That caller was left
// holding a live-looking BoardSession whose backend was closed
// underneath it, so its next Send failed for no reason it could see.
//
// The assertion that actually discriminates is the SECOND entry into
// NewSession: b1 == b2 can hold by luck even with the race present, and
// a spawn count read after the fact cannot tell "never happened" from
// "happened and was cleaned up". Waiting for a second caller to reach
// the backend is the direct observation of the bug.
func TestBoardOpenIsSerializedUnderConcurrency(t *testing.T) {
	g := newGatedOpener()
	e := newEngine(t, g)

	const callers = 8
	var wg sync.WaitGroup
	got := make([]*BoardSession, callers)
	errs := make([]error, callers)
	start := make(chan struct{})
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // release them together, so they actually overlap
			got[i], errs[i] = e.OpenBoard(context.Background(), BoardOpts{})
		}()
	}
	close(start)

	// One caller must reach the backend; that is the whole point.
	select {
	case <-g.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no caller ever reached the backend")
	}
	// No SECOND caller may. Unserialized, the other seven pile in here
	// while the first is still held inside NewSession — which is exactly
	// the window the old code left open.
	select {
	case <-g.entered:
		close(g.release)
		wg.Wait()
		t.Fatal("a second caller reached the backend: OpenBoard is not serialized")
	case <-time.After(500 * time.Millisecond):
	}
	close(g.release)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: %v", i, err)
		}
	}
	for i, b := range got {
		if b != got[0] {
			t.Errorf("caller %d got a different board session; every caller must share one", i)
		}
	}
	// the survivor must actually be usable: the failure this guards
	// against handed a caller a session that had already been stopped.
	if err := got[0].Send(context.Background(), "still alive?"); err != nil {
		t.Fatalf("the shared board session is not live: %v", err)
	}
}

// TestBoardSurfacesBackendBudgetExhausted proves a backend-reported
// credit cap reaches the transcript instead of vanishing.
//
// A board session has no gummi envelope, and the pump's default arm used
// to swallow agent.EventBudgetExhausted on exactly that reasoning. But
// the event does not report gummi's envelope — it reports the BACKEND's
// own cap, which copilot really does emit
// (SessionLimitsExhaustedRequestedData), and copilot is a board backend.
// Swallowed, the conversation just stopped answering with nothing on
// screen explaining why.
func TestBoardSurfacesBackendBudgetExhausted(t *testing.T) {
	f := agent.NewFake("hi")
	e := newEngine(t, f)

	b, err := e.OpenBoard(context.Background(), BoardOpts{})
	if err != nil {
		t.Fatal(err)
	}
	e.handleBoard(b, agent.Event{Kind: agent.EventBudgetExhausted})

	snap := b.Snapshot()
	if snap.Busy {
		t.Error("busy is still set after the cap was reported; the surface would spin forever")
	}
	var found bool
	for _, msg := range snap.Transcript {
		if msg.Author == AuthorSystem && strings.Contains(msg.Content, "credit cap") {
			found = true
		}
	}
	if !found {
		t.Errorf("no note about the backend's cap in the transcript: %+v", snap.Transcript)
	}
}

// TestBoardRolePairsModelAndBackend reproduces the failure this fix
// exists for, in the shape a real workspace produced it: profiles.yaml
// declaring the four stage roles and no board role, a default model of
// "gpt-5" left over from the single-model config, and claude as the
// default agent. resolveRole's fallback answered with the model from one
// source and, via agentFor(""), the backend from another, so the agent
// tab opened onto "claude backend cannot drive model \"gpt-5\"".
//
// The assertion is on the pairing, not on any particular model: whatever
// the board session ends up asking for, the backend chosen alongside it
// has to be one that can drive it.
func TestBoardRolePairsModelAndBackend(t *testing.T) {
	e := newEngine(t, agent.NewFake("hi"))
	// the single-model default, as a workspace that predates profiles
	// carries it — deliberately a model the profile's backend cannot run.
	e.cfg.Model = "gpt-5"
	e.cfg.Profiles = config.Profiles{
		Default: "claude",
		Profiles: map[string]config.Profile{
			"claude": {
				"architect":   {Backend: "claude", Model: "claude-sonnet-5"},
				"implementer": {Backend: "claude", Model: "claude-sonnet-5"},
			},
		},
	}

	rc, backend := e.resolveBoardRole("")
	if backend != "claude" {
		t.Errorf("backend = %q, want claude (borrowed from the architect role)", backend)
	}
	if rc.Model != "claude-sonnet-5" {
		t.Errorf("model = %q, want claude-sonnet-5 — the architect's, paired with its backend; "+
			"the bare default model would be gpt-5, which that backend refuses", rc.Model)
	}
}

// TestBoardRoleWithNoProfileNamesNoModel: with nothing declared at all,
// the board session must name no model rather than the engine's default
// one. An empty Model lets the backend's own CLI pick something it can
// actually drive; naming e.cfg.Model here is what produced the mismatch
// TestBoardRolePairsModelAndBackend covers.
func TestBoardRoleWithNoProfileNamesNoModel(t *testing.T) {
	e := newEngine(t, agent.NewFake("hi"))
	e.cfg.Model = "gpt-5"
	e.cfg.Profiles = config.Profiles{}

	rc, backend := e.resolveBoardRole("")
	if rc.Model != "" {
		t.Errorf("model = %q, want empty so the backend picks its own", rc.Model)
	}
	if backend != "" {
		t.Errorf("backend = %q, want empty (the engine's default)", backend)
	}
}

// TestBoardOpenAppliesModelOverride: BoardOpts.Model overrides the
// resolved role's model in the spawn path, applied right after
// resolveBoardRole returns — resolveBoardRole itself must stay a pure
// profile→role lookup (see spawnBoardLocked's comment on why), so the
// override has to be visible on the SessionOpts the adapter actually
// received, not just on some intermediate value this test can't reach.
func TestBoardOpenAppliesModelOverride(t *testing.T) {
	r := &recorder{Fake: agent.NewFake("hi")}
	e := newEngine(t, r)
	e.cfg.Profiles = config.Profiles{
		Default: "p",
		Profiles: map[string]config.Profile{
			"p": {"architect": {Model: "profile-model"}},
		},
	}

	if _, err := e.OpenBoard(context.Background(), BoardOpts{Model: "override-model"}); err != nil {
		t.Fatal(err)
	}
	if got := r.opts().Model; got != "override-model" {
		t.Errorf("Model = %q, want the override", got)
	}
}

// TestBoardOpenEmptyModelLeavesProfileAlone: an empty BoardOpts.Model is
// "use the profile's", not "use nothing" — the zero value must not
// stomp a model the profile actually named.
func TestBoardOpenEmptyModelLeavesProfileAlone(t *testing.T) {
	r := &recorder{Fake: agent.NewFake("hi")}
	e := newEngine(t, r)
	e.cfg.Profiles = config.Profiles{
		Default: "p",
		Profiles: map[string]config.Profile{
			"p": {"architect": {Model: "profile-model"}},
		},
	}

	if _, err := e.OpenBoard(context.Background(), BoardOpts{}); err != nil {
		t.Fatal(err)
	}
	if got := r.opts().Model; got != "profile-model" {
		t.Errorf("Model = %q, want the profile's own model, unmodified", got)
	}
}

// TestBoardSessionProfileRecordsSpawnOpts pins BoardSession.Profile() to
// the name it was actually opened with, so the UI's picker can mark the
// live entry.
func TestBoardSessionProfileRecordsSpawnOpts(t *testing.T) {
	e := newEngine(t, agent.NewFake("hi"))
	b, err := e.OpenBoard(context.Background(), BoardOpts{Profile: "premium"})
	if err != nil {
		t.Fatal(err)
	}
	if got := b.Profile(); got != "premium" {
		t.Errorf("Profile() = %q, want premium", got)
	}
}

// TestReopenBoardReplacesLiveSession: ReopenBoard on a live board session
// returns a different *BoardSession (never the prior one, unlike
// OpenBoard) and the prior session is stopped — a further Send on it
// fails rather than quietly reaching an orphaned backend.
func TestReopenBoardReplacesLiveSession(t *testing.T) {
	r := &recorder{Fake: agent.NewFake("hi")}
	e := newEngine(t, r)
	ctx := context.Background()

	b1, err := e.OpenBoard(ctx, BoardOpts{})
	if err != nil {
		t.Fatal(err)
	}
	b2, err := e.ReopenBoard(ctx, BoardOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if b2 == b1 {
		t.Error("ReopenBoard returned the prior session; it must always spawn fresh")
	}
	if !b1.sess.finalizedState() {
		t.Error("the prior board session was not stopped by ReopenBoard")
	}
	if err := b1.Send(ctx, "still there?"); err == nil {
		t.Error("Send on the replaced session succeeded; its backend should be closed")
	}
	if n := r.count(); n != 2 {
		t.Errorf("agent NewSession called %d times, want 2 (one per spawn)", n)
	}
	// the new session is the one the engine now hands out.
	b3, err := e.OpenBoard(ctx, BoardOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if b3 != b2 {
		t.Error("OpenBoard after ReopenBoard did not reuse the fresh session")
	}
}

// TestReopenBoardWithNoPriorSessionJustOpens: ReopenBoard on an engine
// with no board session yet behaves like a plain OpenBoard rather than
// erroring on "nothing to close".
func TestReopenBoardWithNoPriorSessionJustOpens(t *testing.T) {
	r := &recorder{Fake: agent.NewFake("hi")}
	e := newEngine(t, r)

	b, err := e.ReopenBoard(context.Background(), BoardOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if b == nil {
		t.Fatal("nil board session")
	}
	if n := r.count(); n != 1 {
		t.Errorf("agent NewSession called %d times, want 1", n)
	}
}

// TestBoardRolePrefersADeclaredBoardRole: a profile that does declare
// one wins over the architect fallback, so an operator can point the
// board tab at a cheaper model than the one that plans cards.
func TestBoardRolePrefersADeclaredBoardRole(t *testing.T) {
	e := newEngine(t, agent.NewFake("hi"))
	e.cfg.Profiles = config.Profiles{
		Default: "p",
		Profiles: map[string]config.Profile{
			"p": {
				"architect": {Backend: "claude", Model: "claude-sonnet-5"},
				"board":     {Backend: "claude", Model: "claude-haiku-4-5-20251001"},
			},
		},
	}
	rc, backend := e.resolveBoardRole("")
	if rc.Model != "claude-haiku-4-5-20251001" || backend != "claude" {
		t.Errorf("got %q/%q, want the declared board role", backend, rc.Model)
	}
}

// TestEngineBoardTracksTheLiveSession pins the accessor the UI's
// stale-handle guard is built on (sendBoardMessage): it must report the
// session the engine would actually deliver to right now, so a handle
// captured before a ReopenBoard can be recognised as no longer current.
// A guard reading anything staler than this would pass exactly when it
// most needs to fail.
func TestEngineBoardTracksTheLiveSession(t *testing.T) {
	e := newEngine(t, agent.NewFake("hi"))
	ctx := context.Background()

	if got := e.Board(); got != nil {
		t.Errorf("Board() = %v before any open, want nil", got)
	}
	b1, err := e.OpenBoard(ctx, BoardOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if e.Board() != b1 {
		t.Error("Board() did not report the session OpenBoard installed")
	}
	b2, err := e.ReopenBoard(ctx, BoardOpts{Profile: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if e.Board() != b2 {
		t.Error("Board() still reports the replaced session after a reopen")
	}
	if e.Board() == b1 {
		t.Error("the stale handle is indistinguishable from the live one; the UI guard cannot work")
	}
}
