package ui

import (
	"context"
	"encoding/json"
	"regexp"
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

// downScrollMarker matches the thread body's "↓ N more" marker
// (composeThread) — present exactly when something newer than what is on
// screen is scrolled out of view below.
var downScrollMarker = regexp.MustCompile(`↓ \d+ more`)

// upScrollMarker is downScrollMarker's counterpart for older content
// hidden above the window.
var upScrollMarker = regexp.MustCompile(`↑ \d+ more`)

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
	line := ansi.Strip(foldedReceiptLine(m0Styles(), seg, nil, 1, 80))
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
	if strings.Contains(view, "brainstorm ·") {
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
	if !strings.Contains(view, "brainstorm ·") {
		t.Errorf("thread view missing the folded brainstorm receipt once events loaded:\n%s", view)
	}
}

// m0Styles is a plain style set for thread-rendering unit tests that
// don't need a whole Shell.
func m0Styles() *theme.Styles { return NewShell(theme.GummiDark(), "test").styles }

// TestFoldedReceiptPrefersPerSegmentSpend: credits shown on a folded
// receipt come from that segment's own stage_exit payload, not the
// stage_spend rollup — stage_spend is keyed by (feature, stage, model,
// role), so it can only answer "what did this stage cost across every
// session", never "what did this one session cost". The rollup is used
// only as a fallback, and only when the stage ran exactly once, for a
// card whose payload predates the credits field.
func TestFoldedReceiptPrefersPerSegmentSpend(t *testing.T) {
	seg := stageSegment{stage: domain.StageImplement, role: "implementer", exited: true, credits: 6}
	metered := map[domain.Stage]float64{domain.StageImplement: 41}

	// one segment: the payload wins over the rollup even though both exist.
	line := ansi.Strip(foldedReceiptLine(m0Styles(), seg, metered, 1, 80))
	if !strings.Contains(line, "6 credits") {
		t.Errorf("receipt %q did not use the segment's own payload", line)
	}
	if strings.Contains(line, "41 credits") {
		t.Errorf("receipt %q used the stage rollup over the segment", line)
	}

	// no payload (predates the credits field), one segment: the rollup is
	// still better than nothing.
	bare := stageSegment{stage: domain.StageImplement, role: "implementer", exited: true}
	line = ansi.Strip(foldedReceiptLine(m0Styles(), bare, metered, 1, 80))
	if !strings.Contains(line, "41 credits") {
		t.Errorf("receipt %q dropped the rollup fallback", line)
	}

	// no payload, more than one segment for the stage: the rollup cannot
	// be attributed to this session, so nothing is shown rather than a
	// number that may not even be this segment's.
	line = ansi.Strip(foldedReceiptLine(m0Styles(), bare, metered, 2, 80))
	if strings.Contains(line, "credits") {
		t.Errorf("receipt %q showed an unattributable rollup across multiple segments", line)
	}
}

// TestFoldedReceiptPerSessionSpendDiffers is the bug this fixes, made
// concrete: a card that bounced through review→fix four times has four
// fix segments, and the stage_spend rollup is one number shared by all
// of them. Printing that rollup on every receipt made a ~172-credit card
// read as 53.5 (whatever the rollup happened to be) four times over.
// Each segment must show its own payload instead.
func TestFoldedReceiptPerSessionSpendDiffers(t *testing.T) {
	rollup := map[domain.Stage]float64{domain.StageFix: 53.5} // the misleading stage total
	first := stageSegment{stage: domain.StageFix, exited: true, credits: 12}
	second := stageSegment{stage: domain.StageFix, exited: true, credits: 34}

	l1 := ansi.Strip(foldedReceiptLine(m0Styles(), first, rollup, 2, 80))
	l2 := ansi.Strip(foldedReceiptLine(m0Styles(), second, rollup, 2, 80))
	if !strings.Contains(l1, "12 credits") {
		t.Errorf("first fix receipt %q did not show its own 12 credits", l1)
	}
	if !strings.Contains(l2, "34 credits") {
		t.Errorf("second fix receipt %q did not show its own 34 credits", l2)
	}
	if strings.Contains(l1, "53.5") || strings.Contains(l2, "53.5") {
		t.Errorf("a receipt showed the stage rollup instead of its own spend: %q / %q", l1, l2)
	}
}

// The composer owns the keyboard the moment a card opens, the way a
// coding agent's does: you type your reply, you do not unlock a field
// first — and it keeps the keyboard for as long as the page is open. The
// accelerators are reached by word, by the ↑ inventory or by the / menu,
// never by blurring, and the keys that are not text (enter on an empty
// line, the arrows, the page keys, alt+j/k) work without leaving the line.
// esc is the way off the page, in one press, with the draft kept.
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

	// alt+j/alt+k step cards without leaving the line: J and K are text
	// here, so the pair the blurred layer used to own moved to alt
	before := m.sel
	m = press(t, m, tea.KeyPressMsg{Code: 'j', Mod: tea.ModAlt})
	if m.sel == before {
		t.Fatal("alt+j did not step to the next card")
	}
	// the draft is scoped to the card it was typed on (F5): stepping to a
	// different card must not carry it along, or the letters typed here
	// meant for one card's own line
	if got := m.threadInput.Value(); got != "" {
		t.Fatalf("the previous card's draft bled onto this one: %q", got)
	}
	m = press(t, m, tea.KeyPressMsg{Code: 'k', Mod: tea.ModAlt})
	if m.sel != before {
		t.Fatalf("alt+k did not step back: sel = %d, want %d", m.sel, before)
	}
	if got := m.threadInput.Value(); got != "g and v are just letters here" {
		t.Fatalf("stepping back lost the draft: %q", got)
	}

	// esc leaves the page in one press, without eating the draft — the
	// page hides, it is never discarded
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.cardOpen {
		t.Fatal("esc did not leave the card page")
	}
	if got := m.threadInput.Value(); got != "g and v are just letters here" {
		t.Fatalf("esc discarded the draft: %q", got)
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "BACKLOG") {
		t.Error("esc did not land back on the backlog list")
	}
}

