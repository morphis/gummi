package ui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// foldedStagesShell is a card page whose thread carries several finished
// stages above its current one — the shape every real card has after a
// few sessions, and the one no golden covered until this file. The plan
// stage deliberately records no message turns: an artifact written and
// critiqued has no conversation to count, and its receipt must say
// nothing about turns rather than "0 turns".
func foldedStagesShell(t *testing.T, w, h int) *Shell {
	t.Helper()
	m := populatedShell(w, h)
	m.sel = 1 // FD-042, at implement
	id := m.rows[m.sel].F.ID
	at := time.Date(2026, 8, 1, 12, 4, 0, 0, time.UTC)
	enter := func(role string) string {
		p, _ := json.Marshal(map[string]string{"role": role, "model": "claude-sonnet"})
		return string(p)
	}
	exit, _ := json.Marshal(map[string]any{"verdict": "pass"})
	msg := func(author, content string) string {
		p, _ := json.Marshal(map[string]string{"author": author, "content": content})
		return string(p)
	}
	m.cardEvents[id] = []state.CardEvent{
		{Kind: state.EventStageEnter, Stage: domain.StageBrainstorm, At: at, Payload: enter("architect")},
		{Kind: state.EventMessage, Stage: domain.StageBrainstorm, At: at, Payload: msg("user", "where should it live?")},
		{Kind: state.EventMessage, Stage: domain.StageBrainstorm, At: at, Payload: msg("architect", "at the theme layer.")},
		{Kind: state.EventStageExit, Stage: domain.StageBrainstorm, At: at.Add(7 * time.Minute), Payload: string(exit)},

		{Kind: state.EventStageEnter, Stage: domain.StageSpec, At: at.Add(8 * time.Minute), Payload: enter("architect")},
		{Kind: state.EventMessage, Stage: domain.StageSpec, At: at.Add(8 * time.Minute), Payload: msg("architect", "spec written.")},
		{Kind: state.EventStageExit, Stage: domain.StageSpec, At: at.Add(11 * time.Minute), Payload: string(exit)},

		// a plan stage with no message turns at all: the receipt must not
		// claim "0 turns"
		{Kind: state.EventStageEnter, Stage: domain.StagePlan, At: at.Add(12 * time.Minute), Payload: enter("architect")},
		{Kind: state.EventStageExit, Stage: domain.StagePlan, At: at.Add(15 * time.Minute), Payload: string(exit)},

		{Kind: state.EventStageEnter, Stage: domain.StageImplement, At: at.Add(16 * time.Minute), Payload: enter("implementer")},
	}
	m.cardOpen = true
	return m
}

// TestFoldedStagesGolden is the review surface for a card's folded
// history: several finished stages, each one row, above the stage that is
// still open. Nothing covered this before — every prior golden showed at
// most the pinned spec line's own ⌄.
func TestFoldedStagesGolden(t *testing.T) {
	m := foldedStagesShell(t, 100, 30)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	golden.RequireEqual(t, []byte(model.(*Shell).View().Content))
}

// TestFoldedReceiptOmitsZeroTurns: a stage that recorded no message turns
// says nothing about turns. "0 turns" counts a thing that did not happen,
// and a plan stage that only wrote and critiqued an artifact has none.
func TestFoldedReceiptOmitsZeroTurns(t *testing.T) {
	m := foldedStagesShell(t, 100, 30)
	view := ansi.Strip(m.threadView(100, 30))
	if strings.Contains(view, "0 turns") {
		t.Errorf("a turnless stage claimed \"0 turns\":\n%s", view)
	}
	if !strings.Contains(view, "2 turns") {
		t.Errorf("the brainstorm's two turns were not counted:\n%s", view)
	}
	if !strings.Contains(view, "1 turn") || strings.Contains(view, "1 turns") {
		t.Errorf("a single turn was not rendered in the singular:\n%s", view)
	}
}

// TestThreadNarrowDecisionGolden is the 36×9 frame — the width the design
// says the thread is actually driven at, and one no golden covered. What
// must survive here is the card's identity, the question, the highlighted
// answer and the line to type on; what yields is the crumb, the composer's
// blank row, and the body.
func TestThreadNarrowDecisionGolden(t *testing.T) {
	m := reviewGateWorkspace(t)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 36, Height: 9})
	m = model.(*Shell)
	golden.RequireEqual(t, []byte(m.View().Content))
}

// TestNarrowKeepsTheQuestionWhole: at 36 columns the question moves onto
// its own row rather than being truncated to keep the title company. A
// control whose question you cannot read is not one you can answer, and
// the design's own rule for this width is that the question never yields.
func TestNarrowKeepsTheQuestionWhole(t *testing.T) {
	m := reviewGateWorkspace(t)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 36, Height: 9})
	m = model.(*Shell)
	view := ansi.Strip(m.View().Content)
	// the question wraps onto its own rows rather than being cut short, so
	// it is checked in the pieces the wrap leaves — the point is that none
	// of it was dropped, not which column it broke at
	for _, part := range []string{"review is ready for your", "decision."} {
		if !strings.Contains(view, part) {
			t.Errorf("the question lost %q at 36 columns:\n%s", part, view)
		}
	}
	if strings.Contains(view, "ready for y…") {
		t.Errorf("the question was truncated rather than wrapped:\n%s", view)
	}
	// the crumb yields so the card's identity can stay
	if strings.Contains(view, "esc backlog") {
		t.Errorf("the crumb held its row on a nine-row terminal:\n%s", view)
	}
	if !strings.Contains(view, "FD-001") {
		t.Errorf("the card's own identity is missing at 36×9:\n%s", view)
	}
}

// TestWideKeepsTheQuestionBesideTheTitle: the wrap is a narrow-terminal
// measure only — at the widths a decision is normally read at, the title
// and its question share one row exactly as before.
func TestWideKeepsTheQuestionBesideTheTitle(t *testing.T) {
	s := m0Styles()
	head := pickerHead(s, "gummi", "review is ready for your decision.", 100)
	if len(head) != 1 {
		t.Fatalf("wide head = %q, want the title and question on one row", head)
	}
	narrow := pickerHead(s, "gummi", "review is ready for your decision.", 30)
	if len(narrow) < 2 {
		t.Fatalf("narrow head = %q, want the question on its own row", narrow)
	}
	if len(narrow) > pickerQuestionLines+1 {
		t.Errorf("narrow head ran to %d rows, want the title plus at most %d", len(narrow), pickerQuestionLines)
	}
}
