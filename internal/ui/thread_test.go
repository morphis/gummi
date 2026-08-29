package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui/theme"
)

// TestStageSequence checks that the thread's stage strip is derived from
// the workflow package rather than a hardcoded string, and that it
// differs correctly across kinds and skip flags.
func TestStageSequence(t *testing.T) {
	cases := []struct {
		name string
		f    domain.Feature
		want []domain.Stage
	}{
		{
			"feature, no skips",
			domain.Feature{Kind: domain.KindFeature},
			[]domain.Stage{
				domain.StageTodo, domain.StageBrainstorm, domain.StageSpec, domain.StagePlan,
				domain.StageImplement, domain.StageReview, domain.StageVerify, domain.StageDone,
			},
		},
		{
			"feature, brainstorm and plan skipped",
			domain.Feature{Kind: domain.KindFeature, Skip: domain.SkipFlags{Brainstorm: true, Plan: true}},
			[]domain.Stage{
				domain.StageTodo, domain.StageSpec, domain.StageImplement,
				domain.StageReview, domain.StageVerify, domain.StageDone,
			},
		},
		{
			"bug, no skips",
			domain.Feature{Kind: domain.KindBug},
			[]domain.Stage{
				domain.StageTodo, domain.StageTriage, domain.StageDiagnose, domain.StageFix,
				domain.StageReview, domain.StageVerify, domain.StageDone,
			},
		},
		{
			"research",
			domain.Feature{Kind: domain.KindResearch},
			[]domain.Stage{
				domain.StageTodo, domain.StageInvestigate, domain.StageShape,
				domain.StageReview, domain.StageVerify, domain.StageDone,
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := stageSequence(c.f)
			if len(got) != len(c.want) {
				t.Fatalf("stageSequence() = %v, want %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("stageSequence()[%d] = %s, want %s (full: %v)", i, got[i], c.want[i], got)
				}
			}
		})
	}
}

// TestStageSegmentsReconstructsHistory builds a synthetic event log —
// one finished brainstorm generation, one open (unclosed) spec
// generation — and checks stageSegments folds it into exactly what the
// thread's folded receipts and live-stage fallback need.
func TestStageSegmentsReconstructsHistory(t *testing.T) {
	t0 := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	events := []state.CardEvent{
		{
			Kind: state.EventStageEnter, Stage: domain.StageBrainstorm, At: t0,
			Payload: `{"role":"architect","model":"fake-model"}`,
		},
		{
			Kind: state.EventMessage, Stage: domain.StageBrainstorm, At: t0.Add(time.Minute),
			Payload: `{"author":"you","content":"hi"}`,
		},
		{
			Kind: state.EventMessage, Stage: domain.StageBrainstorm, At: t0.Add(2 * time.Minute),
			Payload: `{"author":"architect","content":"ok"}`,
		},
		{
			Kind: state.EventStageExit, Stage: domain.StageBrainstorm, At: t0.Add(4 * time.Minute),
			Payload: `{"verdict":"","credits":6}`,
		},
		{
			Kind: state.EventStageEnter, Stage: domain.StageSpec, At: t0.Add(5 * time.Minute),
			Payload: `{"role":"architect","model":"fake-model"}`,
		},
		{
			Kind: state.EventTool, Stage: domain.StageSpec, At: t0.Add(6 * time.Minute), Status: state.StatusOK,
			Payload: `{"label":"edit spec.md"}`,
		},
	}
	segs := stageSegments(events)
	if len(segs) != 2 {
		t.Fatalf("stageSegments() = %d segments, want 2: %+v", len(segs), segs)
	}
	first := segs[0]
	if first.stage != domain.StageBrainstorm || !first.exited || first.credits != 6 {
		t.Errorf("first segment wrong: %+v", first)
	}
	if got := len(first.events); got != 2 {
		t.Errorf("first segment carries %d events, want 2 (the two messages)", got)
	}
	second := segs[1]
	if second.stage != domain.StageSpec || second.exited {
		t.Errorf("second segment should be the open (live) one: %+v", second)
	}
	if len(second.events) != 1 {
		t.Errorf("second segment carries %d events, want 1 (the tool call)", len(second.events))
	}
}

// TestFoldedReceiptLineIsOneLine is the fold's whole point: whatever a
// finished stage carried, it renders as exactly one line.
func TestFoldedReceiptLineIsOneLine(t *testing.T) {
	seg := stageSegment{
		stage: domain.StageBrainstorm, role: "architect", exited: true,
		credits: 6, exitAt: time.Date(2026, 8, 1, 12, 4, 0, 0, time.UTC),
		events: []state.CardEvent{
			{Kind: state.EventMessage}, {Kind: state.EventMessage}, {Kind: state.EventTool},
		},
	}
	line := ansi.Strip(foldedReceiptLine(m0Styles(), seg, 80))
	if strings.Contains(line, "\n") {
		t.Fatalf("folded receipt spans more than one line: %q", line)
	}
	for _, want := range []string{"brainstorm", "architect", "2 turns", "6 credits", "12:04"} {
		if !strings.Contains(line, want) {
			t.Errorf("folded receipt %q missing %q", line, want)
		}
	}
}

// TestPinnedSpecLineNamesOpenQuestions checks the pinned line's anchor
// (section) and its open-%%-count badge.
func TestPinnedSpecLineNamesOpenQuestions(t *testing.T) {
	r := featureRow{
		F:          domain.Feature{Kind: domain.KindFeature, Stage: domain.StageSpec},
		OpenSpecQs: 2,
	}
	line := ansi.Strip(pinnedSpecLine(m0Styles(), r))
	for _, want := range []string{"spec", "Chosen approach", "2 open %%", "s"} {
		if !strings.Contains(line, want) {
			t.Errorf("pinned spec line %q missing %q", line, want)
		}
	}

	// a stage with no natural section (todo) renders nothing to pin.
	r.F.Stage = domain.StageTodo
	if got := pinnedSpecLine(m0Styles(), r); got != "" {
		t.Errorf("pinnedSpecLine at todo = %q, want empty", got)
	}
}

