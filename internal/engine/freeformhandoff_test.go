package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/exp/golden"
	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/state"
)

// briefReply is what the fake answers the brief turn with: the four
// sections the contract demands, and content that is not merely the
// person's own asks — it carries what the agent found, did, and asked.
const briefReply = `asked
- why the retry test flakes on CI, and a small fix

decided
- retry twice, not three times (you answered)

done
- the retry loop in sync.go now retries twice; the fix is on the branch uncommitted

remaining
- the flake's root cause is still unknown; the loop only masks it`

// briefResponder is a fake Responder that answers the brief turn with
// briefReply and every other turn with reply. started is closed when the
// brief turn reaches the backend, and release holds it there until closed,
// so a test can look at the card while the turn runs.
func briefResponder(reply string, started, release chan struct{}) func(agent.SessionOpts, string) []agent.Event {
	return func(opts agent.SessionOpts, msg string) []agent.Event {
		if !strings.Contains(msg, "handoff brief") {
			return []agent.Event{{Kind: agent.EventMessage, Text: reply}, {Kind: agent.EventIdle}}
		}
		if started != nil {
			close(started)
		}
		if release != nil {
			<-release
		}
		return []agent.Event{
			{Kind: agent.EventMessage, Text: briefReply},
			{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 1, OutputTokens: 10, Model: opts.Model}},
			{Kind: agent.EventIdle},
		}
	}
}

