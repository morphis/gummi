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
	rec := recordingAgent()
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
	if err != nil || got != "" {
		t.Fatalf("nil backend: got (%q, %v), want (\"\", nil)", got, err)
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
	if err != nil || got != "" {
		t.Fatalf("backend error: got (%q, %v), want (\"\", nil)", got, err)
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
	if err != nil || got != "" {
		t.Fatalf("chatty reply: got (%q, %v), want (\"\", nil)", got, err)
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
	if err != nil || got != "" {
		t.Fatalf("attributed draft: got (%q, %v), want (\"\", nil)", got, err)
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
	if err != nil || got != "" {
		t.Fatalf("timeout: got (%q, %v), want (\"\", nil)", got, err)
	}
}
