package ui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui/theme"
	"github.com/morphis/gummi/internal/workflow"
)

// The card thread: a single scrollable surface a card's page opens onto.
// Top to bottom it renders identity + the stage strip, the pinned spec
// line, one folded line per finished stage session, the live stage (a
// session boundary naming the fresh context it started, its events, and
// the streaming activity line while one is running), the decision-receipt
// slot, the "next" card verbatim from nextsteps.go, and the input.
//
// The thread never blocks a frame on IO: its per-stage history comes
// from featureRow.Events, populated lazily and only for the selected
// card (msgs.go, shell.go's loadCardEvents). With nothing loaded yet it
// simply omits the folded receipts and falls back to whatever a live
// engine session already holds in memory.

// threadView renders the selected card's thread into the card page.
func (m *Shell) threadView(w, h int) string {
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

	var lines []string
	line := func(str string) { lines = append(lines, ansi.Truncate(str, w, "…")) }
	blank := func() { lines = append(lines, "") }

	line("")
	for _, l := range threadHeader(s, m, r) {
		line(l)
	}
	blank()

	if sl := pinnedSpecLine(s, r); sl != "" {
		line(sl)
		blank()
	}

	segs := stageSegments(r.Events)
	if len(segs) > 1 {
		spend := stageSpendByStage(r.StageSpend)
		for _, seg := range segs[:len(segs)-1] {
			line(foldedReceiptLine(s, seg, spend, w))
			// Expansion is real and driven by Shell.expandedStages; no
			// key sets that flag yet, so every receipt renders folded —
			// binding ⌄ to it is all that remains.
			if m.expandedStages[stageSegmentKey(f.ID, seg)] {
				for _, ev := range seg.events {
					line("    " + stageEventLine(s, ev, w))
				}
			}
		}
		blank()
	}

	if ls := m.liveStageBlock(s, r, segs, w); len(ls) > 0 {
		for _, l := range ls {
			line(l)
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
					line(l)
				}
			}
			blank()
		}
	}

	// The decision receipt — what a run decided while nobody watched —
	// belongs here, below the live stage and above the next card, because
	// that is when it happened. Nothing renders it yet.

	if nl := nextCardBlock(s, m.nextInputFor(r)); len(nl) > 0 {
		for _, l := range nl {
			line(l)
		}
		blank()
	}

	// the input is a multi-row widget: truncate each row, or a stray tail
	// of the second one lands on the first.
	for _, l := range strings.Split(inputBlock(s, r, w), "\n") {
		line(l)
	}

	if h > 0 && len(lines) > h {
		lines = lines[:h]
	}
	return strings.Join(lines, "\n")
}

// threadHeader is the thread's two-line masthead: identity, profile,
// autopilot mode, spend and the active loop's round badge, then the
// stage strip.
func threadHeader(s *theme.Styles, m *Shell, r featureRow) []string {
	f := r.F
	head := s.Title.Render(string(f.ID)) + " " + s.Base.Render("· "+f.Title)
	if f.Profile != "" {
		head += "  " + s.ProfileTag.Render("["+f.Profile+"]")
	}
	head += "  " + s.Faint.Render("autopilot: "+autopilotLabel(f.GateApproval))
	if f.Budget.Envelope > 0 {
		head += "  " + s.Faint.Render(budgetSummary(f))
	} else if !f.Spend.Zero() {
		head += "  " + s.Faint.Render(featureSpend(f.Spend))
	}
	if sk := skipSummary(f); sk != "" {
		head += "  " + s.Faint.Render("skips "+sk)
	}
	if rl := roundLabel(m, f); rl != "" {
		head += "  " + s.Faint.Render(rl)
	}
	return []string{head, stageStrip(s, f)}
}

