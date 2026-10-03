package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/engine"
)

// TestAltZRewindsAFreeformConversation: alt+z takes the conversation back
// to before the last message and puts it in the composer; again, over that
// untouched line, it goes one further back; over a draft of the reader's
// own, it does nothing.
func TestAltZRewindsAFreeformConversation(t *testing.T) {
	ag := &agent.Fake{Responder: func(_ agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{{Kind: agent.EventMessage, Text: "did " + msg}, {Kind: agent.EventIdle}}
	}}
	m, eng := agentWorkspace(t, ag)
	r := freeformRow(7, "tidy the parser", true)
	ctx := context.Background()
	if err := m.store.CreateFeature(ctx, &r.F); err != nil {
		t.Fatal(err)
	}
	ff, err := eng.OpenFreeform(ctx, r.F)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"split the lexer", "inline the helpers"} {
		if err := ff.Send(ctx, line); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for ff.Busy() || !strings.Contains(lastContent(ff.Snapshot().Transcript), "did "+line) {
			if time.Now().After(deadline) {
				t.Fatalf("the turn %q never settled", line)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	m.rewindFreeform(r)
	if got := m.threadInput.Value(); got != "inline the helpers" {
		t.Fatalf("composer = %q after one rewind", got)
	}
	m.rewindFreeform(r)
	if got := m.threadInput.Value(); got != "split the lexer" {
		t.Fatalf("composer = %q after a second rewind", got)
	}
	m.threadInput.SetValue("something of my own")
	m.rewindFreeform(r)
	if got := m.threadInput.Value(); got != "something of my own" {
		t.Errorf("rewind overwrote a draft: %q", got)
	}
}

func lastContent(tr []engine.Message) string {
	if len(tr) == 0 {
		return ""
	}
	return tr[len(tr)-1].Content
}
