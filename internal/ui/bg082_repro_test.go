package ui

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

// TestBG082BoardCountsResearchCards is BG-082's regression test.
//
// boardCounts walks domain.SuperStates and appends formatCount for every
// super-state holding a card. formatCount named four of the five and
// returned "" for the research super-state, and the caller appended that
// empty string, so strings.Join rendered the missing count as a separator
// with nothing between it: "1 todo · 2 active ·  · 1 in review". The
// summary was the only surface refusing to count a card the board itself
// listed — and the shape of that refusal was stray punctuation rather
// than a visible absence.
//
// Research is no longer a super-state of its own — one graph means a
// research card is at the same stages every other card is, and counts as
// active like them — so what is asserted here is the surviving half: every
// card lands in some count, and no super-state can put a hole in the bar
// even if its wording is missing.
func TestBG082BoardCountsResearchCards(t *testing.T) {
	m := populatedShell(140, 40)
	ws, store, wt := uiRepo(t)
	m.Attach(store, wt, ws)

	todo := mkFeature(t, store, 1, "a backlog card", domain.StageTodo)
	impl := mkFeature(t, store, 2, "a card being built", domain.StageImplement)
	inv := mkFeature(t, store, 3, "a topic being investigated", domain.StagePlan)
	inv.Kind = domain.KindResearch
	shape := mkFeature(t, store, 4, "a topic being shaped", domain.StagePlan)
	shape.Kind = domain.KindResearch
	m.rows = []featureRow{{F: todo}, {F: impl}, {F: inv}, {F: shape}}

	got := m.boardCounts()

	// the two research cards are at a work stage like any other card, so
	// they count as active alongside the one being built.
	if !strings.Contains(got, "3 active") {
		t.Errorf("the board summary does not count the two research cards: %q", got)
	}
	// the dangling separator is the visible symptom, and it must not come
	// back by any route: no empty segment between two separators, and none
	// trailing off either end.
	if strings.Contains(got, "·  ·") {
		t.Errorf("the board summary renders an empty segment between separators: %q", got)
	}
	for _, seg := range strings.Split(got, " · ") {
		if strings.TrimSpace(seg) == "" {
			t.Errorf("the board summary has an empty segment: %q", got)
		}
	}
	// the counts the bar already carried are unchanged
	for _, want := range []string{"1 todo", "3 active"} {
		if !strings.Contains(got, want) {
			t.Errorf("the board summary lost %q: %q", want, got)
		}
	}
}

// TestBG082EverySuperStateIsNamed guards the root cause rather than the
// symptom: formatCount is the only thing standing between a super-state
// and a hole in the status bar, so every member of domain.SuperStates
// must have a wording. A new kanban group added without one would
// otherwise reintroduce BG-082 silently.
func TestBG082EverySuperStateIsNamed(t *testing.T) {
	for _, super := range domain.SuperStates {
		if got := formatCount(super, 1); got == "" {
			t.Errorf("super-state %q has no wording in formatCount, so the board summary cannot count it", super)
		}
	}
}