// TestThreadInputPlaceholderAdvertisesTheActions: ↑ is the only key that
// reaches the card's actions now that esc leaves the page instead of
// blurring, so the composer has to say so — in the placeholder, which
// occupies exactly the state that opens them (an empty line). F11 made ↑
// off the top of an open decision the very same route, so the placeholder
// advertises it there too now — the two placeholders that used to exist
// (one hedged, one did not) collapsed into one honest promise.
func TestThreadInputPlaceholderAdvertisesTheActions(t *testing.T) {
	m := attachedBoard(t, 120, 34)
	m.sel = 5 // FD-039, done — nothing to decide, so ↑ is the inventory's door
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if v := ansi.Strip(m.View().Content); !strings.Contains(v, "↑ for actions") {
		t.Errorf("the composer does not advertise the action inventory:\n%s", v)
	}

	// FD-042 (implement, nothing running) has an open decision — ↑ still
	// reaches the inventory from it (off the top), so the same promise
	// holds
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	m.sel = 1
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if d := m.openDecision(m.rows[m.sel]); d == nil {
		t.Fatal("precondition: FD-042 should have an open decision")
	}
	if v := ansi.Strip(m.View().Content); !strings.Contains(v, "↑ for actions") {
		t.Errorf("the placeholder no longer promises ↑ while a decision is open:\n%s", v)
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
// composer is empty, ↑↓ move its cursor and enter answers the highlighted
// option — the arrows are the decision's. Neither direction wraps any
// more (F11): ↓ on the last option holds still, and ↑ off the top is the
// one route into the action inventory instead — dependencies, envelope,
// duplicate, delete, set-repo, rebase, auto-approve-gates, none of which
// had a key on a working card before this. Keep pressing ↑ and you
// eventually reach it, which is the whole point: there is still only ever
// one list on screen at a time (DESIGN §6.3).
func TestThreadDecisionOwnsThePickerKeys(t *testing.T) {
	m := attachedBoard(t, 120, 34)
	m.sel = 3 // FD-049, spec — nothing running, so a decision is open
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	d := m.openDecision(m.rows[m.sel])
	if d == nil || len(d.actions) < 2 {
		t.Fatalf("precondition: an idle spec card has a multi-option decision, got %+v", d)
	}
	if m.decisionCursor != 0 {
		t.Fatalf("precondition: decision opens on the first option, got %d", m.decisionCursor)
	}

	// down moves through the options and stops dead on the last one —
	// no wrap back to the first
	for range len(d.actions) + 2 {
		m = press(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if _, ok := m.Overlay.Top().(*cardActionsDialog); ok {
		t.Fatal("down opened the action pop-over — only ↑ off the top may")
	}
	if m.decisionCursor != len(d.actions)-1 {
		t.Fatalf("down cursor = %d, want %d (held at the last option, no wrap)", m.decisionCursor, len(d.actions)-1)
	}

	// up steps back down through the options one at a time — still no
	// pop-over, since the cursor has not reached the top yet
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	if _, ok := m.Overlay.Top().(*cardActionsDialog); ok {
		t.Fatal("up opened the action pop-over before reaching the first option")
	}
	if want := len(d.actions) - 2; m.decisionCursor != want {
		t.Fatalf("up cursor = %d, want %d", m.decisionCursor, want)
	}

	// back to the first option, then one more ↑ escapes into the
	// inventory instead of trying (and failing) to wrap
	for m.decisionCursor > 0 {
		m = press(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	top, ok := m.Overlay.Top().(*cardActionsDialog)
	if !ok {
		t.Fatalf("↑ on the first option opened %T, want the action inventory", m.Overlay.Top())
	}
	if top.feature != string(m.rows[m.sel].F.ID) {
		t.Errorf("inventory opened for %q, want %q", top.feature, m.rows[m.sel].F.ID)
	}
}

// TestThreadInputSurvivesTabSwitch: the unsent buffer has to survive
// leaving and returning to the board tab. It always did — the draft is a
// Shell field, not a child of the page — but it used to survive into a
// page that had been closed behind it, so coming back meant finding the
// card again to reach your own half-written line. The page is a board
// surface now like every other, hidden on the way out and restored on
// the way in (DESIGN §6), so the draft comes back where it was left.
func TestThreadInputSurvivesTabSwitch(t *testing.T) {
	m := attachedBoard(t, 120, 34)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = typeString(t, m, "not sent yet")

	m = press(t, m, tea.KeyPressMsg{Code: '2', Mod: tea.ModAlt}) // -> inbox tab
	if m.tab != TabInbox {
		t.Fatalf("tab = %v, want inbox", m.tab)
	}
	m = press(t, m, tea.KeyPressMsg{Code: '1', Mod: tea.ModAlt}) // -> board tab
	if m.tab != TabBoard {
		t.Fatalf("tab = %v, want board", m.tab)
	}
	if !m.cardOpen {
		t.Fatal("the card page did not come back with the board tab")
	}
	if m.threadInput.Value() != "not sent yet" {
		t.Fatalf("draft lost across a tab switch: %q", m.threadInput.Value())
	}
	// and it is still the composer's, not the accelerators' — the trip
	// restores the page as it was rather than as it opens fresh
	if !m.threadInput.Focused() {
		t.Error("the composer lost the keyboard across the trip")
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

// TestSubmitThreadInputRoutesFreeVerbImmediately: diff/spec fire straight
// away when they carry no remainder — no chip, no extra step. (park is a
// state-changing verb — chipVerbs — so it always chips; see
// TestParkVerbPausesRatherThanOpeningDeps.)
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
// Real submits go through submitThreadLine (handleThreadInputKey's enter
// case), not submitThreadInput directly — the flag-consuming half of the
// contract now lives there (F2).
func TestChipEscRestoresLineAndSendsAsMessageNext(t *testing.T) {
	m := populatedShell(120, 34)
	r := m.rows[m.sel]
	m.threadInput.SetValue("verify the csv path")
	m.submitThreadInput(r.F)
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
	m.submitThreadLine(r, "verify the csv path")
	if m.threadChip != nil {
		t.Fatal("the deliberate 'send as a message' resubmit re-raised the chip")
	}
	if !strings.Contains(m.notice.text, "no live session") {
		t.Fatalf("expected the no-live-session notice from sendThreadMessage, got %q", m.notice.text)
	}
	// F8: sendThreadMessage found no session to hand the text to, so the
	// line stays exactly where it was rather than vanishing with it.
	if m.threadInput.Value() != "verify the csv path" {
		t.Fatalf("a failed send should not have cleared the input: %q", m.threadInput.Value())
	}
	if m.threadSkipParse != "" {
		t.Fatalf("the promise should be spent after the resubmit either way, got %q", m.threadSkipParse)
	}
}

// TestChipEscPromiseDoesNotOutliveItsLine is F2's own repro
// (ptytest-thread.sh section 8 drives the same defect with "spec", a
// free verb that fires immediately rather than chipping — this uses
// "verify", another always-chipping verb, so a leaked promise and a
// correctly-parsed fresh line are distinguishable by one thing: whether
// a chip comes up): esc on a chip arms "send as a message", but only for
// the exact line that raised it. ctrl+u then a different verb must not
// silently inherit that promise; "verify" has to go through the parser
// (and raise its own chip) rather than being sent to the agent as chat.
func TestChipEscPromiseDoesNotOutliveItsLine(t *testing.T) {
	m := populatedShell(120, 34)
	r := m.rows[m.sel]
	m.threadInput.SetValue("clean")
	m.submitThreadInput(r.F)
	if m.threadChip == nil || m.threadChip.verb != "clean" {
		t.Fatalf("clean should have raised a chip, got %+v", m.threadChip)
	}

	m.handleChipKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.threadSkipParse != "clean" {
		t.Fatalf("threadSkipParse = %q, want the exact line the chip was raised for", m.threadSkipParse)
	}

	// ctrl+u, then a different verb
	m.threadInput.Reset()
	m.threadInput.SetValue("verify")

	m.submitThreadLine(r, "verify")
	if m.threadChip == nil || m.threadChip.verb != "verify" {
		t.Fatalf("verify should have raised its own chip rather than being sent as a message: chip=%+v notice=%q", m.threadChip, m.notice.text)
	}
	if m.threadSkipParse != "" {
		t.Fatalf("the stale promise should be spent by the mismatched submit, got %q", m.threadSkipParse)
	}
}

// TestChipEscPromiseDroppedWhenComposerEmptied covers the same-line edge
// case text comparison alone cannot catch: ctrl+u then retyping the
// EXACT words the chip was raised for should not resurrect the promise —
// that would send a freshly typed command as a message with no chip at
// all, indistinguishable from someone deliberately retyping it. Emptying
// the composer drops the promise outright (clearSkipParseIfEmptied), so
// a fresh line — even an identical one — goes through the parser again.
func TestChipEscPromiseDroppedWhenComposerEmptied(t *testing.T) {
	m := populatedShell(120, 34)
	r := m.rows[m.sel]
	m.threadInput.Focus() // Update no-ops unfocused; ctrl+u must reach the textarea
	m.threadInput.SetValue("clean")
	m.submitThreadInput(r.F)
	if m.threadChip == nil {
		t.Fatal("clean should have raised a chip")
	}
	m.handleChipKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.threadSkipParse != "clean" {
		t.Fatalf("threadSkipParse = %q, want %q", m.threadSkipParse, "clean")
	}

	m.handleThreadInputKey(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}) // real ctrl+u, through the key router
	if m.threadInput.Value() != "" {
		t.Fatalf("ctrl+u should have emptied the composer, got %q", m.threadInput.Value())
	}
	if m.threadSkipParse != "" {
		t.Fatalf("emptying the composer should drop the promise, got %q", m.threadSkipParse)
	}

	m.threadInput.SetValue("clean")
	m.submitThreadLine(r, "clean")
	if m.threadChip == nil {
		t.Fatal("retyping the same word fresh should raise its own chip, not reuse the stale promise")
	}
}

// TestChipSurvivesAltJAndCardSteps is F6: alt+j/alt+k, alt+o and
// pgup/pgdown all predate the chip and are documented as working
// mid-draft ("never text"); routing them through handleChipKey's default
// case first — which cancels whatever it does not recognise and forwards
// the key to the textarea — used to eat the chip and the card step both,
// since a modifier chord does nothing typed into a textarea. Hoisted
// above the chip branch, alt+j both steps the card AND leaves the chip
// standing (it is keyed to its own feature — inputBlock — so it simply
// stops rendering on the new card rather than being discarded).
func TestChipSurvivesAltJAndCardSteps(t *testing.T) {
	m := attachedBoard(t, 120, 34)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // open the card page
	a := m.rows[m.sel].F.ID
	m = typeString(t, m, "verify")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.threadChip == nil || m.threadChip.verb != "verify" {
		t.Fatalf("verify should have raised a chip, got %+v", m.threadChip)
	}

	m = press(t, m, tea.KeyPressMsg{Code: 'j', Mod: tea.ModAlt})
	if m.rows[m.sel].F.ID == a {
		t.Fatal("alt+j should have stepped to a different card")
	}
	if m.threadChip == nil || m.threadChip.verb != "verify" || m.threadChip.feature != a {
		t.Fatalf("the chip did not survive alt+j: %+v", m.threadChip)
	}

	m = press(t, m, tea.KeyPressMsg{Code: 'k', Mod: tea.ModAlt})
	if m.rows[m.sel].F.ID != a {
		t.Fatalf("alt+k did not step back to %s, got %s", a, m.rows[m.sel].F.ID)
	}
	if m.threadChip == nil || m.threadChip.verb != "verify" {
		t.Fatalf("the chip did not survive the round trip: %+v", m.threadChip)
	}
}

// TestChipStandingOnAnotherCardDoesNotClaimEnter is the corollary F6
// surfaced: once alt+j/alt+k can step past a pending chip, the chip can
// be standing for a card that is no longer selected. Before this, both
// the bar and handleThreadInputKey gated on "a chip exists" rather than
// "a chip exists for THIS card" — so the bar kept promising "confirm"
// on the new card, and enter there would have fired the OLD card's chip
// (fireVerb acts on whichever card is currently selected) while also
// wiping the new card's own unsent draft via handleChipKey's
// Reset(). Off its own card, a chip must be inert: the bar reads like a
// plain composer, and enter/other keys reach the textarea untouched.
func TestChipStandingOnAnotherCardDoesNotClaimEnter(t *testing.T) {
	m := attachedBoard(t, 120, 34)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // open card A
	a := m.rows[m.sel].F.ID
	m = typeString(t, m, "verify")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.threadChip == nil {
		t.Fatal("verify should have raised a chip")
	}

	m = press(t, m, tea.KeyPressMsg{Code: 'j', Mod: tea.ModAlt}) // step to B
	b := m.rows[m.sel].F.ID
	bStage := m.rows[m.sel].F.Stage
	if b == a {
		t.Fatal("precondition: alt+j should have moved to a different card")
	}
	if m.threadChip == nil || m.threadChip.feature != a {
		t.Fatalf("precondition: the chip should still belong to A, got %+v", m.threadChip)
	}

	for _, bnd := range m.threadInputBindings() {
		if bnd.key == "enter" && bnd.label == "confirm" {
			t.Errorf("the bar offered 'confirm' on %s for a chip that belongs to %s", b, a)
		}
	}

	m = typeString(t, m, "B's own note")
	if got := m.threadInput.Value(); got != "B's own note" {
		t.Fatalf("typing on B should have reached the textarea, not the stale chip: %q", got)
	}
	if m.rows[m.sel].F.Stage != bStage {
		t.Fatalf("typing must not have fired A's chip against B: stage = %s, want %s (B's stage before typing)", m.rows[m.sel].F.Stage, bStage)
	}
	if m.threadChip == nil || m.threadChip.feature != a {
		t.Fatalf("the chip should still be standing for A: %+v", m.threadChip)
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

// TestSlashVerbResolvesLikeBareVerb is the regression for the finding:
// "/" and a bare verb used to be two different vocabularies. The command
// menu (m.globalCommands) never carried the closed verb vocabulary
// (verbs.go), so "/park" searched a list with no "park" in it and landed
// on "no commands match", while bare "park" fired straight through
// fireVerb — same word, opposite outcomes. For every word in verbs, "/"
// + that word must now resolve exactly like the bare word: same chip
// state (or lack of one), same notice, and the command menu never opens.
func TestSlashVerbResolvesLikeBareVerb(t *testing.T) {
	for verb := range verbs {
		t.Run(verb, func(t *testing.T) {
			// attachedBoard, not the bare detached populatedShell: "spec"
			// fires immediately (it is not in chipVerbs) and openSpec
			// touches the real worktree pool unconditionally, which panics
			// on a detached shell (TestVerbKeysLandOnMatchingHandler's own
			// "spec" subtest needs the same fixture for the same reason).
			bare := attachedBoard(t, 120, 34)
			// Research: exercises the worktree-gated verbs' (diff, rebase,
			// land, squash, clean) own "no X" notice uniformly, the same
			// setup TestVerbKeysLandOnMatchingHandler uses — a verb that
			// fires immediately has something verb-named to compare.
			bare.rows[bare.sel].F.Kind = domain.KindResearch
			bf := bare.rows[bare.sel].F
			bare.threadInput.SetValue(verb)
			pump(t, bare, bare.submitThreadInput(bf))

			slash := attachedBoard(t, 120, 34)
			slash.rows[slash.sel].F.Kind = domain.KindResearch
			sf := slash.rows[slash.sel].F
			slash.threadInput.SetValue("/" + verb)
			pump(t, slash, slash.submitThreadInput(sf))

			if _, opened := slash.Overlay.Top().(*commandMenu); opened {
				t.Fatalf("/%s opened the command menu instead of routing the verb — the two-vocabularies bug", verb)
			}
			if (slash.threadChip == nil) != (bare.threadChip == nil) {
				t.Fatalf("/%s chip raised = %v, bare %q chip raised = %v", verb, slash.threadChip != nil, verb, bare.threadChip != nil)
			}
			if slash.threadChip != nil {
				if slash.threadChip.verb != bare.threadChip.verb || slash.threadChip.remainder != bare.threadChip.remainder {
					t.Fatalf("/%s chip = %+v, bare %q chip = %+v", verb, slash.threadChip, verb, bare.threadChip)
				}
			}
			if slash.notice.text != bare.notice.text {
				t.Fatalf("/%s notice = %q, bare %q notice = %q", verb, slash.notice.text, verb, bare.notice.text)
			}
		})
	}
}

// TestSlashMenuIncludesCardActionsOnCardPage covers the other half of the
// fix: words outside the closed verb vocabulary — envelope, duplicate —
// are only ever reachable through the card's own action inventory
// (cardactions.go, opened by ↑), which the command menu never carried on
// the card's thread page. "/envelope" now finds it there, merged in by
// globalCommands, the same way "/park" now finds its answer in the verb
// route above instead of in this menu.
func TestSlashMenuIncludesCardActionsOnCardPage(t *testing.T) {
	m := populatedShell(120, 34)
	m.cardOpen = true
	f := m.rows[m.sel].F

	m.threadInput.SetValue("/envelope")
	m.submitThreadInput(f)
	cm, ok := m.Overlay.Top().(*commandMenu)
	if !ok {
		t.Fatalf("/envelope did not open the command menu: %T", m.Overlay.Top())
	}
	var found *command
	for i, c := range cm.cmds {
		if c.label == "envelope" {
			found = &cm.cmds[i]
		}
	}
	if found == nil {
		t.Fatal("command menu has no envelope entry on a card page — cardactions.go's inventory did not merge in")
	}
	if !found.available {
		t.Fatalf("envelope entry = %+v, want available", *found)
	}
	if found.key != "u" {
		t.Fatalf("envelope entry key = %q, want the card action's own key %q", found.key, "u")
	}

	// "i" (the inbox) is both a global command and, worded as a card
	// action, the identical action — it must appear once, not twice.
	inboxCount := 0
	for _, c := range cm.cmds {
		if c.key == "i" {
			inboxCount++
		}
	}
	if inboxCount != 1 {
		t.Fatalf("inbox entries with key \"i\" = %d, want exactly 1 (global and card-action versions must not duplicate)", inboxCount)
	}
}

// TestSlashMenuOmitsCardActionsOffTheCardPage keeps globalCommands' old
// behaviour on the plain board dashboard, where the card's actions are
// already on screen inline (globalCommands' own doc comment) — merging
// them into the menu there would only lengthen it for nothing newly
// reachable.
func TestSlashMenuOmitsCardActionsOffTheCardPage(t *testing.T) {
	m := populatedShell(120, 34)
	m.cardOpen = false
	for _, c := range m.globalCommands() {
		if c.label == "envelope" {
			t.Fatalf("envelope leaked into the board-level menu with no card page open: %+v", c)
		}
	}
}

// TestPlainMessageRoutesToSendWithNoLiveSession: prose with no matching
// verb (verbNone) routes to sendThreadMessage exactly like a chat send —
// with nothing live to send to (a detached shell in this test), it says
// so instead of silently doing nothing, and — F8 — the line the user
// typed is still there afterwards rather than vanishing along with the
// notice explaining why it wasn't sent: attach (enter) and resend beats
// retyping it from memory.
func TestPlainMessageRoutesToSendWithNoLiveSession(t *testing.T) {
	m := populatedShell(120, 34)
	f := m.rows[m.sel].F
	m.threadInput.SetValue("looks good, but verify the padding")
	m.submitThreadInput(f)
	if m.threadChip != nil {
		t.Fatal("prose starting with a non-verb word must not raise a chip")
	}
	if !strings.Contains(m.notice.text, "no live session") {
		t.Fatalf("notice = %q, want the no-live-session notice", m.notice.text)
	}
	if m.threadInput.Value() != "looks good, but verify the padding" {
		t.Fatalf("a failed send should not have cleared the input: %q", m.threadInput.Value())
	}
}

// TestThreadInputSendsToLiveSession is the end-to-end wiring check: a
// message typed into the thread input, through the real key-handling
// path, reaches the card's live engine session exactly like the chat
// pane's own send did — against a fake agent, never the network.
func TestThreadInputSendsToLiveSession(t *testing.T) {
	m, eng := agentWorkspace(t, agent.NewFake("sure, got it"))
	m = openAndAttach(t, m) // opens the card page, attaches the conversation, kicks off a turn
	settleChat(t, eng)
	m = toKeys(t, m) // the accelerator layer; the session and the card page stay
	if m.threadInput.Focused() {
		t.Fatal("toKeys did not blur the thread input")
	}
	if !m.cardOpen {
		t.Fatal("blurring the composer should not close the card page")
	}
	// '/' refocuses the composer, which is where a message is typed —
	// the layer's only way back into the line (backlog.go keeps it for
	// exactly this reason)
	m = press(t, m, tea.KeyPressMsg{Code: '/'})
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
// more rows — the heights are the window where the fixture's history
// actually overflows, or these assert nothing.
//
// "Newest on screen" is no longer "the live stage's boundary rule is
// visible": the (now-uncapped, Task 3) live stage's own event list can
// itself run longer than the window, in which case the boundary rule is
// the OLDEST thing in it and is exactly what should have scrolled off —
// see TestPagingUpReachesTheLiveStageBoundary. The honest signal that the
// view is anchored at the end is the absence of a down-scroll marker
// (composeThread only draws one when something newer is off-screen).
func TestThreadOpensAtTheNewestEvent(t *testing.T) {
	m := threadWithHistory(t)
	out := ansi.Strip(m.threadView(80, 20))
	if downScrollMarker.MatchString(out) {
		t.Errorf("a down-scroll marker showed at the newest position — the thread did not open at its end:\n%s", out)
	}
	if !upScrollMarker.MatchString(out) {
		t.Error("expected the fixture's uncapped history to overflow the window and show an up-scroll marker")
	}
	if strings.Contains(out, "brainstorm · architect") {
		t.Error("the oldest folded receipt is still on screen — the body was not anchored to its end")
	}
}

// TestPagingUpReachesTheLiveStageBoundary is Task 3's regression test:
// the live stage's event list used to be capped to its last 6 entries,
// with the transcript view (t) as the only way to see the rest. That cap
// is gone — a long session is reached by scrolling instead — so paging
// all the way up must surface the live stage's own boundary rule
// ("fresh context"), not just the folded receipts above it. Without this
// test, a future re-introduction of a cap would pass every other test in
// this file unnoticed.
func TestPagingUpReachesTheLiveStageBoundary(t *testing.T) {
	m := threadWithHistory(t)
	m.width, m.height = 80, 25
	for range 6 { // more pages than the body has, to prove the clamp
		m.scrollThread(true)
	}
	w, h := m.threadSize()
	out := ansi.Strip(m.threadView(w, h))
	if !strings.Contains(out, "fresh context") {
		t.Errorf("pgup did not reach the live stage's boundary rule:\n%s", out)
	}
	if !strings.Contains(out, "brainstorm · architect") {
		t.Error("pgup did not also reach the oldest folded receipt")
	}
}

// Paging up reaches the history and paging back down returns to the
// newest, clamped at both ends so neither runs into blank space. The
// height is the narrow band where the fixture's history actually
// overflows the window (the pinned decision costs the body two rows) and
// the live-stage boundary still fits the newest view; the scroll amount
// is asserted against the clamp rather than assumed.
//
// The band sits higher than it used to: the page now spends rows on the
// blanks that separate its regions — the conversation from the decision,
// the decision from the composer, and the composer from the status bar
// (thread.go's sep and cardPageChrome) — so the body's own budget at a
// given terminal height is smaller by that many.
func TestThreadScrollsWithPageKeys(t *testing.T) {
	m := threadWithHistory(t)
	m.width, m.height = 80, 25
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
	// "back at the newest" is the absence of a down marker, not the
	// presence of "fresh context" — the live stage's own history can be
	// longer than the window (Task 3 removed its cap), so its boundary
	// rule is not necessarily what's newest on screen.
	if down := ansi.Strip(m.threadView(w, h)); downScrollMarker.MatchString(down) {
		t.Errorf("paging back down did not return to the newest — a down marker is still showing:\n%s", down)
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

// --- FINDING 03: "park" must pause the live session, never open the
// dependency picker off it, and every other verbKeys entry has to land on
// a boardVerb case whose action matches the word. ---

// TestParkVerbPausesRatherThanOpeningDeps is finding 03's repro and fix.
// boardVerb's "p" case is two board-only actions sharing one reused
// letter: pause a live non-interactive session, or — with nothing to
// pause — open the dependency picker. That fallback makes sense for a
// physical key someone can see is dual-purpose on the board itself, but a
// user who types the word "park" never means "open something unrelated".
// fireVerb now special-cases "park" (parkVerb, shell.go) so it only ever
// pauses, and says something true instead of silently opening the picker
// when there is nothing to pause — matching what cardactions.go's own
// pauseLabelWhy already calls "park" (a settled session, "so it stops
// asking") and what cardevents.go's ParkPayload doc calls "a human parked
// them with p".
func TestParkVerbPausesRatherThanOpeningDeps(t *testing.T) {
	t.Run("live session pauses, deps picker never opens", func(t *testing.T) {
		// No trailing idle event: the session stays busy/running so the
		// test can fire "park" against a real live, non-interactive
		// session instead of a raced one (mirrors run_test.go's
		// TestWatchAttachesRunningSession).
		ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
			if opts.Role == agent.RoleScribe {
				return []agent.Event{{Kind: agent.EventIdle}}
			}
			return []agent.Event{
				{Kind: agent.EventMessage, Text: "Wiring the toggle."},
				{Kind: agent.EventToolCall, Tool: "edit theme.go"},
			}
		}}
		m, eng := agentWorkspace(t, ag)
		m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
		m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
		m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
		m = openAndAttach(t, m)
		waitForActivity(t, eng)

		s := m.sessionFor("FD-001")
		if s == nil || s.Interactive {
			t.Fatalf("setup: want a live non-interactive session, got %+v", s)
		}

		cmd := m.fireVerb("park", "")
		pump(t, m, cmd)

		if m.deps != nil {
			t.Fatal("park opened the dependency picker — it must only ever pause the session")
		}
		if !strings.Contains(m.notice.text, "paused") {
			t.Fatalf("notice = %q, want it to say the session paused", m.notice.text)
		}
		if got := eng.Get("FD-001"); got == nil || got.State() != engine.StatePaused {
			state := "nil"
			if got != nil {
				state = string(got.State())
			}
			t.Fatalf("session state after park = %s, want paused", state)
		}
	})

	t.Run("no live session says so instead of opening the deps picker", func(t *testing.T) {
		m, _ := agentWorkspace(t, agent.NewFake("hi"))
		if m.sessionFor("FD-001") != nil {
			t.Fatal("setup: expected no live session on the freshly created card")
		}

		cmd := m.fireVerb("park", "")
		pump(t, m, cmd)

		if m.deps != nil {
			t.Fatal("park opened the dependency picker with nothing to pause")
		}
		if !strings.Contains(m.notice.text, "nothing running to park") {
			t.Fatalf("notice = %q, want a true explanation instead of an unrelated view opening", m.notice.text)
		}
	})
}

// TestVerbKeysLandOnMatchingHandler is the audit finding 03 asked for:
// every entry in verbKeys (and the "park" special case beside it) has to
// reach a boardVerb case whose action matches the word. Each scenario is
// chosen so the RIGHT handler and every OTHER handler this suite covers
// produce visibly different, verb-named results — a swapped or wrong key
// mapping fails loudly here rather than only in a human's pty session.
func TestVerbKeysLandOnMatchingHandler(t *testing.T) {
	// diff/rebase/land/squash/clean all guard on workflow.NeedsWorktree
	// for a research card, each with its own verb-named "no <thing>"
	// notice — routing to the wrong key would fire a DIFFERENT one of
	// these guards instead of the one matching the typed word.
	guarded := []struct{ verb, want string }{
		{"diff", "no diff"},
		{"rebase", "no rebase"},
		{"land", "no merge"},
		{"squash", "no squash"},
		{"clean", "no cleanup"},
	}
	for _, tc := range guarded {
		t.Run(tc.verb, func(t *testing.T) {
			m := populatedShell(120, 34)
			m.rows[m.sel].F.Kind = domain.KindResearch
			cmd := m.fireVerb(tc.verb, "")
			pump(t, m, cmd)
			if !strings.Contains(m.notice.text, tc.want) {
				t.Fatalf("%s notice = %q, want it to contain %q", tc.verb, m.notice.text, tc.want)
			}
		})
	}

	t.Run("verify", func(t *testing.T) {
		m := attachedBoard(t, 120, 34)
		m.rows[m.sel].F.Kind = domain.KindFeature
		cmd := m.fireVerb("verify", "")
		pump(t, m, cmd)
		if !strings.Contains(m.notice.text, "no checks yet") {
			t.Fatalf("verify notice = %q, want runChecks' no-artifact notice", m.notice.text)
		}
	})

	t.Run("spec", func(t *testing.T) {
		m := attachedBoard(t, 120, 34)
		cmd := m.fireVerb("spec", "")
		pump(t, m, cmd)
		if m.spec == nil {
			t.Fatalf("spec did not open the spec view (notice: %q)", m.notice.text)
		}
	})

	t.Run("approve", func(t *testing.T) {
		ctx := context.Background()
		ws, store, wt := uiRepo(t)
		m := NewShell(theme.GummiDark(), "v0-test")
		m.Attach(store, wt, ws)
		f := domain.Feature{ID: "FD-001", Num: 1, Title: "approve me", Slug: "approve-me", Stage: domain.StageTodo}
		if err := store.CreateFeature(ctx, &f); err != nil {
			t.Fatal(err)
		}
		m.rows = []featureRow{{F: f}}
		m.sel = 0

		cmd := m.fireVerb("approve", "")
		pump(t, m, cmd)

		got, err := store.GetFeature(ctx, "FD-001")
		if err != nil {
			t.Fatal(err)
		}
		if got.Stage != domain.StageBrainstorm {
			t.Fatalf("stage after approve = %s, want brainstorm — approve means advance the stage", got.Stage)
		}
	})
}

// TestStageEventLineClosedVocabulary is P1: before this fix,
// stageEventLine's default branch — the fallback for a kind it does not
// recognize — caught both park and decision_open, because neither had a
// case of its own, and printed the literal words "park" and
// "decision_open" as lines in a card's conversation. Store.Events
// returns every kind ever recorded (its own doc comment,
// state/cardevents.go), so any card whose log held either row rendered
// its bare kind word to a person reading the thread.
//
// This guards the fix and the vocabulary both: every kind
// stageEventLine can actually be asked to render — every constant
// declared in state/cardevents.go except EventStageEnter/EventStageExit,
// which stageSegments consumes before stageEventLine ever sees them (see
// TestStageSegmentsConsumesEnterExit) — is exercised here with a
// representative payload, and none of them may render as its own bare
// kind word. The next kind added to cardevents.go without a case here
// fails this test the same way park and decision_open did, instead of
// silently printing itself in production.
func TestStageEventLineClosedVocabulary(t *testing.T) {
	s := m0Styles()
	answered := map[string]bool{} // decision_open below is the unanswered case
	cases := []struct {
		kind    string
		payload string
	}{
		{state.EventMessage, `{"author":"user","content":"hello"}`},
		{state.EventTool, `{"label":"running tests"}`},
		{state.EventGate, `{"from":"spec","to":"plan","actor":"user"}`},
		{state.EventAsk, `{"question":"Persist where?","answer":"per-device","actor":"user"}`},
		// EventAutopilot with Event == "" is appendAutopilotEvent's shape
		// (state/cardevents.go): every SetGateApproval mode change writes
		// one of these, from both the TUI and the headless driver, so
		// real card logs already carry rows exactly this shape — this is
		// a third instance of the bug this test guards, not a
		// hypothetical one.
		{state.EventAutopilot, `{"mode":"full"}`},
		{state.EventPark, `{"reason":"needs-you","detail":"the run failed twice"}`},
		{state.EventDecisionOpen, `{"id":"d1","kind":"gate","question":"advance to plan?"}`},
	}
	for _, c := range cases {
		t.Run(c.kind, func(t *testing.T) {
			ev := state.CardEvent{Kind: c.kind, Payload: c.payload}
			got := ansi.Strip(stageEventLine(s, ev, 80, answered))
			if got == c.kind {
				t.Errorf("stageEventLine(%s) rendered its own bare kind word %q; give it a case", c.kind, got)
			}
		})
	}
}

// TestStageSegmentsConsumesEnterExit documents why EventStageEnter and
// EventStageExit are excluded from TestStageEventLineClosedVocabulary
// above rather than exercised there like every other kind:
// stageSegments consumes both itself, opening and closing a segment on
// them, and only forwards every other kind of event into that segment's
// own events slice (its own switch, thread.go). stageEventLine's default
// branch therefore can never actually be handed either kind in real use
// — there is no line it renders for them to get wrong — and what a
// stage_enter/stage_exit pair said is carried by the segment's own
// fields (boundaryRule, foldedReceiptLine), not by a line stageEventLine
// produces.
func TestStageSegmentsConsumesEnterExit(t *testing.T) {
	events := []state.CardEvent{
		{Kind: state.EventStageEnter, Stage: domain.StageSpec, Payload: `{"role":"architect"}`},
		{Kind: state.EventMessage, Stage: domain.StageSpec, Payload: `{"author":"user","content":"hi"}`},
		{Kind: state.EventStageExit, Stage: domain.StageSpec, Payload: `{"verdict":"ok"}`},
	}
	segs := stageSegments(events)
	if len(segs) != 1 {
		t.Fatalf("stageSegments() = %d segments, want 1", len(segs))
	}
	if len(segs[0].events) != 1 || segs[0].events[0].Kind != state.EventMessage {
		t.Fatalf("segment events = %v, want exactly the one message", segs[0].events)
	}
	for _, ev := range segs[0].events {
		if ev.Kind == state.EventStageEnter || ev.Kind == state.EventStageExit {
			t.Errorf("segment retained a %s event; stageSegments should have consumed it", ev.Kind)
		}
	}
}

// TestParkEventLine is P1's fix for EventPark: the line must carry the
// sentence the user was actually shown (ParkPayload.Detail), kept
// verbatim, not the bare word "park" the old default branch produced.
// Only an old row written before ParkPayload carried Detail at all — an
// empty string here — falls back to a sentence derived from Reason.
func TestParkEventLine(t *testing.T) {
	s := m0Styles()
	answered := map[string]bool{}
	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{
			"detail kept verbatim",
			`{"reason":"needs-you","detail":"verify failed twice in a row"}`,
			"parked — verify failed twice in a row",
		},
		{
			"quit, no detail",
			`{"reason":"quit"}`,
			"parked — the board quit",
		},
		{
			"gave up, no detail",
			`{"reason":"gave-up"}`,
			"parked — it gave up",
		},
		{
			"needs you, no detail",
			`{"reason":"needs-you"}`,
			"parked — it needs you",
		},
		{
			"unrecognized reason, no detail, reads as needs-you",
			`{"reason":"something-new-later"}`,
			"parked — it needs you",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev := state.CardEvent{Kind: state.EventPark, Payload: c.payload}
			got := ansi.Strip(stageEventLine(s, ev, 80, answered))
			if !strings.Contains(got, c.want) {
				t.Errorf("park line = %q, want to contain %q", got, c.want)
			}
		})
	}
}

// TestAutopilotEventLine is P1's fix for EventAutopilot. Store.
// SetGateApproval (internal/state/store.go) writes one of these, with
// AutopilotPayload.Event left "", on every gate-approval mode change
// from either the TUI or the headless driver (appendAutopilotEvent,
// state/cardevents.go), so real card logs already carry this row —
// before this fix, stageEventLine's default branch rendered it as the
// bare word "autopilot", the same bug this whole task is about, just a
// third instance of it. A row with Event set to a took-over/handed-back
// boundary is a different fact — a stretch of unattended driving, not a
// single mode change — and gets its own rendering from a later phase;
// for now it renders as nothing, same as the closed-vocabulary case.
func TestAutopilotEventLine(t *testing.T) {
	s := m0Styles()
	answered := map[string]bool{}
	t.Run("plain mode change", func(t *testing.T) {
		ev := state.CardEvent{Kind: state.EventAutopilot, Payload: `{"mode":"full"}`}
		got := ansi.Strip(stageEventLine(s, ev, 80, answered))
		if !strings.Contains(got, "autopilot set to full") {
			t.Errorf("autopilot line = %q, want to contain %q", got, "autopilot set to full")
		}
	})
	t.Run("took-over boundary renders nothing, for now", func(t *testing.T) {
		ev := state.CardEvent{Kind: state.EventAutopilot,
			Payload: `{"event":"took-over","reason":"idle","mode":"full"}`}
		if got := stageEventLine(s, ev, 80, answered); got != "" {
			t.Errorf("took-over boundary rendered %q, want empty (later phase's job)", got)
		}
	})
	t.Run("handed-back boundary renders nothing, for now", func(t *testing.T) {
		ev := state.CardEvent{Kind: state.EventAutopilot,
			Payload: `{"event":"handed-back","mode":"full"}`}
		if got := stageEventLine(s, ev, 80, answered); got != "" {
			t.Errorf("handed-back boundary rendered %q, want empty (later phase's job)", got)
		}
	})
}

// TestDecisionOpenEventLine is P1's fix for EventDecisionOpen, and the
// two DESIGN sections it has to satisfy at the same time. §6.3: once a
// decision is answered it collapses into that answer — the gate or ask
// row already rendered beside it — so the decision_open row that opened
// it renders nothing, or the same stop would say itself twice. §10.18:
// nothing may block a card without leaving a row, so a decision that was
// opened and then superseded WITHOUT ever being answered (a later run
// raised a different decision before a human reached this one) must
// still leave a visible trace, because the pinned open-decision control
// only ever shows the current decision — an unanswered, superseded one
// has nowhere else in the card's history to appear.
func TestDecisionOpenEventLine(t *testing.T) {
	s := m0Styles()
	open := state.CardEvent{
		Kind:    state.EventDecisionOpen,
		Payload: `{"id":"d1","kind":"gate","question":"advance to plan?"}`,
	}
	t.Run("answered collapses into its answer's row, not its own", func(t *testing.T) {
		answered := map[string]bool{"d1": true}
		if got := stageEventLine(s, open, 80, answered); got != "" {
			t.Errorf("answered decision_open rendered %q, want empty", got)
		}
	})
	t.Run("unanswered leaves a trace", func(t *testing.T) {
		answered := map[string]bool{} // d1 never answered
		got := ansi.Strip(stageEventLine(s, open, 80, answered))
		for _, want := range []string{"advance to plan?", "unanswered", "superseded"} {
			if !strings.Contains(got, want) {
				t.Errorf("unanswered decision_open line %q missing %q", got, want)
			}
		}
	})
}

// TestAnsweredDecisions checks the id set threadRender builds once per
// render and hands down to stageEventLine's decision_open case: every
// GatePayload.ID and AskPayload.ID appearing anywhere in a card's event
// log, per the correlation EventDecisionOpen's own doc comment describes
// (state/cardevents.go) — and nothing else, so a decision that opened
// but whose id never shows up on a later gate/ask stays unanswered.
func TestAnsweredDecisions(t *testing.T) {
	events := []state.CardEvent{
		{Kind: state.EventDecisionOpen, Payload: `{"id":"d1","kind":"gate"}`},
		{Kind: state.EventGate, Payload: `{"from":"spec","to":"plan","actor":"user","id":"d1"}`},
		{Kind: state.EventDecisionOpen, Payload: `{"id":"d2","kind":"ask"}`},
		{Kind: state.EventAsk, Payload: `{"question":"q","answer":"a","actor":"user","id":"d2"}`},
		{Kind: state.EventDecisionOpen, Payload: `{"id":"d3","kind":"gate"}`}, // never answered
	}
	got := answeredDecisions(events)
	if !got["d1"] || !got["d2"] {
		t.Errorf("answeredDecisions() = %v, want d1 and d2 answered", got)
	}
	if got["d3"] {
		t.Error("answeredDecisions() marked d3 answered, but nothing in the log ever answered it")
	}
}
