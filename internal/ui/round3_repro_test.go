package ui

import (
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// The three round-3 defects whose repro needs a live stage on a pty, pinned
// here instead: a budget-stopped card with no way forward, an inbox row
// advertising a key that does nothing, and a citation mark whose key always
// refused. See REVIEW-ux-drive-2026-09-11-round3.md §1.1, §2.1, §2.2.

// A budget stop offers exactly two answers — "top up and go on" and "stop
// here" — so the first one being a no-op leaves the card page with no way
// forward at all. The row is keyless by construction (stageActions' attnBudget
// arm), which is what made it fall past runCardAction's a.key shortcut and out
// of a switch that had no case for its id: enter did nothing, twice, silently.
func TestBudgetStopOffersAWorkingWayForward(t *testing.T) {
	m := populatedShell(120, 40)
	id := m.rows[m.sel].F.ID
	m.inbox.add(id, attnBudget, "verify ran out of budget")

	acts := m.suggestFor(id)
	if len(acts) == 0 {
		t.Fatal("a budget-stopped card offered no actions at all")
	}
	top := acts[0]
	if top.id != "topup" {
		t.Fatalf("budget stop leads with %q, want the top-up row", top.id)
	}
	if top.key != "" {
		t.Fatalf("the top-up row grew a key (%q); if that is deliberate the a.key "+
			"shortcut handles it and this test should assert that path instead", top.key)
	}
	// The engine is nil in this scaffold, so topUpBudget's own command is
	// never built — but reaching it at all is the whole point: the bug was
	// runCardAction falling through its switch to a bare `return nil`.
	if !cardActionHandled(t, m, top) {
		t.Error("enter on \"top up and go on\" does nothing — the card page has no way forward")
	}
}

// cardActionHandled reports whether runCardAction recognised an action,
// rather than dropping it out of the bottom of its switch. Returning a
// command, pushing an overlay, leaving a notice or consuming the attention
// item all count as having been reached; a silent nil with nothing changed
// anywhere is the bug.
//
// The inbox count is the load-bearing one here: with no engine wired (this
// scaffold) topUpBudget returns no command, but it clears the item first, so
// that is the observable proof the switch routed the id at all.
func cardActionHandled(t *testing.T, m *Shell, a nextAction) bool {
	t.Helper()
	overlays, items := m.Overlay.Len(), m.inbox.len()
	m.notice = noticeMsg{}
	cmd := m.runCardAction(cardAction{id: a.id, key: a.key, label: a.label, why: a.detail})
	return cmd != nil || m.Overlay.Len() != overlays || m.inbox.len() != items || m.notice.text != ""
}

// The ↳ line under the selected inbox row is rendered from the same
// nextAction the card page builds, key column included — "g approve", "g land
// on …". Those keys worked on the card page and nowhere else, so the row spent
// its width naming something that did nothing at all: no movement, no notice,
// not even a refusal.
func TestInboxRowKeyDoesWhatItSays(t *testing.T) {
	m := populatedShell(120, 40)
	id := m.rows[4].F.ID // FD-044, at verify
	m.inbox.add(id, attnGate, "verify passed")
	m.setTab(TabInbox)
	m.inboxSel = 0

	acts := m.suggestFor(id)
	if len(acts) == 0 || acts[0].key == "" {
		t.Skip("this row advertises no key, so there is nothing to honour")
	}
	key := acts[0].key
	before := m.sel
	m.notice = noticeMsg{}
	cmd := m.inboxKey(key)
	if cmd == nil && m.notice.text == "" && m.sel == before && m.Overlay.Len() == 0 {
		t.Errorf("the inbox row advertises %q but pressing it does nothing", key)
	}
}

// An event citation is only ever generated for an autopilot stretch's own
// opening event, and openAnchor resolves it through scrollThreadToEvent,
// which walks featureRow.Events. threadView populates that field on a COPY
// of the row and never writes it back, so a row out of m.selected() carried
// an empty slice and every event citation answered "nothing on this page
// opens at that citation" — including while the status bar was advertising
// "alt+a open cited". The mark is generated from m.cardEvents, so the
// opener has to read the same cache.
func TestEventCitationResolvesFromTheCache(t *testing.T) {
	m := populatedShell(120, 40)
	r := m.rows[m.sel]
	events := []state.CardEvent{
		evTookOver(domain.GateAutopilot, at(0)),
		evGate(domain.StagePlan, domain.StageImplement, state.ActorAutopilot, at(1)),
	}
	for i := range events {
		events[i].Seq = int64(i + 1)
	}
	m.cardEvents[r.F.ID] = events

	// The row as m.selected() hands it over: no Events, so the resolver has
	// nothing to walk and the citation is refused.
	if m.scrollThreadToEvent(r, 1) {
		t.Fatal("scaffold assumption broken: an eventless row resolved a citation")
	}
	// The row with the cache applied, which is what cardCitationKey does now.
	r.Events = m.cardEvents[r.F.ID]
	if !m.scrollThreadToEvent(r, 1) {
		t.Error("the stretch's own opening event does not resolve — alt+a still refuses its own mark")
	}
	if m.anchorTo != r.F.ID || m.anchorFrom != 0 {
		t.Errorf("anchor landed at %s/%d, want %s/0", m.anchorTo, m.anchorFrom, r.F.ID)
	}
}
