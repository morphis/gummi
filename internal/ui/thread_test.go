package ui

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
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
	line := ansi.Strip(foldedReceiptLine(m0Styles(), seg, nil, 80))
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
	line := ansi.Strip(pinnedSpecLine(m0Styles(), r, 80))
	for _, want := range []string{"spec", "Chosen approach", "2 open %%", "s"} {
		if !strings.Contains(line, want) {
			t.Errorf("pinned spec line %q missing %q", line, want)
		}
	}

	// a stage with no natural section (todo) renders nothing to pin.
	r.F.Stage = domain.StageTodo
	if got := pinnedSpecLine(m0Styles(), r, 80); got != "" {
		t.Errorf("pinnedSpecLine at todo = %q, want empty", got)
	}
}

// TestAutopilotLabel: the header shows exactly what the card carries,
// without inventing new vocabulary (empty reads as gates).
func TestAutopilotLabel(t *testing.T) {
	cases := map[string]string{
		"":               domain.GateGates,
		domain.GateGates: domain.GateGates,
		domain.GateOff:   domain.GateOff,
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
	m := NewShell(theme.GummiDark(), "v0-test")
	r := featureRow{
		F:            domain.Feature{ID: "FD-001"},
		DrivenAbroad: true,
		Foreign:      state.ForeignDrive{PID: 4242},
	}
	out := ansi.Strip(m.inputBlock(m0Styles(), r, 60))
	if !strings.Contains(out, "read-only") || !strings.Contains(out, "4242") {
		t.Errorf("withheld input block = %q, want it to name the read-only reason and the owning pid", out)
	}
	if strings.Contains(out, "message the agent") {
		t.Errorf("withheld input block = %q, should not render the live message box", out)
	}
}

// TestThreadViewDegradesWithoutEvents: a card page opened with nothing
// loaded into the event cache yet must still render the header, the
// pinned spec line and composer, simply omitting the folded
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

// TestFoldedReceiptPrefersMeteredSpend: credits shown on a folded receipt
// come from the stage_spend rollup, the meter of record, not from the
// copy carried in the stage_exit event — the two are free to drift, and
// only one of them is authoritative.
func TestFoldedReceiptPrefersMeteredSpend(t *testing.T) {
	seg := stageSegment{stage: domain.StageImplement, role: "implementer", exited: true, credits: 6}
	metered := map[domain.Stage]float64{domain.StageImplement: 41}

	line := ansi.Strip(foldedReceiptLine(m0Styles(), seg, metered, 80))
	if !strings.Contains(line, "41 credits") {
		t.Errorf("receipt %q did not use the metered rollup", line)
	}
	if strings.Contains(line, "6 credits") {
		t.Errorf("receipt %q used the event payload over the meter", line)
	}
	// with no rollup loaded the payload is still better than nothing
	line = ansi.Strip(foldedReceiptLine(m0Styles(), seg, nil, 80))
	if !strings.Contains(line, "6 credits") {
		t.Errorf("receipt %q dropped the fallback", line)
	}
}

// The composer owns the keyboard the moment a card opens, the way a
// coding agent's does: you type your reply, you do not unlock a field
// first. The accelerators are still there, one esc away, with the draft
// kept — and the keys that are not text (enter on an empty line, the
// arrows, the page keys) keep working without leaving the line at all.
func TestThreadInputOwnsTheKeyboardOnOpen(t *testing.T) {
	m := attachedBoard(t, 120, 34)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // open the card page
	if !m.cardOpen {
		t.Fatal("enter did not open the card page")
	}
	if !m.threadInput.Focused() {
		t.Fatal("the composer is not focused on arrival — it has to be ready to type into")
	}

	// letters that used to be accelerators are just letters now
	m = typeString(t, m, "g and v are just letters here")
	if got := m.threadInput.Value(); got != "g and v are just letters here" {
		t.Fatalf("composer = %q, want the typed text verbatim", got)
	}

	// esc hands the keyboard back without eating the draft
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.threadInput.Focused() {
		t.Fatal("esc did not blur the composer")
	}
	if got := m.threadInput.Value(); got != "g and v are just letters here" {
		t.Fatalf("esc discarded the draft: %q", got)
	}
	if !m.cardOpen {
		t.Fatal("esc left the card page instead of only blurring")
	}

	// blurred, the accelerators answer again and do not leak into the line
	before := m.threadInput.Value()
	m = press(t, m, tea.KeyPressMsg{Code: 'J', Text: "J"})
	if m.threadInput.Value() != before {
		t.Fatalf("accelerator leaked into the composer: %q", m.threadInput.Value())
	}
}

// TestThreadInputUpOpensCardActions: with no decision open, ↑ on an empty
// composer raises the action inventory. The card used here is done — a
// stage with nothing to decide — because while a decision IS open the
// arrows belong to it (TestThreadDecisionOwnsThePickerKeys).
func TestThreadInputUpOpensCardActions(t *testing.T) {
	m := attachedBoard(t, 120, 34)
	m.sel = 5 // FD-039, done — no open decision, so ↑ is the inventory's door
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyUp})

	top, ok := m.Overlay.Top().(*cardActionsDialog)
	if !ok {
		t.Fatalf("up on the empty composer opened %T, want cardActionsDialog", m.Overlay.Top())
	}
	if top.list.Len() != len(top.list.actions) {
		t.Fatalf("popover shows %d navigable rows for %d legal actions", top.list.Len(), len(top.list.actions))
	}
	before := top.list.cursor
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	if top.list.cursor != before+1 {
		t.Fatalf("down moved cursor to %d, want %d", top.list.cursor, before+1)
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.Overlay.HasDialogs() {
		t.Fatal("esc did not return from the action pop-over to the composer")
	}

	m = typeString(t, m, "draft")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	if m.Overlay.HasDialogs() {
		t.Fatal("up opened actions while the composer contained text")
	}
}

