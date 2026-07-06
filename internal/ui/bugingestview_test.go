package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
)

func sampleBugImport() engine.BugIngestResult {
	return engine.BugIngestResult{
		Source: "acme/app",
		Proposals: []domain.BugProposal{
			{Title: "Login loops", ExternalRef: "https://x/1"},
			{Title: "Logout crash", ExternalRef: "https://x/2"},
			{Title: "Footer typo", ExternalRef: "https://x/3"},
		},
		Skipped: []domain.BugProposal{{Title: "old", ExternalRef: "https://x/0"}},
	}
}

func TestBugImportFilterNarrowsVisibleAndKept(t *testing.T) {
	bv := newBugIngestView(sampleBugImport(), "thrifty", 0)
	if bv.keptCount() != 3 || len(bv.visible()) != 3 {
		t.Fatalf("unfiltered: keep=%d visible=%d, want 3/3", bv.keptCount(), len(bv.visible()))
	}

	bv.filter.SetValue("log") // matches "Login loops" and "Logout crash"
	vis := bv.visible()
	if len(vis) != 2 {
		t.Fatalf("filter 'log' visible = %d, want 2", len(vis))
	}
	kept := bv.kept()
	if len(kept) != 2 || kept[0].Title != "Login loops" || kept[1].Title != "Logout crash" {
		t.Errorf("filtered kept = %+v", kept)
	}

	// a filter matching nothing hides everything and keeps nothing.
	bv.filter.SetValue("zzz")
	if len(bv.visible()) != 0 || bv.keptCount() != 0 {
		t.Errorf("non-matching filter: visible=%d keep=%d, want 0/0", len(bv.visible()), bv.keptCount())
	}
}

func TestBugImportDropRespectsFilter(t *testing.T) {
	bv := newBugIngestView(sampleBugImport(), "thrifty", 0)
	bv.filter.SetValue("log")
	// cursor 0 within the filtered view is "Login loops"; dropping it must
	// hit that proposal, not props[0] of the full list (same here, but the
	// index mapping is what we're checking).
	bv.cursor = 1 // "Logout crash"
	if i := bv.selected(); i != 1 {
		t.Fatalf("selected props index = %d, want 1", i)
	}
	bv.props[bv.selected()].dropped = true
	if bv.keptCount() != 1 {
		t.Errorf("keep after drop = %d, want 1", bv.keptCount())
	}
	// clearing the filter reveals all three; the drop persists.
	bv.filter.SetValue("")
	if len(bv.visible()) != 3 || bv.keptCount() != 2 {
		t.Errorf("after clear: visible=%d keep=%d, want 3/2", len(bv.visible()), bv.keptCount())
	}
}

func TestBugImportFilterKeyFlow(t *testing.T) {
	m := &Shell{}
	m.bugIngest = newBugIngestView(sampleBugImport(), "thrifty", 0)
	bv := m.bugIngest

	// '/' enters filter mode; typing narrows live.
	m.handleBugIngestKey(tea.KeyPressMsg{Code: '/', Text: "/"})
	if !bv.filtering {
		t.Fatal("'/' did not enter filter mode")
	}
	for _, r := range "foot" {
		m.handleBugIngestKey(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if len(bv.visible()) != 1 {
		t.Fatalf("after typing 'foot' visible = %d, want 1 (Footer typo)", len(bv.visible()))
	}

	// enter locks the filter in (query kept, editing off).
	m.handleBugIngestKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if bv.filtering || !bv.active() {
		t.Errorf("after enter: filtering=%v active=%v, want false/true", bv.filtering, bv.active())
	}

	// re-enter and esc clears the filter back to the full list.
	m.handleBugIngestKey(tea.KeyPressMsg{Code: '/', Text: "/"})
	m.handleBugIngestKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if bv.filtering || bv.active() || len(bv.visible()) != 3 {
		t.Errorf("after esc-clear: filtering=%v active=%v visible=%d, want false/false/3", bv.filtering, bv.active(), len(bv.visible()))
	}

	// esc when not filtering discards the whole import.
	m.handleBugIngestKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.bugIngest != nil {
		t.Error("esc (not filtering) should discard the import")
	}
}
