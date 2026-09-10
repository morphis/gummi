package ui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui/theme"
)

type decisionKind string

const (
	decisionAsk     decisionKind = "ask"
	decisionGate    decisionKind = "gate"
	decisionVerify  decisionKind = "verify"
	decisionBudget  decisionKind = "budget"
	decisionFailure decisionKind = "failure"
	decisionIdle    decisionKind = "idle"
)

// threadDecision is a render-time projection, not a second stored model.
// Step 4 makes open decisions durable; until then asks come from the live
// session and workflow options are regenerated from nextActions.
type threadDecision struct {
	key      string
	kind     decisionKind
	question string
	actions  []nextAction
	ask      *engine.Ask
}

func (m *Shell) openDecision(r featureRow) *threadDecision {
	if r.DrivenAbroad {
		// Nothing here could answer: the pending ask's resolver lives in
		// the owning process, and nextActions' suggestions are about
		// driving the card locally, which would be a lie on screen. The
		// question itself is not swallowed — the followed session's own
		// footer (follow.go) names it and says which pid to answer it
		// from. A picker that can answer across the process boundary is
		// later work (PLAN R5).
		//
		// The composer is NOT withheld, as this said: submitThreadLine
		// routes a foreign card's line to its consult session instead.
		return nil
	}
	if sess := m.sessionFor(r.F.ID); sess != nil {
		snap := sess.Snapshot()
		if ask := snap.PendingAsk; ask != nil {
			key := "ask|" + string(r.F.ID) + "|" + ask.CallID + "|" + ask.Question
			return &threadDecision{key: key, kind: decisionAsk, question: ask.Question, ask: ask}
		}
		if sess.Interactive && snap.Busy {
			// the architect is mid-turn in this very thread: the composer
			// below is the input to a conversation still going, and there
			// is nothing to decide about work that has not finished. This
			// is §10.19's rule and it reads in one direction only — a bare
			// composer means an agent is working, so the moment one stops
			// working the composer must stop being bare. Suppressing the
			// decision for the whole life of an interactive session, as
			// this did, left a finished spec with no way to approve it on
			// the surface that is supposed to be the way through the
			// workflow.
			return nil
		}
	}

	in := m.nextInputFor(r)
	// stageActions, not nextActions: the picker renders the ANSWER SET —
	// the workflow answers to "what now" — while nextActions is the
	// action inventory's ranking feed and carries the per-card nudges
	// (the PR-review pull) that are reading surfaces rather than answers.
	// Rendering those here is what made the control a flat list of
	// equal-weight options in the first place (nextsteps.go's own doc).
	actions := stageActions(in)
	if len(actions) == 0 || in.landed || in.stage == domain.StageDone {
		return nil
	}
	kind := decisionIdle
	switch {
	case in.attn == attnFailure:
		kind = decisionFailure
	case in.attn == attnBudget:
		kind = decisionBudget
	case in.stage == domain.StageVerify && in.finished():
		kind = decisionVerify
	case in.attn == attnGate:
		kind = decisionGate
	}
	question := decisionQuestion(kind, r, in)
	ids := make([]string, 0, len(actions))
	for _, action := range actions {
		ids = append(ids, action.id)
	}
	key := strings.Join([]string{string(kind), string(r.F.ID), string(r.F.Stage), strings.Join(ids, ",")}, "|")
	return &threadDecision{key: key, kind: kind, question: question, actions: actions}
}

// visibleDecision is openDecision narrowed to a decision that is also
// currently on screen: nil both when there is no open decision and when
// there is one but windowDecisionBlock dropped it for lack of room (F21).
// The bar and every key handler that acts on a decision must gate on this
// rather than the bare openDecision(r) != nil check, or they act on a
// picker the reader cannot see (BG-058).
//
// It force-refreshes m.decisionDrawn with a real (non-measure) render
// before answering, rather than trusting whatever the last View() call
// left behind: Update() can be, and in gummi's own headless driving
// paths routinely is, called several times between renders, so a cache
// only threadRender writes to would answer against a stale height or a
// stale row. threadRender is already idempotent to repeat calls with
// unchanged state (it exists to be the single honest measure of the
// layout — maxThreadScroll relies on the same property), so paying for
// an extra render here costs nothing beyond the CPU cycles.
//
// The render is forced at cardThreadSize, not the raw m.width/m.height:
// the frame threadView actually paints is narrower and shorter than the
// bare shell dimensions by the main pane's own gutter and the card
// page's crumb/blank chrome (cardPageView), and checking against the
// wrong box answers "would this draw" for a frame the reader never
// sees (BG-058 review).
func (m *Shell) visibleDecision(r featureRow) *threadDecision {
	d := m.openDecision(r)
	if d == nil {
		return nil
	}
	if m.width <= 0 || m.height <= 0 {
		// nothing has rendered at all yet (pre-WindowSizeMsg) — View()
		// itself refuses to draw at this size, so nothing is on screen.
		return nil
	}
	w, h := m.cardThreadSize()
	m.threadRender(w, h, false)
	if !m.decisionDrawn {
		return nil
	}
	return d
}

