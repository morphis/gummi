package ui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui/theme"
	"github.com/morphis/gummi/internal/verdict"
	"github.com/morphis/gummi/internal/workflow"
)

// The card thread: a single scrollable surface a card's page opens onto.
// Top to bottom it renders identity + the stage strip, the pinned spec
// line, one folded line per finished stage session, the live stage (a
// session boundary naming the fresh context it started, its events, and
// the streaming activity line while one is running), a pinned open
// decision when the card needs one, and the input.
//
// What a card decided while running itself is not a region of its own.
// It used to be — a rollup pinned below the live stage — and that was
// the bug: appended last, it took every arriving line above itself and
// stayed permanently newer than the conversation it described. Those
// periods are drawn among the history instead, bracketed by rules where
// they actually began and ended (stretch.go).
//
// The thread never blocks a frame on IO: its per-stage history comes
// from featureRow.Events, populated lazily and only for the selected
// card (msgs.go, shell.go's loadCardEvents). With nothing loaded yet it
// simply omits the folded receipts and falls back to whatever a live
// engine session already holds in memory.

// threadGutter is the space the thread keeps clear on its right, in
// columns. The pane already insets the surface from the left; without a
// matching gutter the folded receipts' rules, the session boundary and
// the input all ran flush into the terminal edge, so the page read as
// pinned on one side and floating on the other. Reserving it here rather
// than trimming afterwards means each block lays itself out inside the
// width it will actually occupy.
const threadGutter = 2

// composerBlankRows is the thread's own row budget at which the composer
// keeps the blank row beneath it, and cardCrumbRows is the card page's
// budget at which it keeps its crumb. Both are the same fact counted from
// different sides: a twelve-row terminal spends one row on the tab bar
// and one on the status bar, leaving the page ten and the thread — once
// the crumb has taken one — nine. Below that the page is one you are
// operating rather than reading, and chrome yields to the control.
const (
	composerBlankRows = 9
	cardCrumbRows     = 10
)

// headerGap separates the masthead's fields. Two spaces was not enough to
// read them as separate facts — the id, the profile, the mode, the spend
// and the round badge ran together as one long line, which is the one
// place on the page where five unrelated things sit side by side. Three
// is the design's own spacing, and it is what "generous padding" (§6.2)
// buys here.
const headerGap = "   "

// stageJoin separates the stages in the strip. The bare rule the strip
// used ran the names into each other, so it read as one hyphenated word
// rather than a row of stops with the current one lit; padding the rule
// gives each name air without spending a glyph on it.
const stageJoin = " ─ "

// cardPageChrome reports how many of the card page's rows go to chrome
// rather than to the thread: the crumb above it, and below it the blank
// row that stops the composer from reading as part of the status bar —
// two chrome-coloured rows stacked with nothing between them come across
// as one control. Each is present only when the page can afford to spend
// a row on it; on a short terminal an option you can see is worth more
// than air, and the two rows touch again.
//
// Both cardPageView and threadSize resolve it here, so the height the
// thread is rendered at and the height its scroll clamp is measured
// against can never disagree — a clamp computed from a different budget
// than the render uses stops paging in the wrong place.
// crumb counts the rows the way-back line costs: one for the line, and
// one above it so it does not sit flush against the tab bar — the same
// separation the composer gets from the status bar at the other end of
// the page. That row is the first of the page's chrome to go, since a
// line you can read without air is worth more than air.
func cardPageChrome(h int) (crumb, blank int) {
	if h >= cardCrumbRows {
		crumb = 1
		if h > cardCrumbRows {
			crumb++
		}
	}
	if h-crumb >= composerBlankRows {
		blank = 1
	}
	return crumb, blank
}

// threadView renders the selected card's thread into the card page.
//
// The surface is four regions, not one list. The masthead and the pinned
// spec line hold the top; an open decision and the input hold the bottom;
// the conversation scrolls between them. That split is what
// makes the page usable on a short terminal: the old single list was
// truncated to the window height from the top, so the first thing lost
// on a small screen was the input box — the one part you always need.
//
// The body is anchored to its END, so opening a card lands on the newest
// event the way a chat does, and threadScroll counts lines back from
// there rather than forward from the start. Keeping the offset relative
// to the bottom means arriving output does not shove the view upward
// while you are reading the latest of it.
func (m *Shell) threadView(w, h int) string { return m.threadRender(w, h, false) }

