package engine

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/worktree"
)

func TestParseCommitMsg(t *testing.T) {
	fenced := "```gummi-commit\nfeat(ui): sort the board\n\n- sort by severity\n```"
	cases := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"fenced-good", fenced, "feat(ui): sort the board\n\n- sort by severity", true},
		{"fenced-lead", "here is a draft:\n" + fenced, "", false},
		{"fenced-trail", fenced + "\nhope that helps", "", false},
		{"no-fence", "feat(ui): a bare line", "", false},
		{"wrong-fence", "```gobbledygook\nfeat(ui): x\n```", "", false},
		{"empty-fence", "```gummi-commit\n\n```", "", false},
		{"unterminated", "```gummi-commit\nfeat(ui): x\n", "", false},
		{"chatty", "Sure! " + fenced + " Let me know if you want changes.", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseGummiCommit(tc.in)
			if ok != tc.ok || got != tc.want {
				t.Errorf("parseGummiCommit(%q) = (%q,%v), want (%q,%v)", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestDraftCommitMsgRunsScribe(t *testing.T) {
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		if opts.Role != agent.RoleScribe {
			t.Errorf("draft used role %q, want scribe", opts.Role)
		}
		return []agent.Event{
			{Kind: agent.EventMessage, Text: "```gummi-commit\nfeat(ui): prefill the merge dialog\n\n- drafts from the spec\n```"},
			{Kind: agent.EventIdle},
		}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })
	f := feature(1, "impl", domain.StageImplement)
	withWorktree(t, wt, f)
	got, err := e.DraftCommitMessage(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "prefill the merge dialog") {
		t.Errorf("draft = %q, want the fenced body", got)
	}
}

func TestDraftCommitMsgCarriesArtifactPath(t *testing.T) {
	// a fenced reply so the pass yields a clean draft (the scribe would
	// otherwise be guard-rejected for the unparseable "ok" echo).
	rec := &recorder{Fake: agent.NewFake("```gummi-commit\nfeat(ui): prefill the merge dialog\n\n- drafts from the spec\n```")}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(rec), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })
	f := feature(1, "impl", domain.StageImplement)
	withWorktree(t, wt, f)
	if _, err := e.DraftCommitMessage(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(wt.Root(), f.ArtifactPath())
	if rec.opts().ArtifactPath != want {
		t.Errorf("ArtifactPath = %s, want %s", rec.opts().ArtifactPath, want)
	}
	if got := rec.opts().ExtraReadAllows; !reflect.DeepEqual(got, []string{want}) {
		t.Errorf("ExtraReadAllows = %v, want [%s]", got, want)
	}
}

func TestDraftCommitMsgNilBackend(t *testing.T) {
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: map[string]agent.Agent{}, Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })
	f := feature(1, "impl", domain.StageImplement)
	withWorktree(t, wt, f)
	got, err := e.DraftCommitMessage(context.Background(), f)
	if got != "" {
		t.Fatalf("nil backend: draft = %q, want empty", got)
	}
	if err == nil {
		t.Fatal("nil backend: expected a non-empty reason, got nil")
	}
}

func TestDraftCommitMsgBackendErrorIsEmpty(t *testing.T) {
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{{Kind: agent.EventError, Err: errors.New("boom")}}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })
	f := feature(1, "impl", domain.StageImplement)
	withWorktree(t, wt, f)
	got, err := e.DraftCommitMessage(context.Background(), f)
	if got != "" {
		t.Fatalf("backend error: draft = %q, want empty", got)
	}
	if err == nil {
		t.Fatal("backend error: expected a non-empty reason, got nil")
	}
}

func TestDraftCommitMsgChattyReplyFallsBackEmpty(t *testing.T) {
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{
			{Kind: agent.EventMessage, Text: "Sure, here is a nice message: feat(ui): x"},
			{Kind: agent.EventIdle},
		}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })
	f := feature(1, "impl", domain.StageImplement)
	withWorktree(t, wt, f)
	got, err := e.DraftCommitMessage(context.Background(), f)
	if got != "" {
		t.Fatalf("chatty reply: draft = %q, want empty", got)
	}
	if _, ok := err.(*CommitDraftGuardError); !ok {
		t.Fatalf("chatty reply: got reason %v, want a *CommitDraftGuardError", err)
	}
}

func TestDraftCommitMsgScrubsAttribution(t *testing.T) {
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{
			{Kind: agent.EventMessage, Text: "```gummi-commit\nfeat(ui): x\n\nCo-authored-by: copilot <x@y>\n```"},
			{Kind: agent.EventIdle},
		}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })
	f := feature(1, "impl", domain.StageImplement)
	withWorktree(t, wt, f)
	got, err := e.DraftCommitMessage(context.Background(), f)
	if got != "" {
		t.Fatalf("attributed draft: draft = %q, want empty", got)
	}
	if _, ok := err.(*CommitDraftGuardError); !ok {
		t.Fatalf("attributed draft: got reason %v, want a *CommitDraftGuardError", err)
	}
}

