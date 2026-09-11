package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
)

// TestQuestionMarkOpensHelpOverAnOpenReviewSurface is §2.1's regression.
// specview.go's own key table lists "?" with bar: true, so the footer on
// both the spec and diff surfaces advertises it — but the card page opens
// with its thread composer focused and never blurs it (openSurfaces'
// doc comment, keymap_tiers_test.go), so textEntry() used to report true
// there regardless of what was actually drawn over the composer, and "?"
// typed into a draft nobody was looking at instead of opening help. It
// is the one key the spec/diff tables list that did nothing.
//
// A card is opened for real (cardOpen + a focused thread composer) on
// top of each surface, matching what the live page does — openSurfaces
// in keymap_tiers_test.go builds the surfaces alone, which is why that
// suite never caught this: without cardOpen the composer is never in the
// way to begin with.
func TestQuestionMarkOpensHelpOverAnOpenReviewSurface(t *testing.T) {
	id, _ := domain.NewFeatureID(1)
	f := domain.Feature{ID: id, Num: 1, Title: "x", Slug: "x", Stage: domain.StagePlan}
	content := "## Problem\n\nA line to sit on.\n"

	cases := map[string]func(*Shell){
		"spec": func(m *Shell) {
			m.spec = &specView{f: f, path: "p.md", content: content, doc: spec.Parse(content), cursor: 1}
		},
		"diff": func(m *Shell) {
			m.diff = newDiffView(f, "diff --git a/x b/x\n@@ -1 +1 @@\n+x\n", nil)
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			m := populatedShell(100, 30)
			setup(m)
			m.cardOpen = true
			m.focusThreadInput()
			if !m.threadInput.Focused() {
				t.Fatal("precondition: the card page opens with the composer focused")
			}
			before := m.threadInput.Value()

			m.handleKey(tea.KeyPressMsg{Code: '?', Text: "?"})

			if !m.Overlay.Contains("help") {
				t.Errorf("? over an open %s surface did not open help", name)
			}
			if got := m.threadInput.Value(); got != before {
				t.Errorf("? over an open %s surface typed into the composer instead: draft %q -> %q", name, before, got)
			}
		})
	}
}

// TestQuestionMarkTypesIntoAnUnobscuredThreadComposer is the other half
// pinned in the same test, per the review's instruction: with no spec or
// diff surface open, the card's thread composer is what the user is
// actually looking at, and "?" is ordinary punctuation there — the exact
// case textEntry() exists to protect (its own doc comment). This must
// keep working exactly as before.
func TestQuestionMarkTypesIntoAnUnobscuredThreadComposer(t *testing.T) {
	m := populatedShell(100, 30)
	m.cardOpen = true
	m.focusThreadInput()
	if !m.threadInput.Focused() {
		t.Fatal("precondition: the card page opens with the composer focused")
	}
	if m.spec != nil || m.diff != nil {
		t.Fatal("precondition: no review surface should be open")
	}

	m.handleKey(tea.KeyPressMsg{Code: '?', Text: "?"})

	if m.Overlay.Contains("help") {
		t.Error("? opened help over a bare thread composer instead of typing into it")
	}
	if got := m.threadInput.Value(); got != "?" {
		t.Errorf("composer = %q, want the literal \"?\" typed", got)
	}
}
