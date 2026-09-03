package ui

import (
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

// TestBG083EverySuperStateHasItsOwnGlyph is BG-083's regression test.
//
// stageGlyph encodes a card's super-state in the row marker's shape, so
// the board reads without colour. It named four of the five kanban
// groups and fell through to "?" — the marker for a stage it does not
// recognise — for SuperResearch, so an ordinary research card sitting at
// investigate or shape read as one the board could not classify.
//
// The assertion is over domain.SuperStates rather than over research
// alone: the defect was a switch that fell behind the list it switches
// on, and a sixth group added later would land in exactly the same hole.
// Distinctness is asserted too, since a case that returns another
// group's glyph loses the same information more quietly.
func TestBG083EverySuperStateHasItsOwnGlyph(t *testing.T) {
	// one stage per super-state, so the glyph is reached the way a board
	// row reaches it
	stages := map[domain.SuperState]domain.Stage{}
	for _, st := range domain.Stages {
		if _, seen := stages[st.SuperState()]; !seen {
			stages[st.SuperState()] = st
		}
	}

	seen := map[string]domain.SuperState{}
	for _, super := range domain.SuperStates {
		st, ok := stages[super]
		if !ok {
			t.Errorf("no stage maps to super-state %q", super)
			continue
		}
		g := stageGlyph(st)
		if g == "?" {
			t.Errorf("super-state %q (stage %q) draws the unknown-stage marker on the board", super, st)
			continue
		}
		if other, dup := seen[g]; dup {
			t.Errorf("super-states %q and %q share the glyph %q, so the row's shape cannot tell them apart", other, super, g)
		}
		seen[g] = super
	}
}

// TestBG083ResearchCardRowShowsItsGlyph is the same defect at the
// surface it was seen on: the board row a research card actually draws.
func TestBG083ResearchCardRowShowsItsGlyph(t *testing.T) {
	m := populatedShell(140, 40)
	ws, store, wt := uiRepo(t)
	m.Attach(store, wt, ws)

	f := mkFeature(t, store, 6, "a topic being investigated", domain.StageInvestigate)
	f.Kind = domain.KindResearch
	r := featureRow{F: f}
	m.rows = []featureRow{r}

	if got := stageGlyph(f.Stage); got == "?" {
		t.Fatalf("a research card at investigate draws %q on its board row", got)
	}
}