// TestThreadDecisionOwnsThePickerKeys: while a decision is open and the
// composer is empty, ↑↓ move its cursor (wrapping, like the chat pane's
// picker) and enter answers the highlighted option — the arrows are the
// decision's, not the inventory's, so there is still only ever one list
// on screen (DESIGN §6.3).
func TestThreadDecisionOwnsThePickerKeys(t *testing.T) {
	m := attachedBoard(t, 120, 34)
	m.sel = 3 // FD-049, spec — nothing running, so a decision is open
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	d := m.openDecision(m.rows[m.sel])
	if d == nil || len(d.actions) < 2 {
		t.Fatalf("precondition: an idle spec card has a multi-option decision, got %+v", d)
	}

	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyUp}) // no pop-over may open
	if _, ok := m.Overlay.Top().(*cardActionsDialog); ok {
		t.Fatal("up opened the action pop-over while a decision was open")
	}
	if m.decisionCursor != len(d.actions)-1 {
		t.Fatalf("up moved the decision cursor to %d, want %d (wrapped to the last option)", m.decisionCursor, len(d.actions)-1)
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	if m.decisionCursor != 1 {
		t.Fatalf("downs moved the decision cursor to %d, want 1 (wrapped back around)", m.decisionCursor)
	}
}

// TestThreadInputSurvivesTabSwitch: the unsent buffer has to survive
// leaving and returning to the board tab, the same rule the chat pane's
// own m.chat already honours. Leaving the board tab closes the card page
// itself (tabs.go's setTab — unrelated to this feature, and unchanged by
// it), but the draft is a Shell field, not a child of that page, so it is
// still there once the card page is reopened.
func TestThreadInputSurvivesTabSwitch(t *testing.T) {
	m := attachedBoard(t, 120, 34)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = typeString(t, m, "not sent yet")

	m = press(t, m, tea.KeyPressMsg{Code: '2', Mod: tea.ModAlt}) // -> inbox tab
	if m.tab != TabInbox {
		t.Fatalf("tab = %v, want inbox", m.tab)
	}
	if m.cardOpen {
		t.Fatal("leaving the board tab should close the card page (tabs.go, unrelated to this feature)")
	}
	m = press(t, m, tea.KeyPressMsg{Code: '1', Mod: tea.ModAlt}) // -> board tab
	if m.tab != TabBoard {
		t.Fatalf("tab = %v, want board", m.tab)
	}
	if m.threadInput.Value() != "not sent yet" {
		t.Fatalf("draft lost across a tab switch: %q", m.threadInput.Value())
	}

	// reopening the same card shows the preserved draft.
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.cardOpen {
		t.Fatal("enter should reopen the card page")
	}
	if m.threadInput.Value() != "not sent yet" {
		t.Fatalf("draft lost once the card page reopened: %q", m.threadInput.Value())
	}
}

