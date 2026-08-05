package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
)

// fixedNow is a deterministic clock for spec-capture marker dates.
func fixedNow() time.Time { return time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC) }

// askArgs builds an ask_user tool-call argument blob.
func askArgs(t *testing.T, a Ask) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// clientToolFake advertises ClientTools and, on its first turn, emits an
// ask_user client-tool call instead of finishing.
func clientToolFake(args json.RawMessage) *agent.Fake {
	f := agent.NewFake("")
	f.Caps = agent.Capabilities{ClientTools: true, Interrupt: true, UsageEvents: true}
	first := true
	f.Responder = func(_ agent.SessionOpts, msg string) []agent.Event {
		if first {
			first = false
			return []agent.Event{
				{Kind: agent.EventClientToolCall, ToolCall: &agent.ToolCall{ID: "call-1", Name: "ask_user", Args: args}},
			}
		}
		return []agent.Event{{Kind: agent.EventMessage, Text: "done: " + msg}, {Kind: agent.EventIdle}}
	}
	return f
}

func TestAskUserSurfacesAndResolves(t *testing.T) {
	args := askArgs(t, Ask{
		Question: "Persist where?",
		Options:  []AskOption{{Label: "per-device"}, {Label: "synced"}},
	})
	ag := clientToolFake(args)
	e := newEngine(t, ag)
	ctx := context.Background()

	s, err := e.Attach(ctx, feature(1, "Dark mode", domain.StageBrainstorm))
	if err != nil {
		t.Fatal(err)
	}
	// the kickoff turn triggers the ask
	waitFor(t, e, EventQuestion)

	snap := s.Snapshot()
	if snap.PendingAsk == nil || snap.PendingAsk.Question != "Persist where?" {
		t.Fatalf("pending ask not surfaced: %+v", snap.PendingAsk)
	}
	if snap.Busy {
		t.Error("session should not be busy while waiting on the user")
	}

	// answering resolves the blocked tool call and records the choice.
	// (A real backend resumes the same turn to idle; the Fake models the
	// resolve synchronously, so no idle follows here.)
	if err := e.Answer(ctx, "FD-001", "per-device"); err != nil {
		t.Fatal(err)
	}

	if s.Snapshot().PendingAsk != nil {
		t.Error("pending ask not cleared after answer")
	}
	// the fake session recorded what gummi resolved the call with
	type resolver interface {
		Resolved(string) (string, bool)
	}
	if r, ok := s.agent().(resolver); ok {
		if got, _ := r.Resolved("call-1"); got != "per-device" {
			t.Errorf("resolved with %q, want per-device", got)
		}
	} else {
		t.Fatal("fake session is not a resolver")
	}
	// the answer is in the transcript as a user turn
	found := false
	for _, m := range s.Snapshot().Transcript {
		if m.Author == AuthorUser && m.Content == "per-device" {
			found = true
		}
	}
	if !found {
		t.Errorf("answer not recorded in transcript: %+v", s.Snapshot().Transcript)
	}
}