// threadRender is threadView with the measure pass split out. Measuring
// renders the whole thread — the body unwindowed and the decision
// unbounded — so the scroll clamp counts every row there is to reach,
// but it must still lay the head and the foot out at the height the
// screen will actually use: a decoration that appears only above a
// certain height (the head's leading blank, the composer's own) has to
// be counted by the measure exactly when the render will draw it.
// Measuring at a height of zero, as this used to, made the two disagree
// by a row and left the oldest line unreachable.
func (m *Shell) threadRender(w, h int, measure bool) string {
	if m.sel < 0 || m.sel >= len(m.rows) {
		return ""
	}
	s := m.styles
	r := m.rows[m.sel]
	// Events is populated for the selected card only, lazily (msgs.go's
	// doc comment on featureRow.Events) — apply the cache here rather
	// than on every row at load time, which would be the unbounded IO
	// the row snapshot exists to avoid.
	r.Events = m.cardEvents[r.F.ID]
	f := r.F

	inner := max(w-threadGutter, 8)
	clip := func(str string) string {
		if strings.TrimSpace(str) == "" {
			return "" // a blank row is blank, not a run of spaces
		}
		return ansi.Truncate(str, inner, "…")
	}

	// The page's regions are separated by a blank row apiece, so the
	// conversation, the decision it ends in and the line you type on read
	// as three things rather than one wall of text running into the
	// chrome. They are decorations: on a short page the rows buy an
	// option instead, which is why the 36×9 frame has none of them.
	sep := 0
	if h >= composerBlankRows {
		sep = 1
	}

	// --- head: pinned to the top, most important row first ---
	// The order is the order it yields in: composeThread trims the head to
	// a prefix, so whatever must survive a short terminal has to come
	// first. That is the card's own identity — being told which card you
	// are deciding about is worth more than the strip, the spec line or
	// the spacing around them.
	buildHead := func(sep int) []string {
		var head []string
		for i, l := range threadHeader(s, m, r, inner) {
			// a row between the card's title and its stage strip: they are two
			// different questions — which card is this, and how far along is it
			// — and stacked flush they read as one block of small print.
			if i > 0 {
				head = append(head, make([]string, sep)...)
			}
			head = append(head, clip(l))
		}
		head = append(head, "")
		if sl := pinnedSpecLine(s, r, inner); sl != "" {
			head = append(head, clip(sl), "")
		}
		if sep > 0 && len(head) > 0 {
			// the leading blank separates the masthead from the page's crumb
			// above it.
			head = append([]string{""}, head...)
		}
		return head
	}
	head := buildHead(sep)
	// headMin is head with its own sep-gated separators — the leading
	// blank and the one between the identity line and the strip — given
	// up. composeThread falls back to it, and to decisionMin/footMin
	// below, whenever the full layout does not fit at h, which is what
	// keeps the rail from being sacrificed just to buy back the air sep
	// switched on (BG-050).
	headMin := head
	if sep > 0 {
		headMin = buildHead(0)
	}

	// --- body: the conversation, scrollable ---
	var body []string
	add := func(str string) { body = append(body, clip(str)) }
	blank := func() { body = append(body, "") }

	segs := stageSegments(r.Events)
	// Computed once from the card's whole event log rather than once per
	// line: stageEventLine only ever sees the one event it is asked to
	// render, and it needs this set to tell an answered decision_open
	// (render nothing — DESIGN §6.3) from one superseded before anyone
	// answered it (render a trace — DESIGN §10.18). A card's history can
	// hold many decision_open rows, so re-deriving the set inside the
	// per-line renderer would redo the same scan of r.Events once per
	// line for no reason.
	answered := answeredDecisions(r.Events)
	// The periods this card ran itself (stretch.go), derived once from the
	// same event slice the segments came from so both are indexed against
	// it. Folding a stage to one receipt loses its position, and the rules
	// that bracket a period are placed by position, so the two have to be
	// resolved together or not at all.
	stretches := autopilotStretches(f, r.Events)
	// segOf answers which folded segment an event index fell in, and -1
	// for an index before the first stage ever started — where the switch
	// writes its takeover when it starts a card sitting in todo, since
	// nothing has entered a stage yet at that moment.
	segOf := func(idx int) int {
		seg := -1
		for i := range segs {
			if segs[i].enterIdx <= idx {
				seg = i
			}
		}
		return seg
	}
	// A period opening before any stage started still opens above the
	// first one: max(_, 0). Its rule cannot be drawn earlier than the
	// history it brackets.
	openSeg := func(st autopilotStretch) int { return max(segOf(st.from), 0) }
	// closeSeg clamps the same way openSeg does, and for the same reason.
	// A period can both open and close before any stage ever started —
	// hand a card in todo to autopilot with no agent configured, then
	// switch it straight back off — and without the clamp its opening
	// rule would be drawn against the first segment while its closing
	// rule matched no segment at all, leaving a period on screen that
	// never ends.
	closeSeg := func(st autopilotStretch) int { return max(segOf(st.to), 0) }
	live := len(segs) - 1
	// The period this card should open on rather than on its newest line,
	// and where its opening rule ends up in the body. anchorIdx stays -1
	// unless that rule is actually drawn, so a period whose rule the
	// render never reaches cannot move the scroll to a row that is not
	// there.
	// markSeen (shell.go) already resolved which period this is and left
	// its opening index behind; the render only has to notice when it
	// draws that period's rule. It must not put the question again here:
	// markSeen advances the read mark at the same moment it sets the
	// anchor, so by the time this runs the honest answer to "what is
	// unread" is always "nothing".
	anchoring := !measure && m.anchorTo == f.ID
	anchorIdx := -1
	markAnchor := func(st autopilotStretch) {
		if anchoring && st.from == m.anchorFrom {
			anchorIdx = len(body)
		}
	}
	// Which periods open inside the live stage rather than above it, so
	// the live block can draw their rules where the folded loop cannot
	// reach. The two must partition: the folded loop covers segments
	// before the live one, this covers the live one, and openSeg's own
	// clamp to 0 is what puts a takeover written before any stage
	// started — the switch pressed on a card sitting in todo — into
	// whichever of the two owns the first segment. A card with a single
	// stage has no folded loop at all, so that is this one.
	var liveOpens []autopilotStretch
	for _, st := range stretches {
		if live >= 0 && openSeg(st) == live {
			liveOpens = append(liveOpens, st)
		}
	}
	if len(segs) > 1 {
		spend := stageSpendByStage(r.StageSpend)
		// how many segments each stage folds to a receipt for — the review
		// →fix loop can bounce a card through fix four times, and a stage
		// with more than one segment can only trust its own stage_exit
		// payload for its spend (foldedReceiptLine's comment on why).
		folded := segs[:len(segs)-1]
		counts := make(map[domain.Stage]int, len(folded))
		for _, seg := range folded {
			counts[seg.stage]++
		}
		for i, seg := range folded {
			for _, st := range stretches {
				if openSeg(st) == i {
					markAnchor(st)
					add(stretchOpenLine(s, st, inner))
				}
			}
			add(foldedReceiptLine(s, seg, spend, counts[seg.stage], inner))
			// The decisions autopilot made inside this stage, pulled back
			// out of the fold and printed under the receipt they came from
			// (stretch.go's stretchDecisionLine). They print after it, even
			// though their stamps can precede its close: the group reads as
			// "this stage, and what it decided inside it", where printing
			// them first would put a stage's decisions above the line that
			// names the stage they happened in.
			for k, ev := range seg.events {
				_, in := stretchAt(stretches, seg.evIdx[k])
				if l := stretchDecisionLine(s, ev, in, inner-2); l != "" {
					add("  " + l)
				}
			}
			for _, st := range stretches {
				if !st.running() && closeSeg(st) == i {
					for _, l := range stretchCloseLines(s, st, inner) {
						add(l)
					}
				}
			}
		}
		blank()
	}

	// The live stage draws the rules for periods that reach into it, so it
	// is the only one that knows where inside its own block the anchored
	// one landed — and that is the ordinary case, since a period usually
	// opens and parks inside the stage still on screen. It reports the
	// offset back rather than the caller guessing, and the offset is
	// translated into a body index here, where the body's length is known.
	anchorWant := -1
	if anchoring {
		anchorWant = m.anchorFrom
	}
	if ls, at := m.liveStageBlock(s, r, segs, inner, answered, stretches, liveOpens, anchorWant); len(ls) > 0 {
		base := len(body)
		for _, l := range ls {
			add(l)
		}
		if at >= 0 && anchorIdx < 0 {
			anchorIdx = base + at
		}
		blank()
	}

	// The card's consult exchange, if any — appended after the live
	// stage block, not interleaved with it: the two are logically
	// separate conversations a line addresses one at a time (arming
	// decides which), so this always renders last regardless of which
	// side started more recently (History, Chosen approach).
	if cl := m.consultBlock(s, r, inner); len(cl) > 0 {
		for _, l := range cl {
			add(l)
		}
		blank()
	}

	// A finished `v` run's results have nowhere else to surface: with no
	// live session there is no stage block to carry them, and they are
	// not events on the card. The detail pane showed them in exactly this
	// slot, so the thread does too.
	if m.sessionFor(f.ID) == nil {
		if res := m.checks[f.ID]; len(res) > 0 {
			for _, l := range strings.Split(verifySummary(s, res), "\n") {
				if l != "" {
					add(l)
				}
			}
			blank()
		}
	}

	// What a card decided while nobody was watching used to be summarised
	// here, in a block appended after the live stage. It is drawn as a
	// bounded period among the history above instead (stretch.go), where
	// it happened: a rollup pinned below everything was permanently the
	// newest thing on the page, so it sat under your own turns describing
	// a period that had ended hours earlier.
	//
	// A card that never entered a stage has no segment for a rule to be
	// placed against, and both placement paths above bail out on that —
	// so a card handed to autopilot and taken straight back, with the
	// advance never producing a stage, would silently lose the record
	// that it changed hands at all. There is no position to resolve here,
	// only an order, so the rules go down in the order they happened.
	if len(segs) == 0 && len(stretches) > 0 {
		for _, st := range stretches {
			add(stretchOpenLine(s, st, inner))
			if !st.running() {
				for _, l := range stretchCloseLines(s, st, inner) {
					add(l)
				}
			}
		}
		blank()
	}

	// todo and done are the two stages currentSpecSection has no anchor
	// for, and todo is exactly the stage where "what is this card about"
	// is the whole question — with nothing run yet there is no receipt,
	// live stage or decision to fill the body either, so without this it
	// was ~25 blank rows between the stage strip and the composer.
	if len(body) == 0 {
		add(threadEmptyLine(s, f))
	}
	body = trimTrailingBlanks(body)

	// threadScroll is a distance from the end of body, so an appended line
	// would otherwise slide the window forward by one for every line that
	// arrives, even though the reader pressed nothing (BG-042). Advancing
	// threadScroll by the same amount the body grew keeps the window's
	// absolute position instead — the fix only applies while scrolled back
	// (up == 0 already tail-follows correctly) and only against the same
	// card's own previous frame, so switching cards or the measure pass
	// never perturbs it.
	if !measure {
		if m.threadBodyCard == f.ID && m.threadScroll > 0 && len(body) > m.threadBodyLen {
			m.threadScroll += len(body) - m.threadBodyLen
		}
		m.threadBodyCard = f.ID
		m.threadBodyLen = len(body)
	}

	// Consume the anchor. threadScroll counts rows back from the end of
	// the body, so landing the period's opening rule at the top of the
	// window means scrolling back by everything below it. It is clamped
	// by composeThread on this very render, so an anchor larger than the
	// body can hold simply lands at the oldest line rather than off the
	// end — and it is cleared either way, because the jump is a thing
	// that happens once when you arrive, not a position the page holds.
	if anchoring {
		if anchorIdx >= 0 {
			m.threadScroll = max(len(body)-anchorIdx-1, 0)
		}
		m.anchorTo = ""
	}

	// --- foot: pinned to the bottom ---
	// footMin is foot without its own sep-gated lead-in blank — the foot's
	// share of headMin's treatment, for the same reason (BG-050).
	var footMin []string
	// the input is a multi-row widget: clip each row, or a stray tail of
	// the second one lands on the first.
	for _, l := range strings.Split(m.inputBlock(s, r, inner), "\n") {
		footMin = append(footMin, clip(l))
	}
	foot := footMin
	if sep > 0 {
		foot = append(make([]string, sep), footMin...)
	}

	// --- decision: pinned directly above the composer while open ---
	// its row budget is everything the foot leaves: the decision may not
	// be squeezed out by the body (the body yields first), and within the
	// budget windowDecisionBlock keeps the question and the highlighted
	// answer visible however many options the legal set holds. The h<=0
	// measure pass (maxThreadScroll) renders it unbounded, so the scroll
	// clamp counts the whole control rather than its narrow-terminal
	// window.
	budget := 0
	if h > 0 && !measure {
		// One row is reserved for the head before the decision takes the
		// rest. Without it a tall decision fills a short page entirely and
		// the card being decided about goes unnamed — at 36×9 the masthead
		// was the first thing to go and the question the last, which is
		// backwards: you can answer a question you can see on a card you
		// cannot identify, but you should not have to. The question and the
		// highlighted answer still never yield; windowDecisionBlock gives
		// up unfocused options instead.
		reserve := 0
		if len(head) > 0 {
			reserve = 1
		}
		// sep is subtracted too: the decision's own separator is a row it
		// will occupy, so budgeting without it would let the block grow
		// one row past what the page can hold.
		budget = max(h-len(foot)-reserve-sep, 1)
	}
	// decisionMin is the decision's content with its own sep-gated lead-in
	// blank given up — headMin's and footMin's treatment, applied here too
	// (BG-050).
	decisionMin := m.openDecisionBlock(s, r, inner, budget)
	for i := range decisionMin {
		decisionMin[i] = clip(decisionMin[i])
	}
	decision := decisionMin
	if len(decisionMin) > 0 && sep > 0 {
		decision = append(make([]string, sep), decisionMin...)
	}

	// the measure wants every row there is, so it composes at zero — the
	// unwindowed branch — having laid the regions out at the real height.
	// That branch never scrolls, so it never spends a row on the markers
	// composeThread's windowed branch adds below; maxThreadScroll adds
	// their cost back in rather than leaving the clamp two rows short of
	// the oldest line.
	composeH := h
	if measure {
		composeH = 0
	}
	return strings.Join(composeThread(s, head, headMin, body, decision, decisionMin, foot, footMin, composeH, m.threadScroll, inner), "\n")
}