// TestFocusThreadInputWithholdsForDrivenAbroad: a card another gummi
// process drives withholds the input entirely — "/" must refuse to focus
// it, matching the read-only line inputBlock renders for such a card.
func TestFocusThreadInputWithholdsForDrivenAbroad(t *testing.T) {
	m := populatedShell(120, 34)
	m.rows[m.sel].DrivenAbroad = true
	m.rows[m.sel].Foreign = state.ForeignDrive{PID: 99}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.threadInput.Focused() {
		t.Fatal("/ focused the input on a card driven by another process")
	}
}

// TestSubmitThreadInputRoutesFreeVerbImmediately: diff/spec/park fire
// straight away when they carry no remainder — no chip, no extra step.
func TestSubmitThreadInputRoutesFreeVerbImmediately(t *testing.T) {
	m := populatedShell(120, 34)
	m.rows[m.sel].F.Kind = domain.KindResearch // boardVerb("d")'s guard fires a synchronous, storeless notice
	f := m.rows[m.sel].F
	m.threadInput.SetValue("diff")

	cmd := m.submitThreadInput(f)
	pump(t, m, cmd)
	if m.threadChip != nil {
		t.Fatal("a remainder-free free verb should not raise a chip")
	}
	if m.threadInput.Value() != "" {
		t.Fatalf("input not cleared after an immediate fire: %q", m.threadInput.Value())
	}
	if !strings.Contains(m.notice.text, "no diff") {
		t.Fatalf("diff did not reach boardVerb(\"d\"): notice = %q", m.notice.text)
	}
}

// TestSubmitThreadInputChipsAFreeVerbWithARemainder: the one exception to
// "free verbs fire immediately" — a remainder they have nowhere to spend
// must not be silently dropped, so it raises the chip instead.
func TestSubmitThreadInputChipsAFreeVerbWithARemainder(t *testing.T) {
	m := populatedShell(120, 34)
	f := m.rows[m.sel].F
	m.threadInput.SetValue("diff please check line 42")

	m.submitThreadInput(f)
	if m.threadChip == nil {
		t.Fatal("a free verb with a remainder should still raise the chip")
	}
	if m.threadChip.verb != "diff" || m.threadChip.remainder != "please check line 42" {
		t.Fatalf("chip = %+v, want verb diff with the typed remainder", m.threadChip)
	}
	if m.notice.text != "" {
		t.Fatalf("diff must not have fired yet: notice = %q", m.notice.text)
	}
}

// TestSubmitThreadInputChipsStateChangingVerbs: every state-changing verb
// chips even with no remainder at all.
func TestSubmitThreadInputChipsStateChangingVerbs(t *testing.T) {
	for verb := range chipVerbs {
		t.Run(verb, func(t *testing.T) {
			m := populatedShell(120, 34)
			f := m.rows[m.sel].F
			m.threadInput.SetValue(verb)
			m.submitThreadInput(f)
			if m.threadChip == nil {
				t.Fatalf("%s should always raise the chip", verb)
			}
			if m.threadChip.verb != verb {
				t.Fatalf("chip verb = %q, want %q", m.threadChip.verb, verb)
			}
		})
	}
}

// TestChipEnterFires: confirming the chip runs the mapped board verb,
// clears the chip, and clears the input.
func TestChipEnterFires(t *testing.T) {
	m := populatedShell(120, 34)
	m.rows[m.sel].F.Kind = domain.KindResearch
	f := m.rows[m.sel].F
	m.threadInput.SetValue("clean")
	m.submitThreadInput(f)
	if m.threadChip == nil {
		t.Fatal("clean should have raised a chip")
	}

	cmd := m.handleChipKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	pump(t, m, cmd)
	if m.threadChip != nil {
		t.Fatal("enter on the chip should clear it")
	}
	if m.threadInput.Value() != "" {
		t.Fatalf("enter on the chip should clear the input: %q", m.threadInput.Value())
	}
	if !strings.Contains(m.notice.text, "no cleanup") {
		t.Fatalf("clean did not reach boardVerb(\"c\"): notice = %q", m.notice.text)
	}
}