// cardThreadSize is the width and height threadView is actually given
// when it paints the open card's page: the main pane less the -3
// column gutter and any notice band Draw reserves before ever calling
// mainView, then cardPageView's own crumb/blank rows on top. It exists
// so visibleDecision's forced re-render measures the same box as the
// one on screen, rather than the raw shell size (BG-058 review) —
// threadSize (shell.go) answers a related but distinct question (the
// scroll step) and does not subtract the gutter, so it is not reused
// here.
func (m *Shell) cardThreadSize() (int, int) {
	l := m.computeLayout()
	w := max(l.Main.Dx()-3, 0)
	h := l.Main.Dy()
	if band := m.noticeBand(w); len(band) > 0 {
		h = max(h-len(band)-1, 0)
	}
	crumb, blank := cardPageChrome(h)
	return w, max(h-crumb-blank, 1)
}

// wordConsumer is the index of the option that consumes the composer's
// line, or -1 when none does: the first action in the decision's order
// whose delivery takes prose — the run that opens (or re-runs) a stage,
// which rides the line as its kickoff note or first turn, and the bounce
// that sends findings back with it. Everything else — advance, read the
// spec, open the inbox — has nowhere to spend words, so a typed line sends
// as a turn instead (DESIGN §6.3: prose is always accepted and always
// safe; it becomes a turn, never an action nobody offered).
func (d *threadDecision) wordConsumer() int {
	if d == nil || d.ask != nil {
		return -1
	}
	for i, action := range d.actions {
		if action.id == "run" || action.id == "bounce" || action.id == "changes" {
			return i
		}
	}
	return -1
}

// optionCount is the number of rows the picker shows for this decision —
// the workflow actions, or a live ask's options plus the synthetic "Chat
// about this" row askPickerOptions appends. It is the one place that
// counts rows, so every bound check (↑↓, digit-select, the o-key's
// landing, answerDecision's enter guard) agrees on where the synthetic
// row lives without a shared constant (invariant, Considered approaches).
func (d *threadDecision) optionCount() int {
	if d.ask != nil {
		return len(askPickerOptions(d.ask))
	}
	return len(d.actions)
}

// pickerOption is one row in the decision's picker: what the choice is,
// and the detail that says what it does.
//
// There is no key field. There used to be — nextAction.key, the board
// accelerator (g, s, A, b, d, v…) — rendered in a right-hand column, but
// that accelerator only fires from the backlog list, where this picker
// never shows: on the card page the same letter reaches the composer and
// types (F12). A control that prints a key which does something else
// entirely one keystroke later is worse than one that names no key at
// all, so the column is gone rather than fixed — the row's number is
// still there for the digit that does work (F14).
type pickerOption struct {
	label    string
	detail   string
	danger   bool
	noToggle bool
}

// askPickerOptions shapes a live ask_user question for the picker. A
// free-form ask gains a synthetic, always-present "Chat about this" row
// after the real options, so the free-form channel is visible before the
// user ever types anything — closing the gap where the picker offered no
// legible way to see that prose would be routed there (Chosen approach).
// The row's index is always len(ask.Options): appended last and never
// reordered, so wordAim, the digit/arrow bound checks, the o-key handler,
// and answerDecision's enter guard all agree on it without a shared
// constant.
func askPickerOptions(ask *engine.Ask) []pickerOption {
	options := make([]pickerOption, 0, len(ask.Options)+1)
	for _, option := range ask.Options {
		options = append(options, pickerOption{label: option.Label, detail: option.Detail})
	}
	if ask.FreeForm {
		options = append(options, pickerOption{
			label:    "Chat about this",
			detail:   "reply with your own words instead of picking an option",
			noToggle: true,
		})
	}
	return options
}

// pickerView is the shared inline decision picker. The card thread feeds
// it a live ask_user question; it also renders regenerated workflow
// actions. Selection state is explicit so neither caller has to fake the
// other's model merely to reuse its renderer.
// pickerQuestionLines caps how far a question may wrap onto its own
// rows. Two is enough for the questions the guide actually poses, and a
// cap is what stops a long one from eating the answers it is asking
// about.
const pickerQuestionLines = 2