func TestCommitmsgPromptGuardsFormat(t *testing.T) {
	feed := &worktree.DraftFeed{}
	p := commitmsgPrompt(feed)
	for _, want := range []string{
		// every body line, including each "- " bullet, stays at or under 72.
		"stays at or under 72",
		// continue an over-length bullet onto an indented line.
		"continue it on an indented line",
		// no line-by-line diff/implementation enumeration.
		"Never enumerate the diff or implementation line-by-line",
		// each bullet states rationale (the "why"), not a mechanical "what".
		"stating the change's rationale",
		// one short good/bad example pair.
		"Good:",
		"Bad:",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("commitmsgPrompt missing %q", want)
		}
	}
}

func TestIsDiffDump(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"diff-git", "feat(ui): x\n\ndiff --git a/b.go b/b.go", true},
		{"plus-plus", "--- a/f.go\n+++ b/f.go", true},
		{"minus", "\n- a bullet\n\n--- a/x.go", true},
		{"hunk", "\n@@ -1,2 +1,2 @@", true},
		{"clean-subject-only", "feat(ui): sort the board", false},
		{"clean-bullets", "feat(ui): sort the board\n\n- sort by severity\n- keep it simple", false},
		{"bullet-leading-minus", "feat(ui): x\n\n- remove dead code\n- --- is not a marker in a bullet", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isDiffDump(tc.in); got != tc.want {
				t.Errorf("isDiffDump(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestWrapBodyLines(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		width int
		want  string
	}{
		{
			"over-length-bullet",
			"- short line and the wrapped continuation",
			13,
			"- short line\n  and the\n  wrapped\n  continuation",
		},
		{
			"short-lines-untouched",
			"- sort by severity",
			72,
			"- sort by severity",
		},
		{
			"blank-lines-untouched",
			"- a short bullet\n\n- another short bullet",
			72,
			"- a short bullet\n\n- another short bullet",
		},
		{
			"long-single-line-nests-three",
			strings.Repeat("word ", 30),
			72,
			strings.TrimSpace(strings.Repeat("word ", 14)) + "\n  " +
				strings.TrimSpace(strings.Repeat("word ", 14)) + "\n  " +
				strings.TrimSpace(strings.Repeat("word ", 2)),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := wrapBodyLines(tc.in, tc.width); got != tc.want {
				t.Errorf("wrapBodyLines = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWrapBodyLinesNoTrailingWhitespace(t *testing.T) {
	got := wrapBodyLines(strings.Repeat("word ", 30), 72)
	lines := strings.Split(got, "\n")
	if len(lines) < 3 {
		t.Fatalf("expected nested wrap across 3 lines, got %d lines: %q", len(lines), got)
	}
	for _, line := range lines {
		if strings.HasSuffix(line, " ") {
			t.Errorf("wrapped line ends with trailing whitespace: %q", line)
		}
		if len(line) > 72 {
			t.Errorf("wrapped line exceeds 72 chars: %q", line)
		}
	}
}

func TestDraftCommitMsgWrapsLongBody(t *testing.T) {
	long := "- explain why wrapping the body matters for the landing message that persists in main's history"
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{
			{Kind: agent.EventMessage, Text: "```gummi-commit\nfeat(ui): wrap the landing body\n\n" + long + "\n```"},
			{Kind: agent.EventIdle},
		}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })
	f := feature(1, "impl", domain.StageImplement)
	withWorktree(t, wt, f)
	got, err := e.DraftCommitMessage(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(got, "\n") {
		if len(line) > 72 {
			t.Errorf("draft has a body line over 72 chars: %q", line)
		}
	}
	if got == "" {
		t.Fatal("draft was discarded to empty; expected a wrapped draft")
	}
}

func TestDraftCommitMsgDiscardsDiffDump(t *testing.T) {
	body := "\n\ndiff --git a/commitmsg.go b/commitmsg.go\nindex 000..111\n--- a/commitmsg.go\n+++ b/commitmsg.go\n@@ -1,2 +1,2 @@"
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{
			{Kind: agent.EventMessage, Text: "```gummi-commit\nfeat(ui): x\n" + body + "\n```"},
			{Kind: agent.EventIdle},
		}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })
	f := feature(1, "impl", domain.StageImplement)
	withWorktree(t, wt, f)
	got, err := e.DraftCommitMessage(context.Background(), f)
	if got != "" {
		t.Fatalf("diff-dump draft: draft = %q, want empty", got)
	}
	if _, ok := err.(*CommitDraftGuardError); !ok {
		t.Fatalf("diff-dump draft: got reason %v, want a *CommitDraftGuardError", err)
	}
}

func TestCommitMsgTimeoutSizedForScribeLatency(t *testing.T) {
	// Guard against re-tightening the bound below the measured scribe
	// latency. A real local opencode model produced a correct, scrub-clean
	// draft in ~60s; at the old 30s bound the reply (delivered as one
	// end-of-stream message) had accumulated 0 bytes and every draft came
	// back empty. Tighten only after re-measuring a working scribe.
	if got := commitDraftTimeout; got < 90*time.Second {
		t.Fatalf("commitDraftTimeout = %v, want >= 90s (measured scribe latency ~60s)", got)
	}
}

func TestDraftCommitMsgTimeoutReturnsEmpty(t *testing.T) {
	// A scribe that never finishes must let the pass time out to an empty
	// draft and no error — the merge can never be blocked by this pass.
	block := make(chan struct{})
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		<-block
		return []agent.Event{{Kind: agent.EventIdle}}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })
	f := feature(1, "impl", domain.StageImplement)
	withWorktree(t, wt, f)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	got, err := e.DraftCommitMessage(ctx, f)
	if got != "" {
		t.Fatalf("timeout: draft = %q, want empty", got)
	}
	if err == nil {
		t.Fatal("timeout: expected a non-empty reason, got nil")
	}
}

