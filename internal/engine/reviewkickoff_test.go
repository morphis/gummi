package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/worktree"
)

// reviewRig drives one card through the Review stage and captures the
// kickoff the agent was handed. Committing work on the branch first is
// what gives the stage a diff to talk about at all.
type reviewRig struct {
	e   *Engine
	wt  *worktree.Manager
	f   domain.Feature
	mu  *sync.Mutex
	got *string
}

func newReviewRig(t *testing.T, body string) *reviewRig {
	t.Helper()
	ws, store, wt := newRepo(t)
	var mu sync.Mutex
	var got string
	ag := &agent.Fake{Responder: func(_ agent.SessionOpts, msg string) []agent.Event {
		mu.Lock()
		got = msg
		mu.Unlock()
		return []agent.Event{
			{Kind: agent.EventMessage, Text: "VERDICT: pass"},
			{Kind: agent.EventIdle},
		}
	}}
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws,
		Model: "m", MaxActive: 1, Permission: agent.PermissionAllowAll})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "review me", domain.StageReview)
	withWorktree(t, wt, f)
	wtPath, err := wt.Path(&f)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtPath, "feature.go"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.CommitAll(context.Background(), &f, "the work under review"); err != nil {
		t.Fatal(err)
	}
	return &reviewRig{e: e, wt: wt, f: f, mu: &mu, got: &got}
}

func (r *reviewRig) run(t *testing.T) string {
	t.Helper()
	if err := r.e.Run(r.f); err != nil {
		t.Fatal(err)
	}
	waitState(t, r.e, "FD-001", StateDone)
	r.mu.Lock()
	defer r.mu.Unlock()
	return *r.got
}

// TestReviewKickoffCarriesDiffAndChecks: a review session was measured
// spending 33 turns and 28 Bash calls with zero edits — about twelve of
// them rebuilding `git diff base..HEAD` a file at a time, then running
// the repo's build and vet itself. It is handed both now.
func TestReviewKickoffCarriesDiffAndChecks(t *testing.T) {
	r := newReviewRig(t, "package main\n\nfunc Added() {}\n")
	writeSpecChecks(t, r.wt, r.f, "- name: build\n  cmd: \"true\"\n")

	got := r.run(t)
	for _, want := range []string{
		"gummi assembled this review's diff",
		"Do NOT rebuild it file by file",
		"feature.go",        // the diff itself, inline
		"func Added()",      //
		"gummi already ran", // the check results
		"build: pass",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("review kickoff missing %q:\n%s", want, got)
		}
	}
	// the base SHA is named, so "review the diff" is never a guess at a range
	base, err := r.wt.DiffBase(context.Background(), &r.f)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, base) {
		t.Errorf("review kickoff does not name the diff base %s:\n%s", base, got)
	}
}

// TestReviewKickoffFallsBackToStat: the kickoff preamble is re-read on
// every turn, so a diff too large to carry is summarized instead — the
// stat plus the command for the rest, never silence.
func TestReviewKickoffFallsBackToStat(t *testing.T) {
	r := newReviewRig(t, "package main\n\n"+strings.Repeat("// a line of the change\n", 400))
	t.Setenv("GUMMI_REVIEW_DIFF_MAX", "256")

	got := r.run(t)
	if strings.Contains(got, "a line of the change") {
		t.Error("an over-cap diff was carried inline anyway")
	}
	for _, want := range []string{
		"too large to carry in every turn",
		"Read the parts you need with `git diff",
		"feature.go", // the stat still names the files
	} {
		if !strings.Contains(got, want) {
			t.Errorf("review kickoff missing %q:\n%s", want, got)
		}
	}
}

// TestReviewKickoffNoDiffStaysQuiet: a branch with nothing on it gets no
// preamble at all — an empty diff block would assert there is nothing to
// review, which is the reviewer's own call to make.
func TestReviewKickoffNoDiffStaysQuiet(t *testing.T) {
	ws, store, wt := newRepo(t)
	var mu sync.Mutex
	var got string
	ag := &agent.Fake{Responder: func(_ agent.SessionOpts, msg string) []agent.Event {
		mu.Lock()
		got = msg
		mu.Unlock()
		return []agent.Event{{Kind: agent.EventMessage, Text: "VERDICT: pass"}, {Kind: agent.EventIdle}}
	}}
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws,
		Model: "m", MaxActive: 1, Permission: agent.PermissionAllowAll})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "nothing yet", domain.StageReview)
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-001", StateDone)

	mu.Lock()
	defer mu.Unlock()
	if strings.Contains(got, "gummi assembled this review's diff") {
		t.Errorf("empty branch still got a diff preamble:\n%s", got)
	}
}

// TestReviewChecksAreNotVerifysRun: review reads a branch that is not
// final, so what it is handed is its own snapshot. Nothing is recorded
// from it, and verify still runs the commands itself — the two answer
// different questions at different times.
func TestReviewChecksAreNotVerifysRun(t *testing.T) {
	r := newReviewRig(t, "package main\n\nfunc Added() {}\n")
	writeSpecChecks(t, r.wt, r.f, "- name: build\n  cmd: \"true\"\n")
	if got := r.run(t); !strings.Contains(got, "build: pass") {
		t.Fatalf("review kickoff missing its own check run:\n%s", got)
	}
	// the review pass wrote no check baseline: verify's comparison must
	// still rest on the approval-time capture, not on a mid-branch run.
	rows, err := r.e.cfg.Store.CheckBaseline(context.Background(), r.f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("review recorded %d baseline row(s); its results are not verify's", len(rows))
	}
}