// TestChipEscRestoresLineAndSendsAsMessageNext: esc on the chip puts the
// original line back — it does not resend it — and the NEXT submit of
// that same line sends it as a message rather than raising the same chip
// again (the "esc no, send as a message" half of the chip contract).
func TestChipEscRestoresLineAndSendsAsMessageNext(t *testing.T) {
	m := populatedShell(120, 34)
	f := m.rows[m.sel].F
	m.threadInput.SetValue("verify the csv path")
	m.submitThreadInput(f)
	if m.threadChip == nil {
		t.Fatal("verify should have raised a chip")
	}

	m.handleChipKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.threadChip != nil {
		t.Fatal("esc should clear the chip")
	}
	if m.threadInput.Value() != "verify the csv path" {
		t.Fatalf("esc should put the original line back in the input: %q", m.threadInput.Value())
	}

	// resubmitting the exact same line now sends it as a message instead
	// of raising the same chip a second time.
	m.submitThreadInput(f)
	if m.threadChip != nil {
		t.Fatal("the deliberate 'send as a message' resubmit re-raised the chip")
	}
	if m.threadInput.Value() != "" {
		t.Fatalf("the message resubmit should clear the input: %q", m.threadInput.Value())
	}
	if !strings.Contains(m.notice.text, "no live session") {
		t.Fatalf("expected the no-live-session notice from sendThreadMessage, got %q", m.notice.text)
	}
}

// TestChipOtherKeyResumesEditing: any key besides enter/esc cancels the
// chip and continues editing the (untouched) original line in place.
func TestChipOtherKeyResumesEditing(t *testing.T) {
	m := populatedShell(120, 34)
	f := m.rows[m.sel].F
	m.threadInput.Focus() // Update no-ops unfocused; the real flow always types focused
	m.threadInput.SetValue("approve")
	m.submitThreadInput(f)
	if m.threadChip == nil {
		t.Fatal("approve should have raised a chip")
	}

	m.handleChipKey(tea.KeyPressMsg{Code: '!', Text: "!"})
	if m.threadChip != nil {
		t.Fatal("typing over the chip should cancel it")
	}
	if m.threadInput.Value() != "approve!" {
		t.Fatalf("input = %q, want the original line plus the new keystroke", m.threadInput.Value())
	}
}

// TestNotWiredVerbsCarryTheirRemainder: changes, bounce and autopilot are
// parsed and reported, remainder included, rather than silently dropped
// or given invented engine behaviour.
func TestNotWiredVerbsCarryTheirRemainder(t *testing.T) {
	m := populatedShell(120, 34)
	for _, verb := range []string{"changes", "bounce", "autopilot"} {
		t.Run(verb, func(t *testing.T) {
			m.notice = noticeMsg{}
			cmd := m.fireVerb(verb, "because the CI flake is fixed")
			if cmd != nil {
				t.Fatalf("%s should have no engine effect yet", verb)
			}
			if !strings.Contains(m.notice.text, verb) || !strings.Contains(m.notice.text, "not wired") && !strings.Contains(m.notice.text, "isn't wired") {
				t.Fatalf("%s notice = %q, want it to name the verb and say it isn't wired", verb, m.notice.text)
			}
			if !strings.Contains(m.notice.text, "because the CI flake is fixed") {
				t.Fatalf("%s notice dropped the remainder: %q", verb, m.notice.text)
			}
		})
	}
}

