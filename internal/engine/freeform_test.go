package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// freeformCard builds a freeform work item (FF-NNN) at the one stage such
// a card ever holds, with an envelope, since the envelope is the single
// floor a freeform card keeps.
func freeformCard(num int, title string) domain.Feature {
	id, _ := domain.NewID(domain.KindFreeform, num)
	slug, _ := domain.Slugify(title)
	now := time.Now()
	return domain.Feature{
		ID: id, Num: num, Kind: domain.KindFreeform, Title: title, Slug: slug,
		Stage: domain.StageOpen, Budget: domain.Budget{Envelope: 400},
		BranchScheme: domain.BranchSchemeKind,
		CreatedAt:    now, UpdatedAt: now,
	}
}

func waitFreeformIdle(t *testing.T, ff *FreeformSession) {
	t.Helper()
	deadline := time.After(testWaitTimeout)
	for {
		if !ff.Snapshot().Busy {
			return
		}
		select {
		case <-deadline:
			t.Fatal("freeform session never went idle")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// TestAFreeformTurnDoesNotCommit: every commit on a freeform card's branch
// is one somebody meant. A turn ending is not a reason to commit, so what
// the turn wrote is in the card's worktree, on its branch's checkout, and
// still uncommitted.
func TestAFreeformTurnDoesNotCommit(t *testing.T) {
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, _ string) []agent.Event {
		// write into the session's own cwd, which must be the card's
		// worktree — the freeform card's "gets its worktree/branch" half
		if err := os.WriteFile(filepath.Join(opts.WorkDir, "sketch.txt"), []byte("hand-rolled\n"), 0o600); err != nil {
			t.Error(err)
		}
		return []agent.Event{{Kind: agent.EventMessage, Text: "wrote it"}, {Kind: agent.EventIdle}}
	}}
	ag.Caps = agent.Capabilities{UsageEvents: true, Interrupt: true}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
	t.Cleanup(func() { e.Close() })
	ctx := context.Background()

	f := freeformCard(1, "poke at the pty leak")
	createFeature(t, store, f)

	ff, err := e.OpenFreeform(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := ff.Send(ctx, "drop the leaked fd"); err != nil {
		t.Fatal(err)
	}
	waitFreeformIdle(t, ff)

	tree := filepath.Join(ws.Root, f.WorktreePath())
	if _, err := os.Stat(filepath.Join(tree, "sketch.txt")); err != nil {
		t.Fatalf("the turn's file is not in the card's worktree: %v", err)
	}
	if got := strings.TrimSpace(gitOut(t, tree, "rev-parse", "--abbrev-ref", "HEAD")); got != f.BranchName() {
		t.Errorf("worktree is on %s, want the card's branch %s", got, f.BranchName())
	}
	if out := gitOut(t, tree, "status", "--porcelain"); !strings.Contains(out, "sketch.txt") {
		t.Errorf("the turn's file was committed, want it left in the worktree:\n%s", out)
	}
	if log := gitOut(t, tree, "log", "--oneline", "--no-decorate", "--grep=checkpoint"); strings.TrimSpace(log) != "" {
		t.Errorf("a checkpoint was committed after the turn:\n%s", log)
	}
}

// TestAFreeformSessionGetsNoArtifact: a freeform card has no document, so
// the session must not be handed one — a backend told where its artifact
// is would go and look for a file that does not exist.
func TestAFreeformSessionGetsNoArtifact(t *testing.T) {
	r := recordingAgent()
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(r), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
	t.Cleanup(func() { e.Close() })
	ctx := context.Background()

	f := freeformCard(3, "no document here")
	createFeature(t, store, f)
	if _, err := e.OpenFreeform(ctx, f); err != nil {
		t.Fatal(err)
	}
	opts := r.opts()
	if opts.ArtifactPath != "" {
		t.Errorf("freeform session was given an artifact at %q", opts.ArtifactPath)
	}
	if want := filepath.Join(ws.Root, f.WorktreePath()); opts.WorkDir != want {
		t.Errorf("WorkDir = %q, want the card's worktree %q", opts.WorkDir, want)
	}
	// The envelope is the one floor it keeps, so the cap must be real.
	if opts.MaxCredits <= 0 {
		t.Errorf("MaxCredits = %v, want the card's envelope enforced", opts.MaxCredits)
	}
	// No hint may name an artifact path: there is no file to name, and a
	// session pointed at one would go looking for it.
	for _, h := range opts.SystemHints {
		if strings.Contains(h, ".gummi/specs") || strings.Contains(h, f.SpecPath()) {
			t.Errorf("a freeform hint points at an artifact:\n%s", h)
		}
	}
}

// TestTheFreeformContractPromisesNoCeremony pins what the card's own hint
// says, and mostly what it does not: a session told to emit a verdict, fill
// a section or wait at a gate would invent the ceremony this kind exists to
// do without.
func TestTheFreeformContractPromisesNoCeremony(t *testing.T) {
	f := freeformCard(11, "no ceremony")
	hint := freeformContractHint(f, "/tmp/wt")
	// The ceremony is named only to be denied — an agent that has run
	// gummi's stages before will assume a document and a verdict unless it
	// is told otherwise, so silence is not the same as absence here.
	for _, want := range []string{"no design document", "no plan to write", "no verdict to emit"} {
		if !strings.Contains(hint, want) {
			t.Errorf("the freeform contract never says %q:\n%s", want, hint)
		}
	}
	// What it must never carry is the verdict grammar itself: a session
	// shown "VERDICT: pass" will emit one, and nothing here parses it.
	if strings.Contains(hint, "VERDICT:") {
		t.Errorf("the freeform contract shows the verdict grammar:\n%s", hint)
	}
	for _, want := range []string{string(f.ID), f.BranchName(), "/tmp/wt", "committed"} {
		if !strings.Contains(hint, want) {
			t.Errorf("the freeform contract never names %q — the session has no other way to learn it:\n%s", want, hint)
		}
	}
}

// TestOpenFreeformIsIdempotentPerCard: one session per card, so a second
// caller (the diff surface sending comments while the card page is open)
// reaches the session that holds the worktree rather than spawning a
// second writer into it.
func TestOpenFreeformIsIdempotentPerCard(t *testing.T) {
	r := recordingAgent()
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(r), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
	t.Cleanup(func() { e.Close() })
	ctx := context.Background()

	f := freeformCard(4, "one session only")
	createFeature(t, store, f)
	a, err := e.OpenFreeform(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.OpenFreeform(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("OpenFreeform returned a second session for the same card")
	}
	if r.count() != 1 {
		t.Errorf("backend spawned %d times, want 1", r.count())
	}
	if got := e.Freeform(f.ID); got != a {
		t.Errorf("Freeform(id) = %p, want %p", got, a)
	}
}

// TestAFreeformSendTurnRefusedImagesNotRecorded asserts that a freeform
// turn's images, refused only once the send actually reaches the backend
// (a live per-model check checkImageCapable's structural gate cannot see
// — simulated via Fake.RefuseImages), are never durably recorded: not in
// the persisted session row a restart would restore from, and not as an
// echo in the session's own live transcript.
func TestAFreeformSendTurnRefusedImagesNotRecorded(t *testing.T) {
	ctx := context.Background()
	ws, store, wt := newRepo(t)
	ag := agent.NewFake("ack")
	ag.Caps.Images = true
	ag.RefuseImages = true
	unregister := agent.RegisterCapabilities("fake", ag.Caps)
	defer unregister()
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", Persist: true})
	t.Cleanup(func() { e.Close() })

	f := freeformCard(4, "image refusal")
	createFeature(t, store, f)
	ff, err := e.OpenFreeform(ctx, f)
	if err != nil {
		t.Fatal(err)
	}

	ref := putTestImage(t, e)
	before := len(ff.Snapshot().Transcript)
	err = ff.SendTurn(ctx, "look at this", []AttachmentRef{ref})
	if !errors.Is(err, agent.ErrImagesUnsupported) {
		t.Fatalf("err = %v, want ErrImagesUnsupported", err)
	}
	if after := len(ff.Snapshot().Transcript); after != before {
		t.Fatalf("refused turn changed the freeform transcript: %d -> %d entries", before, after)
	}

	snaps, err := store.LoadSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, snap := range snaps {
		for _, m := range snap.Transcript {
			if m.Content == "look at this" {
				t.Fatalf("refused turn was persisted: %+v", m)
			}
		}
	}
}

// TestAFreeformSessionRefusesACardInTheWorkflow: the session has no
// stage, no verdict and no gate, so opening one on a feature would be a
// second way to run that card which skips all three.
func TestAFreeformSessionRefusesACardInTheWorkflow(t *testing.T) {
	r := recordingAgent()
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(r), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
	t.Cleanup(func() { e.Close() })

	f := feature(9, "a real feature", domain.StageImplement)
	createFeature(t, store, f)
	if _, err := e.OpenFreeform(context.Background(), f); err == nil {
		t.Fatal("OpenFreeform accepted a feature card")
	}
	if r.count() != 0 {
		t.Errorf("a backend was spawned for a refused card (%d sessions)", r.count())
	}
}

// TestAFreeformCardHoldsItsCardLock: the hold spans the conversation, not
// one turn — between two turns the worktree holds uncommitted work and the
// branch holds commits nothing has reviewed, and a landing arriving in
// that window is what the lock excludes. Dropping it is Close's job.
func TestAFreeformCardHoldsItsCardLock(t *testing.T) {
	r := recordingAgent()
	ws, store, wt := newRepo(t)
	e := New(Config{
		Agents: singleAgent(r), Store: store, Worktrees: wt, Workspace: ws, Model: "m",
		CardLocks: state.NewCardLocks(ws),
	})
	t.Cleanup(func() { e.Close() })
	ctx := context.Background()

	f := freeformCard(5, "locked while open")
	createFeature(t, store, f)
	ff, err := e.OpenFreeform(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	// A second holder outside this process's refcounted pool is excluded
	// while the session lives.
	if release, err := state.AcquireLock(ws.CardLockFile(f.ID)); err == nil {
		release()
		t.Fatal("the card lock was free while its freeform session was open")
	}
	if err := ff.Close(); err != nil {
		t.Fatal(err)
	}
	release, err := state.AcquireLock(ws.CardLockFile(f.ID))
	if err != nil {
		t.Fatalf("the card lock was still held after Close: %v", err)
	}
	release()
}

// TestAFreeformCardsToolSurfaceIsResolveAnnotationOnly pins what a
// freeform session may reach for: the review loop's resolve tool, and
// nothing else. No spec tools (there is no artifact) and no ask_user (the
// person is in the thread, so a reply is the answer).
func TestAFreeformCardsToolSurfaceIsResolveAnnotationOnly(t *testing.T) {
	tools := stageTools(domain.StageOpen, flavorStage, nil)
	if len(tools) != 1 || tools[0].Name != resolveToolName {
		var names []string
		for _, td := range tools {
			names = append(names, td.Name)
		}
		t.Fatalf("freeform tools = %v, want just %s", names, resolveToolName)
	}
}

// TestAFreeformCardResolvesItsDiffComments is the review loop the whole
// kind leans on: the reader's comments arrive as a turn, the agent marks
// each addressed through the same resolve_annotation tool a stage uses, and
// the open count burns down live. A freeform card is the simplest consumer
// of that machinery — one session, always the writer — and this asserts it
// is genuinely the same machinery rather than a second copy.
func TestAFreeformCardResolvesItsDiffComments(t *testing.T) {
	var resolved atomic.Int64
	ag := &agent.Fake{Responder: func(_ agent.SessionOpts, msg string) []agent.Event {
		// the compiled turn carries each comment's [id]; answer the first
		if id := resolved.Add(1); id == 1 {
			return []agent.Event{{Kind: agent.EventClientToolCall, ToolCall: &agent.ToolCall{
				ID: "call-1", Name: resolveToolName, Args: json.RawMessage(`{"id": 1}`),
			}}, {Kind: agent.EventMessage, Text: "fixed the error path"}, {Kind: agent.EventIdle}}
		}
		return []agent.Event{{Kind: agent.EventMessage, Text: "nothing else"}, {Kind: agent.EventIdle}}
	}}
	ag.Caps = agent.Capabilities{ClientTools: true, UsageEvents: true, Interrupt: true}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
	t.Cleanup(func() { e.Close() })
	ctx := context.Background()

	f := freeformCard(6, "review me")
	createFeature(t, store, f)
	annID, err := store.AddDiffAnnotation(ctx, domain.DiffAnnotation{
		Feature: f.ID, File: "sketch.txt", Anchor: "a1",
		Excerpt: "hand-rolled", Comment: "this leaks on the error path too",
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	ff, err := e.OpenFreeform(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	anns, err := store.ListDiffAnnotations(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	turn := CompileDiffComments(anns, true)
	if !strings.Contains(turn, "this leaks on the error path too") {
		t.Fatalf("the compiled turn does not carry the comment:\n%s", turn)
	}
	if err := ff.Send(ctx, turn); err != nil {
		t.Fatal(err)
	}
	waitFreeformIdle(t, ff)

	got, err := store.ListDiffAnnotations(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range got {
		if a.ID == annID && !a.Resolved {
			t.Errorf("comment [%d] is still open after the agent resolved it", a.ID)
		}
	}
}

// TestAFreeformSessionInheritsOpenComments: a conversation that idled out
// between "request changes" and the fix must not lose the request. The
// comments are in the store, so every backend this card spawns reads them
// in its opening hints — the same path a cold stage takes.
func TestAFreeformSessionInheritsOpenComments(t *testing.T) {
	r := recordingAgent()
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(r), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
	t.Cleanup(func() { e.Close() })
	ctx := context.Background()

	f := freeformCard(7, "inherit my comments")
	createFeature(t, store, f)
	if _, err := store.AddDiffAnnotation(ctx, domain.DiffAnnotation{
		Feature: f.ID, File: "sketch.txt", Anchor: "a1",
		Excerpt: "hand-rolled", Comment: "name this something else",
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.OpenFreeform(ctx, f); err != nil {
		t.Fatal(err)
	}
	hints := strings.Join(r.opts().SystemHints, "\n")
	if !strings.Contains(hints, "name this something else") {
		t.Errorf("a fresh freeform backend did not inherit the open comment:\n%s", hints)
	}
}

// TestAFreeformCardCarriesOnAfterATopUp: the envelope is the one floor a
// freeform card keeps, so running into it must be a stop rather than an
// ending. The exhaustion latch lives on the backend, so a topped-up card
// gets a fresh one — otherwise "raise the envelope to carry on" would be a
// sentence with nothing behind it.
func TestAFreeformCardCarriesOnAfterATopUp(t *testing.T) {
	ag := agent.NewFake("ok")
	ag.Caps = agent.Capabilities{UsageEvents: true, Interrupt: true}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
	t.Cleanup(func() { e.Close() })
	ctx := context.Background()

	f := freeformCard(8, "spend it all")
	createFeature(t, store, f)
	ff, err := e.OpenFreeform(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	// Stand in for the spend: latch the backend exhausted the way an
	// over-budget usage event would.
	ff.mu.Lock()
	spent := ff.sess
	ff.mu.Unlock()
	if !spent.markExhausted() {
		t.Fatal("precondition: the session was already exhausted")
	}

	// With nothing left, the turn is refused and says what would unblock it.
	if err := store.AddSpend(ctx, f.ID, 500, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	setEnvelope(t, store, f.ID, 100)
	err = ff.Send(ctx, "carry on")
	if err == nil {
		t.Fatal("an exhausted freeform card took another turn with nothing left")
	}
	if !strings.Contains(err.Error(), "raise it") {
		t.Errorf("the refusal does not say what would unblock it: %v", err)
	}

	// Topped up, the same card carries on — on a fresh backend that keeps
	// the conversation.
	setEnvelope(t, store, f.ID, 4000)
	if err := ff.Send(ctx, "carry on"); err != nil {
		t.Fatalf("a topped-up freeform card still refuses turns: %v", err)
	}
	waitFreeformIdle(t, ff)
	ff.mu.Lock()
	fresh := ff.sess
	ff.mu.Unlock()
	if fresh == spent {
		t.Error("the exhausted backend was reused; its cap is still the spent one")
	}
	var saw bool
	for _, m := range fresh.Snapshot().Transcript {
		if strings.Contains(m.Content, "carry on") {
			saw = true
		}
	}
	if !saw {
		t.Error("the respawned backend did not carry the conversation")
	}
}

// setEnvelope writes a card's envelope straight to the store, standing in
// for a top-up at the board without RaiseEnvelope's floor arithmetic (the
// point here is a spent envelope, which that floor exists to prevent).
func setEnvelope(t *testing.T, store *state.Store, id domain.FeatureID, to int) {
	t.Helper()
	ctx := context.Background()
	f, err := store.GetFeature(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	f.Budget.Envelope = to
	if err := store.UpdateFeature(ctx, &f); err != nil {
		t.Fatal(err)
	}
}

// TestAFreeformSessionIsNotToldTheWorkflowGovernsIt guards a contradiction
// the pty drive found: the repo-precedence hint every stage session gets
// asserts that gummi governs "the stage, the gates" and that "the workflow
// wins", and a freeform card has none of those — so on such a card that
// paragraph contradicts, in the same prompt, its own contract saying there
// is no gate and no verdict. What must survive is the half that is true of
// every card: never land it yourself, never run a second gummi.
func TestAFreeformSessionIsNotToldTheWorkflowGovernsIt(t *testing.T) {
	r := recordingAgent()
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(r), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
	t.Cleanup(func() { e.Close() })

	f := freeformCard(9, "no contradictions")
	createFeature(t, store, f)
	if _, err := e.OpenFreeform(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	hints := strings.Join(r.opts().SystemHints, "\n")
	for _, forbidden := range []string{"the workflow wins", "the stage, the gates", "skip review for small changes"} {
		if strings.Contains(hints, forbidden) {
			t.Errorf("a freeform session is told %q, which is not true of it:\n%s", forbidden, hints)
		}
	}
	for _, want := range []string{"never merge or land this branch yourself", "never run another\ngummi"} {
		if !strings.Contains(hints, want) {
			t.Errorf("the freeform precedence hint drops %q, which holds on every card", want)
		}
	}
}

// TestInterruptingAFreeformTurnCommitsWhatItWrote: a turn a reader stops
// has still written whatever it wrote before being stopped, and on a
// freeform card nothing else will commit it — the idle that normally does
// never arrives for a turn the backend abandoned. The pty drive found the
// stop unreachable at all; this pins both halves of it.
func TestInterruptingAFreeformTurnCommitsWhatItWrote(t *testing.T) {
	var interrupted atomic.Bool
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, _ string) []agent.Event {
		if err := os.WriteFile(filepath.Join(opts.WorkDir, "half.txt"), []byte("half\n"), 0o600); err != nil {
			t.Error(err)
		}
		// No idle: the turn is still in flight when the reader stops it.
		return []agent.Event{{Kind: agent.EventTextDelta, Text: "working"}}
	}}
	ag.Caps = agent.Capabilities{UsageEvents: true, Interrupt: true}
	ag.OnInterrupt = func() { interrupted.Store(true) }
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
	t.Cleanup(func() { e.Close() })
	ctx := context.Background()

	f := freeformCard(10, "stop me")
	createFeature(t, store, f)
	ff, err := e.OpenFreeform(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := ff.Send(ctx, "go slowly"); err != nil {
		t.Fatal(err)
	}
	if !ff.Snapshot().Busy {
		t.Fatal("precondition: the session is not busy")
	}
	// The stand-in writes on its own goroutine, so wait for the turn to
	// have actually written something before stopping it — otherwise the
	// test asserts on a file that did not exist yet, and only loses that
	// race under load.
	tree := filepath.Join(ws.Root, f.WorktreePath())
	waitForFile(t, filepath.Join(tree, "half.txt"))
	if err := e.InterruptFreeform(ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	if !interrupted.Load() {
		t.Error("the backend was never interrupted")
	}
	if ff.Snapshot().Busy {
		t.Error("the session still reports itself busy after the stop")
	}
	if out := gitOut(t, tree, "status", "--porcelain"); !strings.Contains(out, "half.txt") {
		t.Errorf("the interrupted turn's work was committed, want it left in the worktree:\n%s", out)
	}
	// And the conversation survives the stop: the backend is kept, so the
	// next turn continues rather than starting a new session.
	if err := ff.Send(ctx, "carry on"); err != nil {
		t.Errorf("the session refuses turns after a stop: %v", err)
	}
}

// TestAFreeformCardsWorktreeIsLeftAloneOnShutdown: a board quitting
// mid-turn commits nothing on a freeform card's behalf. What the turn wrote
// stays in the worktree, uncommitted, for somebody to commit on purpose.
func TestAFreeformCardsWorktreeIsLeftAloneOnShutdown(t *testing.T) {
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, _ string) []agent.Event {
		if err := os.WriteFile(filepath.Join(opts.WorkDir, "inflight.txt"), []byte("x\n"), 0o600); err != nil {
			t.Error(err)
		}
		return []agent.Event{{Kind: agent.EventTextDelta, Text: "still going"}}
	}}
	ag.Caps = agent.Capabilities{UsageEvents: true, Interrupt: true}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
	ctx := context.Background()

	f := freeformCard(14, "quit on me")
	createFeature(t, store, f)
	ff, err := e.OpenFreeform(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := ff.Send(ctx, "go slowly"); err != nil {
		t.Fatal(err)
	}
	tree := filepath.Join(ws.Root, f.WorktreePath())
	waitForFile(t, filepath.Join(tree, "inflight.txt"))
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if out := gitOut(t, tree, "status", "--porcelain"); !strings.Contains(out, "inflight.txt") {
		t.Errorf("the shutdown committed the in-flight turn's work, want it left in the worktree:\n%s", out)
	}
}

// waitForFile polls until path exists. The stand-in agents write on their
// own goroutines, so a test that asserts what a turn left behind has to
// wait for the turn to have left it — a check that only fails under load
// is worse than no check.
func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.After(testWaitTimeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("%s never appeared", path)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// TestAFreeformConversationSurvivesARestart is the requirement this
// persistence exists for: a person who comes back finds the conversation
// they left, and — the half that matters more — the agent continues with
// it rather than answering as though nothing had been said.
//
// It drives two engines over one store, which is what a board restart is.
func TestAFreeformConversationSurvivesARestart(t *testing.T) {
	ws, store, wt := newRepo(t)
	ctx := context.Background()
	f := freeformCard(20, "remember me")
	createFeature(t, store, f)

	// --- the board that does the work
	first := agent.NewFake("Added pty.go — the fd is still leaked on the error path.")
	first.Caps = agent.Capabilities{UsageEvents: true, Interrupt: true}
	e1 := New(Config{
		Agents: singleAgent(first), Store: store, Worktrees: wt, Workspace: ws,
		Model: "m", Persist: true,
	})
	ff, err := e1.OpenFreeform(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := ff.Send(ctx, "drop the leaked pty fd"); err != nil {
		t.Fatal(err)
	}
	waitFreeformIdle(t, ff)
	if err := e1.Close(); err != nil {
		t.Fatal(err)
	}

	// --- the board that comes back
	second := recordingAgent()
	second.Caps = agent.Capabilities{UsageEvents: true, Interrupt: true} // no Resume: a BYOK backend
	e2 := New(Config{
		Agents: singleAgent(second), Store: store, Worktrees: wt, Workspace: ws,
		Model: "m", Persist: true,
	})
	t.Cleanup(func() { e2.Close() })
	if err := e2.Restore(ctx); err != nil {
		t.Fatal(err)
	}

	// The session is back, with the conversation, and nothing was spawned
	// to get it: a board that opens on ten freeform cards must not start
	// ten agents.
	back := e2.Freeform(f.ID)
	if back == nil {
		t.Fatal("the freeform session did not survive the restart")
	}
	if second.count() != 0 {
		t.Errorf("restoring spawned %d backend(s); it must wait for a turn", second.count())
	}
	said := transcriptText(back.Snapshot())
	for _, want := range []string{"drop the leaked pty fd", "Added pty.go"} {
		if !strings.Contains(said, want) {
			t.Errorf("the restored conversation is missing %q:\n%s", want, said)
		}
	}
	// The card lock is free while no backend runs, so a landing from
	// elsewhere is not blocked by a board that merely has the card open.
	if release, err := state.AcquireLock(ws.CardLockFile(f.ID)); err != nil {
		t.Errorf("a restored session with no backend still holds the card lock: %v", err)
	} else {
		release()
	}

	// And the next turn carries the context to the model. This backend
	// cannot resume its own conversation, so the transcript is replayed
	// into its prompt — without that the thread would show a history the
	// model does not share.
	if err := back.Send(ctx, "now fix it"); err != nil {
		t.Fatal(err)
	}
	waitFreeformIdle(t, back)
	hints := strings.Join(second.opts().SystemHints, "\n")
	if !strings.Contains(hints, "conversation so far") {
		t.Errorf("the respawned backend was given no conversation:\n%s", hints)
	}
	for _, want := range []string{"drop the leaked pty fd", "Added pty.go"} {
		if !strings.Contains(hints, want) {
			t.Errorf("the replayed conversation is missing %q:\n%s", want, hints)
		}
	}
}

// TestAResumingBackendIsNotAlsoHandedTheReplay: a backend that continues
// its OWN conversation already has these turns; replaying them on top
// would state everything twice, once as history and once as its own
// memory, and pay for the larger prompt every turn after a restart.
func TestAResumingBackendIsNotAlsoHandedTheReplay(t *testing.T) {
	seed := []Message{
		{Author: AuthorUser, Content: "drop the leaked pty fd"},
		{Author: AuthorAssistant, Content: "Added pty.go."},
	}
	if got := freeformReplayHint(seed, true); got != "" {
		t.Errorf("a resuming backend was handed a replay:\n%s", got)
	}
	got := freeformReplayHint(seed, false)
	if !strings.Contains(got, "them: drop the leaked pty fd") || !strings.Contains(got, "you: Added pty.go.") {
		t.Errorf("the replay does not read as the session's own conversation:\n%s", got)
	}
	if freeformReplayHint(nil, false) != "" {
		t.Error("an empty conversation produced a replay hint")
	}
}

// TestTheReplayKeepsTheNewestTurnsWithinItsBudget: a long card can hold
// hundreds of turns, and a replay that grows without limit makes every
// turn after a restart the most expensive one of the card. What survives
// the budget is the newest end — the work in front of the person — and the
// reader is told the rest was dropped rather than left to assume it was
// all there.
func TestTheReplayKeepsTheNewestTurnsWithinItsBudget(t *testing.T) {
	var seed []Message
	for i := range 200 {
		seed = append(seed,
			Message{Author: AuthorUser, Content: fmt.Sprintf("turn %d: %s", i, strings.Repeat("x", 80))},
			Message{Author: AuthorAssistant, Content: fmt.Sprintf("done %d", i)})
	}
	got := freeformReplayHint(seed, false)
	if len(got) > freeformReplayBudget*2 {
		t.Errorf("the replay is %d bytes for a %d-byte budget", len(got), freeformReplayBudget)
	}
	if !strings.Contains(got, "done 199") {
		t.Errorf("the newest turn was dropped:\n%s", got[:min(len(got), 400)])
	}
	if strings.Contains(got, "turn 0:") {
		t.Error("the oldest turn survived a budget that should have dropped it")
	}
	if !strings.Contains(got, "earlier turns are not replayed") {
		t.Error("the replay does not say that it is partial")
	}
}

// transcriptText flattens a snapshot's transcript for assertions.
func transcriptText(snap Snapshot) string {
	var b strings.Builder
	for _, m := range snap.Transcript {
		b.WriteString(string(m.Author) + ": " + m.Content + "\n")
	}
	return b.String()
}

// TestTheReplayCarriesWhatWasSaidAndNotWhatGummiDid: the transcript keeps
// tool lines for the reader, and two kinds live there — the backend's own
// calls and gummi's own activity notes. A restored row cannot tell them
// apart, and the pty drive caught the consequence: "you ran: worktree
// committed", which the session did not do and which contradicts the
// contract telling it gummi commits for it.
func TestTheReplayCarriesWhatWasSaidAndNotWhatGummiDid(t *testing.T) {
	seed := []Message{
		{Author: AuthorUser, Content: "drop the leaked pty fd"},
		{Author: AuthorTool, Content: "write  pty.go"},
		{Author: AuthorAssistant, Content: "Added pty.go."},
		{Author: AuthorTool, Content: "worktree committed: FF-001: turn checkpoint"},
	}
	got := freeformReplayHint(seed, false)
	for _, want := range []string{"them: drop the leaked pty fd", "you: Added pty.go."} {
		if !strings.Contains(got, want) {
			t.Errorf("the replay drops what was said (%q):\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"worktree committed", "write  pty.go"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("the replay carries %q, which is not conversation:\n%s", unwanted, got)
		}
	}
}
