package ui

import (
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

// TestBG087NoticeDoesNotOutliveItsSurface is BG-087's regression test.
//
// BG-038 settled the rule — a notice is cleared when the view changes,
// except an error about the card still selected — and clearTransientNotice
// implements exactly that. What was missing was the wiring: the function
// was called from a handful of ACTION sites (running a card action,
// toggling the lock, the diff/spec verbs) and from no view-change site
// at all. 136 call sites set a notice, 4 clear it, and nothing anywhere
// in internal/ui expires one on time, so a notice naming one card stood
// on the bar while the reader was on another card, another surface, or
// another tab.
func TestBG087NoticeDoesNotOutliveItsSurface(t *testing.T) {
	setup := func(t *testing.T) *Shell {
		t.Helper()
		m := populatedShell(140, 40)
		ws, store, wt := uiRepo(t)
		m.Attach(store, wt, ws)
		a := mkFeature(t, store, 1, "the card it is about", domain.StageSpec)
		b := mkFeature(t, store, 2, "a different card", domain.StageImplement)
		m.rows = []featureRow{{F: a}, {F: b}}
		m.sel = 0
		return m
	}

	const notice = "RS-007: no diff — research cards carry no branch"

	t.Run("leaving the card page", func(t *testing.T) {
		m := setup(t)
		m.cardOpen = true
		m.notice = noticeMsg{text: notice}
		m.closeCard()
		if m.notice.text != "" {
			t.Errorf("the notice survived leaving the card page: %q", m.notice.text)
		}
	})

	t.Run("moving to another card", func(t *testing.T) {
		m := setup(t)
		m.notice = noticeMsg{text: notice}
		m.moveSel(1)
		if m.notice.text != "" {
			t.Errorf("the notice survived moving to another card: %q", m.notice.text)
		}
	})

	t.Run("switching tabs", func(t *testing.T) {
		m := setup(t)
		m.notice = noticeMsg{text: notice}
		m.setTab(TabInbox)
		if m.notice.text != "" {
			t.Errorf("the notice survived switching to the inbox tab: %q", m.notice.text)
		}
	})

	t.Run("opening a card", func(t *testing.T) {
		m := setup(t)
		m.notice = noticeMsg{text: notice}
		m.openCard()
		if m.notice.text != "" {
			t.Errorf("the notice survived opening a card: %q", m.notice.text)
		}
	})
}

// TestBG087ErrorAboutTheSelectedCardIsStillKept guards the exemption
// BG-038 deliberately chose: an error notice naming the card the reader
// is still on carries something they need to read, and survives. The
// wiring must not turn into "clear everything, always" — that would undo
// the decision rather than implement it.
func TestBG087ErrorAboutTheSelectedCardIsStillKept(t *testing.T) {
	m := populatedShell(140, 40)
	ws, store, wt := uiRepo(t)
	m.Attach(store, wt, ws)
	a := mkFeature(t, store, 1, "the card it is about", domain.StageSpec)
	b := mkFeature(t, store, 2, "a different card", domain.StageImplement)
	m.rows = []featureRow{{F: a}, {F: b}}
	m.sel = 0

	// an error about the selected card, on that card's own page
	m.cardOpen = true
	m.notice = noticeMsg{text: string(a.ID) + ": something went wrong", isErr: true, id: a.ID}
	m.closeCard()
	if m.notice.text == "" {
		t.Error("an error about the still-selected card was cleared on the way back to the board")
	}

	// but moving to a different card does clear it: it has no standing there
	m.moveSel(1)
	if m.notice.text != "" {
		t.Errorf("an error about another card survived onto this one: %q", m.notice.text)
	}
}
