package engine

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

// TestKickoffMessageNamesTheCardsOwnDocument pins the engine half of the
// artifact-noun contract.
//
// The stage kickoff is the go-ahead sent to open an autonomous stage,
// and the thread renders it back to the reader as a system turn. It was
// a fixed string ending "…and the spec." on every kind, so a bug card
// and a research card were both told to consult a document that is not
// theirs — while the pinned line, the inventory row and the viewer
// header on the same page all named the document correctly (BG-079,
// BG-081). The kickoff was the last surface still disagreeing.
//
// Asserted through Session.kickoffMessage rather than the helper it
// calls: that is the string the agent actually receives and the thread
// actually renders, and it reads the noun off the session's own card.
func TestKickoffMessageNamesTheCardsOwnDocument(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind domain.Kind
		want string
	}{
		{"feature", domain.KindFeature, "spec"},
		{"bug", domain.KindBug, "bug report"},
		{"research", domain.KindResearch, "research document"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := feature(1, "a card's kickoff", domain.StageImplement)
			f.Kind = tc.kind
			got := (&Session{Feature: f}).kickoffMessage()

			if !strings.Contains(got, "the "+tc.want+".") {
				t.Errorf("a %s card's kickoff = %q, want it to name the %s", tc.kind, got, tc.want)
			}
			// and never another kind's word: "spec" leaking onto a bug or a
			// research card is the whole defect
			for _, other := range []string{"spec", "bug report", "research document"} {
				if other == tc.want {
					continue
				}
				if strings.Contains(got, other) {
					t.Errorf("a %s card's kickoff = %q, which names another kind's document (%q)", tc.kind, got, other)
				}
			}
		})
	}
}

// TestKickoffMessageLeavesRebaseAlone: a rebase-resolve session opens
// with its own go-ahead, which names no document at all, and the noun
// change must not reach it.
func TestKickoffMessageLeavesRebaseAlone(t *testing.T) {
	s := &Session{Feature: bugFeature("rebasing"), Rebase: true}
	if got := s.kickoffMessage(); got != rebaseKickoff {
		t.Errorf("a rebase session's kickoff = %q, want the rebase go-ahead unchanged (%q)", got, rebaseKickoff)
	}
}

// TestKickoffMessageKeepsItsNote: the review comments RunWith appends
// still ride the kickoff, below the go-ahead.
func TestKickoffMessageKeepsItsNote(t *testing.T) {
	f := feature(1, "with comments", domain.StagePlan)
	s := &Session{Feature: f, kickoffNote: "- L12: split the migration"}
	got := s.kickoffMessage()
	if !strings.Contains(got, "the spec.") || !strings.Contains(got, "split the migration") {
		t.Errorf("kickoff lost its note or its noun: %q", got)
	}
}