// pickerHead renders the control's opening rows: the title, with the
// question beside it when it fits and beneath it when it does not.
//
// The question is what the control is for, so it is never the thing that
// gives up width. At the widths the thread is actually driven at, keeping
// it on the title's line meant truncating it away — "review is ready for
// y…" — which is the one row on the page that has to survive.
func pickerHead(s *theme.Styles, title, question string, width int) []string {
	t, q := sanitize(title), sanitize(question)
	if ansi.StringWidth(q) <= max(width-ansi.StringWidth(t)-2, 0) {
		return []string{s.Muted.Render(t) + "  " + s.Base.Render(q)}
	}
	out := []string{s.Muted.Render(t)}
	wrapped := strings.Split(wrapText(q, max(width-1, 8)), "\n")
	if len(wrapped) > pickerQuestionLines {
		wrapped = wrapped[:pickerQuestionLines]
		wrapped[pickerQuestionLines-1] = ansi.Truncate(wrapped[pickerQuestionLines-1], max(width-2, 4), "") + "…"
	}
	for _, l := range wrapped {
		out = append(out, " "+s.Base.Render(l))
	}
	return out
}

func pickerView(s *theme.Styles, title, question string, options []pickerOption, selected int, picked map[int]bool, multi bool, w int, armed bool) string {
	width := max(w-2, 10)
	var b strings.Builder
	for _, l := range pickerHead(s, title, question, width) {
		b.WriteString(l + "\n")
	}
	for i, option := range options {
		// maxExtra 0: pickerView renders every option at once with no
		// notion of the vertical budget the caller actually has, so it
		// can only ever afford the one truncated line it always has
		// (F20's wrap is openDecisionBlock's own call to make, once it
		// knows what's left over — expandHighlighted, below).
		for _, l := range pickerOptionLines(s, option, i, selected, picked, multi, width, 0, armed) {
			b.WriteString(l + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// pickerOptionLines renders one option's row. With maxExtra 0 (every
// caller but expandHighlighted) it is exactly one line, truncated — the
// marker, the multi-pick box, the number, the label and its detail, cut
// off mid-word if they don't fit, the way this whole row has always
// rendered.
//
// With maxExtra > 0 — asked only of the highlighted option, and only when
// windowDecisionBlock found the block genuinely had rows to spare (F20) —
// a row too long for one line spills its detail onto up to maxExtra
// continuation lines instead of losing the tail to "…". The label and the
// row's own furniture stay on the first line unchanged; only the detail,
// the part that actually runs long ("please point me at the rig…"),
// wraps.
//
// armed says whether enter actually answers this picker right now
// (decisionArmed): the highlighted row wears its bright marker and title
// paint only while armed. The moment something else has taken the
// composer's line — a verb, the confirm chip, an armed free-form channel
// — the row falls back to the same dim, unfocused paint board.go gives a
// remembered-but-not-armed cursor (SelMarkerDim, s.Base, no bright band),
// so the picker never keeps claiming enter after the bar has already
// named a different destination for it.
func pickerOptionLines(s *theme.Styles, option pickerOption, i, selected int, picked map[int]bool, multi bool, width, maxExtra int, armed bool) []string {
	marker := "  "
	label := s.Base
	if i == selected {
		if armed {
			marker = s.KeyHint.Render("▸ ")
			label = s.Title
		} else {
			marker = s.SelMarkerDim.Render("▸ ")
			label = s.Base
		}
	}
	tick := ""
	if multi && !option.noToggle {
		box := "○ "
		if picked[i] {
			box = "● "
		}
		tick = s.Faint.Render(box)
	}
	head := fmt.Sprintf("%s%s%d. %s", marker, tick, i+1, sanitize(option.label))
	if option.danger && i != selected {
		label = s.Error
	}
	full := head
	if option.detail != "" {
		full += s.Faint.Render(" — " + sanitize(option.detail))
	}
	rendered := label.Render(full)
	if maxExtra <= 0 || option.detail == "" || ansi.StringWidth(ansi.Strip(rendered)) <= width {
		return []string{ansi.Truncate(rendered, width, "…")}
	}
	wrapWidth := max(width-2, 8)
	wrapped := strings.Split(wrapText(sanitize(option.detail), wrapWidth), "\n")
	// maxExtra is the rows this option may GROW by, and the head row it
	// grows from is already spent — so the detail gets maxExtra rows, not
	// maxExtra+1. Budgeting the wrap as if the head were free let a
	// one-row allowance turn one row into three, which at 36×9 pushed the
	// masthead off the page and left the card being decided about unnamed
	// — the exact trade thread.go reserves a head row to prevent.
	if len(wrapped) > maxExtra {
		wrapped = wrapped[:maxExtra]
		last := maxExtra - 1
		wrapped[last] = ansi.Truncate(wrapped[last], wrapWidth, "") + "…"
	}
	out := []string{ansi.Truncate(label.Render(head), width, "…")}
	for _, l := range wrapped {
		out = append(out, ansi.Truncate("  "+s.Faint.Render(l), width, "…"))
	}
	return out
}

func decisionQuestion(kind decisionKind, r featureRow, in nextInput) string {
	switch kind {
	case decisionBudget:
		return string(r.F.Stage) + " reached its envelope."
	case decisionVerify:
		if in.verdict == verdictPass {
			// A research card has no branch, so there is nothing to land
			// and the picker this heads says "mark done" — the question
			// and its only answer have to be about the same act
			// (reviewloop's own gate wording is the third place this
			// decision shows up).
			if r.F.Kind == domain.KindResearch {
				return "verification passed — decide whether the research is done."
			}
			return "verification passed — decide whether this work is ready to land."
		}
		return "verification stopped here — choose what happens next."
	case decisionGate:
		return string(r.F.Stage) + " is ready for your decision."
	case decisionFailure:
		// names what happened rather than reusing the idle sentence
		// (F9): this prints directly under the failure block the thread
		// already rendered, and offers that failure's own options
		// (re-run, attach the agent CLI) — "nothing is running" would be
		// a lie one line above an explanation of why nothing is.
		return string(r.F.Stage) + " failed — choose what happens next."
	default:
		if in.live {
			// a live conversation between turns. Something is very much
			// running — it is just waiting on you — so the idle card's own
			// sentence would be false here, printed as it is directly under
			// the session's spend line.
			return "the agent is waiting — keep talking, or choose what happens next."
		}
		if in.sess == engine.StateInteractive {
			// state persisted as interactive, but no backend is attached: a
			// rehydrated row, not a live wait. sendThreadMessage already
			// routes this case to sendConsultMessage instead of a live turn.
			//
			// It names the state and stops there. Naming a key would be a
			// promise the cursor can walk away from — the same rule the
			// status bar follows when it refuses to claim enter for a
			// choice the line is not aimed at — and the row that does
			// reattach the conversation is in the picker just below
			// (talkAction), where enter's own label already says so.
			return "no agent attached — choose what happens next."
		}
		return "nothing is running — choose what happens next."
	}
}

// wordAim is the composer's emptiness driving the highlight (DESIGN
// §6.3): while a decision is open, a typed prose line aims the cursor at
// the option that consumes words, so the screen always states what enter
// is about to do with them before it does. A "/verb" line never aims —
// the sigil makes it a command the parser owns, so it leaves the
// highlight where the user put it.
func (m *Shell) wordAim(d *threadDecision) int {
	if d == nil {
		return -1
	}
	text := strings.TrimSpace(m.threadInput.Value())
	if text == "" || parseInput(text).Kind != verbNone {
		return -1
	}
	if d.ask != nil {
		if d.ask.FreeForm {
			return len(d.ask.Options)
		}
		return -1
	}
	return d.wordConsumer()
}

// decisionArmed reports whether enter, right now, would actually answer
// this picker: false the moment the composer's line has been claimed by
// something else — a "/verb" line (the parser's own, regardless of what
// the decision offers), or an armed free-form answer channel. Threaded
// into the picker's paint
// (openDecisionBlock through pickerOptionLines) so it never disagrees
// with the bar, which asks this exact question in threadInputBindings to
// name enter's real destination (F7) — one control claims enter at a
// time, and while a verb is pending that control is the composer, not
// the picker's highlighted row.
func (m *Shell) decisionArmed(d *threadDecision) bool {
	if d.ask != nil && d.ask.FreeForm && m.threadFreeForm {
		return false
	}
	text := strings.TrimSpace(m.threadInput.Value())
	if text == "" {
		return true
	}
	if parseInput(text).Kind != verbNone {
		return false
	}
	// Prose in front of an open question is the answer now — the words,
	// not the highlighted row (submitThreadLine). So the row must stop
	// claiming enter, exactly as it does for the armed free-form channel
	// above: one control owns enter at a time, and here it is the
	// composer.
	return d.ask == nil
}

func (m *Shell) syncDecision(d *threadDecision) {
	if d == nil {
		m.decisionKey, m.decisionCursor, m.decisionPicked = "", 0, nil
		m.decisionAimed = false
		m.threadFreeForm = false
		m.reentryPending = nil
		return
	}
	if d.key != m.decisionKey {
		m.decisionKey = d.key
		m.decisionCursor = 0
		m.decisionPicked = map[int]bool{}
		// a reading was of a stop that is gone — the card moved under it,
		// and so has the conversation the stop was in. A read still out is
		// withdrawn for the same reason, and cancelled with it: it would
		// come back a reading of a stop nobody is looking at any more.
		m.reentryPending = nil
		m.withdrawRead()
		m.chatting = nil
		m.decisionAimed = false
		// a different question invalidates the armed free-form channel —
		// it belonged to the answer that is gone
		m.threadFreeForm = false
	}
	n := d.optionCount()
	if n == 0 {
		m.decisionCursor = 0
	} else {
		m.decisionCursor = clamp(m.decisionCursor, 0, n-1)
	}
	if i := m.wordAim(d); i >= 0 {
		if !m.decisionAimed {
			m.decisionAimed = true
			m.decisionAimBase = m.decisionCursor
		}
		m.decisionCursor = i
	} else if m.decisionAimed {
		// the composer that was driving the aim emptied out (or turned into
		// a command) — withdraw to the position the aim overrode, not
		// unconditionally to 0, so a manual pick made before the user
		// started typing prose survives.
		m.decisionAimed = false
		m.decisionCursor = clamp(m.decisionAimBase, 0, n-1)
	}
}

func (m *Shell) openDecisionBlock(s *theme.Styles, r featureRow, w, maxRows int) []string {
	d := m.openDecision(r)
	m.syncDecision(d)
	if d == nil {
		return nil
	}
	if p := m.reentryRead; p != nil && p.id == r.F.ID && d.ask == nil {
		// The reader already answered: enter committed the line to the row
		// it was aimed at, and what is happening now is that answer being
		// carried out. Rows that are no longer waiting on anybody must not
		// go on standing there as though they were — the question comes
		// back only if the answer does not, which is a stopped read. The
		// act itself is reported where the card reports every act in
		// progress: in the conversation (thread.go's reading marker).
		//
		// An open ask is the exception and keeps its picker: it is the
		// agent's own question, not this stop's, and a re-entry reached
		// through a verb while one is up has not answered it.
		return nil
	}
	if p := m.reentryPending; p != nil && d.ask == nil {
		// the chip stands where the picker stood, under the same
		// narration; the picker comes back the moment the chip goes
		width := max(w-2, 10)
		narr := m.narrationBlock(s, d, r, width)
		rows := maxRows
		if maxRows > 0 {
			narr = fitNarration(narr, maxRows-2-1)
			rows = max(maxRows-len(narr), 2)
		}
		return append(narr, m.chipLines(s, r, p, width, rows)...)
	}
	title := "gummi"
	options := make([]pickerOption, 0, len(d.actions))
	multi := false
	// A decision autopilot has taken renders open, with its options,
	// saying whose it is — never a countdown. The answer runs in a
	// command, so there is a real interval where the question is on
	// screen and already spoken for, and the honest thing is to name the
	// answerer rather than let it read as waiting for you. It collapses
	// when the answer event lands, not on a timer: a timer would make the
	// same decision behave differently depending on whether a human
	// happened to be looking, and would diverge the TUI from the driver,
	// which answers immediately. esc keeps its two meanings (blur the
	// composer, close the card) — the take-it-back gesture belonged to
	// the countdown that was cut.
	//
	// The flag, not autopilotAnswers: the rule table says which decisions
	// this card's mode MAY take, and a card sitting idle on gates is one
	// nothing is going to move — marking it from the table would put a
	// standing claim on screen that no answer was ever coming to honour.
	autopilots := m.autopilotAnswering[r.F.ID]
	if d.ask != nil {
		title = string(r.F.ID) + " asks"
		if m.threadFreeForm || m.proseAnswersAsk() {
			// armed, or simply typed into: either way the composer below
			// is the answer channel — say so on the pinned control, the
			// way the pane's free-form mode put the textarea where the
			// picker stood.
			title += " · your line is the answer"
		}
		options = askPickerOptions(d.ask)
		multi = d.ask.MultiPick
	} else {
		aim := m.wordAim(d)
		for i, action := range d.actions {
			// the aimed row's label names what enter will do with the
			// words before it does it (DESIGN §6.3) — the only render that
			// follows the composer's text rather than the card's state
			label := action.label
			if i == aim {
				label += " with your words"
			}
			options = append(options, pickerOption{
				label: label, detail: action.detail, danger: action.danger,
			})
		}
	}
	if autopilots {
		// on the title, not a row of its own: the pinned region's height
		// is load-bearing at 36×9, where a spent row costs an option, and
		// the free-form arming above already set the precedent that who
		// owns the answer is said on the title.
		title += " · autopilot is taking this one"
	}
	armed := m.decisionArmed(d)
	width := max(w-2, 10)
	lines := strings.Split(pickerView(s, title, d.question, options,
		m.decisionCursor, m.decisionPicked, multi, w, armed), "\n")
	// the head is however many rows the question needed (pickerHead wraps
	// it onto its own when it will not sit beside the title), so the
	// window has to be told rather than assuming one.
	head := len(pickerHead(s, title, d.question, width))

	// The narration takes its rows out of the decision's own budget, and
	// it is the first thing in the region to yield: the paragraph is what
	// a reader wants when there is room for it, and the question plus the
	// highlighted answer is what they cannot do without (DESIGN §6.3's
	// yield order). Reserving here rather than after windowing is what
	// makes that true — spent afterwards, the rows would already have
	// gone to expandHighlighted's detail wrap.
	narr := m.narrationBlock(s, d, r, width)
	rows := maxRows
	if maxRows > 0 {
		narr = fitNarration(narr, maxRows-head-1)
		rows = max(maxRows-len(narr), 1)
	}

	windowed := windowDecisionBlock(s, lines, head, len(options), m.decisionCursor, rows)
	block := expandHighlighted(s, windowed, lines, head, options, m.decisionCursor, m.decisionPicked, multi, width, rows, armed)
	if len(block) == 0 {
		// F21: no room for an answer the reader can see. A paragraph
		// explaining a stop, with no way to answer it, is the same
		// unanswerable control with more words.
		return nil
	}
	return append(narr, block...)
}

// narrationBlock renders the stop's paragraph — the rows that sit above
// the answers — or nil when there is nothing to say.
//
// A live ask gets none. The question IS the narration there, printed as
// the control's own head, and a sentence saying "the agent asked a
// question" directly above the question would be the page explaining
// itself to itself. It is also the settled answer to whether a pending
// ask keeps the full answer set: it replaces it, narration included.
func (m *Shell) narrationBlock(s *theme.Styles, d *threadDecision, r featureRow, width int) []string {
	if d == nil || d.ask != nil {
		return nil
	}
	claims := m.cardNarration(m.nextInputFor(r), r)
	if len(claims) == 0 {
		return nil
	}
	var out []string
	n := 0
	for _, c := range claims {
		text := sanitize(c.text)
		// The bracketed number is the citation, and it is only printed
		// for a claim that carries one: numbering every sentence would
		// promise a key for evidence that does not exist. alt+<n> opens
		// it (narration.go's own note on why not the bare digit).
		if !c.a.empty() {
			n++
			// The mark names its own key rather than a bare number.
			// "[1]" would be shorter and is what the design's mock drew,
			// but the chord that opens it is a status-bar hint, and on a
			// board-width terminal the bar has room for about three of
			// those — so the number would routinely appear above nothing
			// that says what to press. A footnote that carries its key is
			// the same information with no dependency on the bar having
			// spare columns. (The key is a letter, not a digit: cardtabs
			// .go says why.)
			text += " [" + citationMark(n) + "]"
		}
		for _, l := range strings.Split(wrapText(text, max(width-1, 8)), "\n") {
			out = append(out, " "+s.Base.Render(l))
		}
	}
	// the blank that separates the paragraph from the answers. It is part
	// of the narration's cost, so it is shed with the sentence it follows
	// rather than surviving as a stray row above the picker.
	return append(out, "")
}

// fitNarration sheds the paragraph from the END until it fits budget,
// dropping whole rows rather than truncating a sentence mid-word — a
// half-claim about why a card stopped is worse than no claim.
//
// budget is what is left once the picker's head and its highlighted
// answer are paid for, so a page too short for both simply has no
// paragraph. It never returns a lone trailing blank: shedding down to
// just the separator would spend a row on the gap between a paragraph
// that is not there and the answers.
func fitNarration(narr []string, budget int) []string {
	blank := func(l string) bool { return strings.TrimSpace(ansi.Strip(l)) == "" }
	body := narr
	for len(body) > 0 && blank(body[len(body)-1]) {
		body = body[:len(body)-1]
	}
	// budget-1, not budget: the separator below the paragraph is a row
	// the block will occupy, so trimming to the full budget and then
	// appending it would put the block one row over what it was given.
	if len(body) > budget-1 {
		body = body[:max(budget-1, 0)]
	}
	for len(body) > 0 && blank(body[len(body)-1]) {
		body = body[:len(body)-1]
	}
	if len(body) == 0 {
		return nil
	}
	return append(body, "")
}

// expandHighlighted spends any vertical budget windowDecisionBlock left
// unused on wrapping the highlighted option's detail instead of leaving
// the rows blank (F20): the answers get one line each however
// consequential today, which is what cuts a live ask's detail off mid-word
// ("please point me at the rig…") even on a page tall enough to spare the
// row.
//
// It only ever fires when windowDecisionBlock returned every line
// untouched — the moment it had to shrink or mark options hidden,
// len(windowed) already equals the budget exactly and there is nothing
// left over to spend (the F21 drop-the-block case returns nil here too,
// short-circuited by the length check below). So the wrap only ever grows
// into rows the block's own budget already owned and was not going to use
// for anything else — never the foot's, and never another option's.
func expandHighlighted(s *theme.Styles, windowed, lines []string, headRows int, options []pickerOption, cursor int, picked map[int]bool, multi bool, width, maxRows int, armed bool) []string {
	if maxRows <= 0 || len(windowed) == 0 || len(windowed) != len(lines) {
		return windowed
	}
	leftover := maxRows - len(windowed)
	if leftover <= 0 || cursor < 0 || cursor >= len(options) {
		return windowed
	}
	i := headRows + cursor
	if i < 0 || i >= len(windowed) {
		return windowed
	}
	wrapped := pickerOptionLines(s, options[cursor], cursor, cursor, picked, multi, width, leftover, armed)
	if len(wrapped) <= 1 {
		// the row already said everything on one line — nothing to gain
		return windowed
	}
	out := make([]string, 0, len(windowed)+len(wrapped)-1)
	out = append(out, windowed[:i]...)
	out = append(out, wrapped...)
	out = append(out, windowed[i+1:]...)
	return out
}

// windowDecisionBlock keeps a decision taller than its row budget usable:
// the question keeps its row, the option rows window around the cursor so
// the highlighted answer is always on screen, and whatever is hidden is
// stated rather than silently dropped — the same contract the action
// list's fold honours (cardactions.go), and the shape the design's 36×9
// frame shows. composeThread would otherwise trim from the bottom, which
// can leave the cursor on an option that is no longer visible.
func windowDecisionBlock(s *theme.Styles, lines []string, headRows, nOptions, cursor, maxRows int) []string {
	if maxRows <= 0 || len(lines) <= maxRows {
		return lines
	}
	// lines[:headRows] are the title and the question (one row when the
	// two fit together, more when the question wrapped onto its own);
	// lines[headRows:] are one row per option.
	rows := maxRows - headRows
	if rows <= 0 {
		// no room for even the highlighted answer: a title nobody can act
		// on is worse than no decision block at all (F21) — the row goes
		// back to conversation (or stays blank) instead. This used to keep
		// the question (and as much of it as fit) on its own, which is how
		// 20×5 degraded to the bare word "gummi" with no option row, and
		// 18×4 to that word being the whole block — a control whose
		// highlighted answer you cannot see is not one you can answer
		// either, so there is nothing honest left to render.
		return nil
	}
	marker := nOptions > rows
	if marker {
		rows-- // one row for the "…N more" count
		if rows <= 0 {
			// no room for both a count and an option: the highlighted
			// answer is worth more than knowing how many options exist
			rows, marker = 1, false
		}
	}
	out := make([]string, 0, rows+headRows+1)
	out = append(out, lines[:headRows]...)
	out = append(out, windowLines(lines[headRows:], cursor, rows)...)
	if marker {
		out = append(out, s.Faint.Render(fmt.Sprintf("  …%d more — ↑↓ to reach them", nOptions-rows)))
	}
	return out
}

// moveDecision steps the highlight through the decision's options. It does
// not wrap: ↑ escaping off the top is handleThreadInputKey's route into the
// action inventory (F11), which only makes sense if the top is really the
// top, and ↓ on the last option has nowhere honest to go either — wrapping
// there would suggest a fourth option cycling back into a three-option
// gate.
func (m *Shell) moveDecision(d *threadDecision, delta int) {
	n := d.optionCount()
	if n == 0 {
		return
	}
	m.decisionCursor = clamp(m.decisionCursor+delta, 0, n-1)
	m.decisionAimed = false
}

// proseAnswersAsk reports whether the composer currently holds a line
// that would be delivered as the open ask's answer: prose, not a verb.
// The picker's title and its armed paint both read it, so what the screen
// says about enter and what enter does cannot drift apart.
func (m *Shell) proseAnswersAsk() bool {
	text := strings.TrimSpace(m.threadInput.Value())
	return text != "" && parseInput(text).Kind == verbNone
}

// answerAskWith delivers free-form prose as the answer to the open ask —
// the chat pane's 'o' channel, which the composer makes always-on: the
// question declared allow_free_form, so the line is the answer the ask
// invited (DESIGN §6.3; a structured ask keeps its terms and prose
// routes as a turn instead). Same live-session guard as the picker path,
// and the same F8 shape as sendThreadMessage: the nil check that used to
// run only once the returned command executed is done up front instead,
// so the composer (and the free-form arming) clears only once a session
// is confirmed live — a line typed with nothing to answer stays put
// rather than vanishing under the notice explaining why it wasn't sent.
func (m *Shell) answerAskWith(r featureRow, text string) tea.Cmd {
	sess := m.sessionFor(r.F.ID)
	if sess == nil {
		m.notice = noticeMsg{text: string(r.F.ID) + ": no live session to answer — attach first (enter)"}
		return nil
	}
	m.threadInput.Reset()
	m.threadFreeForm = false
	eng := m.engine
	return func() tea.Msg {
		if eng.Get(r.F.ID) != sess {
			return noticeMsg{text: "session is no longer active", isErr: true}
		}
		if err := eng.Answer(context.Background(), r.F.ID, text); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		// the question is answered: it is no longer something that needs
		// you, and EventQuestion now queues every ask (shell.go) rather
		// than guessing from where the reader happened to be looking.
		return noticeMsg{clearInbox: r.F.ID}
	}
}

func (m *Shell) answerDecision(r featureRow, d *threadDecision) tea.Cmd {
	if d.ask != nil {
		if d.ask.FreeForm && m.decisionCursor == len(d.ask.Options) {
			// the synthetic "Chat about this" row is selected: there is no
			// entry in ask.Options at this index for decisionAnswerText to
			// resolve, so enter arms the free-form channel instead of
			// silently no-oping — the same effect pressing 'o' has.
			m.threadFreeForm = true
			return nil
		}
		answer := decisionAnswerText(d.ask, m.decisionCursor, m.decisionPicked)
		// A gate ask IS the crossing: answering it with the advance option
		// has to move the card, or the question would be a control that
		// looks like a decision and does nothing. Everything else about it
		// — recording the answer, resolving the agent's blocked call —
		// goes through the ordinary answer path first, so the transcript
		// and the spec anchor read the same as any other answer.
		crossing := gateAnswerCrosses(d.ask, answer)
		if answer == "" {
			// Belt to parseAsk's braces. Nothing that reaches here should
			// be able to resolve to an empty answer, but if it ever does,
			// say so: a picker that swallows enter and leaves the card
			// parked on an unanswerable question is the worst outcome
			// available, and strictly worse than an error the reader can
			// act on.
			return func() tea.Msg {
				return noticeMsg{text: "that option carries no answer text — the question cannot be answered as asked", isErr: true}
			}
		}
		sess := m.sessionFor(r.F.ID)
		eng := m.engine
		answerCmd := func() tea.Msg {
			if sess == nil || eng.Get(r.F.ID) != sess {
				return noticeMsg{text: "session is no longer active", isErr: true}
			}
			if err := eng.Answer(context.Background(), r.F.ID, answer); err != nil {
				return noticeMsg{text: sanitize(err.Error()), isErr: true}
			}
			return noticeMsg{clearInbox: r.F.ID}
		}
		if !crossing {
			return answerCmd
		}
		// Sequenced, not batched: the crossing must see the answer already
		// recorded, and the gate's own blocker checks (open threads, the
		// undrafted floor) still run inside Advance — answering a gate
		// asks for the crossing, it does not force one.
		return tea.Sequence(answerCmd, m.advanceStageAs(r.F.ID, state.ActorUser))
	}
	if m.decisionCursor < 0 || m.decisionCursor >= len(d.actions) {
		return nil
	}
	action := d.actions[m.decisionCursor]
	m.clearTransientNotice()
	m.endChat(r.F.ID) // a row picked is the way out of a conversation
	return m.runCardAction(cardAction{
		id: action.id, key: action.key, label: action.label,
		why: action.detail, danger: action.danger,
	})
}

// gateAnswerCrosses reports whether answering ask with this text is the
// gate crossing itself, rather than an ordinary answer the stage then
// acts on. Only a gate ask can cross, and only its advance option does —
// "not yet" and any free-form reply are answers that leave the card
// exactly where it is.
func gateAnswerCrosses(ask *engine.Ask, answer string) bool {
	return ask != nil && ask.Gate && answer == engine.GateAdvanceLabel
}

func decisionAnswerText(ask *engine.Ask, cursor int, picked map[int]bool) string {
	if ask.MultiPick {
		var labels []string
		for i, option := range ask.Options {
			if picked[i] {
				labels = append(labels, option.Label)
			}
		}
		if len(labels) > 0 {
			return strings.Join(labels, ", ")
		}
	}
	if cursor >= 0 && cursor < len(ask.Options) {
		return ask.Options[cursor].Label
	}
	return ""
}