// composeThread lays the four regions into h rows: head at the top, foot
// at the bottom, a pinned open decision immediately above it, and as much
// of the end of body as fits between them, scrolled back by up lines.
//
// When the window cannot hold everything, the foot wins. The input and
// the actions beside it are what the page is for, and a terminal too
// short for the masthead is still perfectly usable without it. Priority
// is foot, decision, head, body. The decision arrives already windowed
// to what the foot leaves it (windowDecisionBlock), so the trim below is
// only a safety net for a caller that skipped that step.
//
// headMin, decisionMin and footMin are head, decision and foot with their
// own sep-gated separator rows already given up — the four blanks sep
// switches on together, rather than any row that carries content. Below,
// if foot, decision and head do not fit h between them as a whole, the
// three Min variants are used instead, all at once. Each of the four
// separators is exactly as expendable as the other three, so this is the
// row a separator loses to content, not a row of content losing to one:
// trying the with-separator layout only when it fits h means a separator
// can never survive at a row of content's expense. Without this, a raw
// prefix cut of the with-separator head could land between two of its
// own separators and cut the strip to pay for rows that carry nothing —
// and dropping the terminal further below the height that first forces
// this would flip sep back to 0 and give the strip back, which is
// BG-050: the shed was not monotone. The trim beneath this only runs
// when even the separator-free layout does not fit.
func composeThread(s *theme.Styles, head, headMin, body, decision, decisionMin, foot, footMin []string, h, up, w int) []string {
	if h <= 0 {
		out := append(append(append([]string{}, head...), body...), decision...)
		return append(out, foot...)
	}
	if len(foot) >= h {
		return foot[len(foot)-h:]
	}
	if len(foot)+len(decision)+len(head) > h {
		head, decision, foot = headMin, decisionMin, footMin
	}
	remaining := h - len(foot)
	if len(decision) > remaining {
		decision = decision[:remaining]
		head, body = nil, nil
		remaining = 0
	} else {
		remaining -= len(decision)
	}
	if len(head) > remaining {
		// trailing blanks go with the trim: a head cut short mid-way should
		// not spend its last surviving row on the space under a row that no
		// longer fits. head is already the separator-free headMin here —
		// the swap above ran, or this call's head had none to begin with —
		// so what is left to trim is content, not decoration.
		head = trimTrailingBlanks(head[:remaining])
		body = nil
		remaining = 0
	} else {
		remaining -= len(head)
	}

	window := body
	if len(body) > remaining && remaining >= 2 {
		// the body is clipped in one direction or both, and gives no other
		// sign of it — no position, no "more below the fold" — so a reader
		// paging up cannot tell a short conversation from one cut off
		// mid-scroll. Both marker slots come out of the body's own budget,
		// spent together the way scrollNote (backlog.go) spends the
		// backlog's: text on the side that is actually hidden, a blank row
		// on the other, so the row count — and with it what maxThreadScroll
		// has to clamp against — does not change with scroll position.
		budget := remaining - 2
		up = clamp(up, 0, len(body)-budget)
		end := len(body) - up
		start := end - budget
		mark := func(arrow string, n int) string {
			return ansi.Truncate(scrollNote(s.Faint.Render, arrow, n), w, "…")
		}
		window = append([]string{mark("↑", start)}, body[start:end]...)
		window = append(window, mark("↓", len(body)-end))
	} else if len(body) > remaining {
		// too little room to spend two of it on markers (an extreme-short
		// terminal): fall back to a plain, unmarked window rather than
		// letting the reservation eat the only row or two the body has.
		up = clamp(up, 0, len(body)-remaining)
		end := len(body) - up
		window = body[end-remaining : end]
	}

	out := append([]string{}, head...)
	out = append(out, window...)
	// pad so the foot sits on the last row rather than floating up under
	// a short conversation
	for len(out)+len(decision)+len(foot) < h {
		out = append(out, "")
	}
	out = append(out, decision...)
	return append(out, foot...)
}

// maxThreadScroll is how far back the body can be scrolled for a given
// window — the clamp the key handler needs so paging up stops at the
// first line instead of running off into blank space.
func (m *Shell) maxThreadScroll(w, h int) int {
	if m.sel < 0 || m.sel >= len(m.rows) {
		return 0
	}
	// rendering is the only honest measure of how tall the body is: it
	// depends on wrapping, fold state and whether a session is live.
	full := len(strings.Split(m.threadRender(w, h, true), "\n"))
	if full <= h {
		return 0
	}
	// the measure pass renders unwindowed, so it never draws the two rows
	// composeThread spends on the scroll markers once anything scrolls;
	// without adding them back here, paging to this clamp would leave the
	// window still short of the two rows the markers themselves occupy,
	// stopping short of the oldest line rather than reaching it.
	//
	// The +2 is a flat add even on the rare terminal too short for
	// composeThread to afford both marker rows (its own remaining>=2
	// fallback): that only makes this clamp too generous there, never too
	// stingy, because composeThread reclamps up to whatever the real
	// remaining allows on every render regardless of what this function
	// suggested. A too-generous ceiling just makes a few extra pgup
	// presses no-ops once the true oldest line is already on screen; a
	// too-stingy one is the actual bug this function exists to prevent
	// (the oldest line staying forever out of reach), so generous is the
	// side to err on.
	return full - h + 2
}

