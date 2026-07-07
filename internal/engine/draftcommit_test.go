package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/worktree"
)

// draftFixture builds a repo with a feature worktree carrying one
// committed change, so Diff has something to describe.
func draftFixture(t *testing.T) (state.Workspace, *state.Store, *worktree.Manager, domain.Feature) {
	t.Helper()
	ws, store, wt := newRepo(t)
	f := feature(1, "impl", domain.StageImplement)
	withWorktree(t, wt, f)
	if err := wt.CommitFile(context.Background(), &f, "feat.go", "package x // feature work\n", "feature commit"); err != nil {
		t.Fatal(err)
	}
	return ws, store, wt, f
}

func TestDraftCommitMessage(t *testing.T) {
	var prompt string
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		if opts.Role != agent.RoleScribe {
			t.Errorf("draft used role %q, want scribe", opts.Role)
		}
		prompt = msg
		return []agent.Event{
			{Kind: agent.EventMessage, Text: "```\nFD-001: add feature work\n\nAdds feat.go.\n```"},
			{Kind: agent.EventIdle},
		}
	}}
	ws, store, wt, f := draftFixture(t)
	e := New(Config{Agent: ag, Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	got, err := e.DraftCommitMessage(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if want := "FD-001: add feature work\n\nAdds feat.go."; got != want {
		t.Errorf("draft = %q, want %q (fence stripped)", got, want)
	}
	if !strings.Contains(prompt, "feature work") {
		t.Errorf("prompt does not carry the branch diff:\n%s", prompt)
	}
	if !strings.Contains(prompt, string(f.ID)) {
		t.Errorf("prompt does not name the feature:\n%s", prompt)
	}
}

func TestDraftCommitMessageStreamNotDoubled(t *testing.T) {
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{
			{Kind: agent.EventReasoningDelta, Text: "thinking about the diff"},
			{Kind: agent.EventTextDelta, Text: "FD-001: add fea"},
			{Kind: agent.EventTextDelta, Text: "ture work"},
			{Kind: agent.EventMessage, Text: "FD-001: add feature work"},
			{Kind: agent.EventIdle},
		}
	}}
	ws, store, wt, f := draftFixture(t)
	e := New(Config{Agent: ag, Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	got, err := e.DraftCommitMessage(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if got != "FD-001: add feature work" {
		t.Errorf("draft = %q (deltas doubled or reasoning leaked?)", got)
	}
}

func TestDraftCommitMessageNoAgent(t *testing.T) {
	ws, store, wt, f := draftFixture(t)
	e := New(Config{Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })
	if _, err := e.DraftCommitMessage(context.Background(), f); err == nil {
		t.Fatal("nil agent produced a draft")
	}
}

func TestDraftCommitMessageAgentError(t *testing.T) {
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{{Kind: agent.EventError, Err: errors.New("boom")}}
	}}
	ws, store, wt, f := draftFixture(t)
	e := New(Config{Agent: ag, Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })
	if _, err := e.DraftCommitMessage(context.Background(), f); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want the agent error", err)
	}
}

func TestDraftCommitMessageEmptyReply(t *testing.T) {
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{{Kind: agent.EventIdle}}
	}}
	ws, store, wt, f := draftFixture(t)
	e := New(Config{Agent: ag, Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })
	if _, err := e.DraftCommitMessage(context.Background(), f); err == nil {
		t.Fatal("empty reply produced a draft")
	}
}

func TestDraftCommitMessageEmptyDiff(t *testing.T) {
	ws, store, wt := newRepo(t)
	f := feature(1, "impl", domain.StageImplement)
	withWorktree(t, wt, f) // fresh worktree: no changes at all
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		t.Error("scribe consulted despite empty diff")
		return []agent.Event{{Kind: agent.EventIdle}}
	}}
	e := New(Config{Agent: ag, Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })
	if _, err := e.DraftCommitMessage(context.Background(), f); err == nil {
		t.Fatal("empty diff produced a draft")
	}
}

func TestDraftCommitMessageTruncatesDiff(t *testing.T) {
	ws, store, wt := newRepo(t)
	f := feature(1, "impl", domain.StageImplement)
	withWorktree(t, wt, f)
	big := strings.Repeat("lorem ipsum dolor sit amet\n", maxDraftDiffBytes/20)
	if err := wt.CommitFile(context.Background(), &f, "big.txt", big, "big commit"); err != nil {
		t.Fatal(err)
	}
	var prompt string
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		prompt = msg
		return []agent.Event{
			{Kind: agent.EventMessage, Text: "FD-001: add big.txt"},
			{Kind: agent.EventIdle},
		}
	}}
	e := New(Config{Agent: ag, Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })
	if _, err := e.DraftCommitMessage(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "[diff truncated") {
		t.Error("oversized diff not truncated in prompt")
	}
	if len(prompt) > maxDraftDiffBytes+2048 {
		t.Errorf("prompt still huge after truncation: %d bytes", len(prompt))
	}
}

func TestStripFence(t *testing.T) {
	cases := map[string]string{
		"plain text":                     "plain text",
		"```\nfenced body\n```":          "fenced body",
		"```text\nfenced body\n```":      "fenced body",
		"```\nline one\nline two\n```":   "line one\nline two",
		"```":                            "```",
		"```one-liner```":                "```one-liner```",
		"has ``` inside but not wrapped": "has ``` inside but not wrapped",
	}
	for in, want := range cases {
		if got := stripFence(in); got != want {
			t.Errorf("stripFence(%q) = %q, want %q", in, got, want)
		}
	}
}
