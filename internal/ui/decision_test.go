package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
)

// The pinned decision's focused tests: visibility at the width the thread
// is actually driven at, parity with the chat pane's picker, the idle and
// todo states it exists to unstrand, answering a live ask_user from the
// thread, and the collapse into history once answered.

// TestThreadDecisionVisibleAt36x9 is the pinning arithmetic (PLAN §2.4):
// at 36×9 the thread's body renders zero rows, so a decision drawn in the
// body would be invisible at exactly the width the thread is driven at.
// Its own region above the composer must show the question and the
// highlighted answer while the head and body yield.
func TestThreadDecisionVisibleAt36x9(t *testing.T) {
	m := populatedShell(36, 9)
	m.sel = 1 // FD-042, implement — nothing running, so a decision is open
	out := ansi.Strip(m.threadView(33, 6))
	if !strings.Contains(out, "nothing is running") {
		t.Errorf("36x9 thread missing the open decision's question:\n%s", out)
	}
	if !strings.Contains(out, "run implement") {
		t.Errorf("36x9 thread missing the highlighted answer:\n%s", out)
	}
	if !strings.Contains(out, "┃") {
		t.Errorf("36x9 thread lost the composer under the decision:\n%s", out)
	}
	// the head yields to the decision: the pinned spec line is the first
	// thing dropped, the masthead the second
	if strings.Contains(out, "Implementation notes") {
		t.Errorf("36x9 thread spent rows on the spec line while the decision showed:\n%s", out)
	}
}

