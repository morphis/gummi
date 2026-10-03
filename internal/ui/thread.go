package ui

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/threadfold"
	"github.com/morphis/gummi/internal/ui/theme"
	"github.com/morphis/gummi/internal/verdict"
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
	// bodyEventAt tags each row of body with the r.Events index that
	// rendered it, or -1 — add's and blank's own rows are always -1, and
	// the live stage block below overwrites its own span with whatever
	// liveStageBlock reported. A resize (BG-057) reads it to find which
	// event the reader was looking at before the reflow and put it back
	// at the top of the window afterwards, since threadScroll alone is
	// just a row count with no memory of what it was pointing at.
	var bodyEventAt []int
	add := func(str string) { body = append(body, clip(str)); bodyEventAt = append(bodyEventAt, -1) }
	blank := func() { body = append(body, ""); bodyEventAt = append(bodyEventAt, -1) }

	segs := threadfold.Segments(r.Events)
	// Computed once from the card's whole event log rather than once per
	// line: stageEventLine only ever sees the one event it is asked to
	// render, and it needs this set to tell an answered decision_open
	// (render nothing — DESIGN §6.3) from one superseded before anyone
	// answered it (render a trace — DESIGN §10.18). A card's history can
	// hold many decision_open rows, so re-deriving the set inside the
	// per-line renderer would redo the same scan of r.Events once per
	// line for no reason.
	answered := threadfold.AnsweredDecisions(r.Events)
	// The periods this card ran itself (stretch.go), derived once from the
	// same event slice the segments came from so both are indexed against
	// it. Folding a stage to one receipt loses its position, and the rules
	// that bracket a period are placed by position, so the two have to be
	// resolved together or not at all.
	stretches := threadfold.LiveStretches(f, r.Events, m.ws)
	// segOf answers which folded segment an event index fell in, and -1
	// for an index before the first stage ever started — where the switch
	// writes its takeover when it starts a card sitting in todo, since
	// nothing has entered a stage yet at that moment.
	segOf := func(idx int) int {
		seg := -1
		for i := range segs {
			if segs[i].EnterIdx <= idx {
				seg = i
			}
		}
		return seg
	}
	// A period opening before any stage started still opens above the
	// first one: max(_, 0). Its rule cannot be drawn earlier than the
	// history it brackets.
	openSeg := func(st threadfold.Stretch) int { return max(segOf(st.From), 0) }
	// closeSeg clamps the same way openSeg does, and for the same reason.
	// A period can both open and close before any stage ever started —
	// hand a card in todo to autopilot with no agent configured, then
	// switch it straight back off — and without the clamp its opening
	// rule would be drawn against the first segment while its closing
	// rule matched no segment at all, leaving a period on screen that
	// never ends.
	closeSeg := func(st threadfold.Stretch) int { return max(segOf(st.To), 0) }
	live := len(segs) - 1
	// The period this card should open on rather than on its newest line,
	// and where its opening rule ends up in the body. anchorIdx stays -1
	// unless that rule is actually drawn, so a period whose rule the
	// render never reaches cannot move the scroll to a row that is not
	// there. Two different callers arm m.anchorTo/m.anchorFrom for this:
	// markSeen (shell.go), for the unread-period jump on opening a card,
	// and openCitation → openAnchor's "event" case (citations.go), for
	// alt+a on a narration claim that cites the period's opening event —
	// both just want the same rule found and scrolled to, so both share
	// one mechanism rather than each inventing its own.
	// markSeen (shell.go) already resolved which period this is and left
	// its opening index behind; the render only has to notice when it
	// draws that period's rule. It must not put the question again here:
	// markSeen advances the read mark at the same moment it sets the
	// anchor, so by the time this runs the honest answer to "what is
	// unread" is always "nothing". openCitation resolves its own index
	// the same way, against the same stretches (scrollThreadToEvent), and
	// for the same reason: this render pass draws the rule, it does not
	// decide whether one exists to draw.
	//
	// markAnchor below sets anchorIdx directly for a rule drawn in the
	// folded-segment loop; a rule the live stage draws instead reports
	// its own row back through liveStageBlock's anchorAt return (folded
	// in just below, at "if at >= 0 && anchorIdx < 0"), because that is
	// the one block with no access to this closure. Both paths write the
	// same field, so whichever one actually draws the rule is the one
	// that gets to set it — including appendStretchCloses, now, for a
	// period that opened and closed entirely inside the live stage or a
	// followed tail (§2.2 of the 2026-09-10 round-2 review): before that
	// fix this render drew the rule but never told anchorIdx where, and
	// alt+a on such a period cleared the anchor and moved nothing.
	anchoring := !measure && m.anchorTo == f.ID
	anchorIdx := -1
	markAnchor := func(st threadfold.Stretch) {
		if anchoring && st.From == m.anchorFrom {
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
	var liveOpens []threadfold.Stretch
	for _, st := range stretches {
		if live >= 0 && openSeg(st) == live {
			liveOpens = append(liveOpens, st)
		}
	}
	// The same partition for the rules that close a period. The folded
	// loop closes the ones whose ending fell in a stage it draws; these
	// ended in the stage still on screen, and only the live block can
	// place them. The log branch below finds them again by event index
	// and draws them exactly where they happened — this slice is what
	// the two branches that render from a session snapshot instead have,
	// since a snapshot carries no event indices to place a rule against.
	var liveCloses []threadfold.Stretch
	for _, st := range stretches {
		if live >= 0 && !st.Running() && closeSeg(st) == live {
			liveCloses = append(liveCloses, st)
		}
	}
	if len(segs) > 1 {
		spend := threadfold.SpendByStage(r.StageSpend)
		// how many segments each stage folds to a receipt for — the review
		// →fix loop can bounce a card through fix four times, and a stage
		// with more than one segment can only trust its own stage_exit
		// payload for its spend (threadfold.ReceiptCredits says why).
		folded := segs[:len(segs)-1]
		counts := make(map[domain.Stage]int, len(folded))
		for _, seg := range folded {
			counts[seg.Stage]++
		}
		// What each stage spent that no segment claims — the figure a
		// receipt with none of its own falls back to (threadfold.Unclaimed
		// says why).
		unclaimed, unknown := threadfold.Unclaimed(spend, folded)
		// Roles that spent on a stage without ever being a session of it:
		// the scribe's check discovery and its baseline, and the backend's
		// own side-model. They hold a feature rather than a Session, so
		// they fold to no segment and got no line — and discovery is the
		// single largest thing most cards buy (95 to 148 credits on the
		// lxd autopilot drive, 14–25% of each card). The page a person
		// reads to ask "what did this cost me and on what" could not
		// explain 39% of one card's bill, most of it this.
		sessionless := threadfold.SessionlessSpend(r.StageSpend, folded)
		printed := map[domain.Stage]bool{}
		for i, seg := range folded {
			if !printed[seg.Stage] {
				printed[seg.Stage] = true
				for _, row := range sessionless[seg.Stage] {
					add(sessionlessReceiptLine(s, row, inner))
				}
			}
			// A period already running when this stage began brackets the
			// whole of it, so its rule is the one thing that belongs above
			// the receipt. Everything else that fell in this segment
			// happened after the stage started, and goes below in the order
			// the log recorded it.
			for _, st := range stretches {
				if openSeg(st) == i && st.From < seg.EnterIdx {
					markAnchor(st)
					add(stretchOpenLine(s, st, inner))
				}
			}
			add(foldedReceiptLine(s, seg, spend, counts[seg.Stage],
				threadfold.Remainder(seg, unclaimed, unknown), inner))
			// The rest of the segment, sorted by the event that produced
			// it. Drawing all the openings first and all the closings last
			// was what let a card handed to autopilot twice read as one
			// period nested inside another, with its timestamps running
			// backwards: the moment one run ends and the next begins is a
			// single fold apart, so the close and the open that follows it
			// land in the same segment, and grouping by kind put them on
			// the page in the opposite order to the log.
			//
			// The decisions autopilot made inside the stage are pulled back
			// out of the fold and printed here too (stretch.go's
			// stretchDecisionLine) — under the receipt they came from, so
			// the group reads as "this stage, and what it decided inside
			// it", but among the rules rather than always before them: a
			// crossing made after a handover belongs under the rule saying
			// the card changed hands, not above it.
			var inside []segItem
			for _, st := range stretches {
				if openSeg(st) == i && st.From >= seg.EnterIdx {
					inside = append(inside, segItem{at: st.From, open: st, isOpen: true})
				}
			}
			for _, st := range stretches {
				if !st.Running() && closeSeg(st) == i {
					inside = append(inside, segItem{at: st.To, lines: stretchCloseLines(s, st, inner)})
				}
			}
			for k, ev := range seg.Events {
				_, in := threadfold.StretchAt(stretches, seg.EvIdx[k])
				if l := stretchDecisionLine(s, ev, in, inner-2); l != "" {
					inside = append(inside, segItem{at: seg.EvIdx[k], lines: []string{"  " + l}})
				}
			}
			// stable, so an event that both closes a period and is itself
			// a decision keeps the closing rule above its own line — the
			// order the live stage block draws that pair in.
			slices.SortStableFunc(inside, func(a, b segItem) int { return a.at - b.at })
			for _, it := range inside {
				if it.isOpen {
					markAnchor(it.open)
					add(stretchOpenLine(s, it.open, inner))
					continue
				}
				for _, l := range it.lines {
					add(l)
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
	if ls, at, evAt := m.liveStageBlock(s, r, segs, inner, answered, stretches, liveOpens, liveCloses, anchorWant); len(ls) > 0 {
		base := len(body)
		for i, l := range ls {
			add(l)
			if i < len(evAt) {
				bodyEventAt[base+i] = evAt[i]
			}
		}
		if at >= 0 && anchorIdx < 0 {
			anchorIdx = base + at
		}
		blank()
	}

	// A freeform card's whole conversation. It is drawn in the same slot
	// the consult exchange uses, and exactly one of the two ever returns
	// anything: a freeform card has no consult session, and no other kind
	// has a freeform one.
	if fl := m.freeformBlock(s, r, inner); len(fl) > 0 {
		for _, l := range fl {
			add(l)
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
		if res := m.checksFor(f); len(res) > 0 {
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
			if !st.Running() {
				for _, l := range stretchCloseLines(s, st, inner) {
					add(l)
				}
			}
		}
		blank()
	}

	// A line out being read is the newest thing on the page, because it
	// is the only thing still happening — so it goes here, in the
	// conversation, beside every other marker for work in flight. It is
	// deliberately NOT in the decision's control below: the picker's rows
	// are still the answers to a question nobody has answered, and a
	// status standing in their place would hide three choices to say one
	// thing.
	if p := m.reentryRead; p != nil && p.id == f.ID {
		add("  " + s.Info.Render(m.spinner()+" reading your line…"))
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
	// trimTrailingBlanks only ever shortens from the end, so re-slicing
	// bodyEventAt to the same length keeps every row's tag lined up with
	// the row it was appended for.
	bodyEventAt = bodyEventAt[:len(body)]

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
	if !measure {
		// the measure pass forces budget to 0, which openDecisionBlock
		// (via windowDecisionBlock) reads as "unbounded" rather than
		// "dropped" — it is not a real render of this row at this height,
		// so it must never overwrite what the last real one found (BG-058).
		m.decisionDrawn = len(decisionMin) > 0
	}
	for i := range decisionMin {
		decisionMin[i] = clip(decisionMin[i])
	}
	decision := decisionMin
	if len(decisionMin) > 0 && sep > 0 {
		decision = append(make([]string, sep), decisionMin...)
	}

	// BG-057: threadScroll is a row count with no memory of what it points
	// at, so a resize that rewraps body — changing its length — leaves it
	// indexing different content with no key pressed and no notice drawn.
	// pendingScrollAnchor is the WindowSizeMsg handler's request, made
	// just before it applied the new width, to put the event that was at
	// the top of the window back at the top again; threadTopEvent is then
	// refreshed here, on every real render, so the next resize always has
	// a fresh row to ask for. The measure pass is skipped: it renders
	// unwindowed (composeH below) and would only report the newest event
	// as "at the top", which is never what a scrolled-back reader sees.
	if !measure {
		bodyBudget := bodyWindowBudget(head, headMin, decision, decisionMin, foot, footMin, len(body), h)
		if m.pendingScrollAnchor && m.pendingScrollAnchorCard == f.ID {
			m.pendingScrollAnchor = false
			if target := m.pendingScrollAnchorEvent; target >= 0 {
				for row, ev := range bodyEventAt {
					if ev == target {
						maxUp := max(len(body)-bodyBudget, 0)
						m.threadScroll = clamp(len(body)-bodyBudget-row, 0, maxUp)
						break
					}
				}
			}
		}
		m.threadTopEvent = -1
		if len(body) > 0 {
			maxUp := max(len(body)-bodyBudget, 0)
			start := (len(body) - clamp(m.threadScroll, 0, maxUp)) - bodyBudget
			m.threadTopEvent = bodyEventAt[clamp(start, 0, len(body)-1)]
		}
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

// bodyWindowBudget reports how many body rows composeThread has room to
// show at height h once head, decision and foot have taken their share —
// the same number composeThread's own windowing arithmetic below derives,
// pulled out here so BG-057's resize anchor can predict a scroll window
// without rendering one. Keep this in sync with composeThread: it mirrors
// that function's layout branches (the Min swap, the decision and head
// clamps) up to but not including the two rows composeThread spends on
// scroll markers once it actually clips the body, which the anchor math
// accounts for on its own.
func bodyWindowBudget(head, headMin, decision, decisionMin, foot, footMin []string, bodyLen, h int) int {
	if h <= 0 {
		return bodyLen
	}
	if len(foot) >= h {
		return 0
	}
	if len(foot)+len(decision)+len(head) > h {
		head, decision, foot = headMin, decisionMin, footMin
	}
	remaining := h - len(foot)
	if len(decision) > remaining {
		return 0
	}
	remaining -= len(decision)
	if len(head) > remaining {
		return 0
	}
	remaining -= len(head)
	if bodyLen > remaining && remaining >= 2 {
		return remaining - 2
	}
	if bodyLen > remaining {
		return remaining
	}
	return bodyLen
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
		// The marker names the key, not just the direction. In a list the
		// arrow is the answer — the backlog's own scrollNote can leave it
		// at that, since ↑↓ move the list — but in the thread ↑ and ↓
		// belong to the composer's line: they walk a pinned decision's
		// options, and off the top they open the action inventory. So a
		// reader who follows the arrow here moves the highlight instead of
		// the window. The bar's "pgup/pgdn scroll" row says so too, but it
		// is one of the first hints width pressure sheds, and this marker
		// is on screen exactly when the need arises.
		mark := func(arrow, key string, n int) string {
			note := scrollNote(s.Faint.Render, arrow, n)
			if note != "" {
				note += s.Faint.Render(" · ") + s.KeyHint.Render(key)
			}
			return ansi.Truncate(note, w, "…")
		}
		window = append([]string{mark("↑", "pgup", start)}, body[start:end]...)
		window = append(window, mark("↓", "pgdn", len(body)-end))
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

// threadHeader is the thread's masthead: identity, profile, autopilot
// mode, spend and the active loop's round badge, then the stage strip —
// always two lines.
//
// The badge cluster is measured first and the title is truncated to
// whatever room is left, rather than the other way round: the title is
// already shown one row up in the crumb and one keystroke away on the
// board, but the autopilot mode and the remaining credits are the two
// operational facts this row alone carries, and a positional cut blind
// to field boundaries was losing them first (BG-049). Below the width
// where even a minimal title fits beside the full badge cluster, badges
// are shed least-important-first — profile tag (already shown in the
// crumb, same as the title), then skips (rare, static), then round and
// corrective last (the only badges that signal active rework in
// progress) — with autopilot mode and budget/spend never dropped, since
// those are the two facts the masthead exists to protect. The title
// can shrink all the way to nothing before any of that shedding starts
// eating into them.
//
// The row never wraps the badge cluster onto a line of its own: this
// masthead is also the head composeThread trims first when the frame
// is too short, and that trim keeps a prefix of whatever rows it is
// given. A badge row placed behind the title would lose exactly the
// race BG-049 was filed over, just on the height axis instead of the
// width one — the id survives a short frame and the credits vanish
// again. Keeping id, title and badges on the single row that
// composeThread's height trim never has reason to cut is what makes
// that row's survival cover the badges too.
func threadHeader(s *theme.Styles, m *Shell, r featureRow, inner int) []string {
	f := r.F

	id := s.Title.Render(string(f.ID))
	title := s.Base.Render("· " + f.Title)

	profile := ""
	if f.Profile != "" {
		profile = headerGap + s.ProfileTag.Render("["+f.Profile+"]")
	}
	// A freeform card has no gates, so there is nothing for a gate-approval
	// mode to govern and nothing for the field to report. "autopilot: off"
	// on such a card says a mode is holding work back when no mode applies.
	autopilot := ""
	if !f.IsFreeform() {
		autopilot = headerGap + autopilotField(s, m, f)
	}
	budget := ""
	if f.Budget.Envelope > 0 {
		budget = headerGap + s.Faint.Render(budgetSummary(f, m.liveCardSpent(f.ID)))
	} else if !f.Spend.Zero() {
		budget = headerGap + s.Faint.Render(featureSpend(f.Spend))
	}
	skips := ""
	round := ""
	if rl := roundLabel(m, f); rl != "" {
		round = headerGap + s.Faint.Render(rl)
	}
	corrective := ""
	if cl := correctiveLabel(m, f); cl != "" {
		corrective = headerGap + s.Faint.Render(cl)
	}
	badges := func() string { return profile + autopilot + budget + skips + round + corrective }
	// autopilot and budget/spend are deliberately absent from this list —
	// they are never shed.
	dropOrder := []*string{&profile, &skips, &round, &corrective}

	idWidth := ansi.StringWidth(id) + 1 // +1 for the space before the title
	// Room for the id, a minimally readable truncated title and the
	// badge cluster together. While there isn't enough, shed badges
	// least-important-first rather than let the title crowd them out.
	// badgeWidth is tracked incrementally rather than re-measuring the
	// whole concatenated cluster on every iteration.
	badgeWidth := ansi.StringWidth(badges())
	const minTitleCols = 4
	for i := 0; i < len(dropOrder) && inner-idWidth-badgeWidth < minTitleCols; i++ {
		badgeWidth -= ansi.StringWidth(*dropOrder[i])
		*dropOrder[i] = ""
	}
	room := max(inner-idWidth-badgeWidth, 0)
	return []string{id + " " + ansi.Truncate(title, room, "…") + badges(), stageStrip(s, f, inner)}
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
// The trailing word is load-bearing twice over. roundLabel above already
// renders a bare "⟲ n of m" for whichever loop the current stage belongs
// to, and two unlabelled badges of the same shape side by side would be
// two numbers nobody could tell apart. And "corrective" alone is an
// adjective with nothing to modify — a reader has to already know this
// is a count of rounds for it to parse at all. "rounds" is the noun the
// word was missing, and it is the same phrase autopilot.go's own confirm
// dialog already uses for this exact budget ("N corrective rounds"), so
// the masthead badge now says what that dialog says rather than a
// clipped half of it.
func correctiveLabel(m *Shell, f domain.Feature) string {
	n := m.round(f.ID, domain.RoundKindCorrective)
	if n == 0 {
		return ""
	}
	return "⟲ " + itoa(n) + " of " + itoa(verdict.MaxRounds(domain.RoundKindCorrective)) + " corrective rounds"
}

// autopilotField is the masthead's autopilot cell: the stored mode, and
// — while a mode that is not off has the card — the scheduling state of
// that work right now, at Info weight rather than Faint: running names
// a turn in flight, queued names the wait for a free attention slot.
// Queued is named, not folded into running; a parked card claiming a
// turn was in flight is the one misleading thing this field could say.
//
// This is the only thing about autopilot that stays pinned, and it is
// pinned because it is the one autopilot fact that is about *now*. What
// the card decided in the past is history and is drawn as history
// (stretch.go). The difference matters: this is re-read from live
// session state on every frame, so it cannot outlive its own truth the
// way a note about a finished period did — there is nothing here to go
// stale, and nothing to clean up.
func autopilotField(s *theme.Styles, m *Shell, f domain.Feature) string {
	// Both reads go through GateMode rather than the raw field. An unset
	// GateApproval reads as attended (domain.Feature.GateApproval's own
	// doc), and comparing the raw field against GateAttended made every
	// card `bugs new` mints fall through to the live-session branch — so an
	// attended card with a stage running rendered "autopilot: off ·
	// running", claiming autopilot held work it had never been given.
	label := "autopilot: " + threadfold.AutopilotLabel(f.GateMode())
	if f.GateMode() == domain.GateAttended {
		return s.Faint.Render(label)
	}
	sess := m.sessionFor(f.ID)
	if sess == nil {
		return s.Faint.Render(label)
	}
	// Queued counts: the card has been handed over and is waiting on a
	// lane, which is autopilot working on it as much as a turn in flight
	// is — but it says so, rather than borrowing running's claim. Paused
	// and done do not — a mode is what those cards carry, not what they
	// are doing.
	switch sess.State() {
	case engine.StateQueued:
		return s.Info.Render(label + " · queued")
	case engine.StateRunning:
		return s.Info.Render(label + " · running")
	default:
		return s.Faint.Render(label)
	}
}

// roundLabel renders the "⟲ n of m" badge for whichever automatic loop
// the card's current stage belongs to — the plan critique loop at Plan,
// the shared review→fix loop everywhere else a round has been burned.
// Empty once nothing has run yet.
//
// It NAMES ITS LOOP, for the reason correctiveLabel's doc comment gives
// and then only half-applied: "two unlabelled badges of the same shape
// side by side would be two numbers nobody could tell apart". Labelling
// one of the two does not tell a reader what the other one is, and round 3
// put them on the masthead together —
//
//	⟲ 1 of 3   ⟲ 3 of 5 corrective rounds
//
// — with a third number ("reworking (round 1)") in the status bar under
// them and a help legend that has one ⟲ entry describing only one of the
// two. The denominator also changes silently from maxPlanRounds to
// maxReviewRounds as the card crosses into implement, which is only
// legible once the badge says which loop it is counting.
func roundLabel(m *Shell, f domain.Feature) string {
	kind, roundCap, loop := domain.RoundKindReview, maxReviewRounds, "review rounds"
	if f.Stage == domain.StagePlan {
		kind, roundCap, loop = domain.RoundKindPlan, maxPlanRounds, "plan rounds"
	}
	if n := m.round(f.ID, kind); n > 0 {
		return "⟲ " + itoa(n) + " of " + itoa(roundCap) + " " + loop
	}
	return ""
}

// stageStrip renders every stage of this card's own workflow in order,
// picking the current one out with its StagePill.
//
// The strip windows itself around the current stage rather than trusting
// the page's generic right-side clip: a plain left-to-right join truncates
// from the right, and threadfold.StageSequence always runs todo→done, so "cut from
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
	// A freeform card is in no sequence (DESIGN §19), so there is no
	// position to draw: the five stages faint with none of them lit would
	// say the card is somewhere in the workflow, which is the one thing
	// that is not true of it. Its branch goes here instead — the fact a
	// reader of this row actually wants, since it is what they will check
	// out, and the closest thing a freeform card has to "how far along".
	if f.IsFreeform() {
		pill := s.StagePill(f.Stage).Render("freeform")
		if branch := f.BranchName(); branch != "" {
			if full := pill + s.Faint.Render(" · "+branch); width <= 0 || ansi.StringWidth(full) <= width {
				return full
			}
		}
		return pill
	}
	seq := threadfold.StageSequence()
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

// currentSpecSection names the artifact section most relevant to a
// card's current stage — the pinned spec line's anchor into the document
// underneath it. Empty for stages with no single natural section (todo,
// done).
func currentSpecSection(kind domain.Kind, stage domain.Stage) string {
	switch kind {
	case domain.KindBug:
		switch stage {
		case domain.StagePlan:
			return "Root cause"
		case domain.StageImplement:
			return "Fix"
		case domain.StageVerify:
			return "Verification"
		}
	case domain.KindGoal:
		switch stage {
		case domain.StagePlan:
			// the plan gate is judged on the done-when list first
			return spec.GoalSectionDoneWhen
		case domain.StageImplement:
			return spec.GoalSectionCards
		case domain.StageVerify:
			return spec.GoalSectionVerify
		}
	case domain.KindResearch:
		switch stage {
		case domain.StagePlan:
			return "Direction"
		case domain.StageVerify:
			return "Verification plan"
		}
	default:
		switch stage {
		case domain.StagePlan:
			// the design stage writes Problem, Considered approaches and
			// Chosen approach on its way here; Chosen approach is the one
			// it ENDS at, and the section its gate is judged on.
			return "Chosen approach"
		case domain.StageImplement:
			return "Implementation notes"
		case domain.StageVerify:
			return "Verification plan"
		}
	}
	return ""
}

// pinnedSpecLine is the thread's anchor back to the design artifact: the
// section current for this stage, how many open comment threads block
// the gate, and the key that opens the full view.
//
// "open comments", not "open %%": %% is the marker the artifact's own
// file syntax uses to spell a comment, and printing it here put the
// file's own punctuation in front of a reader who has never opened the
// file (2026-09-10 round-2 review, §5's word table).
func pinnedSpecLine(s *theme.Styles, r featureRow, w int) string {
	f := r.F
	section := currentSpecSection(f.Kind, f.Stage)
	if section == "" {
		return ""
	}
	head := "⌄ " + artifactNoun(f.Kind) + " · " + section
	tail := ""
	if r.OpenSpecQs > 0 {
		tail = itoa(r.OpenSpecQs) + " open comment" + plural(r.OpenSpecQs) + "  "
	}
	tail += "alt+s"

	// a rule carries the eye from the section name to the key that opens
	// it, and right-aligns the hint the way a folded receipt's timestamp
	// is aligned — without it the hint floats mid-line, at a different
	// place on every card
	fill := max(w-ansi.StringWidth(head)-ansi.StringWidth(tail)-2, 1)
	out := s.Faint.Render("⌄ ") + s.Muted.Render(artifactNoun(f.Kind)) + s.Faint.Render(" · "+section) +
		" " + s.Separator.Render(strings.Repeat("─", fill)) + " "
	if r.OpenSpecQs > 0 {
		out += s.Warning.Render(itoa(r.OpenSpecQs)+" open comment"+plural(r.OpenSpecQs)) + "  "
	}
	// alt+s, not s: the composer owns every printable key on this page
	// (threadinput.go), so a bare s landed in the reader's draft instead
	// of opening anything — the one surface that draws this line was the
	// one surface its hint could not fire on.
	return out + s.KeyHint.Render("alt+s")
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

// foldedReceiptLine renders one finished stage session as the single
// line folding really means: stage, role, turn count, spend, and the
// outcome marker with the time it closed.
func foldedReceiptLine(s *theme.Styles, seg threadfold.Segment, spend map[domain.Stage]float64, stageSegs int, remainder float64, w int) string {
	turns := seg.Turns()
	// no chevron: a folded receipt used to unfold either through
	// Shell.expandedStages or the transcript view (t), and neither exists
	// any more — the thread is the only view there is, so the glyph would
	// promise an expansion this line can no longer deliver. pinnedSpecLine
	// keeps its own chevron; that one still opens something (alt+s).
	head := string(seg.Stage)
	if seg.Role != "" {
		head += " · " + seg.Role
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
	// which figure a receipt trusts, and why the per-session one wins over
	// the stage rollup, is threadfold.ReceiptCredits' to say.
	if credits := threadfold.ReceiptCredits(seg, spend, stageSegs, remainder); credits > 0 {
		head += fmt.Sprintf(" · %g credits", roundSpend(credits))
	}
	// an open segment keeps the neutral mark; a finished one's is
	// threadfold's Outcome, keyed on the role that ran it.
	mark := eventMarker(s, seg.Outcome())
	ts := ""
	if !seg.ExitAt.IsZero() {
		ts = seg.ExitAt.Format("15:04")
	} else if !seg.EnterAt.IsZero() {
		// The interactive stages (brainstorm, spec, triage, diagnose,
		// shape) never earn a stage_exit on an ordinary approval — Advance
		// tears the session down via Drop without recording one — so
		// seg.ExitAt stays zero forever for them. Falling back to when the
		// segment opened, labeled as a start rather than an end, keeps
		// every row in this chronological column placeable in time.
		ts = "from " + seg.EnterAt.Format("15:04")
	}
	tail := mark + s.Faint.Render(ts)
	fill := max(w-ansi.StringWidth(head)-ansi.StringWidth(ts)-4, 1)
	return s.Faint.Render(head+" ") + s.Separator.Render(strings.Repeat("─", fill)) + " " + tail
}

// liveStageBlock is the thread's most recent stage: a session-boundary
// rule naming the stage, role, model and "fresh context" — every stage
// session starts one, since the spec (not a transcript) is what carries
// context between stages, so the label is never conditional — then that
// stage's whole conversation, then a status line for the state the
// session is in right now: the streaming activity line while an agent
// is mid-turn, the queued wait while it sits in the lane queue. It
// prefers a live engine.Session's Snapshot
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
// evAt tags each returned line with the index (into r.Events) of the
// event that rendered it, or -1 for a line that belongs to no single
// event (a boundary rule, a period's own rule, blanks). It is only
// populated for the reconstructed-segment branch, where events have
// stable indices to tag lines with; the live-session and followed-tail
// branches render from a snapshot with no event log behind it, so they
// return a nil evAt. A resize (BG-057) uses it to find whichever event
// was at the top of the scrolled window before the reflow, so it can ask
// for that same event again after.
// appendStretchCloses draws the closing rules for periods that ended in
// the stage the block is rendering, separated from what came before by a
// row of air the way every other rule in the thread is. It exists for
// the two branches of liveStageBlock that render from a live session's
// snapshot rather than from the event log: those have no indices to
// place a rule against, so the rules go last, and without them a closed
// period had no ending anywhere on the page.
//
// anchorFrom/anchorAt carry the event-citation anchor (see thread.go's
// own doc comment on it, and liveStageBlock's callers below) through
// this, the one place that can still satisfy it in the sess/follow
// branches: a period that opened AND closed inside the session or
// followed tail those branches are rendering has no other rule drawn
// for it anywhere in them — their own open-loops only place a period
// that began before the session started (2026-09-10 round-2 review,
// §2.2, "alt+a — open cited — is dead") — so
// without checking here, alt+a on such a period's opening citation armed
// an anchor this function was the only place left that could have drawn
// it, found nothing, and cleared itself unnoticed. anchorAt is threaded
// through rather than recomputed because the caller may already have
// found the row from an earlier, unrelated loop (the "before this
// session" one) and a later match here must never overwrite that one.
func appendStretchCloses(s *theme.Styles, lines []string, opens, closes []threadfold.Stretch, anchorFrom, anchorAt int, w int) ([]string, int) {
	for _, st := range closes {
		lines = append(lines, "")
		// A period that also opened in this stage has had no rule drawn
		// for it anywhere — the folded loop never reached it and the
		// snapshot has no position to hang it off — so its opening rule
		// goes here too, immediately above its closing one. Half a period
		// is worse than a period drawn without room inside it: the pair
		// at least reads as one run that began and ended in this stage.
		for _, op := range opens {
			if op.From == st.From {
				if op.From == anchorFrom && anchorAt < 0 {
					anchorAt = len(lines)
				}
				lines = append(lines, stretchOpenLine(s, st, w))
				break
			}
		}
		lines = append(lines, stretchCloseLines(s, st, w)...)
	}
	return lines, anchorAt
}

// liveBusyLabel is the live stage's busy line: what is actually running,
// plus how long this run has been at it. It is not simply
// m.runningLabel(snap, since) (loopline.go), for two reasons — a bug fix
// and a value it already has that runningLabel does not:
//
//   - runningVerb (loopline.go) distinguishes a critique pass from the
//     stage's own work for Plan (snap.Critique flips "writing plan" to
//     "critiquing plan") but has no equivalent branch for Implement,
//     whose review→fix loop runs its own fresh-context reviewer pass the
//     same way (engine.CritiqueRoundKind lists both stages). So a review
//     turn on Implement read "implementing" here while the section rule
//     drawn a few lines above it, and the transcript inside it, both
//     said reviewer — the user was told code was being written while it
//     was being read. runningVerb also backs cardBusyWord (the board
//     row, which has no room for a role distinction) and
//     planLoopLine's breadcrumb, so the fix belongs here, at the one
//     caller that both knows the difference and has somewhere to put it,
//     rather than in the shared table every other reader of it trusts to
//     mean "the stage".
//   - since here is the caller's own segs[len(segs)-1].enterAt (see the
//     "at" local in liveStageBlock's sess branch), which is this RUN's
//     start — mirrorEvents (internal/engine/persist.go) stamps each
//     session generation's own stage_enter with that generation's own
//     startedAt, so a retry after a failure gets a fresh timestamp even
//     though the stage never changed. runningLabel's only caller used to
//     pass r.F.UpdatedAt instead, the STAGE's own start, which a retry
//     does not move: a run that failed at 20:35 and restarted at 20:37
//     read "2m38s" a minute later, counting the two minutes the TUI sat
//     closed between them.
func (m *Shell) liveBusyLabel(snap engine.Snapshot, since time.Time) string {
	verb := m.runningVerb(snap)
	if snap.Critique && !snap.Interactive && snap.Feature.Stage == domain.StageImplement {
		verb = "reviewing"
	}
	return withElapsed(verb, m.now(), since)
}

func (m *Shell) liveStageBlock(s *theme.Styles, r featureRow, segs []threadfold.Segment, w int, answered map[string]bool, stretches, liveOpens, liveCloses []threadfold.Stretch, anchorFrom int) (lines []string, anchorAt int, evAt []int) {
	f := r.F
	if sess := m.sessionFor(f.ID); sess != nil && !r.DrivenAbroad {
		snap := sess.Snapshot()
		at := time.Time{}
		if len(segs) > 0 {
			at = segs[len(segs)-1].EnterAt
		}
		// the rule names a context reset; the blank line under it is what
		// makes it read as a boundary rather than a heading glued to the
		// first thing that happened after it
		anchorAt = -1
		lines = []string{boundaryRule(s, string(f.Stage), string(snap.Role), runModel(snap), at, w), ""}
		// A live session renders from its own snapshot rather than the
		// event log, so there are no indices here to place a rule against
		// a period that is STILL RUNNING (to == len(events)) — this loop
		// only ever places one that began before this session, at the
		// top, because that is the one position a snapshot can justify: a
		// switch pressed part-way through the session has no honest
		// place to draw the rule, and putting it at the top would claim
		// it happened before turns that came first. Such a period is left
		// undrawn until the session ends and the log renders it in its
		// own place — the masthead already names the card's autopilot
		// now-state meanwhile, so nothing about it goes unsaid.
		//
		// A period that has already CLOSED is a different question with a
		// different answer, and does not go through this loop at all: it
		// gets its rule from appendStretchCloses below, whether it opened
		// before this session or inside it, because a closed period's
		// whole span is known and a rule for it does not have to guess at
		// a position the way a still-running one would.
		if len(segs) > 0 {
			for _, st := range liveOpens {
				if st.Running() && st.From < segs[len(segs)-1].EnterIdx {
					if st.From == anchorFrom {
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
		// The status switch names the session's now-state: queued checked
		// before busy, as on the board — a queued session is never busy, but
		// the order keeps the reading deterministic. Both lines are
		// live-computed per frame, so neither outlives the state it names.
		switch {
		case snap.State == engine.StateQueued:
			lines = append(lines, "  "+s.Faint.Render("◔ "+m.queuedLabelFor(r.F.ID)))
		case snap.Busy:
			// at, not r.F.UpdatedAt: the row field names when the STAGE
			// began, which a retry after a failed run does not move, so a
			// run that failed at 20:35 and restarted at 20:37 read
			// "2m38s" a minute later — counting the two minutes the TUI
			// sat closed between them. at is this run's own generation
			// (mirrorEvents stamps its stage_enter with the session's own
			// startedAt), the same stamp the boundary rule two lines above
			// already prints, so the clock and the rule it sits under can
			// no longer disagree about when this run began.
			lines = append(lines, "  "+s.Info.Render(m.spinner()+" "+m.liveBusyLabel(snap, at)))
		}
		// A period that ended in this stage still says so. The session
		// object outlives the run that filled it — the engine keeps a
		// finished one, and a restart restores it — so "there is a
		// session here" is not "a machine is driving this card", and
		// reading it as the latter left the newest thing the page said
		// about an unattended run being that it started. The rules go
		// under the transcript because that is the only honest place a
		// snapshot can put them: it carries no event indices, and the
		// period covered the work above.
		lines, anchorAt = appendStretchCloses(s, lines, liveOpens, liveCloses, anchorFrom, anchorAt, w)
		return lines, anchorAt, nil
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
			at = segs[len(segs)-1].EnterAt
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
		lines, anchorAt = appendStretchCloses(s, lines, liveOpens, liveCloses, anchorFrom, anchorAt, w)
		return lines, anchorAt, nil
	}

	if len(segs) == 0 {
		return nil, -1, nil
	}
	last := segs[len(segs)-1]
	anchorAt = -1
	lines = []string{boundaryRule(s, string(last.Stage), last.Role, last.Model, last.EnterAt, w), ""}
	// pad fills evAt up to len(lines) with idx, so every append above can
	// stay exactly as it was and the tagging happens as a single extra
	// call afterwards instead of touching each one.
	pad := func(idx int) {
		for len(evAt) < len(lines) {
			evAt = append(evAt, idx)
		}
	}
	pad(-1)
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
		if st.From >= last.EnterIdx {
			continue
		}
		if st.From == anchorFrom {
			anchorAt = len(lines)
		}
		lines = append(lines, stretchOpenLine(s, st, w), "")
		// A period whose end also predates this stage — handed over and
		// handed straight back before anything ran — closes here too. The
		// inline loop below can only close periods whose closing event is
		// one of this stage's own, so without this its rule would have no
		// end anywhere on the page.
		if !st.Running() && st.To < last.EnterIdx {
			lines = append(lines, stretchCloseLines(s, st, w)...)
			lines = append(lines, "")
		}
		pad(-1)
	}
	// the whole session, not the last few events: capping this to a recent
	// tail was how the (now-gone) transcript view earned its keep, and
	// without it the cap just hid history with no way back to it. The body
	// region scrolls (pgup/pgdn, maxThreadScroll), so a long session is
	// still reachable — it just does not require a second view to see.
	for k, ev := range last.Events {
		idx := last.EvIdx[k]
		// A period that ends inside this stage closes exactly where it
		// ended, so everything after it — the turns you typed once the
		// card was yours again — falls below the rule saying so. The
		// closing event itself is not rendered as a line: for a park the
		// rule already carries its sentence, and printing both would say
		// one ending twice.
		for _, st := range liveOpens {
			if st.From == idx {
				if st.From == anchorFrom {
					anchorAt = len(lines)
				}
				lines = append(lines, stretchOpenLine(s, st, w), "")
			}
		}
		pad(-1)
		closed := false
		for _, st := range stretches {
			if st.Running() || st.To != idx {
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
		pad(-1)
		if closed {
			continue
		}
		if dl := stretchDecisionLine(s, ev, threadfold.InStretch(stretches, idx), w-2); dl != "" {
			lines = append(lines, "  "+dl)
			pad(idx)
			continue
		}
		for _, l := range stageEventLines(s, ev, w, m.threadOutputs, last.Role, answered) {
			lines = append(lines, "  "+l)
		}
		pad(idx)
	}
	// A period nothing in the log ever closed, closed instead by the
	// render-time judgement — threadfold.CloseOrphaned, when the driver went away
	// without writing a thing — has no closing event to be placed
	// against: its `to` is the end of the log, past every index the loop
	// above walks. Its rule closes here, after everything this stage
	// holds, or it is derived and never drawn at all and the page still
	// says a machine has the card (BG-085).
	for _, st := range stretches {
		if st.Running() || st.To < len(r.Events) {
			continue
		}
		lines = append(lines, "")
		lines = append(lines, stretchCloseLines(s, st, w)...)
		pad(-1)
	}
	if r.DrivenAbroad {
		lines = append(lines, "  "+s.Faint.Render("driven elsewhere — "+foreignSummary(r.Foreign)))
		pad(-1)
	} else if r.conducted() {
		// the same sentence a foreign drive gets, for the driver one
		// level in: without it a conducted card reads as a chat whose
		// composer has stopped working for no stated reason.
		lines = append(lines, "  "+s.Faint.Render("run by "+goalDriver(r.F.GoalID)+" — read-only here · "+string(r.F.GoalID)+" takes your notes"))
		pad(-1)
	}
	return lines, anchorAt, evAt
}

// consultBlock renders the card's consult exchange, if any, as its own
// visually distinct, captioned segment — keeping a consult exchange
// recognizably not the stage's. It never spawns a session by being
// drawn: m.consultFor is a lookup only, so a card nobody has asked
// anything renders nothing here at all.
func (m *Shell) consultBlock(s *theme.Styles, r featureRow, w int) []string {
	// A freeform card's conversation is its freeform session, drawn by
	// freeformBlock. It never has a consult session — the composer routes
	// its lines to the session that can actually act on the card — and
	// both blocks read the same in-flight marker, so without this the line
	// on its way would be drawn twice.
	if r.F.IsFreeform() {
		return nil
	}
	c := m.consultFor(r.F.ID)
	asking := m.consultSending[r.F.ID]
	if c == nil {
		if asking == "" {
			// No session in this process — a restart, or a consult asked
			// from another board — but the card's log keeps what was asked
			// and answered (EventConsult), so the exchange is still drawn,
			// in the same slot, from there.
			return m.recordedConsultLines(s, r, w)
		}
		// A line on its way to a session that does not exist yet. It is
		// drawn without the caption because there is no snapshot to write
		// one from — and it is drawn at all because the alternative is a
		// composer that has just emptied itself into nothing visible.
		return m.askingLines(s, asking, w)
	}
	snap := c.Snapshot()
	if len(snap.Transcript) == 0 && asking == "" {
		return nil
	}
	lines := []string{consultCaption(s, snap, w), ""}
	lines = append(lines, transcriptLines(s, snap, w, m.threadOutputs)...)
	if snap.Err != nil {
		for _, l := range strings.Split(wrapError(snap.Err.Error(), max(w-2, 4)), "\n") {
			lines = append(lines, "  "+s.Error.Render(l))
		}
	}
	if asking != "" && !delivered(snap, asking) {
		// delivered, not settled: the session records the turn before its
		// send returns, so for the moment between the two the transcript
		// and the marker would show the same sentence twice.
		lines = append(lines, m.askingLines(s, asking, w)...)
	}
	if snap.Busy {
		lines = append(lines, "  "+s.Info.Render(m.spinner()+" thinking…"))
	}
	return lines
}

// recordedConsultLines is the consult exchange as the card's log recorded
// it, for a card with no consult session in this process: the same
// caption and the same transcript rendering, over the recorded turns.
func (m *Shell) recordedConsultLines(s *theme.Styles, r featureRow, w int) []string {
	var tr []engine.Message
	for _, ev := range r.Events {
		if ev.Kind != state.EventConsult {
			continue
		}
		var p threadfold.MessagePayload
		if json.Unmarshal([]byte(ev.Payload), &p) != nil || p.Content == "" {
			continue
		}
		tr = append(tr, engine.Message{Author: engine.Author(p.Author), Content: p.Content, By: p.By, At: ev.At})
	}
	if len(tr) == 0 {
		return nil
	}
	snap := engine.Snapshot{Role: agent.RoleConsult, Transcript: tr}
	lines := []string{consultCaption(s, snap, w), ""}
	return append(lines, transcriptLines(s, snap, w, m.threadOutputs)...)
}

// freeformBlock renders a freeform card's conversation — the whole of its
// thread, since such a card has no stages to fold into receipts and no
// event log of crossings to draw. consultBlock's shape, for the same
// reason: both are non-stage conversations that render from a session
// snapshot rather than from the card's history.
//
// It never spawns a session by being drawn. Engine.Freeform is a lookup,
// so a card whose page is merely open costs nothing, and the session is
// started by an action instead — creating the card, typing a line, or
// sending the diff's comments.
func (m *Shell) freeformBlock(s *theme.Styles, r featureRow, w int) []string {
	if !r.F.IsFreeform() {
		return nil
	}
	// The engine check guards the LOOKUP, not the block: a detached board
	// (no agent configured) still has to tell the reader what this card is
	// and where its work is, which is the one thing they cannot work out
	// from a page with nothing on it.
	var ff *engine.FreeformSession
	if m.engine != nil {
		ff = m.engine.Freeform(r.F.ID)
	}
	sending := m.consultSending[r.F.ID]
	if ff == nil {
		if sending != "" {
			// A line on its way to a session that does not exist yet, drawn
			// so the composer is not seen to empty itself into nothing.
			return m.askingLines(s, sending, w)
		}
		return m.freeformAbsentLines(s, r, w)
	}
	snap := ff.Snapshot()
	if len(snap.Transcript) == 0 && sending == "" {
		return m.freeformAbsentLines(s, r, w)
	}
	lines := transcriptLines(s, snap, w, m.threadOutputs)
	if snap.Err != nil {
		for _, l := range strings.Split(wrapError(snap.Err.Error(), max(w-2, 4)), "\n") {
			lines = append(lines, "  "+s.Error.Render(l))
		}
	}
	if sending != "" && !delivered(snap, sending) {
		lines = append(lines, m.askingLines(s, sending, w)...)
	}
	if snap.Busy {
		lines = append(lines, "  "+s.Info.Render(m.spinner()+" working…"))
	}
	return lines
}

// freeformAbsentLines is what a freeform card's thread says when no
// conversation is on screen. There are two ways to get here and they need
// different sentences, because one is a card nobody has started and the
// other is one whose work is real and whose transcript simply did not
// outlive the board that held it (a freeform session is not persisted —
// see Engine.persist). Saying nothing in the second case is what would
// read as "my card is gone".
func (m *Shell) freeformAbsentLines(s *theme.Styles, r featureRow, w int) []string {
	if r.F.Stage == domain.StageDone {
		return nil // the closing block already says how it ended
	}
	// HasWorktree is the cheap, already-loaded signal for "this card has
	// been worked on": a freeform card's tree is cut on its first turn, so
	// a card without one has never had a session at all.
	//
	// The conversation is persisted now (Engine.restoreFreeformLocked), so
	// reaching here on a card that HAS a tree means its row is gone rather
	// than that the transcript did not survive — a card whose session was
	// dropped, or one worked on by a gummi that predates the row. Say what
	// is true either way: the work is on the branch.
	said := []string{"type below to start — it works in " + r.F.BranchName() + ", committing every turn"}
	if r.HasWorktree {
		said = []string{
			"its work is on " + r.F.BranchName() + " — alt+d to read the diff",
			"no conversation on record here; say what you want next and it picks the branch up",
		}
	}
	out := make([]string, 0, len(said))
	for _, l := range said {
		out = append(out, "  "+s.Faint.Render(ansi.Truncate(l, max(w-2, 8), "…")))
	}
	return out
}

// delivered reports whether the consult session's transcript already
// holds text as the reader's own newest turn.
func delivered(snap engine.Snapshot, text string) bool {
	for i := len(snap.Transcript) - 1; i >= 0; i-- {
		if msg := snap.Transcript[i]; msg.Author == engine.AuthorUser {
			return msg.Content == text
		}
	}
	return false
}

// askingLines is the marker for a line handed to the consult session and
// not yet delivered: the spinner every busy marker in the package shares,
// and the line itself, so a reader can see WHICH line is in flight rather
// than only that something is. It comes down the moment the session has
// the line — from then on the transcript renders it, and rendering both
// would show the same sentence twice.
func (m *Shell) askingLines(s *theme.Styles, text string, w int) []string {
	out := []string{"  " + s.Info.Render(m.spinner()+" asking…")}
	for _, l := range strings.Split(wrapText(oneLineText(text), max(w-4, 8)), "\n") {
		out = append(out, "    "+s.Subtle.Render(l))
	}
	return out
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
	// "fresh context" is a fact about a session: every stage session
	// starts one, since the artifact rather than a transcript carries
	// context between stages, which is why it is otherwise
	// unconditional. A block for a stage that never opened a session at
	// all (BG-093) has no role and no model because none was ever
	// chosen, and claiming a fresh context for it would describe
	// something that did not happen.
	if role != "" || model != "" {
		label += " · fresh context"
	}
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

// stageEventLine renders one logged card event as a single line, the
// event-log counterpart to a live session's tool ticker (transcript.go's
// transcriptLines).
//
// answered is the set of decision ids this card's log has already
// answered (threadfold.AnsweredDecisions, computed once per render in threadRender
// and threaded down through liveStageBlock/stageEventLines) — it is what
// the EventDecisionOpen case below needs to tell an answered decision
// from a superseded one.
func stageEventLine(s *theme.Styles, ev state.CardEvent, w int, role string, answered map[string]bool) string {
	switch ev.Kind {
	case state.EventTool:
		var p state.ToolPayload
		_ = json.Unmarshal([]byte(ev.Payload), &p)
		status := ev.Status
		tool := p.Tool
		if tool == "" {
			tool, _, _ = strings.Cut(strings.TrimSpace(p.Label), "  ")
		}
		if status == "" && threadfold.WatchTool(tool) {
			status = threadfold.StatusWatching
		}
		return eventMarker(s, status) + toolLineView(s, sanitize(p.Label), max(w-6, 8))
	case state.EventMessage:
		var p threadfold.MessagePayload
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
		label := threadfold.AuthorLabel(p.Author, role)
		if !ev.At.IsZero() {
			label += "  " + ev.At.Format("15:04")
		}
		out = append(out, s.Faint.Render(label))
		for _, l := range rows {
			out = append(out, "  "+body.Render(l))
		}
		return strings.Join(out, "\n")
	case state.EventAsk:
		var p state.AskPayload
		_ = json.Unmarshal([]byte(ev.Payload), &p)
		return s.Success.Render("✓ ") + s.Subtle.Render(ansi.Truncate(threadfold.AskLine(p), max(w-2, 8), "…"))
	case state.EventGate:
		var p state.GatePayload
		_ = json.Unmarshal([]byte(ev.Payload), &p)
		return s.Success.Render("✓ ") + s.Subtle.Render(ansi.Truncate(threadfold.GateLine(p), max(w-2, 8), "…"))
	case state.EventPark:
		var p state.ParkPayload
		_ = json.Unmarshal([]byte(ev.Payload), &p)
		// the sentence, and why Detail wins over the reason code, is
		// threadfold.ParkLine's.
		return eventMarker(s, "") + s.Subtle.Render(ansi.Truncate(threadfold.ParkLine(p), max(w-2, 8), "…"))
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
		return s.Faint.Render(ansi.Truncate(threadfold.SupersededLine(p), max(w-2, 8), "…"))
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
		// p.Mode, and it renders as exactly that one fact (threadfold.ModeLine
		// says how an old spelling reads).
		return eventMarker(s, "") + s.Subtle.Render(ansi.Truncate(threadfold.ModeLine(p), max(w-2, 8), "…"))
	case state.EventGoal:
		// A goal's log — every landing, drop, raise, decision and lead
		// turn — is written as card_events rows on the goal card itself
		// (state.AppendGoalEvent), stamped with whatever stage the goal
		// stood at when it happened, which for a conducted goal is
		// implement. Without an arm here every one of them fell through to
		// the default below and rendered as the bare word "goal", one row
		// each: a column of nothing where the goal's own history belongs.
		var p state.GoalPayload
		if err := json.Unmarshal([]byte(ev.Payload), &p); err != nil || p.Action == "" {
			return s.Faint.Render(ev.Kind)
		}
		return eventMarker(s, ev.Status) + s.Subtle.Render(ansi.Truncate(threadfold.GoalSentence(p), max(w-2, 8), "…"))
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
	case threadfold.StatusWatching:
		return s.Info.Render("◎ ")
	default:
		return s.Faint.Render("· ")
	}
}

// segItem is one thing drawn under a folded stage's receipt, tagged with
// the event index that produced it so the whole group can be put back
// into log order. A period's opening rule carries the period itself
// rather than pre-rendered lines, because drawing it is also what sets
// the unread anchor and that has to happen at the position it lands in.
type segItem struct {
	at     int
	lines  []string
	open   threadfold.Stretch
	isOpen bool
}

// sessionlessReceiptLine draws one of those rows the way a folded session
// receipt is drawn, named by what it is rather than by its role: "checks"
// for the scribe's discovery and baseline, "backend" for a backend's own
// side model. The role alone would tell a reader nothing — nobody asked
// for a scribe.
func sessionlessReceiptLine(s *theme.Styles, row state.StageSpend, w int) string {
	what := row.Role
	switch row.Role {
	case string(agent.RoleScribe):
		what = "checks · discovery and baseline"
	case string(agent.RoleHelper):
		what = "backend's own side model"
	}
	head := string(row.Stage) + " · " + what +
		fmt.Sprintf(" · %g credits", roundSpend(row.Credits))
	fill := max(w-ansi.StringWidth(head)-2, 1)
	return s.Faint.Render(head+" ") + s.Separator.Render(strings.Repeat("─", fill))
}