func TestParallelAsksBounceExtras(t *testing.T) {
	// the model fires two ask_user calls in one turn (parallel tool
	// calls). The first becomes the pending question; the second must be
	// bounced with an immediate result — displacing the first would
	// orphan its blocked tool handler and hang the turn forever.
	q1 := askArgs(t, Ask{Question: "Persist where?", Options: []AskOption{{Label: "per-device"}, {Label: "synced"}}})
	q2 := askArgs(t, Ask{Question: "Which theme default?", Options: []AskOption{{Label: "system"}, {Label: "dark"}}})
	ag := agent.NewFake("")
	ag.Caps = agent.Capabilities{ClientTools: true, Interrupt: true}
	first := true
	ag.Responder = func(_ agent.SessionOpts, _ string) []agent.Event {
		if first {
			first = false
			return []agent.Event{
				{Kind: agent.EventClientToolCall, ToolCall: &agent.ToolCall{ID: "call-1", Name: "ask_user", Args: q1}},
				{Kind: agent.EventClientToolCall, ToolCall: &agent.ToolCall{ID: "call-2", Name: "ask_user", Args: q2}},
			}
		}
		return []agent.Event{{Kind: agent.EventIdle}}
	}
	e := newEngine(t, ag)
	ctx := context.Background()

	s, err := e.Attach(ctx, feature(1, "Dark mode", domain.StageBrainstorm))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventQuestion)

	// the first ask is the open question; the second was not allowed to
	// displace it
	snap := s.Snapshot()
	if snap.PendingAsk == nil || snap.PendingAsk.Question != "Persist where?" {
		t.Fatalf("pending ask = %+v, want the first question", snap.PendingAsk)
	}

	type resolver interface {
		Resolved(string) (string, bool)
	}
	r, ok := s.agent().(resolver)
	if !ok {
		t.Fatal("fake session is not a resolver")
	}
	// the second call was bounced with an immediate result so its handler
	// never hangs (poll: the pump may still be delivering call-2)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if got, done := r.Resolved("call-2"); done {
			if !strings.Contains(got, "one question at a time") {
				t.Errorf("second ask bounced with %q, want a one-question-at-a-time nudge", got)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("second ask never bounced")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// answering resolves the first call — the one the picker showed
	if err := e.Answer(ctx, "FD-001", "per-device"); err != nil {
		t.Fatal(err)
	}
	if got, done := r.Resolved("call-1"); !done || got != "per-device" {
		t.Errorf("first ask resolved with %q done=%v, want per-device", got, done)
	}
	if s.Snapshot().PendingAsk != nil {
		t.Error("pending ask not cleared after answer")
	}
}

func TestAskUserCapturesToSpec(t *testing.T) {
	args := askArgs(t, Ask{
		Question:   "Persist where?",
		Options:    []AskOption{{Label: "per-device"}, {Label: "synced"}},
		SpecAnchor: "## Chosen approach",
	})
	ag := clientToolFake(args)
	e := newEngine(t, ag)
	e.now = fixedNow // deterministic marker date
	ctx := context.Background()

	f := feature(1, "Dark mode", domain.StageBrainstorm)
	s, err := e.Attach(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventQuestion)
	if err := e.Answer(ctx, "FD-001", "per-device"); err != nil {
		t.Fatal(err)
	}

	draft := filepath.Join(e.cfg.Workspace.DraftsDir(), spec.DraftFilename(&f))
	raw, err := os.ReadFile(draft)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "%% @user(2026-07-04): resolved — per-device") {
		t.Errorf("answer not captured into spec:\n%s", raw)
	}
	_ = s
}

func TestAskUserBadAnchorStillAnswers(t *testing.T) {
	args := askArgs(t, Ask{
		Question:   "Persist where?",
		Options:    []AskOption{{Label: "per-device"}},
		SpecAnchor: "no such line anywhere",
	})
	e := newEngine(t, clientToolFake(args))
	ctx := context.Background()
	if _, err := e.Attach(ctx, feature(1, "Dark mode", domain.StageBrainstorm)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventQuestion)
	// a bad anchor must not block the answer
	if err := e.Answer(ctx, "FD-001", "per-device"); err != nil {
		t.Fatalf("answer with bad anchor failed: %v", err)
	}
	// the skip is noted in activity
	var noted bool
	for _, a := range e.Get("FD-001").Snapshot().Activity {
		if strings.Contains(a, "spec capture skipped") {
			noted = true
		}
	}
	if !noted {
		t.Error("bad-anchor skip not noted in activity")
	}
}

func TestConventionAskPath(t *testing.T) {
	// a backend WITHOUT client tools emits a gummi-ask fenced block
	block := "Here's my question.\n```gummi-ask\n" +
		`{"question":"Persist where?","options":[{"label":"per-device"},{"label":"synced"}]}` +
		"\n```"
	ag := agent.NewFake(block)
	ag.Caps = agent.Capabilities{} // no ClientTools
	e := newEngine(t, ag)
	ctx := context.Background()

	s, err := e.Attach(ctx, feature(1, "Dark mode", domain.StageBrainstorm))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventQuestion)

	snap := s.Snapshot()
	if snap.PendingAsk == nil || snap.PendingAsk.Question != "Persist where?" {
		t.Fatalf("convention ask not parsed: %+v", snap.PendingAsk)
	}
	// the block is stripped from the visible message
	last, _ := s.lastAssistant()
	if strings.Contains(last, "gummi-ask") {
		t.Errorf("ask block not stripped from transcript: %q", last)
	}
	// answering a convention ask delivers it as the next turn
	if err := e.Answer(ctx, "FD-001", "synced"); err != nil {
		t.Fatal(err)
	}
}