// autopilotLabel names the card's gate-approval mode as stored
// (domain.GateAuto/GateCaller; empty reads as auto). It shows exactly
// what the card carries rather than inventing wording the stored value
// does not have.
func autopilotLabel(mode string) string {
	if mode == "" {
		return domain.GateAuto
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
	return strings.Join(parts, s.Faint.Render("─"))
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
func pinnedSpecLine(s *theme.Styles, r featureRow) string {
	f := r.F
	section := currentSpecSection(f.Kind, f.Stage)
	if section == "" {
		return ""
	}
	line := s.Faint.Render("⌄ ") + s.Muted.Render(artifactNoun(f.Kind)) + s.Faint.Render(" · "+section)
	if r.OpenSpecQs > 0 {
		line += "  " + s.Warning.Render(itoa(r.OpenSpecQs)+" open %%")
	}
	line += "  " + s.KeyHint.Render("s")
	return line
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
	head += " · " + itoa(turns) + " turns"
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
// stage's events, then, while an agent is mid-turn, the streaming
// activity line. It prefers a live engine.Session's Snapshot (freshest,
// and the only place an open ask_user question lives); with none
// attached to this board it falls back to the last reconstructed segment
// from the event log, so a card between runs still shows what its last
// session did. A card another process drives has no local session here
// either way — its live stream is watched, not rendered inline (chat.go's
// followSession, via enter).
func (m *Shell) liveStageBlock(s *theme.Styles, r featureRow, segs []stageSegment, w int) []string {
	f := r.F
	if sess := m.sessionFor(f.ID); sess != nil && !r.DrivenAbroad {
		snap := sess.Snapshot()
		at := time.Time{}
		if len(segs) > 0 {
			at = segs[len(segs)-1].enterAt
		}
		lines := []string{boundaryRule(s, string(f.Stage), string(snap.Role), runModel(snap), at, w)}
		if ask := snap.PendingAsk; ask != nil {
			// reuse chat.go's own picker renderer rather than
			// reimplementing it; a zero-value pane is enough since
			// pickerView only reads c.feature/c.cursor/c.picked, and this
			// step renders it, it does not yet wire its keys.
			pane := &chatPane{feature: f.ID}
			lines = append(lines, strings.Split(pane.pickerView(s, ask, w), "\n")...)
			return lines
		}
		for _, a := range recentTools(snap, 6) {
			lines = append(lines, "  "+toolMarker(s, a.ToolStatus)+toolLineView(s, sanitize(a.Content), max(w-6, 8)))
		}
		if last := lastAssistant(snap); last != "" {
			for _, l := range strings.Split(wrapText(sanitize(last), max(w-4, 8)), "\n") {
				lines = append(lines, "  "+s.Faint.Render(l))
			}
		}
		if meta := sessionMeta(snap); meta != "" {
			lines = append(lines, "  "+s.Faint.Render(meta))
		}
		switch {
		case snap.Busy:
			lines = append(lines, "  "+s.Info.Render(m.spinner()+" "+m.runningLabel(snap)))
		case len(lines) == 1:
			lines = append(lines, "  "+s.Faint.Render("starting…"))
		}
		return lines
	}

	if len(segs) == 0 {
		return nil
	}
	last := segs[len(segs)-1]
	lines := []string{boundaryRule(s, string(last.stage), last.role, last.model, last.enterAt, w)}
	shown := last.events
	if len(shown) > 6 {
		shown = shown[len(shown)-6:]
	}
	for _, ev := range shown {
		lines = append(lines, "  "+stageEventLine(s, ev, w))
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

// stageEventLine renders one logged card event as a single line, the
// event-log counterpart to a live session's tool ticker (chat.go's
// transcript / dashboard.go's recentTools).
func stageEventLine(s *theme.Styles, ev state.CardEvent, w int) string {
	switch ev.Kind {
	case state.EventTool:
		var p toolPayload
		_ = json.Unmarshal([]byte(ev.Payload), &p)
		return eventMarker(s, ev.Status) + toolLineView(s, sanitize(p.Label), max(w-6, 8))
	case state.EventMessage:
		var p messagePayload
		_ = json.Unmarshal([]byte(ev.Payload), &p)
		return s.Faint.Render(ansi.Truncate(sanitize(p.Author+": "+p.Content), max(w-4, 8), "…"))
	default:
		return s.Faint.Render(ev.Kind)
	}
}

// eventMarker is toolMarker's (chat.go) counterpart for a logged event's
// stored status string rather than a live engine.ToolStatus.
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

// nextCardBlock renders nextsteps.go's nextActions verbatim — the gate
// card any open gate lives on. nextsteps.go stays untouched: this only
// turns its []nextAction into thread lines, the same "key label — why"
// shape inboxView already gives its top suggestion (inboxview.go).
func nextCardBlock(s *theme.Styles, in nextInput) []string {
	acts := nextActions(in)
	if len(acts) == 0 {
		return nil
	}
	lines := []string{s.Subtitle.Render("next")}
	for _, a := range acts {
		lines = append(lines, "  "+s.KeyHint.Render(a.key)+" "+s.Subtle.Render(a.label)+s.Faint.Render(" — "+sanitize(a.why)))
	}
	return lines
}

// inputBlock is the thread's bottom input slot. A card owned by another
// process withholds it — featureRow.DrivenAbroad/.Foreign, the same
// signal newFollowPane (chat.go) withholds send/answer on — rather than
// rendering a box that would fail at send time. Otherwise it reuses
// chat.go's own textarea constructor for visual consistency with the
// chat pane. It renders only: the card page's bindings are unchanged, so
// nothing focuses this slot or feeds it keys.
func inputBlock(s *theme.Styles, r featureRow, w int) string {
	if r.DrivenAbroad {
		return s.Faint.Render(ansi.Truncate("read-only — driven by "+foreignSummary(r.Foreign), w, "…"))
	}
	in := newChatInput()
	in.SetWidth(max(w-2, 10))
	// The chat pane gives the composer three rows because it is the whole
	// surface there. Here it sits under the thread, which owns the height,
	// so it takes one and grows only when someone is actually typing.
	in.SetHeight(1)
	return in.View()
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