func trimTrailingBlanks(lines []string) []string {
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// threadHeader is the thread's two-line masthead: identity, profile,
// autopilot mode, spend and the active loop's round badge, then the
// stage strip.
func threadHeader(s *theme.Styles, m *Shell, r featureRow, width int) []string {
	f := r.F
	head := s.Title.Render(string(f.ID)) + " " + s.Base.Render("· "+f.Title)
	if f.Profile != "" {
		head += headerGap + s.ProfileTag.Render("["+f.Profile+"]")
	}
	head += headerGap + autopilotField(s, m, f)
	if f.Budget.Envelope > 0 {
		head += headerGap + s.Faint.Render(budgetSummary(f))
	} else if !f.Spend.Zero() {
		head += headerGap + s.Faint.Render(featureSpend(f.Spend))
	}
	if sk := skipSummary(f); sk != "" {
		head += headerGap + s.Faint.Render("skips "+sk)
	}
	if rl := roundLabel(m, f); rl != "" {
		head += headerGap + s.Faint.Render(rl)
	}
	if cl := correctiveLabel(m, f); cl != "" {
		head += headerGap + s.Faint.Render(cl)
	}
	return []string{head, stageStrip(s, f, width)}
}

// correctiveLabel is the card's unified rework budget — every review
// bounce, verify bounce and conflict handoff it has cost, against the
// cap an unattended run is finally stopped by.
//
// It lives on the masthead because it is a fact about the whole card.
// It used to be a row inside the block reporting what autopilot decided
// while you were away, under a heading naming a bounded period, which it
// was never scoped to: burnCorrective (reviewloop.go) counts a bounce
// you drove by hand exactly the same as one autopilot drove, so a card
// you sat and watched the whole time could carry that block on the
// strength of this number alone.
//
// The word is load-bearing. roundLabel above already renders a bare
// "⟲ n of m" for whichever loop the current stage belongs to, and two
// unlabelled badges of the same shape side by side would be two numbers
// nobody could tell apart.
func correctiveLabel(m *Shell, f domain.Feature) string {
	n := m.round(f.ID, domain.RoundKindCorrective)
	if n == 0 {
		return ""
	}
	return "⟲ " + itoa(n) + " of " + itoa(verdict.MaxRounds(domain.RoundKindCorrective)) + " corrective"
}

// autopilotField is the masthead's autopilot cell: the stored mode, and
// — while the card is genuinely running under a mode that is not off —
// the fact that it is running right now, at Info weight rather than
// Faint.
//
// This is the only thing about autopilot that stays pinned, and it is
// pinned because it is the one autopilot fact that is about *now*. What
// the card decided in the past is history and is drawn as history
// (stretch.go). The difference matters: this is re-read from live
// session state on every frame, so it cannot outlive its own truth the
// way a note about a finished period did — there is nothing here to go
// stale, and nothing to clean up.
func autopilotField(s *theme.Styles, m *Shell, f domain.Feature) string {
	label := "autopilot: " + autopilotLabel(f.GateApproval)
	if f.GateApproval == domain.GateOff {
		return s.Faint.Render(label)
	}
	sess := m.sessionFor(f.ID)
	if sess == nil {
		return s.Faint.Render(label)
	}
	// Queued counts: the card has been handed over and is waiting on a
	// lane, which is autopilot working on it as much as a turn in flight
	// is. Paused and done do not — a mode is what those cards carry, not
	// what they are doing.
	if st := sess.State(); st != engine.StateRunning && st != engine.StateQueued {
		return s.Faint.Render(label)
	}
	return s.Info.Render(label + " · running")
}

// autopilotLabel names the card's gate-approval mode as stored
// (domain.GateOff/GateGates/GateFull; empty reads as gates). It shows
// exactly what the card carries rather than inventing wording the stored
// value does not have.
func autopilotLabel(mode string) string {
	if mode == "" {
		return domain.GateGates
	}
	return mode
}

// roundLabel renders the "⟲ n of m" badge for whichever automatic loop
// the card's current stage belongs to — the plan critique loop at Plan,
// the shared review→fix loop everywhere else a round has been burned.
// Empty once nothing has run yet.
func roundLabel(m *Shell, f domain.Feature) string {
	kind, roundCap := domain.RoundKindReview, maxReviewRounds
	if f.Stage == domain.StagePlan {
		kind, roundCap = domain.RoundKindPlan, maxPlanRounds
	}
	if n := m.round(f.ID, kind); n > 0 {
		return "⟲ " + itoa(n) + " of " + itoa(roundCap)
	}
	return ""
}

// stageStrip renders every stage of this card's own workflow in order,
// picking the current one out with its StagePill.
//
// The strip windows itself around the current stage rather than trusting
// the page's generic right-side clip: a plain left-to-right join truncates
// from the right, and stageSequence always runs todo→done, so "cut from
// the right" and "cut the stages closest to done" are the same operation —
// exactly wrong, since a card spends the second half of its life in those
// stages. The lit stage is the one thing this row exists to show, so it is
// the last thing dropped, never the first: try the full sequence, then a
// window of the lit stage plus one neighbour either side, then the lit
// stage alone, then a positional summary, and only once none of those
// fit either, the bare pill — which relies on the page's clip to cut it
// down, the same generic right-side truncation this function otherwise
// exists to route around, but is safe here because the pill's own text
// is always its first (and often only) content.
func stageStrip(s *theme.Styles, f domain.Feature, width int) string {
	seq := stageSequence(f)
	cur := 0
	for i, st := range seq {
		if st == f.Stage {
			cur = i
			break
		}
	}

	window := func(lo, hi int) string {
		parts := make([]string, 0, hi-lo+3)
		if lo > 0 {
			parts = append(parts, s.Faint.Render("…"))
		}
		for i := lo; i <= hi; i++ {
			if seq[i] == f.Stage {
				parts = append(parts, s.StagePill(seq[i]).Render(string(seq[i])))
			} else {
				parts = append(parts, s.Faint.Render(string(seq[i])))
			}
		}
		if hi < len(seq)-1 {
			parts = append(parts, s.Faint.Render("…"))
		}
		return strings.Join(parts, s.Faint.Render(stageJoin))
	}

	fits := func(str string) bool { return width <= 0 || ansi.StringWidth(str) <= width }

	if full := window(0, len(seq)-1); fits(full) {
		return full
	}
	if near := window(max(cur-1, 0), min(cur+1, len(seq)-1)); fits(near) {
		return near
	}
	if lit := window(cur, cur); fits(lit) {
		return lit
	}
	pill := s.StagePill(f.Stage).Render(string(f.Stage))
	if positional := pill + s.Faint.Render(" · "+itoa(cur+1)+" of "+itoa(len(seq))); fits(positional) {
		return positional
	}
	return pill
}

// stageSequence derives the ordered list of stages this exact card's
// workflow offers it — a bug's, a feature's and a skip-flagged card's
// differ — from the workflow package rather than a hardcoded string. At
// each stage it picks the same edge engine.Engine.nextStage
// (advance.go) would resolve g into, since a skip flag only ever adds an
// extra legal edge rather than removing the primary one: workflow.Next
// lists the primary forward edge first and an active skip edge after it
// in the underlying table, so the *last* entry is the one actually taken
// — except leaving review/verify, whose last entry is the rerun bounce
// back to the work stage rather than the forward move, so there the
// first entry is the one to take.
func stageSequence(f domain.Feature) []domain.Stage {
	kind := f.Kind
	cur := workflow.Initial(kind)
	seq := []domain.Stage{cur}
	// A backward edge picked here would walk in a circle, and this runs
	// on every frame: stop the first time a stage repeats rather than
	// trusting the edge tables to stay acyclic under this rule.
	seen := map[domain.Stage]bool{cur: true}
	for !workflow.Terminal(kind, cur) {
		nexts := workflow.Next(kind, cur, f.Skip)
		if len(nexts) == 0 {
			break
		}
		next := nexts[len(nexts)-1]
		if cur == domain.StageReview || cur == domain.StageVerify {
			next = nexts[0]
		}
		if seen[next] {
			break
		}
		seen[next] = true
		seq = append(seq, next)
		cur = next
	}
	return seq
}

// currentSpecSection names the artifact section most relevant to a
// card's current stage — the pinned spec line's anchor into the document
// underneath it. Empty for stages with no single natural section (todo,
// done).
func currentSpecSection(kind domain.Kind, stage domain.Stage) string {
	switch kind {
	case domain.KindBug:
		switch stage {
		case domain.StageTriage:
			return "Reproduction"
		case domain.StageDiagnose:
			return "Root cause"
		case domain.StageFix:
			return "Fix"
		case domain.StageReview:
			return "Review"
		case domain.StageVerify:
			return "Verification"
		}
	case domain.KindResearch:
		switch stage {
		case domain.StageInvestigate:
			return "Findings"
		case domain.StageShape:
			return "Direction"
		case domain.StageReview:
			return "Review"
		case domain.StageVerify:
			return "Verification plan"
		}
	default:
		switch stage {
		case domain.StageBrainstorm:
			return "Problem"
		case domain.StageSpec:
			return "Chosen approach"
		case domain.StagePlan, domain.StageImplement:
			return "Implementation notes"
		case domain.StageReview:
			return "Review"
		case domain.StageVerify:
			return "Verification plan"
		}
	}
	return ""
}

// pinnedSpecLine is the thread's anchor back to the design artifact: the
// section current for this stage, how many open %% questions block the
// gate, and the key that opens the full view.
func pinnedSpecLine(s *theme.Styles, r featureRow, w int) string {
	f := r.F
	section := currentSpecSection(f.Kind, f.Stage)
	if section == "" {
		return ""
	}
	head := "⌄ " + artifactNoun(f.Kind) + " · " + section
	tail := ""
	if r.OpenSpecQs > 0 {
		tail = itoa(r.OpenSpecQs) + " open %%  "
	}
	tail += "s"

	// a rule carries the eye from the section name to the key that opens
	// it, and right-aligns the hint the way a folded receipt's timestamp
	// is aligned — without it the hint floats mid-line, at a different
	// place on every card
	fill := max(w-ansi.StringWidth(head)-ansi.StringWidth(tail)-2, 1)
	out := s.Faint.Render("⌄ ") + s.Muted.Render(artifactNoun(f.Kind)) + s.Faint.Render(" · "+section) +
		" " + s.Separator.Render(strings.Repeat("─", fill)) + " "
	if r.OpenSpecQs > 0 {
		out += s.Warning.Render(itoa(r.OpenSpecQs)+" open %%") + "  "
	}
	return out + s.KeyHint.Render("s")
}

// threadEmptyLine is the body's one line when nothing else fills it: a
// card's own one-liner from the creation form, or a plain admission that
// no session has run yet. It only ever shows up alongside an absent
// pinned spec line (todo has no natural section to pin), which is why
// the body cannot just stay empty — todo is the one stage where a
// reader has nothing else on the page telling them what the card is.
func threadEmptyLine(s *theme.Styles, f domain.Feature) string {
	if f.OneLiner != "" {
		return s.Base.Render(f.OneLiner)
	}
	return s.Faint.Render("nothing has run yet")
}

// stageEnterPayload/stageExitPayload/messagePayload/toolPayload mirror
// the JSON shapes the engine writes into card_events (see
// internal/engine/persist.go's mirrorEvents) — kept local because they
// are a rendering-side concern, not something the store needs typed.
type (
	stageEnterPayload struct {
		Role   string `json:"role"`
		Model  string `json:"model"`
		Flavor string `json:"flavor"`
	}
	stageExitPayload struct {
		Verdict string  `json:"verdict"`
		Credits float64 `json:"credits"`
	}
	messagePayload struct {
		Author  string `json:"author"`
		Content string `json:"content"`
	}
	toolPayload struct {
		Label string `json:"label"`
	}
)

// stageSegment is one generation of a stage session, reconstructed from
// its stage_enter/stage_exit event pair. An unclosed segment (exited ==
// false) has no stage_exit yet — that is always the last segment in the
// slice, and it is the live stage, never folded.
type stageSegment struct {
	stage   domain.Stage
	role    string
	model   string
	enterAt time.Time
	exited  bool
	verdict string
	credits float64
	exitAt  time.Time
	events  []state.CardEvent // messages/tools recorded within this segment
	// enterIdx is where this segment's stage_enter sat in the event slice
	// it was reconstructed from, and evIdx holds the same index for each
	// entry of events. Folding loses position — a segment becomes one
	// receipt line — and the autopilot stretches drawn around those
	// receipts are bounded by event indices, so without a way back to the
	// original position there is no way to say which side of a boundary a
	// folded stage fell on.
	enterIdx int
	evIdx    []int
}

// stageSegments reconstructs a card's session history from its event
// log, in seq order: each stage_enter opens a segment, the matching
// stage_exit (same stage, still open) closes it, and every other event
// in between belongs to whichever segment is currently open. Nil input
// (events not loaded yet, or none recorded) yields no segments — the
// caller degrades to omitting the folded receipts and the live-stage
// fallback, exactly as required.
func stageSegments(events []state.CardEvent) []stageSegment {
	var segs []stageSegment
	for i, ev := range events {
		switch ev.Kind {
		case state.EventStageEnter:
			var p stageEnterPayload
			_ = json.Unmarshal([]byte(ev.Payload), &p)
			segs = append(segs, stageSegment{
				stage: ev.Stage, role: p.Role, model: p.Model, enterAt: ev.At, enterIdx: i,
			})
		case state.EventStageExit:
			if len(segs) == 0 {
				continue
			}
			last := &segs[len(segs)-1]
			if last.exited || last.stage != ev.Stage {
				continue
			}
			var p stageExitPayload
			_ = json.Unmarshal([]byte(ev.Payload), &p)
			last.exited, last.verdict, last.credits, last.exitAt = true, p.Verdict, p.Credits, ev.At
		default:
			if len(segs) == 0 {
				continue
			}
			last := &segs[len(segs)-1]
			last.events = append(last.events, ev)
			last.evIdx = append(last.evIdx, i)
		}
	}
	return segs
}

// foldedReceiptLine renders one finished stage session as the single
// line folding really means: stage, role, turn count, spend, and the
// outcome marker with the time it closed.
func foldedReceiptLine(s *theme.Styles, seg stageSegment, spend map[domain.Stage]float64, stageSegs int, w int) string {
	turns := 0
	for _, ev := range seg.events {
		if ev.Kind == state.EventMessage {
			turns++
		}
	}
	// no chevron: a folded receipt used to unfold either through
	// Shell.expandedStages or the transcript view (t), and neither exists
	// any more — the thread is the only view there is, so the glyph would
	// promise an expansion this line can no longer deliver. pinnedSpecLine
	// keeps its own chevron; that one still opens something (s).
	head := string(seg.stage)
	if seg.role != "" {
		head += " · " + seg.role
	}
	// A stage with no message turns says nothing about them: "0 turns" is
	// a count of a thing that did not happen, and a plan stage that only
	// wrote and critiqued an artifact has none. The clause earns its
	// place only when there is a conversation to count.
	if turns > 0 {
		head += " · " + itoa(turns) + " turn"
		if turns > 1 {
			head += "s"
		}
	}
	// stage_spend's primary key is (feature, stage, model, role): it rolls
	// every session of a stage into one number, so it can answer "what did
	// fix cost this card" but not "what did this fix session cost" — a
	// card that bounced through review→fix four times has four segments
	// and one rollup between them, and printing that rollup on each of
	// their receipts is how a ~172-credit card reads as 53.5 (all four
	// print the same total). The stage_exit event payload is the only
	// per-session record there is, so it wins whenever there is more than
	// one segment to tell apart; the rollup is kept as a fallback for a
	// stage that only ran once, in case its payload predates the credits
	// field.
	credits := seg.credits
	if credits == 0 && stageSegs == 1 {
		credits = spend[seg.stage]
	}
	if credits > 0 {
		head += fmt.Sprintf(" · %g credits", roundSpend(credits))
	}
	mark := eventMarker(s, "")
	if seg.exited {
		switch seg.stage {
		case domain.StageReview, domain.StageVerify:
			// review/verify sessions carry a real pass/changes/fail/blocked
			// verdict (internal/verdict); only a resolved "pass" earns ✓, and
			// only "fail" earns ✗ — anything else (including "", the shape
			// left behind by a session that exited without ever calling
			// submit_verdict) stays the neutral · rather than defaulting to
			// a pass that was never recorded.
			switch seg.verdict {
			case verdict.Pass.String():
				mark = eventMarker(s, state.StatusOK)
			case verdict.Fail.String():
				mark = eventMarker(s, state.StatusFail)
			}
		default:
			// every other stage never calls submit_verdict, so verdict=="" is
			// its only possible value and isn't itself a negative signal —
			// exited and not failed still reads ✓.
			if seg.verdict == state.StatusFail {
				mark = eventMarker(s, state.StatusFail)
			} else {
				mark = eventMarker(s, state.StatusOK)
			}
		}
	}
	ts := ""
	if !seg.exitAt.IsZero() {
		ts = seg.exitAt.Format("15:04")
	} else if !seg.enterAt.IsZero() {
		// The interactive stages (brainstorm, spec, triage, diagnose,
		// shape) never earn a stage_exit on an ordinary approval — Advance
		// tears the session down via Drop without recording one — so
		// seg.exitAt stays zero forever for them. Falling back to when the
		// segment opened, labeled as a start rather than an end, keeps
		// every row in this chronological column placeable in time.
		ts = "from " + seg.enterAt.Format("15:04")
	}
	tail := mark + s.Faint.Render(ts)
	fill := max(w-ansi.StringWidth(head)-ansi.StringWidth(ts)-4, 1)
	return s.Faint.Render(head+" ") + s.Separator.Render(strings.Repeat("─", fill)) + " " + tail
}

// liveStageBlock is the thread's most recent stage: a session-boundary
// rule naming the stage, role, model and "fresh context" — every stage
// session starts one, since the spec (not a transcript) is what carries
// context between stages, so the label is never conditional — then that
// stage's whole conversation, then, while an agent is mid-turn, the
// streaming activity line. It prefers a live engine.Session's Snapshot
// (freshest, and the only place an open ask_user question lives); a
// watched card another process drives renders its followed stream
// read-only instead; with neither, the last reconstructed segment from
// the event log stands in, so a card between runs still shows what its
// last session did. A card another process drives without an open tail
// has only that fallback either way — watching it is what opens the tail
// (follow.go's watchForeign, via enter).
// stretches carries the periods this card ran itself, so the rules
// bracketing one that reaches into the live stage are drawn here rather
// than around this whole block. That matters most for the ordinary case:
// autopilot works, parks, and you come back and start typing — all
// inside one stage. Bracketing the block as a whole would put the turns
// you typed after the handback above the rule announcing it.
func (m *Shell) liveStageBlock(s *theme.Styles, r featureRow, segs []stageSegment, w int, answered map[string]bool, stretches, liveOpens []autopilotStretch, anchorFrom int) (lines []string, anchorAt int) {
	f := r.F
	if sess := m.sessionFor(f.ID); sess != nil && !r.DrivenAbroad {
		snap := sess.Snapshot()
		at := time.Time{}
		if len(segs) > 0 {
			at = segs[len(segs)-1].enterAt
		}
		// the rule names a context reset; the blank line under it is what
		// makes it read as a boundary rather than a heading glued to the
		// first thing that happened after it
		anchorAt = -1
		lines = []string{boundaryRule(s, string(f.Stage), string(snap.Role), runModel(snap), at, w), ""}
		// A live session renders from its own snapshot rather than the
		// event log, so there are no indices here to place a rule against.
		// There is only ever one period to draw in that case — a card with
		// something running now is a card whose period, if it has one, has
		// not closed — so its opening rule goes at the top and nothing
		// else is claimed.
		// Only a period that began before this session can be placed
		// here. A live session renders from its own snapshot, which has no
		// event indices to hang a rule off, so a switch pressed part-way
		// through one has no honest position: drawing it at the top would
		// put it above the turns that preceded it, which is the error this
		// change exists to remove. It is left undrawn until the session
		// ends and the log renders it in its own place — and meanwhile the
		// masthead already says the card is running under autopilot.
		if len(segs) > 0 {
			for _, st := range liveOpens {
				if st.running() && st.from < segs[len(segs)-1].enterIdx {
					if st.from == anchorFrom {
						anchorAt = len(lines)
					}
					lines = append(lines, stretchOpenLine(s, st, w), "")
				}
			}
		}
		lines = append(lines, transcriptLines(s, snap, w, m.threadOutputs)...)
		if meta := sessionMeta(snap); meta != "" {
			lines = append(lines, "  "+s.Faint.Render(meta))
		}
		if snap.Err != nil {
			// a failure's diagnosis lives in its tail; wrap the whole
			// message rather than truncating it away, capped to errLines
			for _, l := range strings.Split(wrapError(snap.Err.Error(), max(w-2, 4)), "\n") {
				lines = append(lines, "  "+s.Error.Render(l))
			}
		}
		switch {
		case snap.Busy:
			lines = append(lines, "  "+s.Info.Render(m.spinner()+" "+m.runningLabel(snap)))
		case len(lines) == 1:
			lines = append(lines, "  "+s.Faint.Render("starting…"))
		}
		return lines, anchorAt
	}

	// an open tail is the freshest thing this board has for this card, so
	// it renders whether or not the foreign-drive probe still reports the
	// card as driven abroad — a run that ended between probes is what the
	// footer's "dropped" says, and falling back to the event log instead
	// would silently swap the stream for a staler copy of it.
	if m.follow != nil && m.follow.feature == f.ID {
		snap := m.follow.fl.Snapshot()
		at := time.Time{}
		if len(segs) > 0 {
			at = segs[len(segs)-1].enterAt
		}
		stage := snap.Feature.Stage
		if stage == "" {
			stage = r.F.Stage
		}
		anchorAt = -1
		lines = []string{boundaryRule(s, string(stage), string(snap.Role), runModel(snap), at, w), ""}
		lines = append(lines, transcriptLines(s, snap, w, m.threadOutputs)...)
		if meta := sessionMeta(snap); meta != "" {
			lines = append(lines, "  "+s.Faint.Render(meta))
		}
		lines = append(lines, "  "+s.Warning.Render(m.follow.marker())+
			s.Faint.Render(" — "+m.follow.footer(snap)))
		return lines, anchorAt
	}

	if len(segs) == 0 {
		return nil, -1
	}
	last := segs[len(segs)-1]
	anchorAt = -1
	lines = []string{boundaryRule(s, string(last.stage), last.role, last.model, last.enterAt, w), ""}
	// A period that opened inside this stage rather than before it — the
	// switch pressed on a card already sitting here — gets its rule where
	// it happened, above the first thing it did.
	// A period that began before this stage did opens at the top, because
	// there is nothing earlier here to place it against. One that began
	// inside the stage opens inline, at its own event, further down —
	// pressing the switch after working on a card by hand for a while is
	// ordinary, and hoisting that rule to the top of the stage would put
	// it above the turns you typed before you pressed it.
	for _, st := range liveOpens {
		if st.from >= last.enterIdx {
			continue
		}
		if st.from == anchorFrom {
			anchorAt = len(lines)
		}
		lines = append(lines, stretchOpenLine(s, st, w), "")
		// A period whose end also predates this stage — handed over and
		// handed straight back before anything ran — closes here too. The
		// inline loop below can only close periods whose closing event is
		// one of this stage's own, so without this its rule would have no
		// end anywhere on the page.
		if !st.running() && st.to < last.enterIdx {
			lines = append(lines, stretchCloseLines(s, st, w)...)
			lines = append(lines, "")
		}
	}
	// the whole session, not the last few events: capping this to a recent
	// tail was how the (now-gone) transcript view earned its keep, and
	// without it the cap just hid history with no way back to it. The body
	// region scrolls (pgup/pgdn, maxThreadScroll), so a long session is
	// still reachable — it just does not require a second view to see.
	for k, ev := range last.events {
		idx := last.evIdx[k]
		// A period that ends inside this stage closes exactly where it
		// ended, so everything after it — the turns you typed once the
		// card was yours again — falls below the rule saying so. The
		// closing event itself is not rendered as a line: for a park the
		// rule already carries its sentence, and printing both would say
		// one ending twice.
		for _, st := range liveOpens {
			if st.from == idx {
				if st.from == anchorFrom {
					anchorAt = len(lines)
				}
				lines = append(lines, stretchOpenLine(s, st, w), "")
			}
		}
		closed := false
		for _, st := range stretches {
			if st.running() || st.to != idx {
				continue
			}
			// The rules sit at column 0 like the session boundary above
			// them, not indented with the conversation: they bracket the
			// stage's contents rather than being one of them. Their reason
			// and tally lines carry their own indent already.
			lines = append(lines, stretchCloseLines(s, st, w)...)
			// a row of air before the conversation resumes: what follows a
			// handback is yours, and running it flush against the tally
			// reads as more of the same block
			lines = append(lines, "")
			closed = closed || ev.Kind == state.EventPark || ev.Kind == state.EventAutopilot
		}
		if closed {
			continue
		}
		if dl := stretchDecisionLine(s, ev, inStretch(stretches, idx), w-2); dl != "" {
			lines = append(lines, "  "+dl)
			continue
		}
		for _, l := range stageEventLines(s, ev, w, m.threadOutputs, last.role, answered) {
			lines = append(lines, "  "+l)
		}
	}
	if r.DrivenAbroad {
		lines = append(lines, "  "+s.Faint.Render("driven elsewhere — "+foreignSummary(r.Foreign)))
	}
	return lines, anchorAt
}

// consultBlock renders the card's consult exchange, if any, as its own
// visually distinct, captioned segment — the same idea boardHeader/
// boardThreadRender use to keep the board's own conversation
// recognizably not a card's, applied here to keep a consult exchange
// recognizably not the stage's. It never spawns a session by being
// drawn: m.consultFor is a lookup only, so a card nobody has asked
// anything renders nothing here at all.
func (m *Shell) consultBlock(s *theme.Styles, r featureRow, w int) []string {
	c := m.consultFor(r.F.ID)
	if c == nil {
		return nil
	}
	snap := c.Snapshot()
	if len(snap.Transcript) == 0 {
		return nil
	}
	lines := []string{consultCaption(s, snap, w), ""}
	lines = append(lines, transcriptLines(s, snap, w, m.threadOutputs)...)
	if snap.Err != nil {
		for _, l := range strings.Split(wrapError(snap.Err.Error(), max(w-2, 4)), "\n") {
			lines = append(lines, "  "+s.Error.Render(l))
		}
	}
	if snap.Busy {
		lines = append(lines, "  "+s.Info.Render(m.spinner()+" thinking…"))
	}
	return lines
}

// consultCaption is the consult block's own boundary line: dash-dot
// filled (┄, never boundaryRule's solid ──) so it reads as a different
// KIND of divider on sight, not just a differently-worded one — a stage
// boundary marks a fresh context in the same conversation; this marks a
// second, entirely separate one.
func consultCaption(s *theme.Styles, snap engine.Snapshot, w int) string {
	label := "asked · read-only"
	if mdl := runModel(snap); mdl != "" {
		label += " · " + mdl
	}
	if sp := spendSummary(snap); sp != "" {
		label += " · " + sp
	}
	head := "┄┄ " + label + " "
	fill := max(w-ansi.StringWidth(head)-2, 0)
	return s.Warning.Render(head) + s.Separator.Render(strings.Repeat("┄", fill))
}

// boundaryRule is the live stage's session-boundary line: stage, role,
// model and "fresh context", dash-filled to w with the time it began on
// the right.
func boundaryRule(s *theme.Styles, stage, role, model string, at time.Time, w int) string {
	label := stage
	if role != "" {
		label += " · " + role
	}
	if model != "" {
		label += " · " + model
	}
	label += " · fresh context"
	ts := ""
	if !at.IsZero() {
		ts = at.Format("15:04")
	}
	head := "── " + label + " "
	// With no time known the rule simply runs to the edge; a stray " ──"
	// hanging off the end reads as a missing value rather than as one
	// that was never relevant.
	tail := "──"
	if ts != "" {
		tail = " " + ts + " ──"
	}
	fill := max(w-ansi.StringWidth(head)-ansi.StringWidth(tail), 0)
	return s.Faint.Render(head + strings.Repeat("─", fill) + tail)
}

// stageEventLines renders one logged card event as lines, the event-log
// counterpart to a live session's transcript (transcript.go): one line
// for most events, plus the captured output beneath a tool call — a
// failure's tail always, everything with alt+o. The single-line
// stageEventLine stays for the collapse-into-history ask/gate lines,
// which are one row by contract (DESIGN §6.3).
//
// A blank line from stageEventLine means the event is deliberately
// invisible here — an answered decision_open, which has already
// collapsed into the gate/ask row that answers it (DESIGN §6.3) — and
// that must cost the body no row at all, not a blank one: this is what
// tells the caller to drop the event entirely rather than forwarding a
// one-element slice holding an empty string, which liveStageBlock would
// otherwise indent into a visible blank line.
func stageEventLines(s *theme.Styles, ev state.CardEvent, w int, showOutput bool, role string, answered map[string]bool) []string {
	line := stageEventLine(s, ev, w, role, answered)
	if line == "" {
		return nil
	}
	// EventMessage's arm above returns several newline-joined rows (a
	// label row plus one per wrapped body line); splitting here, rather
	// than forwarding it as one slice element, is what lets the caller's
	// per-event indent (thread.go's stretchRender) land on every row
	// instead of only the first. Every other kind renders a single line
	// with no embedded newline, so this is a no-op for them.
	lines := strings.Split(line, "\n")
	if ev.Kind != state.EventTool || ev.Output == "" {
		return lines
	}
	status := engine.ToolPending
	switch ev.Status {
	case state.StatusOK:
		status = engine.ToolOK
	case state.StatusFail:
		status = engine.ToolFail
	}
	return append(lines, toolOutputLines(s, status, ev.Output, w, showOutput)...)
}

// answeredDecisions returns the set of decision ids this card's event
// log has already answered: every GatePayload.ID and AskPayload.ID that
// shows up on a gate or ask event anywhere in events. Those two fields
// are the correlation EventDecisionOpen's own doc comment describes
// (state/cardevents.go) — a decision_open row and the gate/ask row that
// answers it share one id — so a decision whose id appears here has
// collapsed into that answer's row per DESIGN §6.3, and stageEventLine's
// EventDecisionOpen case uses this set to render nothing for it rather
// than saying the same stop twice.
//
// Callers compute this once per render (threadRender) rather than once
// per line: stageEventLine only ever sees the single event it is asked
// to render, and a card's history can carry many decision_open rows, so
// re-scanning the whole event log inside the per-line renderer would
// redo the same work once per line for nothing.
func answeredDecisions(events []state.CardEvent) map[string]bool {
	answered := map[string]bool{}
	for _, ev := range events {
		var id string
		switch ev.Kind {
		case state.EventGate:
			var p state.GatePayload
			_ = json.Unmarshal([]byte(ev.Payload), &p)
			id = p.ID
		case state.EventAsk:
			var p state.AskPayload
			_ = json.Unmarshal([]byte(ev.Payload), &p)
			id = p.ID
		default:
			continue
		}
		if id != "" {
			answered[id] = true
		}
	}
	return answered
}

// stageEventLine renders one logged card event as a single line, the
// event-log counterpart to a live session's tool ticker (transcript.go's
// transcriptLines).
//
// answered is the set of decision ids this card's log has already
// answered (answeredDecisions, computed once per render in threadRender
// and threaded down through liveStageBlock/stageEventLines) — it is what
// the EventDecisionOpen case below needs to tell an answered decision
// from a superseded one.
func stageEventLine(s *theme.Styles, ev state.CardEvent, w int, role string, answered map[string]bool) string {
	switch ev.Kind {
	case state.EventTool:
		var p toolPayload
		_ = json.Unmarshal([]byte(ev.Payload), &p)
		return eventMarker(s, ev.Status) + toolLineView(s, sanitize(p.Label), max(w-6, 8))
	case state.EventMessage:
		var p messagePayload
		_ = json.Unmarshal([]byte(ev.Payload), &p)
		// who said it decides the weight, the same way the live
		// transcript does (transcript.go): rendering every logged turn at
		// one faint weight made a replayed conversation unreadable next
		// to the live one it is the history of.
		body := s.Subtle
		switch p.Author {
		case string(engine.AuthorUser):
			body = s.Base
		case string(engine.AuthorSystem):
			body = s.Subtle
		}
		// Full match to live (transcriptLines, transcript.go): the label
		// gets its own row and every wrapped line of the body is
		// indented under it, newline-joined so stageEventLines can split
		// it back into rows the caller indents individually. wrapText
		// only breaks long lines into more rows — it never truncates —
		// so a message that needs more rows than fit in one still shows
		// every line instead of losing whatever crossed a single width
		// budget.
		rows := strings.Split(wrapText(sanitize(p.Content), max(w-6, 8)), "\n")
		out := make([]string, 0, len(rows)+1)
		out = append(out, s.Faint.Render(messageAuthorLabel(p.Author, role)))
		for _, l := range rows {
			out = append(out, "  "+body.Render(l))
		}
		return strings.Join(out, "\n")
	case state.EventAsk:
		var p state.AskPayload
		_ = json.Unmarshal([]byte(ev.Payload), &p)
		who := "you"
		if p.Actor == state.ActorAutopilot {
			who = "autopilot"
		}
		line := who + " answered"
		if p.Question != "" {
			line += " “" + sanitize(p.Question) + "”"
		}
		if p.Answer != "" {
			line += " — " + sanitize(p.Answer)
		}
		return s.Success.Render("✓ ") + s.Subtle.Render(ansi.Truncate(line, max(w-2, 8), "…"))
	case state.EventGate:
		var p state.GatePayload
		_ = json.Unmarshal([]byte(ev.Payload), &p)
		who := "you"
		if p.Actor != "" && p.Actor != state.ActorUser {
			who = p.Actor
		}
		line := who + " advanced"
		if p.From != "" || p.To != "" {
			line += " " + p.From + " → " + p.To
		}
		return s.Success.Render("✓ ") + s.Subtle.Render(ansi.Truncate(line, max(w-2, 8), "…"))
	case state.EventPark:
		var p state.ParkPayload
		_ = json.Unmarshal([]byte(ev.Payload), &p)
		// Detail is kept verbatim (ParkPayload's own doc comment,
		// state/cardevents.go) precisely so history explains itself
		// without the reader reconstructing it from the reason code, so it
		// wins whenever a row has one. Only an old row, written before
		// ParkPayload carried Detail at all, falls back to a sentence
		// derived from Reason — the same three-way split QuitStopped
		// already treats ParkReasonQuit as load-bearing and everything
		// else as "a human should look at this," here spelled out as
		// prose instead of a boolean.
		sentence := p.Detail
		if sentence == "" {
			switch p.Reason {
			case state.ParkReasonQuit:
				sentence = "the board quit"
			case state.ParkReasonGaveUp:
				sentence = "it gave up"
			default: // ParkReasonNeedsYou, and any reason not yet named
				sentence = "it needs you"
			}
		}
		line := "parked — " + sanitize(sentence)
		return eventMarker(s, "") + s.Subtle.Render(ansi.Truncate(line, max(w-2, 8), "…"))
	case state.EventDecisionOpen:
		var p state.DecisionPayload
		_ = json.Unmarshal([]byte(ev.Payload), &p)
		if answered[p.ID] {
			// DESIGN §6.3: an answered decision collapses into its answer,
			// and that answer is the gate or ask event already rendered
			// beside this one, correlated by DecisionPayload.ID (see the
			// doc comments on GatePayload.ID and AskPayload.ID in
			// state/cardevents.go). A row for the question and a row for
			// its answer would say one stop twice, so the question's own
			// row renders nothing at all once it has one.
			return ""
		}
		// DESIGN §10.18: nothing may block a card without leaving a row.
		// A decision that was opened and then superseded — a later run
		// raised a different decision before a human got to this one —
		// was never answered, so it has no gate/ask row to collapse into.
		// The pinned open-decision control only ever shows the *current*
		// decision, so without this line a superseded-but-unanswered
		// decision would have no trace anywhere in the card's history.
		line := sanitize(p.Question) + " — unanswered, superseded"
		return s.Faint.Render(ansi.Truncate(line, max(w-2, 8), "…"))
	case state.EventAutopilot:
		var p state.AutopilotPayload
		_ = json.Unmarshal([]byte(ev.Payload), &p)
		if p.Event != "" {
			// A took-over/handed-back boundary (AutopilotPayload.Event is
			// AutopilotTookOver or AutopilotHandedBack) is the edge of a
			// period the card ran unattended, not a single fact about a
			// mode. It is drawn as a rule bracketing that period, the way
			// boundaryRule marks a fresh session — stretch.go owns that,
			// and thread.go places it. Returning nothing here is what
			// stops the same boundary being said twice, once as a rule and
			// once as a line among the tool calls inside it.
			return ""
		}
		// Event == "" is appendAutopilotEvent's shape (state/cardevents.go):
		// every SetGateApproval mode change, human or driver, gets a row
		// here regardless of whether the card is under autopilot at all —
		// this is not a boundary crossing, just the stored mode changing to
		// p.Mode, and it renders as exactly that one fact.
		//
		// autopilotLabel, not p.Mode: the empty string is a legal stored
		// mode (domain.ValidGateApproval accepts it) that everywhere else
		// in this package reads as gates, and printing it raw would leave
		// the row trailing off after "set to" as though the value had gone
		// missing.
		line := "autopilot set to " + sanitize(autopilotLabel(p.Mode))
		return eventMarker(s, "") + s.Subtle.Render(ansi.Truncate(line, max(w-2, 8), "…"))
	default:
		return s.Faint.Render(ev.Kind)
	}
}

// eventMarker is toolMarker's (transcript.go) counterpart for a logged
// event's stored status string rather than a live engine.ToolStatus.
func eventMarker(s *theme.Styles, status string) string {
	switch status {
	case state.StatusOK:
		return s.Success.Render("✓ ")
	case state.StatusFail:
		return s.Error.Render("✗ ")
	default:
		return s.Faint.Render("· ")
	}
}

// stageSpendByStage rolls the per-stage/model spend rows up to one total
// per stage, the shape a folded receipt line needs. stage_spend is the
// meter of record for credits; the event log only carries a copy.
func stageSpendByStage(rows []state.StageSpend) map[domain.Stage]float64 {
	if len(rows) == 0 {
		return nil
	}
	out := make(map[domain.Stage]float64, len(rows))
	for _, r := range rows {
		out[r.Stage] += r.Credits
	}
	return out
}

// messageAuthorLabel names a logged turn's author the way the live pane
// labels the same turn: the user by name, gummi's own kickoffs as gummi,
// and the agent by whichever role was speaking.
func messageAuthorLabel(author, role string) string {
	switch author {
	case string(engine.AuthorUser):
		return "you"
	case string(engine.AuthorSystem):
		return "gummi"
	default:
		if role != "" {
			return role
		}
		if author == "" {
			return "agent"
		}
		return author
	}
}