// toolCallFake advertises client tools and, on its first turn, emits a
// single client-tool call with the given name/args, then idles.
func toolCallFake(name string, args json.RawMessage) *agent.Fake {
	f := agent.NewFake("")
	f.Caps = agent.Capabilities{ClientTools: true, Interrupt: true}
	first := true
	f.Responder = func(_ agent.SessionOpts, _ string) []agent.Event {
		if first {
			first = false
			return []agent.Event{
				{Kind: agent.EventClientToolCall, ToolCall: &agent.ToolCall{ID: "c1", Name: name, Args: args}},
				{Kind: agent.EventIdle},
			}
		}
		return []agent.Event{{Kind: agent.EventIdle}}
	}
	return f
}

func TestSpecAnnotateWritesMarker(t *testing.T) {
	args := json.RawMessage(`{"anchor":"## Chosen approach","note":"per-device or synced?"}`)
	e := newEngine(t, toolCallFake("spec_annotate", args))
	e.now = fixedNow
	ctx := context.Background()

	f := feature(1, "Dark mode", domain.StageBrainstorm)
	if _, err := e.Attach(ctx, f); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventIdle)

	draft := filepath.Join(e.cfg.Workspace.DraftsDir(), spec.DraftFilename(&f))
	raw, err := os.ReadFile(draft)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "%% @architect(2026-07-04): per-device or synced?") {
		t.Errorf("annotation not written:\n%s", raw)
	}
	// the marker is placed under the anchor line, not elsewhere
	if !strings.Contains(string(raw), "## Chosen approach\n%% @architect(2026-07-04): per-device or synced?") {
		t.Errorf("annotation not anchored correctly:\n%s", raw)
	}
}

func TestSubmitVerdictRecorded(t *testing.T) {
	args := json.RawMessage(`{"verdict":"changes","summary":"nil deref in foo"}`)
	ag := toolCallFake("submit_verdict", args)
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "impl", domain.StageReview)
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-001", StateDone)

	snap := e.Get("FD-001").Snapshot()
	if snap.Verdict != "changes" {
		t.Errorf("verdict = %q, want changes", snap.Verdict)
	}
	var noted bool
	for _, a := range snap.Activity {
		if strings.Contains(a, "verdict: changes") && strings.Contains(a, "nil deref") {
			noted = true
		}
	}
	if !noted {
		t.Errorf("verdict summary not in activity: %+v", snap.Activity)
	}
}

// The verify stage gets its own submit_verdict flavor
// (pass|fail|blocked) and a "fail" verdict is recorded like any other.
func TestVerifyVerdictToolAndFailRecorded(t *testing.T) {
	tools := stageTools(domain.StageVerify, flavorStage)
	if len(tools) != 1 || tools[0].Name != "submit_verdict" {
		t.Fatalf("verify tools = %+v, want submit_verdict only", tools)
	}
	if toolHint(domain.StageVerify, flavorStage) == "" {
		t.Error("verify has no tool hint")
	}

	args := json.RawMessage(`{"verdict":"fail","summary":"rock build broken"}`)
	ag := toolCallFake("submit_verdict", args)
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "verify", domain.StageVerify)
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-001", StateDone)

	if got := e.Get("FD-001").Snapshot().Verdict; got != "fail" {
		t.Errorf("verdict = %q, want fail", got)
	}
}

// A verify agent may declare the environment unable to run the plan;
// the blocked verdict is recorded like any other.
func TestVerifyVerdictBlockedRecorded(t *testing.T) {
	args := json.RawMessage(`{"verdict":"blocked","summary":"no pytest in this workspace"}`)
	ag := toolCallFake("submit_verdict", args)
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "verify", domain.StageVerify)
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-001", StateDone)

	if got := e.Get("FD-001").Snapshot().Verdict; got != "blocked" {
		t.Errorf("verdict = %q, want blocked", got)
	}
}

// blocked belongs to verify's vocabulary only: a review agent
// submitting it is bounced (verdict stays empty) rather than recorded,
// so the review loop never sees a verdict outside its contract.
func TestReviewVerdictRejectsBlocked(t *testing.T) {
	args := json.RawMessage(`{"verdict":"blocked","summary":"cannot run this"}`)
	ag := toolCallFake("submit_verdict", args)
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "review", domain.StageReview)
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-001", StateDone)

	if got := e.Get("FD-001").Snapshot().Verdict; got != "" {
		t.Errorf("review recorded out-of-vocabulary verdict %q, want rejection", got)
	}
}