// TestVerbMenuOpensCommandMenuPreFiltered: a bare "/" (already consumed
// by focusThreadInput on the way in, so this covers what happens once the
// user re-types it as content) opens the same command menu boardKey's
// space key does, and "/foo" pre-filters it.
func TestVerbMenuOpensCommandMenuPreFiltered(t *testing.T) {
	m := populatedShell(120, 34)
	f := m.rows[m.sel].F

	m.threadInput.SetValue("/")
	m.submitThreadInput(f)
	cm, ok := m.Overlay.Top().(*commandMenu)
	if !ok {
		t.Fatalf("bare / did not open the command menu: %T", m.Overlay.Top())
	}
	if cm.filter.Value() != "" {
		t.Fatalf("bare / pre-filtered the menu: %q", cm.filter.Value())
	}
	if m.threadInput.Value() != "" {
		t.Fatalf("submitting the menu line should clear the input: %q", m.threadInput.Value())
	}
	m.Overlay.Pop()

	m.threadInput.SetValue("/appro")
	m.submitThreadInput(f)
	cm, ok = m.Overlay.Top().(*commandMenu)
	if !ok {
		t.Fatalf("/appro did not open the command menu: %T", m.Overlay.Top())
	}
	if cm.filter.Value() != "appro" {
		t.Fatalf("menu filter = %q, want the slash remainder", cm.filter.Value())
	}
}

// TestPlainMessageRoutesToSendWithNoLiveSession: prose with no matching
// verb (verbNone) routes to sendThreadMessage exactly like a chat send —
// with nothing live to send to (a detached shell in this test), it says
// so instead of silently doing nothing.
func TestPlainMessageRoutesToSendWithNoLiveSession(t *testing.T) {
	m := populatedShell(120, 34)
	f := m.rows[m.sel].F
	m.threadInput.SetValue("looks good, but verify the padding")
	m.submitThreadInput(f)
	if m.threadChip != nil {
		t.Fatal("prose starting with a non-verb word must not raise a chip")
	}
	if m.threadInput.Value() != "" {
		t.Fatalf("a sent message should clear the input: %q", m.threadInput.Value())
	}
	if !strings.Contains(m.notice.text, "no live session") {
		t.Fatalf("notice = %q, want the no-live-session notice", m.notice.text)
	}
}

// TestThreadInputSendsToLiveSession is the end-to-end wiring check: a
// message typed into the thread input, through the real key-handling
// path, reaches the card's live engine session exactly like the chat
// pane's own send does — against a fake agent, never the network.
func TestThreadInputSendsToLiveSession(t *testing.T) {
	m, eng := chatWorkspace(t, agent.NewFake("sure, got it"))
	m = openAndAttach(t, m) // opens the card page, attaches the chat pane, kicks off a turn
	settleChat(t, eng)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape}) // detach the chat pane; the session and the card page stay
	if m.chat != nil {
		t.Fatal("esc did not detach the chat pane")
	}
	if !m.cardOpen {
		t.Fatal("detaching the chat pane should not close the card page")
	}
	if !m.threadInput.Focused() {
		t.Fatal("/ did not focus the thread input")
	}
	m = typeString(t, m, "quick note from the thread")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	settleChat(t, eng)

	if m.threadInput.Value() != "" {
		t.Fatalf("input not cleared after sending: %q", m.threadInput.Value())
	}
	sess := eng.Get("FD-001")
	if sess == nil {
		t.Fatal("the session should still be live")
	}
	snap := sess.Snapshot()
	if len(snap.Transcript) != 4 {
		t.Fatalf("transcript = %+v, want kickoff+reply+user+reply", snap.Transcript)
	}
	if snap.Transcript[2].Author != engine.AuthorUser || snap.Transcript[2].Content != "quick note from the thread" {
		t.Fatalf("user turn wrong: %+v", snap.Transcript[2])
	}
}

// threadWithHistory is a card carrying enough conversation that the
// thread cannot fit a small window — the case the layout exists for.
func threadWithHistory(t *testing.T) *Shell {
	t.Helper()
	m := populatedShell(120, 34)
	id := m.rows[m.sel].F.ID
	at := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	enter, _ := json.Marshal(map[string]string{"role": "architect", "model": "m"})
	exit, _ := json.Marshal(map[string]any{"verdict": "pass"})

	var evs []state.CardEvent
	for _, st := range []domain.Stage{domain.StageBrainstorm, domain.StageSpec, domain.StagePlan} {
		evs = append(evs,
			state.CardEvent{Stage: st, Kind: state.EventStageEnter, At: at, Payload: string(enter)},
			state.CardEvent{Stage: st, Kind: state.EventStageExit, At: at, Payload: string(exit)},
		)
	}
	evs = append(evs, state.CardEvent{
		Stage: domain.StageImplement, Kind: state.EventStageEnter, At: at, Payload: string(enter),
	})
	for range 20 {
		evs = append(evs, state.CardEvent{
			Stage: domain.StageImplement, Kind: state.EventTool, Status: state.StatusOK, At: at,
			Payload: `{"label":"edit internal/theme/palette.go"}`,
		})
	}
	m.cardEvents[id] = evs
	return m
}

