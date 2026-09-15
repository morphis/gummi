package engine

import (
	"context"
	"sync"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// A reattach continues a conversation the backend already has, so it is
// handed that conversation's id. Without it the new session starts blank
// and spends its first turns re-reading the files the one before it had
// open — which is the whole cost a restored ask pays today.
func TestReattachCarriesTheBackendConversationID(t *testing.T) {
	ctx := context.Background()
	f := feature(1, "carry the conversation", domain.StagePlan)

	var mu sync.Mutex
	var seen []string
	fake := &agent.Fake{Reply: "ack", OnNewSession: func(opts agent.SessionOpts) {
		mu.Lock()
		seen = append(seen, opts.ResumeID)
		mu.Unlock()
	}}
	e := newEngine(t, fake)

	s, err := e.Attach(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	// the shape a restored session has: a transcript from the process that
	// is gone, the backend conversation id it ran under, and no agent.
	s.appendUser("how should this be configured?")
	s.setAgentSessionID("conv-42")
	s.agent().Close()
	s.clearAgent()

	if _, err := e.Attach(ctx, f); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) < 2 {
		t.Fatalf("expected two backend sessions, saw %d", len(seen))
	}
	if seen[0] != "" {
		t.Errorf("a fresh attach resumed %q; there was no conversation to continue", seen[0])
	}
	if seen[len(seen)-1] != "conv-42" {
		t.Errorf("reattach resumed %q, want the prior conversation id conv-42", seen[len(seen)-1])
	}
}

// The card keeps one session row per stage, so the id sitting in it may
// belong to the critique pass that ran last. A conversation is only
// resumed by the same role doing the same job — resuming a reviewer's
// critique as the architect would continue the wrong side of the
// argument.
func TestReattachDoesNotResumeAnotherRolesConversation(t *testing.T) {
	ctx := context.Background()
	f := feature(1, "wrong conversation", domain.StagePlan)

	var mu sync.Mutex
	var seen []string
	fake := &agent.Fake{Reply: "ack", OnNewSession: func(opts agent.SessionOpts) {
		mu.Lock()
		seen = append(seen, opts.ResumeID)
		mu.Unlock()
	}}
	e := newEngine(t, fake)

	s, err := e.Attach(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	s.appendUser("a question")
	s.setAgentSessionID("critique-conv")
	// the row the critique pass left behind: same card, same stage, a
	// different role doing a different job
	s.Role = agent.RoleReviewer
	s.Critique = true
	s.agent().Close()
	s.clearAgent()

	if _, err := e.Attach(ctx, f); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if got := seen[len(seen)-1]; got != "" {
		t.Errorf("reattach resumed %q — a conversation belonging to another role/flavor", got)
	}
}
