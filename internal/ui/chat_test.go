package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/reentry"
)

// The reader answers "question" to the first line and a rewind to any
// later one, and counts how often it was asked at all.
func chattyAgent(asked *int) *agent.Fake {
	return &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		reply := "Persist per device; sync is a follow-up."
		if strings.Contains(msg, "INTENT: <one of the words above>") {
			*asked++
			reply = "INTENT: implementation_wrong"
			if *asked == 1 {
				reply = "INTENT: question"
			}
		}
		return []agent.Event{{Kind: agent.EventMessage, Text: reply}, {Kind: agent.EventIdle}}
	}}
}

// After a question is answered, follow-ups stay in the conversation and
// cost no read; the vocabulary's own words are the way back out, and
// the rest of such a line is what gets read.
func TestFollowUpsStayInTheConversation(t *testing.T) {
	asked := 0
	m, _ := chatWorkspace(t, chattyAgent(&asked))
	m = advanceTo(t, m, domain.StageImplement)
	m = openCardPage(t, m)

	m = typeAndSend(t, m, "why does it read the pool total?")
	if asked != 1 {
		t.Fatalf("the first line was read %d times, want 1", asked)
	}
	if !m.inChat(m.rows[0].F.ID) {
		t.Fatal("an answered question did not start a conversation")
	}

	m = typeAndSend(t, m, "no, the other branch of that function")
	if asked != 1 {
		t.Errorf("a follow-up was read (asked=%d)", asked)
	}
	if m.reentryPending != nil {
		t.Error("a follow-up raised a chip")
	}

	// the way out: a vocabulary word, and the rest is read
	m = typeAndSend(t, m, "send it back: the retry path is wrong")
	if asked != 2 {
		t.Errorf("the rest of a send-it-back line was not read (asked=%d)", asked)
	}
	if p := m.reentryPending; p == nil || p.out.Action != reentry.RerunInPlace {
		t.Errorf("send it back from a conversation raised no chip: %+v", p)
	}
}

// A word that is its own intent needs no read at all.
func TestApproveInAConversationNeedsNoRead(t *testing.T) {
	asked := 0
	m, _ := chatWorkspace(t, chattyAgent(&asked))
	draftRequiredSections(t, m)
	m = pump(t, m, m.loadRows)
	m = openCardPage(t, m)
	m = typeAndSend(t, m, "is the approach sound?")
	if asked != 1 || !m.inChat(m.rows[0].F.ID) {
		t.Fatalf("no conversation to exit from (asked=%d)", asked)
	}
	m = typeAndSend(t, m, "approve")
	if asked != 1 {
		t.Errorf("\"approve\" spent a read (asked=%d)", asked)
	}
	if p := m.reentryPending; p == nil || p.out.Action != reentry.Advance {
		t.Errorf("\"approve\" raised no advance chip: %+v", p)
	}
}

// Every way out that is not a word: a row picked, a verb, the card
// moving, the page closed.
func TestConversationEndsWithoutAWord(t *testing.T) {
	m, _ := chatWorkspace(t, agent.NewFake("ok"))
	id := m.rows[0].F.ID
	m.startChat(id)
	m.threadInput.SetValue("/approve")
	_ = m.submitThreadInput(m.rows[0])
	if m.inChat(id) {
		t.Error("a verb did not end the conversation")
	}
	m.startChat(id)
	m.syncDecision(&threadDecision{key: "somewhere-else"})
	if m.inChat(id) {
		t.Error("the card moving did not end the conversation")
	}
	m.startChat(id)
	m = openCardPage(t, m)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.inChat(id) {
		t.Error("leaving the page did not end the conversation")
	}
}

func TestChatExit(t *testing.T) {
	for line, want := range map[string]struct {
		intent reentry.Intent
		rest   string
		ok     bool
	}{
		"send it back: the retry path":  {"", "the retry path", true},
		"Send it back":                  {"", "", true},
		"bounce — nothing persists":     {"", "nothing persists", true},
		"approve":                       {reentry.Proceed, "", true},
		"go on, it looks right":         {reentry.Proceed, "it looks right", true},
		"land":                          {reentry.Proceed, "", true},
		"new card: the jq bootstrap":    {reentry.SeparateCard, "the jq bootstrap", true},
		"landing is what I'm unsure of": {"", "", false},
		"approved of the plan, mostly":  {"", "", false},
		"what about the quota path?":    {"", "", false},
	} {
		intent, rest, ok := chatExit(line)
		if intent != want.intent || rest != want.rest || ok != want.ok {
			t.Errorf("chatExit(%q) = %q,%q,%v want %q,%q,%v", line, intent, rest, ok, want.intent, want.rest, want.ok)
		}
	}
}