// The input is the one row the page cannot do without: it is how you
// reply, and on a short terminal it used to be the first thing dropped,
// because the view was truncated to the window height from the top.
func TestThreadPinsTheInputToTheBottom(t *testing.T) {
	m := threadWithHistory(t)
	for _, size := range []struct{ w, h int }{{40, 20}, {60, 14}, {80, 10}, {100, 8}} {
		lines := strings.Split(ansi.Strip(m.threadView(size.w, size.h)), "\n")
		if len(lines) > size.h {
			t.Errorf("%dx%d rendered %d lines, want at most %d", size.w, size.h, len(lines), size.h)
		}
		last := lines[len(lines)-1]
		if !strings.Contains(last, "┃") {
			t.Errorf("%dx%d: last row is %q, want the input", size.w, size.h, last)
		}
	}
}

// A card opens on its newest event, the way a chat does — the oldest
// stage scrolls off the top, not the live one off the bottom.
// The height matters: with the pinned next block retired the foot is a
// bare composer, and now the pinned decision costs the body window two
// more rows. The fixture fits outright above 20, and below it the live
// stage boundary scrolls off — the heights are the window where the
// history actually overflows, or these assert nothing.
func TestThreadOpensAtTheNewestEvent(t *testing.T) {
	m := threadWithHistory(t)
	out := ansi.Strip(m.threadView(80, 20))
	if !strings.Contains(out, "fresh context") {
		t.Error("the live stage is not on screen — the thread did not open at its end")
	}
	if strings.Contains(out, "brainstorm · architect") {
		t.Error("the oldest folded receipt is still on screen — the body was not anchored to its end")
	}
}

// Paging up reaches the history and paging back down returns to the
// newest, clamped at both ends so neither runs into blank space. The
// height is the narrow band where the fixture's history actually
// overflows the window (the pinned decision costs the body two rows) and
// the live-stage boundary still fits the newest view; the scroll amount
// is asserted against the clamp rather than assumed.
func TestThreadScrollsWithPageKeys(t *testing.T) {
	m := threadWithHistory(t)
	m.width, m.height = 80, 20
	w, h := m.threadSize()

	for range 6 { // more pages than the body has, to prove the clamp
		m.scrollThread(true)
	}
	if m.threadScroll != m.maxThreadScroll(w, h) {
		t.Errorf("threadScroll = %d after paging up, want the clamp %d", m.threadScroll, m.maxThreadScroll(w, h))
	}
	up := ansi.Strip(m.threadView(w, h))
	if !strings.Contains(up, "brainstorm · architect") {
		t.Error("paging up never reached the oldest receipt")
	}

	for range 6 {
		m.scrollThread(false)
	}
	if m.threadScroll != 0 {
		t.Errorf("threadScroll = %d after paging back down, want 0 (pinned to the newest)", m.threadScroll)
	}
	if !strings.Contains(ansi.Strip(m.threadView(w, h)), "fresh context") {
		t.Error("paging back down did not return to the live stage")
	}
}

// Stepping to another card is a different conversation: it opens at its
// own end rather than inheriting how far back the last one was scrolled.
func TestSteppingCardsResetsTheScroll(t *testing.T) {
	m := threadWithHistory(t)
	m.width, m.height = 80, 19
	m.scrollThread(true)
	if m.threadScroll == 0 {
		t.Fatal("precondition: the thread did not scroll")
	}
	m.stepCard(1)
	if m.threadScroll != 0 {
		t.Errorf("threadScroll = %d after stepping cards, want 0", m.threadScroll)
	}
}
