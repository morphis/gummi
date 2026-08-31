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
// the streaming activity line while one is running), the decision-receipt
// slot, a pinned open decision when the card needs one, and the input.
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
func cardPageChrome(h int) (crumb, blank int) {
	if h >= cardCrumbRows {
		crumb = 1
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

	// --- head: pinned to the top, most important row first ---
	// The order is the order it yields in: composeThread trims the head to
	// a prefix, so whatever must survive a short terminal has to come
	// first. That is the card's own identity — being told which card you
	// are deciding about is worth more than the strip, the spec line or
	// the spacing around them.
	var head []string
	for _, l := range threadHeader(s, m, r) {
		head = append(head, clip(l))
	}
	head = append(head, "")
	if sl := pinnedSpecLine(s, r, inner); sl != "" {
		head = append(head, clip(sl), "")
	}
	if h >= composerBlankRows && len(head) > 0 {
		// the leading blank separates the masthead from the page's crumb
		// above it. It is the head's own decoration and yields on the same
		// terms the composer's does: on a short page the row buys an
		// option instead.
		head = append([]string{""}, head...)
	}

	// --- body: the conversation, scrollable ---
	var body []string
	add := func(str string) { body = append(body, clip(str)) }
	blank := func() { body = append(body, "") }

	segs := stageSegments(r.Events)
	if len(segs) > 1 {
		spend := stageSpendByStage(r.StageSpend)
		for _, seg := range segs[:len(segs)-1] {
			add(foldedReceiptLine(s, seg, spend, inner))
			// Expansion is real: a receipt unfolds either through
			// Shell.expandedStages or, wholesale, through the transcript
			// view (t) — every stage's events laid out in the body.
			if m.expandedStages[stageSegmentKey(f.ID, seg)] || m.threadTranscript {
				for _, ev := range seg.events {
					for _, l := range stageEventLines(s, ev, inner, m.threadOutputs) {
						add("    " + l)
					}
				}
			}
		}
		blank()
	}

	if ls := m.liveStageBlock(s, r, segs, inner); len(ls) > 0 {
		for _, l := range ls {
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

	// The decision receipt — what a run decided while nobody watched —
	// belongs here, below the live stage, because
	// that is when it happened.
	corrective := m.round(f.ID, domain.RoundKindCorrective)
	receipt := buildDecisionReceipt(r.Events, stageSpendByStage(r.StageSpend),
		f.Budget.Envelope, corrective, verdict.MaxRounds(domain.RoundKindCorrective))
	if rl := decisionReceiptBlock(s, receipt); len(rl) > 0 {
		for _, l := range rl {
			add(l)
		}
		blank()
	}
	body = trimTrailingBlanks(body)

	// The page's regions are separated by a blank row apiece, so the
	// conversation, the decision it ends in and the line you type on read
	// as three things rather than one wall of text running into the
	// chrome. They are decorations on the same terms as the row beneath
	// the composer (cardPageChrome): on a short page the rows buy an
	// option instead, which is why the 36×9 frame has none of them.
	sep := 0
	if h >= composerBlankRows {
		sep = 1
	}

	// --- foot: pinned to the bottom ---
	foot := make([]string, sep)
	// the input is a multi-row widget: clip each row, or a stray tail of
	// the second one lands on the first.
	for _, l := range strings.Split(m.inputBlock(s, r, inner), "\n") {
		foot = append(foot, clip(l))
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
	decision := m.openDecisionBlock(s, r, inner, budget)
	for i := range decision {
		decision[i] = clip(decision[i])
	}
	if len(decision) > 0 && sep > 0 {
		decision = append(make([]string, sep), decision...)
	}

	// the measure wants every row there is, so it composes at zero — the
	// unwindowed branch — having laid the regions out at the real height.
	composeH := h
	if measure {
		composeH = 0
	}
	return strings.Join(composeThread(head, body, decision, foot, composeH, m.threadScroll), "\n")
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
func composeThread(head, body, decision, foot []string, h, up int) []string {
	if h <= 0 {
		out := append(append(append([]string{}, head...), body...), decision...)
		return append(out, foot...)
	}
	if len(foot) >= h {
		return foot[len(foot)-h:]
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
		head = head[:remaining]
		body = nil
		remaining = 0
	} else {
		remaining -= len(head)
	}

	window := body
	if len(body) > remaining {
		up = min(max(up, 0), len(body)-remaining)
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
	return max(full-h, 0)
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
func threadHeader(s *theme.Styles, m *Shell, r featureRow) []string {
	f := r.F
	head := s.Title.Render(string(f.ID)) + " " + s.Base.Render("· "+f.Title)
	if f.Profile != "" {
		head += headerGap + s.ProfileTag.Render("["+f.Profile+"]")
	}
	head += headerGap + s.Faint.Render("autopilot: "+autopilotLabel(f.GateApproval))
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
	return []string{head, stageStrip(s, f)}
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
func stageStrip(s *theme.Styles, f domain.Feature) string {
	seq := stageSequence(f)
	parts := make([]string, len(seq))
	for i, st := range seq {
		if st == f.Stage {
			parts[i] = s.StagePill(st).Render(string(st))
		} else {
			parts[i] = s.Faint.Render(string(st))
		}
	}
	return strings.Join(parts, s.Faint.Render(stageJoin))
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

// stageSegmentKey identifies one stage segment for Shell.expandedStages
// lookups, stable across renders of the same segment — its enter time
// disambiguates two generations of the same stage (a bounce back and a
// second run of it).
func stageSegmentKey(id domain.FeatureID, seg stageSegment) string {
	return string(id) + "|" + string(seg.stage) + "|" + seg.enterAt.Format(time.RFC3339Nano)
}

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
	for _, ev := range events {
		switch ev.Kind {
		case state.EventStageEnter:
			var p stageEnterPayload
			_ = json.Unmarshal([]byte(ev.Payload), &p)
			segs = append(segs, stageSegment{stage: ev.Stage, role: p.Role, model: p.Model, enterAt: ev.At})
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
		}
	}
	return segs
}

// foldedReceiptLine renders one finished stage session as the single
// line folding really means: stage, role, turn count, spend, and the
// outcome marker with the time it closed.
func foldedReceiptLine(s *theme.Styles, seg stageSegment, spend map[domain.Stage]float64, w int) string {
	turns := 0
	for _, ev := range seg.events {
		if ev.Kind == state.EventMessage {
			turns++
		}
	}
	head := "⌄ " + string(seg.stage)
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
	// Spend is metered into stage_spend already; reading it back from the
	// event payload would be a second source of truth for the same number,
	// free to drift from the first. The payload is only the fallback for a
	// stage whose rollup has not been loaded.
	credits := spend[seg.stage]
	if credits == 0 {
		credits = seg.credits
	}
	if credits > 0 {
		head += fmt.Sprintf(" · %g credits", roundSpend(credits))
	}
	mark := eventMarker(s, "")
	if seg.exited {
		if seg.verdict == state.StatusFail {
			mark = eventMarker(s, state.StatusFail)
		} else {
			mark = eventMarker(s, state.StatusOK)
		}
	}
	ts := ""
	if !seg.exitAt.IsZero() {
		ts = seg.exitAt.Format("15:04")
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
func (m *Shell) liveStageBlock(s *theme.Styles, r featureRow, segs []stageSegment, w int) []string {
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
		lines := []string{boundaryRule(s, string(f.Stage), string(snap.Role), runModel(snap), at, w), ""}
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
		return lines
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
		lines := []string{boundaryRule(s, string(stage), string(snap.Role), runModel(snap), at, w), ""}
		lines = append(lines, transcriptLines(s, snap, w, m.threadOutputs)...)
		if meta := sessionMeta(snap); meta != "" {
			lines = append(lines, "  "+s.Faint.Render(meta))
		}
		lines = append(lines, "  "+s.Warning.Render(m.follow.marker())+
			s.Faint.Render(" — "+m.follow.footer(snap)))
		return lines
	}

	if len(segs) == 0 {
		return nil
	}
	last := segs[len(segs)-1]
	lines := []string{boundaryRule(s, string(last.stage), last.role, last.model, last.enterAt, w), ""}
	shown := last.events
	// the transcript view (t) reads a finished stage in full; otherwise
	// the block keeps its recent-events summary and the receipts above
	// stay folded.
	if !m.threadTranscript && len(shown) > 6 {
		shown = shown[len(shown)-6:]
	}
	for _, ev := range shown {
		for _, l := range stageEventLines(s, ev, w, m.threadOutputs) {
			lines = append(lines, "  "+l)
		}
	}
	if r.DrivenAbroad {
		lines = append(lines, "  "+s.Faint.Render("driven elsewhere — "+foreignSummary(r.Foreign)))
	}
	return lines
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
func stageEventLines(s *theme.Styles, ev state.CardEvent, w int, showOutput bool) []string {
	lines := []string{stageEventLine(s, ev, w)}
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
func stageEventLine(s *theme.Styles, ev state.CardEvent, w int) string {
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
		return s.Faint.Render(messageAuthorLabel(p.Author)+" ") +
			body.Render(ansi.Truncate(sanitize(p.Content), max(w-6, 8), "…"))
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
func messageAuthorLabel(author string) string {
	switch author {
	case string(engine.AuthorUser):
		return "you"
	case string(engine.AuthorSystem):
		return "gummi"
	default:
		if author == "" {
			return "agent"
		}
		return author
	}
}