// TestDraftCommitMsgSurfacesDistinctReasons pins the contract the bug
// regressed: every failure mode returns a distinct, non-empty reason
// alongside an empty draft (never a silent ("", nil)), and deliberate
// guard rejections are typed so the dialog can render them differently
// from a backend/session fault.
func TestDraftCommitMsgSurfacesDistinctReasons(t *testing.T) {
	newEngine := func(ag agent.Agent) (*Engine, domain.Feature) {
		ws, store, wt := newRepo(t)
		e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
		t.Cleanup(func() { e.Close() })
		f := feature(1, "impl", domain.StageImplement)
		withWorktree(t, wt, f)
		return e, f
	}
	fake := func(evs ...agent.Event) *agent.Fake {
		return &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event { return evs }}
	}
	blocked := func() (*agent.Fake, chan struct{}) {
		block := make(chan struct{})
		return &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
			<-block
			return []agent.Event{{Kind: agent.EventIdle}}
		}}, block
	}

	cases := []struct {
		name  string
		run   func(t *testing.T) (draft string, release func(), err error)
		guard bool
	}{
		{
			// a closed agent is the session-open failure: the scribe role
			// points at an unservable backend (missing model, dead agent).
			name: "session-open",
			run: func(t *testing.T) (string, func(), error) {
				ag := &agent.Fake{}
				e, f := newEngine(ag)
				if err := ag.Close(); err != nil {
					t.Fatalf("closing fake: %v", err)
				}
				draft, err := e.DraftCommitMessage(context.Background(), f)
				return draft, func() {}, err
			},
		},
		{
			name: "event-error",
			run: func(t *testing.T) (string, func(), error) {
				e, f := newEngine(fake(agent.Event{Kind: agent.EventError, Err: errors.New("boom")}))
				draft, err := e.DraftCommitMessage(context.Background(), f)
				return draft, func() {}, err
			},
		},
		{
			name:  "unparseable",
			guard: true,
			run: func(t *testing.T) (string, func(), error) {
				e, f := newEngine(fake(
					agent.Event{Kind: agent.EventMessage, Text: "Sure! feat(ui): x"},
					agent.Event{Kind: agent.EventIdle},
				))
				draft, err := e.DraftCommitMessage(context.Background(), f)
				return draft, func() {}, err
			},
		},
		{
			name:  "diff-dump",
			guard: true,
			run: func(t *testing.T) (string, func(), error) {
				e, f := newEngine(fake(
					agent.Event{Kind: agent.EventMessage, Text: "```gummi-commit\nfeat(ui): x\n\ndiff --git a/x.go b/x.go\n```"},
					agent.Event{Kind: agent.EventIdle},
				))
				draft, err := e.DraftCommitMessage(context.Background(), f)
				return draft, func() {}, err
			},
		},
		{
			name:  "attribution",
			guard: true,
			run: func(t *testing.T) (string, func(), error) {
				e, f := newEngine(fake(
					agent.Event{Kind: agent.EventMessage, Text: "```gummi-commit\nfeat(ui): x\n\nCo-authored-by: copilot <x@y>\n```"},
					agent.Event{Kind: agent.EventIdle},
				))
				draft, err := e.DraftCommitMessage(context.Background(), f)
				return draft, func() {}, err
			},
		},
		{
			name: "timeout",
			run: func(t *testing.T) (string, func(), error) {
				ag, block := blocked()
				e, f := newEngine(ag)
				ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
				defer cancel()
				draft, err := e.DraftCommitMessage(ctx, f)
				return draft, func() { close(block) }, err
			},
		},
	}

	seen := map[string]bool{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			draft, release, err := tc.run(t)
			release()
			if draft != "" {
				t.Fatalf("draft = %q, want empty on failure", draft)
			}
			if err == nil {
				t.Fatal("expected a non-empty reason, got nil")
			}
			if seen[err.Error()] {
				t.Fatalf("reason %q is not distinct across failure modes", err.Error())
			}
			seen[err.Error()] = true
			if _, ok := err.(*CommitDraftGuardError); ok != tc.guard {
				t.Fatalf("guard classification = %v, want %v (reason: %v)", ok, tc.guard, err)
			}
		})
	}
}