// TestSessionHandoffBrief pins the live brief turn end to end: the reply is
// the brief and says so, the turn's session was granted no tools at all (so
// it reads and writes nothing — the project memory tier included), the
// gummi-authored line is on the transcript as the system author beneath
// which the brief arrives, and the card carries the in-flight brief flag
// while the turn runs.
func TestSessionHandoffBrief(t *testing.T) {
	ws, store, wt := newRepo(t)
	ctx := context.Background()
	f := freeformCard(7, "the retry test flakes on CI")
	createFeature(t, store, f)

	var optsMu sync.Mutex
	var briefOpts []agent.SessionOpts
	started := make(chan struct{})
	release := make(chan struct{})
	ag := &agent.Fake{Responder: briefResponder("looking at it", started, release)}
	ag.Caps = agent.Capabilities{UsageEvents: true, Interrupt: true}
	ag.OnNewSession = func(opts agent.SessionOpts) {
		if !strings.Contains(strings.Join(opts.SystemHints, "\n"), "handoff summary") {
			return // the card's own conversation session, not the brief turn's
		}
		optsMu.Lock()
		briefOpts = append(briefOpts, opts)
		optsMu.Unlock()
	}
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", Persist: true})
	t.Cleanup(func() { e.Close() })

	ff, err := e.OpenFreeform(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := ff.Send(ctx, "why does the retry test flake on CI?"); err != nil {
		t.Fatal(err)
	}
	waitFreeformIdle(t, ff)

	type result struct {
		brief  string
		source BriefSource
		err    error
	}
	done := make(chan result, 1)
	go func() {
		brief, source, err := e.SessionHandoffBrief(ctx, f.ID)
		done <- result{brief, source, err}
	}()

	// While the turn runs, the card says gummi is drafting — and is not
	// "busy", which is the live session's word for its own turns.
	<-started
	deadline := time.Now().Add(testWaitTimeout)
	for {
		snap := ff.Snapshot()
		if snap.Briefing {
			if snap.Busy {
				t.Fatalf("the brief turn reads as an ordinary busy turn: %+v", snap)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the in-flight brief flag never rose")
		}
		time.Sleep(5 * time.Millisecond)
	}

	close(release)
	res := <-done
	if res.err != nil {
		t.Fatalf("SessionHandoffBrief: %v", res.err)
	}
	if res.source != BriefLive {
		t.Errorf("source = %q, want live", res.source)
	}
	if !strings.Contains(res.brief, "remaining") || !strings.Contains(res.brief, "root cause is still unknown") {
		t.Errorf("the brief is not the agent's four-section answer:\n%s", res.brief)
	}

	// The turn's session: no tools granted, the card's own worktree, the
	// session's own model.
	optsMu.Lock()
	n, opts := len(briefOpts), agent.SessionOpts{}
	if n > 0 {
		opts = briefOpts[0]
	}
	optsMu.Unlock()
	if n != 1 {
		t.Fatalf("the brief turn opened %d sessions, want 1", n)
	}
	if len(opts.Tools) != 0 {
		t.Errorf("the brief turn was granted %d tool(s), want none — it must read and write nothing", len(opts.Tools))
	}
	if opts.WorkDir != ff.WorkDir() {
		t.Errorf("the brief turn ran in %q, want the card's worktree %q", opts.WorkDir, ff.WorkDir())
	}
	if opts.Model != "m" {
		t.Errorf("the brief turn ran on model %q, want the session's own m", opts.Model)
	}

	// The thread: the gummi-authored line (system author) names the
	// drafting and the contract, and the brief arrives beneath it as the
	// agent's reply.
	snap := ff.Snapshot()
	tr := snap.Transcript
	line, reply := -1, -1
	for i, m := range tr {
		if m.Author == AuthorSystem && strings.Contains(m.Content, "handoff brief") {
			line = i
		}
		if m.Author == AuthorAssistant && strings.Contains(m.Content, "root cause is still unknown") {
			reply = i
		}
	}
	if line < 0 {
		t.Fatalf("the transcript never recorded the gummi-authored brief line:\n%s", transcriptText(snap))
	}
	if reply < 0 {
		t.Fatalf("the brief never landed in the thread as the agent's reply:\n%s", transcriptText(snap))
	}
	if reply < line {
		t.Errorf("the brief (at %d) sits above the line that asked for it (at %d)", reply, line)
	}
	if !strings.Contains(tr[line].Content, "asked:, decided:, done:, remaining:") ||
		!strings.Contains(tr[line].Content, "no markdown headings") {
		t.Errorf("the gummi line does not state the brief's contract: %q", tr[line].Content)
	}
	// The flag is down again once the turn has ended.
	if snap.Briefing {
		t.Error("the in-flight brief flag stayed up after the turn ended")
	}
	// The turn spent the card's envelope like any turn.
	spent, err := store.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if spent.Spend.Credits != 1 {
		t.Errorf("the brief turn booked %.0f credits, want 1", spent.Spend.Credits)
	}
}

// TestSessionHandoffBriefRefusesMidTurn: the refusals any send rides fire
// before the brief turn spends anything — mid-turn first, then an
// unanswered question.
func TestSessionHandoffBriefRefusesMidTurn(t *testing.T) {
	ws, store, wt := newRepo(t)
	ctx := context.Background()
	f := freeformCard(8, "hold the turn")
	createFeature(t, store, f)

	hold := make(chan struct{})
	block := "Which retry count?\n```gummi-ask\n" +
		`{"question":"Which retry count?","options":[{"label":"twice"},{"label":"thrice"}]}` +
		"\n```"
	ag := &agent.Fake{Responder: func(_ agent.SessionOpts, msg string) []agent.Event {
		if strings.Contains(msg, "handoff brief") {
			return []agent.Event{{Kind: agent.EventMessage, Text: "a brief"}, {Kind: agent.EventIdle}}
		}
		if strings.Contains(msg, "retries") {
			return []agent.Event{{Kind: agent.EventMessage, Text: block}, {Kind: agent.EventIdle}}
		}
		// the first turn blocks until the test is done refusing
		<-hold
		return []agent.Event{{Kind: agent.EventMessage, Text: "done"}, {Kind: agent.EventIdle}}
	}}
	ag.Caps = agent.Capabilities{UsageEvents: true, Interrupt: true}
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
	t.Cleanup(func() { e.Close() })

	ff, err := e.OpenFreeform(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := ff.Send(ctx, "start something"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(testWaitTimeout)
	for !ff.Busy() {
		if time.Now().After(deadline) {
			t.Fatal("the turn never started")
		}
		time.Sleep(5 * time.Millisecond)
	}
	_, _, err = e.SessionHandoffBrief(ctx, f.ID)
	if err == nil {
		t.Fatal("the brief was taken while the session was mid-turn")
	}
	if !strings.Contains(err.Error(), "mid-turn") {
		t.Errorf("the refusal = %v, want it to say the session is mid-turn", err)
	}
	// the refusals fire before any state changes: nothing sits on the
	// transcript beyond the person's own turn
	snap := ff.Snapshot()
	if last := snap.Transcript[len(snap.Transcript)-1]; last.Author != AuthorUser {
		t.Errorf("the refusal left a %q line on the transcript, want nothing after the person's own turn", last.Author)
	}
	close(hold)
	waitFreeformIdle(t, ff)

	// A question the agent asked and nobody answered refuses the same way.
	if err := ff.Send(ctx, "how many retries should we keep?"); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(testWaitTimeout)
	for ff.Snapshot().PendingAsk == nil {
		if time.Now().After(deadline) {
			t.Fatalf("the question never went up: %s", transcriptText(ff.Snapshot()))
		}
		time.Sleep(5 * time.Millisecond)
	}
	_, _, err = e.SessionHandoffBrief(ctx, f.ID)
	if err == nil {
		t.Fatal("the brief was taken while a question was unanswered")
	}
	if !strings.Contains(err.Error(), "waiting on your answer") {
		t.Errorf("the refusal = %v, want it to name the open question", err)
	}
	if err := e.Answer(ctx, f.ID, "twice"); err != nil {
		t.Fatal(err)
	}
	waitFreeformIdle(t, ff)
}

// TestSessionHandoffBriefRefusesASecondBrief: the in-flight flag is
// claimed atomically — check and raise in one step — so two simultaneous
// dialog opens cannot both pass the refusal and both run brief turns, and
// the refused one never reaches the backend at all.
func TestSessionHandoffBriefRefusesASecondBrief(t *testing.T) {
	ws, store, wt := newRepo(t)
	ctx := context.Background()
	f := freeformCard(10, "hold the brief")
	createFeature(t, store, f)

	started := make(chan struct{})
	release := make(chan struct{})
	once := &sync.Once{}
	var optsMu sync.Mutex
	var briefOpts []agent.SessionOpts
	ag := &agent.Fake{Responder: func(_ agent.SessionOpts, msg string) []agent.Event {
		if strings.Contains(msg, "handoff brief") {
			once.Do(func() { close(started) })
			<-release
			return []agent.Event{{Kind: agent.EventMessage, Text: briefReply}, {Kind: agent.EventIdle}}
		}
		return []agent.Event{{Kind: agent.EventMessage, Text: "on it"}, {Kind: agent.EventIdle}}
	}}
	ag.Caps = agent.Capabilities{UsageEvents: true, Interrupt: true}
	ag.OnNewSession = func(opts agent.SessionOpts) {
		if !strings.Contains(strings.Join(opts.SystemHints, "\n"), "handoff summary") {
			return
		}
		optsMu.Lock()
		briefOpts = append(briefOpts, opts)
		optsMu.Unlock()
	}
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", Persist: true})
	t.Cleanup(func() { e.Close() })

	ff, err := e.OpenFreeform(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := ff.Send(ctx, "hold the brief"); err != nil {
		t.Fatal(err)
	}
	waitFreeformIdle(t, ff)

	done := make(chan error, 1)
	go func() {
		_, _, err := e.SessionHandoffBrief(ctx, f.ID)
		done <- err
	}()
	<-started
	deadline := time.Now().Add(testWaitTimeout)
	for !ff.Briefing() {
		if time.Now().After(deadline) {
			t.Fatal("the in-flight brief flag never rose")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// the second dialog open is refused, not spent: the claim is the
	// guard, and the refused fetch never opened a brief session of its own
	_, _, err = e.SessionHandoffBrief(ctx, f.ID)
	if !errors.Is(err, agent.ErrBusy) {
		t.Fatalf("the second brief was taken while the first ran, want a busy refusal: %v", err)
	}
	if !strings.Contains(err.Error(), "already") {
		t.Errorf("the refusal = %v, want it to say the card is drafting already", err)
	}
	optsMu.Lock()
	n := len(briefOpts)
	optsMu.Unlock()
	if n != 1 {
		t.Fatalf("the refused fetch opened %d brief sessions, want 1", n)
	}

	// the guard releases when the first turn ends
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("the first brief: %v", err)
	}
	brief, source, err := e.SessionHandoffBrief(ctx, f.ID)
	if err != nil || source != BriefLive {
		t.Fatalf("the guard never released: brief=%q source=%q err=%v", brief, source, err)
	}
}

// TestSessionHandoffBriefFallsBackAfterRestart: with no live session — as
// after a restart — the brief is assembled from the persisted transcript,
// and the source says so rather than passing it off as the session's words.
func TestSessionHandoffBriefFallsBackAfterRestart(t *testing.T) {
	ws, store, wt := newRepo(t)
	ctx := context.Background()
	f := freeformCard(9, "remember the asks")
	createFeature(t, store, f)

	e1 := New(Config{
		Agents: singleAgent(agent.NewFake("Started the retry loop; it now retries twice.")),
		Store:  store, Worktrees: wt, Workspace: ws, Model: "m", Persist: true,
	})
	ff, err := e1.OpenFreeform(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := ff.Send(ctx, "rewrite the retry loop in the config sync"); err != nil {
		t.Fatal(err)
	}
	waitFreeformIdle(t, ff)
	if err := e1.Close(); err != nil {
		t.Fatal(err)
	}

	e2 := New(Config{
		Agents: singleAgent(agent.NewFake("unused — no live session to ask")),
		Store:  store, Worktrees: wt, Workspace: ws, Model: "m", Persist: true,
	})
	t.Cleanup(func() { e2.Close() })
	if err := e2.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	if e2.Freeform(f.ID) == nil {
		t.Fatal("the restored board lost the session")
	}
	brief, source, err := e2.SessionHandoffBrief(ctx, f.ID)
	if err != nil {
		t.Fatalf("SessionHandoffBrief: %v", err)
	}
	if source != BriefAssembled {
		t.Errorf("source = %q, want assembled", source)
	}
	if !strings.Contains(brief, "rewrite the retry loop in the config sync") {
		t.Errorf("the assembled draft lost the person's ask:\n%s", brief)
	}
}

// briefFixture is the transcript TestAssembledBriefGolden pins its draft
// on: two asks, one ask_user question with its answer echo, and the
// agent's last reply as the state of the work.
func briefFixture() Snapshot {
	return Snapshot{Transcript: []Message{
		{Author: AuthorUser, Content: "rewrite the retry loop in the config sync"},
		{Author: AuthorAssistant, Content: "Found it — sync/config.go gives up after one try. Rewriting."},
		{Author: AuthorTool, Content: askToolName, Tool: askToolName, Detail: "Retry twice or three times?"},
		{Author: AuthorUser, Content: "yes, retry twice", AnsweredBy: state.ActorUser},
		{Author: AuthorAssistant, Content: "Done — the loop retries twice now, and the sync test covers it."},
		{Author: AuthorUser, Content: "also update the docs page for the retry flag"},
	}}
}

// TestAssembledBriefGolden pins the assembled draft's shape on a fixed
// transcript: the asks in order under asked, the ask_user exchange under
// decided, and the agent's last reply as the state of the work.
func TestAssembledBriefGolden(t *testing.T) {
	draft := AssembledBrief(briefFixture())

	// The person's ask appears verbatim as the asked section's first
	// entry — the transcript's user turns are listed in order.
	lines := strings.Split(draft, "\n")
	askedAt := -1
	for i, l := range lines {
		if l == "asked:" {
			askedAt = i
			break
		}
	}
	if askedAt < 0 || askedAt+1 >= len(lines) {
		t.Fatalf("the draft has no asked section:\n%s", draft)
	}
	if !strings.HasPrefix(lines[askedAt+1], "- rewrite the retry loop in the config sync") {
		t.Errorf("the asked section does not open on the person's ask:\n%s", draft)
	}

	// The answer appears exactly once — under decided, as the answer to
	// its ask_user question, never under asked: an answer echo is not
	// something the person typed.
	if n := strings.Count(draft, "yes, retry twice"); n != 1 {
		t.Errorf("%q appears %d times in the draft, want exactly once:\n%s", "yes, retry twice", n, draft)
	}
	if !strings.Contains(draft, "Retry twice or three times? — answered: yes, retry twice") {
		t.Errorf("the decided section does not carry the question with its answer:\n%s", draft)
	}
	for _, section := range []string{"asked:\n", "decided:\n", "done:\n", "remaining:\n"} {
		if !strings.Contains(draft, section) {
			t.Errorf("the draft lost its %q section:\n%s", section, draft)
		}
	}
	golden.RequireEqual(t, []byte(draft))
}

// TestBriefCapTrimsOldestAsks: the cap trims the assembled draft's oldest
// asks first — the opening turn, often the fullest statement of what was
// wanted, is not the first thing discarded — and never drops a decided
// answer while anything still fits.
func TestBriefCapTrimsOldestAsks(t *testing.T) {
	var tr []Message
	for i := 0; i < 40; i++ {
		tr = append(tr,
			Message{Author: AuthorUser, Content: fmt.Sprintf("ask number %d about the config sync: %s",
				i, strings.Repeat("and the timing details matter here, too, ", 5))},
			Message{Author: AuthorAssistant, Content: fmt.Sprintf("handled ask %d", i)},
		)
	}
	// the decision comes last, so the cap has asks to spend before
	// anything decided is touched
	tr = append(tr,
		Message{Author: AuthorTool, Content: askToolName, Tool: askToolName, Detail: "retry twice or three times?"},
		Message{Author: AuthorUser, Content: "yes, retry twice", AnsweredBy: state.ActorUser},
	)
	draft := AssembledBrief(Snapshot{Transcript: tr})
	if len(draft) > SpecBriefMax {
		t.Fatalf("the draft is %d characters, want it bounded at %d", len(draft), SpecBriefMax)
	}
	if strings.Contains(draft, "ask number 0 ") || strings.Contains(draft, "ask number 10 ") {
		t.Errorf("the oldest asks survived the cap:\n%s", draft)
	}
	if !strings.Contains(draft, "older entries left out to fit") {
		t.Errorf("the draft does not say what the cap cut:\n%s", draft)
	}
	if !strings.Contains(draft, "yes, retry twice") {
		t.Errorf("the cap dropped a decided answer while the draft still had asks to spend:\n%s", draft)
	}
	// the newest asks survive: the cap trims from the oldest end
	if !strings.Contains(draft, "ask number 39") {
		t.Errorf("the cap dropped the newest ask:\n%s", draft)
	}

	// When nothing fits — a reply that alone fills the cap — the decisions
	// go too, and the reply is what gets hard-trimmed rather than the
	// section shape being abandoned.
	trimmed := AssembledBrief(Snapshot{Transcript: []Message{
		{Author: AuthorTool, Content: askToolName, Tool: askToolName, Detail: "first question?"},
		{Author: AuthorUser, Content: "first answer", AnsweredBy: state.ActorUser},
		{Author: AuthorTool, Content: askToolName, Tool: askToolName, Detail: "second question?"},
		{Author: AuthorUser, Content: "second answer", AnsweredBy: state.ActorUser},
		{Author: AuthorAssistant, Content: strings.Repeat("y", SpecBriefMax)},
	}})
	if strings.Contains(trimmed, "first answer") || strings.Contains(trimmed, "second answer") {
		t.Errorf("a decision survived a draft nothing could make fit:\n%s", trimmed[:300])
	}
	if !strings.Contains(trimmed, "trimmed to fit") {
		t.Errorf("the over-long reply was not trimmed:\n%s", trimmed[:300])
	}
	if len(trimmed) > SpecBriefMax {
		t.Errorf("the trimmed draft is %d characters, want it bounded at %d", len(trimmed), SpecBriefMax)
	}
	if !strings.Contains(trimmed, "decided:") || !strings.Contains(trimmed, "done:") {
		t.Errorf("the section shape did not survive the trim:\n%s", trimmed[:300])
	}
}
