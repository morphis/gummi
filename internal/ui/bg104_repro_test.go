package ui

import (
	"regexp"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/ui/theme"
)

// TestBG104AttentionTextNamesNoBareKeys is BG-104's regression test. The
// sentence a stage records when it runs out of credits is written once
// and read on two surfaces: the inbox row, where the keys it named
// really do advance and top up, and the card's own history, where it is
// kept verbatim as the reason the run stopped. The card page composer
// takes every printable key (BG-078), so on that page those letters just
// typed themselves into the message box one line under the sentence
// telling the reader to press them — while the page's own next step,
// three rows down, correctly said "from there".
//
// The assertion is over every message these paths raise rather than the
// budget one the drive tripped over: any of them can end up as a park
// detail on a card page, and a bare key named in any of them is the same
// lie. Text a surface renders with its own key hints (nextsteps, the
// inbox's own action lines) is free to name keys — those are drawn by
// the surface that owns them.
func TestBG104AttentionTextNamesNoBareKeys(t *testing.T) {
	ws, store, wt := uiRepo(t)
	m := NewShell(theme.GummiDark(), "v0-test")
	m.Attach(store, wt, ws)
	f := mkFeature(t, store, 9, "config device add unix-char path", domain.StageReview)

	// "— g advance", "(u) top up", " u top up": a single letter offered as
	// something to press. Two-letter and longer words are prose.
	bareKey := regexp.MustCompile(`(^|[\s(—])[a-z]([\s)]|$)`)

	for _, committed := range []bool{true, false} {
		m.inbox = newInbox(m.now)
		m.raiseAttention(f.ID, attnBudget, budgetAttentionText(f.Stage, committed))
		it, ok := m.inbox.get(f.ID)
		if !ok {
			t.Fatalf("committed=%v: nothing was raised", committed)
		}
		if bareKey.MatchString(it.Text) {
			t.Errorf("committed=%v: the recorded sentence names a bare key, which does nothing on a card page: %q",
				committed, it.Text)
		}
		if !strings.Contains(it.Text, "inbox") {
			t.Errorf("committed=%v: the sentence names no surface to act on: %q", committed, it.Text)
		}
	}

	// the card page's own next step is the one place these keys belong,
	// and it still points at the surface that has them
	steps := nextActions(nextInput{
		kind: domain.KindFeature, stage: domain.StageReview, attn: attnBudget, sess: engine.StateDone,
	})
	if len(steps) == 0 || !strings.Contains(steps[0].label+steps[0].why, "inbox") {
		t.Errorf("the card page no longer sends a budget stop to the inbox: %+v", steps)
	}
}