// The verify hint carries the verdict contract and the no-dangling-
// questions rule for both kinds, so convention backends (no client
// tools) still produce a parseable outcome.
func TestVerifyHintCarriesVerdictContract(t *testing.T) {
	for _, kind := range []domain.Kind{domain.KindFeature, domain.KindBug} {
		h := verifyHint(kind)
		if !strings.Contains(h, "VERDICT: pass") || !strings.Contains(h, "VERDICT: fail") ||
			!strings.Contains(h, "VERDICT: blocked") {
			t.Errorf("%s verify hint missing the verdict contract:\n%s", kind, h)
		}
		if !strings.Contains(h, "never end with one") {
			t.Errorf("%s verify hint missing the no-questions rule", kind)
		}
	}
}

func TestResolveAnnotationMarksResolved(t *testing.T) {
	ws, store, wt := newRepo(t)
	f := feature(1, "impl", domain.StageImplement)
	withWorktree(t, wt, f)
	ctx := context.Background()
	if err := store.CreateFeature(ctx, &f); err != nil {
		t.Fatal(err)
	}
	annID, err := store.AddDiffAnnotation(ctx, domain.DiffAnnotation{
		Feature: f.ID, File: "main.go", Excerpt: "x := 1", Comment: "name this better",
	}, fixedNow())
	if err != nil {
		t.Fatal(err)
	}

	ag := toolCallFake("resolve_annotation", json.RawMessage(fmt.Sprintf(`{"id":%d}`, annID)))
	var hints []string
	inner := ag.Responder
	ag.Responder = func(opts agent.SessionOpts, msg string) []agent.Event {
		hints = opts.SystemHints
		return inner(opts, msg)
	}
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-001", StateDone)

	// the run's hints carried the comment with its [id] and the resolve
	// instruction (the client-tool form of diffReviewHints)
	joined := strings.Join(hints, "\n")
	if !strings.Contains(joined, fmt.Sprintf("- [%d] main.go — x := 1: name this better", annID)) {
		t.Errorf("hints missing the [id]-tagged comment:\n%s", joined)
	}
	if !strings.Contains(joined, "resolve_annotation") {
		t.Errorf("hints missing the resolve_annotation instruction:\n%s", joined)
	}

	// the store now shows the comment resolved
	anns, err := store.ListDiffAnnotations(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(anns) != 1 || !anns[0].Resolved {
		t.Errorf("annotation not resolved in store: %+v", anns)
	}

	// the tool call was answered with the burn-down count
	type resolver interface {
		Resolved(string) (string, bool)
	}
	r, ok := e.Get("FD-001").agent().(resolver)
	if !ok {
		t.Fatal("fake session is not a resolver")
	}
	if got, done := r.Resolved("c1"); !done || !strings.Contains(got, "0 still open") {
		t.Errorf("tool resolved with %q done=%v, want a burn-down confirmation", got, done)
	}

	// and the resolution is in the activity feed
	var noted bool
	for _, a := range e.Get("FD-001").Snapshot().Activity {
		if strings.Contains(a, "resolved diff comment") && strings.Contains(a, "name this better") {
			noted = true
		}
	}
	if !noted {
		t.Error("resolution not noted in activity")
	}
}

func TestResolveAnnotationRejectsForeignID(t *testing.T) {
	ws, store, wt := newRepo(t)
	f := feature(1, "impl", domain.StageImplement)
	withWorktree(t, wt, f)
	ctx := context.Background()
	// the only annotation belongs to a different feature — the agent must
	// not be able to resolve it from FD-001's session
	other := feature(2, "other", domain.StageImplement)
	if err := store.CreateFeature(ctx, &other); err != nil {
		t.Fatal(err)
	}
	annID, err := store.AddDiffAnnotation(ctx, domain.DiffAnnotation{
		Feature: other.ID, File: "a.go", Comment: "not yours",
	}, fixedNow())
	if err != nil {
		t.Fatal(err)
	}

	ag := toolCallFake("resolve_annotation", json.RawMessage(fmt.Sprintf(`{"id":%d}`, annID)))
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-001", StateDone)

	type resolver interface {
		Resolved(string) (string, bool)
	}
	r, ok := e.Get("FD-001").agent().(resolver)
	if !ok {
		t.Fatal("fake session is not a resolver")
	}
	if got, done := r.Resolved("c1"); !done || !strings.Contains(got, "no diff comment") {
		t.Errorf("foreign id bounced with %q done=%v, want a not-found result", got, done)
	}
	anns, err := store.ListDiffAnnotations(ctx, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(anns) != 1 || anns[0].Resolved {
		t.Errorf("foreign annotation must stay open: %+v", anns)
	}
}

func TestCompileDiffComments(t *testing.T) {
	anns := []domain.DiffAnnotation{
		{ID: 3, File: "a.go", Excerpt: "x := 1", Comment: "rename"},
		{ID: 4, File: "b.go", Comment: "already handled", Resolved: true},
	}
	withTool := CompileDiffComments(anns, true)
	if !strings.Contains(withTool, "- [3] a.go — x := 1: rename") {
		t.Errorf("tool form missing [id] line:\n%s", withTool)
	}
	if !strings.Contains(withTool, "resolve_annotation") {
		t.Errorf("tool form missing resolve instruction:\n%s", withTool)
	}
	if strings.Contains(withTool, "b.go") {
		t.Errorf("resolved comment leaked into the turn:\n%s", withTool)
	}
	plain := CompileDiffComments(anns, false)
	if strings.Contains(plain, "[3]") || strings.Contains(plain, "resolve_annotation") {
		t.Errorf("plain form must not mention ids or the tool:\n%s", plain)
	}
	if !strings.Contains(plain, "- a.go — x := 1: rename") {
		t.Errorf("plain form missing the comment line:\n%s", plain)
	}
	if got := CompileDiffComments([]domain.DiffAnnotation{{ID: 1, Resolved: true}}, true); got != "" {
		t.Errorf("all-resolved must compile to empty, got %q", got)
	}
}

// TestAllowedVerdictsPerStage pins the verdict vocabulary of each
// session type: review negotiates "changes" (never "fail" — that
// slipped through the old fallthrough with no downstream handling),
// verify reports pass/fail/blocked, critique mirrors review, and
// stages that don't offer submit_verdict get nil so a stray call is
// refused rather than silently accepted.
func TestAllowedVerdictsPerStage(t *testing.T) {
	cases := []struct {
		name string
		s    *Session
		want []string
	}{
		{"review", &Session{Feature: domain.Feature{Stage: domain.StageReview}}, []string{"pass", "changes"}},
		{"verify", &Session{Feature: domain.Feature{Stage: domain.StageVerify}}, []string{"pass", "fail", "blocked"}},
		{"critique", &Session{Critique: true, Feature: domain.Feature{Stage: domain.StagePlan}}, []string{"pass", "changes"}},
		{"implement (no verdict tool)", &Session{Feature: domain.Feature{Stage: domain.StageImplement}}, nil},
	}
	for _, tc := range cases {
		got := allowedVerdicts(tc.s)
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestUnknownClientToolAutoResolves(t *testing.T) {
	ag := agent.NewFake("")
	ag.Caps = agent.Capabilities{ClientTools: true, Interrupt: true}
	ag.Responder = func(_ agent.SessionOpts, _ string) []agent.Event {
		return []agent.Event{
			{Kind: agent.EventClientToolCall, ToolCall: &agent.ToolCall{ID: "c1", Name: "mystery", Args: json.RawMessage(`{}`)}},
			{Kind: agent.EventIdle},
		}
	}
	e := newEngine(t, ag)
	ctx := context.Background()
	s, err := e.Attach(ctx, feature(1, "x", domain.StageBrainstorm))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventIdle)
	// an unknown tool never becomes a pending question
	if s.Snapshot().PendingAsk != nil {
		t.Error("unknown tool surfaced as a question")
	}
	type resolver interface {
		Resolved(string) (string, bool)
	}
	if r, ok := s.agent().(resolver); ok {
		if got, done := r.Resolved("c1"); !done || !strings.Contains(got, "unknown tool") {
			t.Errorf("unknown tool not auto-resolved: %q done=%v", got, done)
		}
	}
}
