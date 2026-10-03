package engine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/morphis/gummi/internal/agent"
)

// TestRewindTakesTheConversationBackNotTheBranch: rewinding to the last
// message drops it and what followed, hands its text back, and the next
// turn's backend is replayed the shorter conversation with the note that
// the branch kept everything — not resumed into the one that was cut.
func TestRewindTakesTheConversationBackNotTheBranch(t *testing.T) {
	var mu sync.Mutex
	var hints []string
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		mu.Lock()
		hints = append(hints, strings.Join(opts.SystemHints, "\n"))
		mu.Unlock()
		return []agent.Event{{Kind: agent.EventMessage, Text: "did " + msg}, {Kind: agent.EventIdle}}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
	t.Cleanup(func() { e.Close() })
	ctx := context.Background()
	f := freeformCard(1, "tidy the parser")
	createFeature(t, store, f)
	ff, err := e.OpenFreeform(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"split the lexer", "inline the helpers"} {
		if err := ff.Send(ctx, line); err != nil {
			t.Fatal(err)
		}
		waitFreeformIdle(t, ff)
	}

	text, err := ff.Rewind(1)
	if err != nil || text != "inline the helpers" {
		t.Fatalf("Rewind = %q, %v; want the last message back", text, err)
	}
	var got []string
	for _, m := range ff.Snapshot().Transcript {
		got = append(got, string(m.Author)+":"+m.Content)
	}
	joined := strings.Join(got, "|")
	if strings.Contains(joined, "user:inline the helpers") || !strings.Contains(joined, "assistant:did split the lexer") ||
		!strings.Contains(joined, "The branch was not rewound") {
		t.Fatalf("transcript after rewind = %q", got)
	}
	if _, err := ff.Rewind(5); !errors.Is(err, ErrNothingToRewind) {
		t.Errorf("Rewind past the start = %v, want ErrNothingToRewind", err)
	}

	if err := ff.Send(ctx, "rename it instead"); err != nil {
		t.Fatal(err)
	}
	waitFreeformIdle(t, ff)
	mu.Lock()
	last := hints[len(hints)-1]
	mu.Unlock()
	if !strings.Contains(last, "them: split the lexer") || !strings.Contains(last, "gummi: Rewound") ||
		strings.Contains(last, "them: inline the helpers") {
		t.Errorf("the respawned backend's replay:\n%s", last)
	}
}

// TestFreeformCompactRoutesToTheBackend: /compact is offered only where
// the backend can compact, goes to a Compactor's Compact rather than out
// as a prompt, and goes out as typed to a backend that reads it itself.
func TestFreeformCompactRoutesToTheBackend(t *testing.T) {
	for _, tc := range []struct {
		name               string
		compact            bool
		wantOffered        bool
		wantCompacts, sent int
	}{
		{"compactor", true, true, 1, 0},
		{"no compaction", false, false, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ag := agent.NewFake("ok")
			ag.Caps.Compact = tc.compact
			ws, store, wt := newRepo(t)
			e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
			t.Cleanup(func() { e.Close() })
			ctx := context.Background()
			f := freeformCard(1, "tidy the parser")
			createFeature(t, store, f)
			ff, err := e.OpenFreeform(ctx, f)
			if err != nil {
				t.Fatal(err)
			}
			if err := ff.Send(ctx, "start"); err != nil {
				t.Fatal(err)
			}
			waitFreeformIdle(t, ff)
			_, _, offered := FindProjectCommand(ff.Commands(), "/compact")
			if offered != tc.wantOffered {
				t.Errorf("/compact offered = %v, want %v", offered, tc.wantOffered)
			}
			if err := ff.Send(ctx, "/compact keep the parser notes"); err != nil {
				t.Fatal(err)
			}
			waitFreeformIdle(t, ff)
			ff.mu.Lock()
			live := ff.sess
			ff.mu.Unlock()
			fs := live.agent().(interface {
				CompactCount() int
				Turns() []agent.Turn
			})
			if got := fs.CompactCount(); got != tc.wantCompacts {
				t.Errorf("Compact called %d times, want %d", got, tc.wantCompacts)
			}
			turns := fs.Turns()
			if got := len(turns) - 1; got != tc.sent {
				t.Errorf("%d /compact turns sent as text, want %d", got, tc.sent)
			} else if tc.sent == 1 && turns[1].Text != "/compact keep the parser notes" {
				t.Errorf("sent %q, want the line as typed", turns[1].Text)
			}
		})
	}
}
