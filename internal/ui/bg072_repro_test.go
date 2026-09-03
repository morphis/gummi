package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
)

// TestBG072SpecOpensAtTheSectionTheCardPointsAt is BG-072's regression
// test. The thread pins the artifact with the section the current stage
// is about ("⌄ spec · Implementation notes ─ s"), and the plan gate's own
// decision row says the plan "lives in the spec's Implementation notes".
// Both then opened the document at line 1 and left the reader to find it.
func TestBG072SpecOpensAtTheSectionTheCardPointsAt(t *testing.T) {
	doc := "# FD-001: a card\n\n## Problem\n\nsomething is wrong.\n\n" +
		"## Out of scope\n\nnot this.\n\n## Considered approaches\n\none, two.\n\n" +
		"## Chosen approach\n\nthe first.\n\n## Implementation notes\n\n1. do the thing.\n"

	m := populatedShell(100, 30)
	f := m.rows[m.sel].F
	f.Stage = domain.StagePlan
	f.Kind = domain.KindFeature

	// the section the thread's pinned line promises for this stage
	section := currentSpecSection(f.Kind, f.Stage)
	if section != "Implementation notes" {
		t.Fatalf("precondition: the plan stage pins %q", section)
	}
	want, ok := spec.HeadingLine(doc, section)
	if !ok {
		t.Fatal("precondition: the fixture has no such heading")
	}

	model, _ := m.Update(specLoadedMsg{f: f, path: "/tmp/spec.md", content: doc})
	m = model.(*Shell)
	if m.spec == nil {
		t.Fatal("the spec surface did not open")
	}
	if m.spec.cursor != want {
		t.Errorf("opened at line %d, want %d (the %q heading) — the reader has to hunt for the section the card just pointed at",
			m.spec.cursor, want, section)
	}
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "## "+section) {
		t.Errorf("the section is not even on screen:\n%s", view)
	}

	// a document that has no such heading still opens at the top rather
	// than somewhere arbitrary
	model, _ = m.Update(specLoadedMsg{f: f, path: "/tmp/other.md", content: "# draft\n\nnothing yet.\n"})
	if got := model.(*Shell).spec.cursor; got != 1 {
		t.Errorf("a document without the section opened at line %d, want 1", got)
	}

}