// TestAutopilotLabel: the header shows exactly what the card carries,
// without inventing new vocabulary (empty reads as auto).
func TestAutopilotLabel(t *testing.T) {
	cases := map[string]string{
		"":                domain.GateAuto,
		domain.GateAuto:   domain.GateAuto,
		domain.GateCaller: domain.GateCaller,
	}
	for in, want := range cases {
		if got := autopilotLabel(in); got != want {
			t.Errorf("autopilotLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestInputBlockWithholdsForForeignCard: carry-over #2 — a card another
// gummi process drives has no session this board can send to, so the
// thread must withhold the input the same way newFollowPane does,
// rather than rendering a box that would fail at send time.
func TestInputBlockWithholdsForForeignCard(t *testing.T) {
	r := featureRow{
		F:            domain.Feature{ID: "FD-001"},
		DrivenAbroad: true,
		Foreign:      state.ForeignDrive{PID: 4242},
	}
	out := ansi.Strip(inputBlock(m0Styles(), r, 60))
	if !strings.Contains(out, "read-only") || !strings.Contains(out, "4242") {
		t.Errorf("withheld input block = %q, want it to name the read-only reason and the owning pid", out)
	}
	if strings.Contains(out, "message the agent") {
		t.Errorf("withheld input block = %q, should not render the live message box", out)
	}
}

// TestNextCardBlockRendersNextActionsVerbatim: the thread's "next" card
// is nextsteps.go's nextActions(nextInput), not a reimplementation of it.
func TestNextCardBlockRendersNextActionsVerbatim(t *testing.T) {
	in := nextInput{stage: domain.StageTodo, kind: domain.KindFeature}
	want := nextActions(in)
	lines := nextCardBlock(m0Styles(), in)
	if len(lines) != len(want)+1 { // +1 for the "next" header line
		t.Fatalf("nextCardBlock() has %d lines, want %d (header + one per action)", len(lines), len(want)+1)
	}
	for i, a := range want {
		got := ansi.Strip(lines[i+1])
		if !strings.Contains(got, a.key) || !strings.Contains(got, a.label) || !strings.Contains(got, a.why) {
			t.Errorf("nextCardBlock line %q does not carry action %+v verbatim", got, a)
		}
	}
}

// TestThreadViewDegradesWithoutEvents: a card page opened with nothing
// loaded into the event cache yet must still render the header, the
// pinned spec line and the next card, simply omitting the folded
// receipts — and never panic.
func TestThreadViewDegradesWithoutEvents(t *testing.T) {
	m := populatedShell(120, 34)
	view := ansi.Strip(m.threadView(116, 30))
	if !strings.Contains(view, "FD-042") {
		t.Errorf("thread view missing the card identity:\n%s", view)
	}
	if strings.Contains(view, "⌄ brainstorm") {
		t.Errorf("thread view rendered a folded receipt with no events loaded:\n%s", view)
	}
}

// TestOpenCardLoadsEventsIntoTheThread is the end-to-end wiring check:
// opening the card page fires loadCardEvents (shell.go), and once it
// lands the thread's folded receipts show a finished stage's history —
// all through the real key-handling path (backlog.go's backlogKey). It
// needs a feature actually persisted in the store (not just an
// in-memory row) — card_events foreign-keys onto it.
func TestOpenCardLoadsEventsIntoTheThread(t *testing.T) {
	ws, store, wt := uiRepo(t)
	m := NewShell(theme.GummiDark(), "v0.1.0-test")
	m.Attach(store, wt, ws)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	m = model.(*Shell)

	ctx := context.Background()
	id, err := domain.NewFeatureID(1)
	if err != nil {
		t.Fatal(err)
	}
	f := domain.Feature{
		ID: id, Num: 1, Title: "dark mode", Slug: "dark-mode", Stage: domain.StageSpec,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := store.CreateFeature(ctx, &f); err != nil {
		t.Fatal(err)
	}

	t0 := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	seed := []state.CardEvent{
		{
			Feature: id, Kind: state.EventStageEnter, Stage: domain.StageBrainstorm, At: t0,
			Payload: `{"role":"architect","model":"fake-model"}`,
		},
		{
			Feature: id, Kind: state.EventStageExit, Stage: domain.StageBrainstorm, At: t0.Add(3 * time.Minute),
			Payload: `{"verdict":"","credits":4}`,
		},
		{
			Feature: id, Kind: state.EventStageEnter, Stage: domain.StageSpec, At: t0.Add(4 * time.Minute),
			Payload: `{"role":"architect","model":"fake-model"}`,
		},
	}
	for _, ev := range seed {
		if err := store.AppendEvent(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}

	m = pump(t, m, m.Init())
	if len(m.rows) != 1 || m.rows[0].F.ID != id {
		t.Fatalf("row not loaded for the persisted feature: %+v", m.rows)
	}

	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.cardOpen {
		t.Fatal("enter should open the card page")
	}
	if _, ok := m.cardEvents[id]; !ok {
		t.Fatalf("opening the card page did not load its event log into the cache")
	}

	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "⌄ brainstorm") {
		t.Errorf("thread view missing the folded brainstorm receipt once events loaded:\n%s", view)
	}
}

// m0Styles is a plain style set for thread-rendering unit tests that
// don't need a whole Shell.
func m0Styles() *theme.Styles { return NewShell(theme.GummiDark(), "test").styles }