// TestThreadDecisionAdvancesIdleTodo: a card in todo has no key at all
// now that empty-composer enter runs nothing — the idle decision is what
// keeps it reachable. Enter answers the highlighted option ("start"),
// which routes through the same advance verb its accelerator names.
func TestThreadDecisionAdvancesIdleTodo(t *testing.T) {
	m, _ := chatWorkspace(t, agent.NewFake("hi"))
	// a fresh second card sits in todo, untouched by the fixture's advance
	m = press(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	m = typeString(t, m, "Second card")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	selectRow(t, m, "FD-002")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // open its card page

	d := m.openDecision(m.rows[m.sel])
	if d == nil || len(d.actions) == 0 || d.actions[0].id != "advance" {
		t.Fatalf("todo card has no idle decision to answer: %+v", d)
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // answer it
	if m.rows[m.sel].F.Stage != domain.StageBrainstorm {
		t.Fatalf("answering the idle decision left the card at %s, want brainstorm", m.rows[m.sel].F.Stage)
	}
}

// TestThreadDecisionMatchesTheChatPicker is the parity contract: the
// thread's pinned decision and the chat pane's picker render the same
// control for the same pending ask, because they are the same renderer.
func TestThreadDecisionMatchesTheChatPicker(t *testing.T) {
	m, eng := chatWorkspace(t, askingFake())
	m = openAndAttach(t, m)
	waitAsk(t, eng)
	ask := eng.Get("FD-001").Snapshot().PendingAsk
	if ask == nil {
		t.Fatal("precondition: the ask is pending")
	}
	out := ansi.Strip(m.threadView(100, 30))
	want := pickerView(m0Styles(), "FD-001 asks", ask.Question,
		askPickerOptions(ask), 0, nil, ask.MultiPick, 100)
	for _, line := range strings.Split(ansi.Strip(want), "\n") {
		if line == "" {
			continue
		}
		if !strings.Contains(out, line) {
			t.Errorf("thread decision missing the picker row %q:\n%s", line, out)
		}
	}
}

// TestThreadDecisionAnswersLiveAsk: the thread itself can answer a
// blocking ask_user — the pinned decision keeps the question answerable
// on the card page, from the composer that is already focused. This is
// the capability the chat pane's retirement depends on.
func TestThreadDecisionAnswersLiveAsk(t *testing.T) {
	m, eng := chatWorkspace(t, askingFake())
	m = openAndAttach(t, m)
	waitAsk(t, eng)

	press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // answer the pinned decision
	deadline := time.After(testWaitTimeout)
	for eng.Get("FD-001").Snapshot().PendingAsk != nil {
		select {
		case <-deadline:
			t.Fatal("answering from the thread did not clear the pending ask")
		case <-time.After(10 * time.Millisecond):
		}
	}
	var got string
	for _, msg := range eng.Get("FD-001").Snapshot().Transcript {
		if msg.Author == engine.AuthorUser {
			got = msg.Content
		}
	}
	if got != "per-device" {
		t.Errorf("thread answer recorded as %q, want per-device", got)
	}
}

// TestAnsweredAskCollapsesIntoHistory: once answered, an ask is not a
// pinned control any more — it is one compact line in the body's history
// where it happened, the same as a crossed gate.
func TestAnsweredAskCollapsesIntoHistory(t *testing.T) {
	s := m0Styles()
	at := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	ask := state.CardEvent{
		Kind: state.EventAsk, At: at,
		Payload: `{"question":"Persist where?","answer":"per-device","actor":"user"}`,
	}
	gate := state.CardEvent{
		Kind: state.EventGate, At: at,
		Payload: `{"from":"spec","to":"plan","actor":"user"}`,
	}
	askLine := ansi.Strip(stageEventLine(s, ask, 80))
	for _, want := range []string{"you answered", "Persist where?", "per-device"} {
		if !strings.Contains(askLine, want) {
			t.Errorf("answered ask line %q missing %q", askLine, want)
		}
	}
	gateLine := ansi.Strip(stageEventLine(s, gate, 80))
	for _, want := range []string{"you advanced", "spec", "plan"} {
		if !strings.Contains(gateLine, want) {
			t.Errorf("crossed gate line %q missing %q", gateLine, want)
		}
	}
	if strings.Contains(askLine, "\n") || strings.Contains(gateLine, "\n") {
		t.Error("answered decisions must collapse to one line each")
	}
}

// TestThreadDecisionWindowsAroundTheCursor: a decision with more options
// than its row budget keeps the question and the highlighted answer
// visible and states what is hidden, rather than trimming from the bottom
// and leaving the cursor on a row that is no longer on screen.
func TestThreadDecisionWindowsAroundTheCursor(t *testing.T) {
	s := m0Styles()
	lines := []string{"gummi  the question."}
	for _, label := range []string{"one", "two", "three", "four", "five"} {
		lines = append(lines, "  "+label)
	}
	// three rows: the question, one option row, the hidden count
	got := windowDecisionBlock(s, lines, 1, 5, 3, 3)
	if len(got) != 3 {
		t.Fatalf("windowed decision = %q, want 3 rows", got)
	}
	if got[0] != lines[0] {
		t.Errorf("question row lost: %q", got[0])
	}
	if !strings.Contains(ansi.Strip(got[1]), "four") {
		t.Errorf("window did not follow the cursor: %q", got[1])
	}
	if !strings.Contains(ansi.Strip(got[2]), "…4 more") {
		t.Errorf("hidden count not stated: %q", got[2])
	}

	// a budget of two keeps the highlighted answer over the count
	got = windowDecisionBlock(s, lines, 1, 5, 4, 2)
	if len(got) != 2 || !strings.Contains(ansi.Strip(got[1]), "five") {
		t.Fatalf("two-row window = %q, want the question and the cursor's row", got)
	}
}

// TestThreadDecisionDropsBelowTheHighlightedAnswer is F21:
// windowDecisionBlock's comment promises the question and the highlighted
// answer never yield, but at a small enough budget it used to keep the
// question (and only the question) — a title with no way to act on it,
// which at 20×5 read as the bare word "gummi" with no option row, and at
// 18×4 as that word being the whole block. When the budget cannot hold
// even the highlighted answer, the whole decision block drops instead —
// the row is more honestly spent on conversation or left blank.
func TestThreadDecisionDropsBelowTheHighlightedAnswer(t *testing.T) {
	m := attachedBoard(t, 120, 34)
	m.sel = 3 // FD-049, spec — an idle workflow decision
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	r := m.rows[m.sel]
	d := m.openDecision(r)
	if d == nil {
		t.Fatal("precondition: FD-049 has an open decision")
	}

	s := m0Styles()
	// maxRows 1: less than the question alone needs (headRows is at
	// least 1) — no room left for even the highlighted answer.
	if got := m.openDecisionBlock(s, r, 60, 1); len(got) != 0 {
		t.Errorf("openDecisionBlock at maxRows=1 = %q, want no block at all", got)
	}
	// a plainly generous budget still renders the question and the
	// highlighted answer, so the drop is specific to the tiny budget
	// above rather than something broken in general.
	if got := m.openDecisionBlock(s, r, 60, 8); len(got) < 2 {
		t.Errorf("openDecisionBlock at maxRows=8 = %q, want the question and at least one answer", got)
	}
}

// TestThreadDecisionSmallFrameDropsRatherThanBareTitle exercises F21 end
// to end through composeThread at the exact terminal sizes the finding
// names: 20×5 used to degrade to the bare word "gummi" with no option
// row, and 18×4 to that word being the whole block. Both must now show no
// decision block at all, and 36×9 — the design's own frame — must be
// unaffected (TestThreadDecisionVisibleAt36x9 already covers that in
// full; this only reconfirms it stays intact alongside the two drops).
func TestThreadDecisionSmallFrameDropsRatherThanBareTitle(t *testing.T) {
	// the decision's own question text — unique to the block, unlike its
	// "gummi" title, which the masthead's own logo pill also prints, so
	// checking for the title alone would pass even with the block gone.
	const question = "nothing is running"
	for _, sz := range []struct{ w, h int }{{20, 5}, {18, 4}} {
		m := attachedBoard(t, sz.w, sz.h)
		m.sel = 3 // FD-049, spec — an idle workflow decision
		m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
		out := ansi.Strip(m.View().Content)
		if strings.Contains(out, question) {
			t.Errorf("%dx%d still rendered a decision block nobody can act on:\n%s", sz.w, sz.h, out)
		}
	}

	m36 := attachedBoard(t, 36, 9)
	m36.sel = 3
	m36 = press(t, m36, tea.KeyPressMsg{Code: tea.KeyEnter})
	out := ansi.Strip(m36.View().Content)
	if !strings.Contains(out, question) {
		t.Errorf("36x9 lost the decision's question:\n%s", out)
	}
	if !strings.Contains(out, "┃") {
		t.Errorf("36x9 lost the composer under the decision:\n%s", out)
	}
}

// reviewGateWorkspace walks a fresh feature to review and raises the gate
// attention, so its card page carries a gate decision — read the findings,
// bounce to implement, advance to verify — with the bounce as the option
// that consumes words.
func reviewGateWorkspace(t *testing.T) *Shell {
	t.Helper()
	m, _ := newWorkspace(t)
	m = pump(t, m, m.Init())
	m = press(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	m = typeString(t, m, "Bouncy")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	for range 5 { // todo→brainstorm→spec→plan→implement→review
		m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	}
	if m.rows[0].F.Stage != domain.StageReview {
		t.Fatalf("stage = %s, want review", m.rows[0].F.Stage)
	}
	m.raiseAttention("FD-001", attnGate, "review is ready for your decision")
	return press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // open the card page
}

// TestThreadDecisionTypingGolden is the coupled state's review surface:
// the composer holds prose, so the decision's highlight has moved onto
// the option that consumes the words, that option's label names what
// enter will do with them, and the status bar's enter hint says the same
// — the screen states the delivery before it happens (DESIGN §6.3).
// 120 columns so the bar has room to name enter beside the pills.
func TestThreadDecisionTypingGolden(t *testing.T) {
	m := reviewGateWorkspace(t)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
	m = model.(*Shell)
	m = typeString(t, m, "the contrast is off in dark mode")
	golden.RequireEqual(t, []byte(m.View().Content))
}

// TestThreadDecisionTypingIsChoosing is the composer coupling (DESIGN
// §6.3): typing prose while a decision is open aims the highlight at the
// option that consumes words and relabels it to say what enter will do
// with them, and enter delivers the line there. At a review gate that
// option is the bounce: the findings go back with it.
func TestThreadDecisionTypingIsChoosing(t *testing.T) {
	m := reviewGateWorkspace(t)

	d := m.openDecision(m.rows[m.sel])
	if d == nil || d.kind != decisionGate {
		t.Fatalf("review gate has no gate decision: %+v", d)
	}
	if i := d.wordConsumer(); i != 1 {
		t.Fatalf("word consumer = %d, want the bounce at 1 (actions %v)", i, d.actions)
	}

	m = typeString(t, m, "the contrast is off in dark mode")
	out := ansi.Strip(m.threadView(100, 30))
	if !strings.Contains(out, "bounce to implement with your words") {
		t.Errorf("typing did not aim and relabel the word-eating option:\n%s", out)
	}
	if m.decisionCursor != 1 {
		t.Errorf("typed prose left the cursor at %d, want the bounce at 1", m.decisionCursor)
	}

	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.rows[m.sel].F.Stage != domain.StageImplement {
		t.Fatalf("enter did not deliver the line to the bounce: stage %s", m.rows[m.sel].F.Stage)
	}
	if got := m.bounceNotes["FD-001"]; got != "the contrast is off in dark mode" {
		t.Errorf("bounce carried %q, want the composer's line", got)
	}
}

// TestThreadDecisionBounceNoteRidesTheNextRun: the note a bounce carries
// has nowhere to land until the reborn work stage runs — it waits in
// bounceNotes and rides that run's kickoff, the delivery the headless
// --bounce note takes.
func TestThreadDecisionBounceNoteRidesTheNextRun(t *testing.T) {
	m, eng := chatWorkspace(t, agent.NewFake("on it"))
	// advance to implement: the work stage a bounce rewinds to
	for range 3 { // brainstorm→spec→plan→implement
		m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	}
	if m.rows[m.sel].F.Stage != domain.StageImplement {
		t.Fatalf("stage = %s, want implement", m.rows[m.sel].F.Stage)
	}
	m.bounceNotes = map[domain.FeatureID]string{
		"FD-001": "the findings say retry the flaky check first",
	}

	// the run the stash waits for: enter answers the idle decision
	m = openAndAttach(t, m)
	settleChat(t, eng)

	snap := eng.Get("FD-001").Snapshot()
	// the kickoff is gummi's own turn, not the user's — the bounce note
	// rides inside it, quoted, the same way a RunWith review note does
	if len(snap.Transcript) == 0 || snap.Transcript[0].Author != engine.AuthorSystem {
		t.Fatalf("kickoff missing from the transcript: %+v", snap.Transcript)
	}
	if !strings.Contains(snap.Transcript[0].Content, "the findings say retry the flaky check first") {
		t.Errorf("bounce note did not ride the kickoff: %q", snap.Transcript[0].Content)
	}
	if _, ok := m.bounceNotes["FD-001"]; ok {
		t.Error("the bounce note survived the run that consumed it")
	}
}

// TestThreadDecisionTypedProseRidesTheRun: at an autonomous idle decision
// the word-eater is the run — typed prose re-runs the stage with the line
// appended to its kickoff.
func TestThreadDecisionTypedProseRidesTheRun(t *testing.T) {
	m, eng := chatWorkspace(t, agent.NewFake("on it"))
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"}) // brainstorm→spec
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"}) // spec→plan
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // open the card page

	d := m.openDecision(m.rows[m.sel])
	if d == nil || d.wordConsumer() != 0 || d.actions[0].id != "run" {
		t.Fatalf("plan idle decision has no run to aim at: %+v", d)
	}

	m = typeString(t, m, "focus the plan on the retry path")
	out := ansi.Strip(m.threadView(100, 30))
	if !strings.Contains(out, "run the planner with your words") {
		t.Errorf("typing did not relabel the run:\n%s", out)
	}
	press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	settleChat(t, eng)

	snap := eng.Get("FD-001").Snapshot()
	if len(snap.Transcript) == 0 {
		t.Fatal("run never started")
	}
	// the typed line rides gummi's own kickoff turn, not a user turn —
	// same as the bounce note in TestThreadDecisionBounceNoteRidesTheNextRun
	for _, msg := range snap.Transcript {
		if msg.Author == engine.AuthorSystem && strings.Contains(msg.Content, "focus the plan on the retry path") {
			return
		}
	}
	t.Errorf("the line did not ride the kickoff: %+v", snap.Transcript)
}

// TestThreadDecisionTypedProseAnswersTheAsk: a free-form ask consumes the
// composer's line as the answer — the pane's 'o' channel, always on here
// because the composer is always on.
func TestThreadDecisionTypedProseAnswersTheAsk(t *testing.T) {
	m, eng := chatWorkspace(t, askingFake())
	m = openAndAttach(t, m)
	waitAsk(t, eng)

	// The line starts with a word on purpose: on an empty line a digit is
	// a picker key that answers an option (the pane's own contract), so
	// prose wanting to start with one would be hijacked mid-sentence —
	// the thread types prose the way the pane's 'o' channel does, from
	// the second keystroke on.
	m = typeString(t, m, "the floor is 2.4 to 1, note the exception in the spec")
	press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	deadline := time.After(testWaitTimeout)
	for eng.Get("FD-001").Snapshot().PendingAsk != nil {
		select {
		case <-deadline:
			t.Fatal("typed prose did not answer the ask")
		case <-time.After(10 * time.Millisecond):
		}
	}
	var got string
	for _, msg := range eng.Get("FD-001").Snapshot().Transcript {
		if msg.Author == engine.AuthorUser {
			got = msg.Content
		}
	}
	if got != "the floor is 2.4 to 1, note the exception in the spec" {
		t.Errorf("ask answered with %q, want the composer's line", got)
	}
}

// TestThreadDecisionStructuredAskLabelsEnterSend is F4: with a structured
// (non-free-form) ask pinned and prose typed in front of it, the bar used
// to keep the label "answer" — but submitThreadLine only ever routes
// prose to the ask when it declared allow_free_form (DESIGN §6.3), so a
// structured ask's typed line falls through and goes out as an ordinary
// turn while the question stays open. Confirmed against a live agent: the
// ask never got a reply and the spinner ran forever. The fix is the
// label, not the routing — enter has to say "send" here, the same as any
// other prose the decision has nowhere to spend.
func TestThreadDecisionStructuredAskLabelsEnterSend(t *testing.T) {
	m, eng := chatWorkspace(t, structuredAskFake())
	m = openAndAttach(t, m)
	waitAsk(t, eng)

	ask := eng.Get("FD-001").Snapshot().PendingAsk
	if ask == nil || ask.FreeForm {
		t.Fatalf("precondition: a structured (non-free-form) ask is pending, got %+v", ask)
	}

	m = typeString(t, m, "please point me at the rig")
	for _, b := range m.threadInputBindings() {
		if b.key != "enter" {
			continue
		}
		if b.label != "send" {
			t.Errorf("enter labeled %q while pinned to a structured ask with typed prose, want \"send\"", b.label)
		}
		if !strings.Contains(b.help, "message") {
			t.Errorf("enter help %q does not say the line becomes a message", b.help)
		}
	}

	// confirm the routing itself is untouched (DESIGN §6.3): enter really
	// does send a turn, and the ask is still open behind it
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	deadline := time.After(testWaitTimeout)
	for {
		snap := eng.Get("FD-001").Snapshot()
		if len(snap.Transcript) > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("enter never sent the line as a turn")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if eng.Get("FD-001").Snapshot().PendingAsk == nil {
		t.Error("the structured ask closed on its own — it should still be open")
	}
}

// TestThreadDecisionACommandKeepsTheParser: the collision the composer
// coupling settles — verb-words are commands the parser owns (the chip is
// their confirmation), so typing one leaves the highlight where it was,
// raises the chip on enter, and its esc still sends the line as a
// message. The screen never claims the words for an option enter will
// not deliver them to.
func TestThreadDecisionACommandKeepsTheParser(t *testing.T) {
	m := reviewGateWorkspace(t)

	m = typeString(t, m, "verify the contrast is right")
	out := ansi.Strip(m.threadView(100, 30))
	if strings.Contains(out, "with your words") {
		t.Errorf("a command aimed the highlight at the word-eater:\n%s", out)
	}
	if m.decisionCursor != 0 {
		t.Errorf("a command moved the cursor to %d, want 0", m.decisionCursor)
	}

	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.threadChip == nil || m.threadChip.verb != "verify" {
		t.Fatalf("enter did not raise the chip: %+v", m.threadChip)
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape}) // no — send as a message
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !strings.Contains(m.notice.text, "no live session") {
		t.Errorf("chip esc did not send the line as a message: %+v", m.notice)
	}
	if m.rows[m.sel].F.Stage != domain.StageReview {
		t.Fatalf("the bounced card moved to %s", m.rows[m.sel].F.Stage)
	}
}

// TestThreadDecisionDigitSelectsWorkflowOption is F14's other half: on a
// workflow decision a digit used to type into the composer and, because
// the line was then non-empty, wordAim yanked the highlight onto the
// word-consuming option instead — on a four-option gate, pressing 2
// selected 1. A digit now selects the option it names directly, on an
// empty composer, the same as ↑↓, and leaves it to enter to commit. The
// review gate is the reproduction case in the finding: four options,
// bounce (a word-eater) sitting at index 1.
func TestThreadDecisionDigitSelectsWorkflowOption(t *testing.T) {
	m := reviewGateWorkspace(t)
	d := m.openDecision(m.rows[m.sel])
	if d == nil || len(d.actions) < 4 || d.actions[1].id != "bounce" {
		t.Fatalf("precondition: a four-option review gate with bounce at 1, got %+v", d)
	}

	// digit 2 selects option index 1 — it must not land in the composer,
	// and it must not commit on its own
	m = press(t, m, tea.KeyPressMsg{Code: '2', Text: "2"})
	if got := m.threadInput.Value(); got != "" {
		t.Fatalf("the digit typed into the composer instead of selecting: %q", got)
	}
	if m.decisionCursor != 1 {
		t.Fatalf("digit 2 left the cursor at %d, want 1", m.decisionCursor)
	}
	if m.rows[m.sel].F.Stage != domain.StageReview {
		t.Fatalf("the digit alone committed the option — stage moved to %s", m.rows[m.sel].F.Stage)
	}

	// only enter commits it
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.rows[m.sel].F.Stage != domain.StageImplement {
		t.Fatalf("enter did not commit the selected (bounce) option: stage = %s", m.rows[m.sel].F.Stage)
	}
}

// TestThreadDecisionProseNothingConsumesSends: where no option takes
// prose — a budget stop offers top-up and park, not a listener — the
// line sends as a turn, always safe (DESIGN §6.3), and the bar says so.
func TestThreadDecisionProseNothingConsumesSends(t *testing.T) {
	m, _ := chatWorkspace(t, agent.NewFake("on it"))
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"}) // brainstorm→spec
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"}) // spec→plan
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	m.raiseAttention("FD-001", attnBudget, "the plan stage reached its envelope")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // open the card page

	d := m.openDecision(m.rows[m.sel])
	if d == nil || d.kind != decisionBudget || d.wordConsumer() != -1 {
		t.Fatalf("budget decision mis-shaped: %+v", d)
	}

	m = typeString(t, m, "top it up to 400")
	for _, b := range m.threadInputBindings() {
		if b.key == "enter" && b.label != "send" {
			t.Errorf("bar claims enter for %q while nothing consumes the words", b.label)
		}
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !strings.Contains(m.notice.text, "no live session") {
		t.Errorf("prose with no consumer did not send as a message: %+v", m.notice)
	}
	if m.rows[m.sel].F.Stage != domain.StagePlan {
		t.Fatalf("the card moved to %s", m.rows[m.sel].F.Stage)
	}
}
