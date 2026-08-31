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
	"github.com/morphis/gummi/internal/verdict"
)

// The `A` overlay: point autopilot at a card and the card runs — not at
// the next gate, now, from wherever it currently sits. It replaces the
// old two-state gate-approval toggle (boardactions.go's former
// toggleGateApproval/confirmGateAuto) with the three stored stops
// (domain.GateOff/GateGates/GateFull) and, unlike that toggle, actually
// moves the card rather than only recording a preference for next time.
//
// Both entry points — the `A` key (shell.go's boardVerb) and the "gate"
// card action it supersedes (boardactions.go's runCardAction) — go
// through openAutopilot, so the plan they show can never drift apart.

// autopilotStop is one of the three gate-approval modes the overlay
// offers, in the order they are shown.
type autopilotStop struct {
	mode  string
	label string
	why   string
}

var autopilotStops = []autopilotStop{
	{domain.GateOff, "off", "every gate stops for you"},
	{domain.GateGates, "gates", "design gates cross themselves; questions still stop for you"},
	{domain.GateFull, "full", "it runs to a verified branch on its own"},
}

// autopilotCursorFor finds the stop index matching a card's stored mode,
// reading empty as domain.GateGates like everywhere else the field is
// interpreted (domain.Feature.GateApproval's own doc comment).
func autopilotCursorFor(mode string) int {
	if mode == "" {
		mode = domain.GateGates
	}
	for i, st := range autopilotStops {
		if st.mode == mode {
			return i
		}
	}
	return autopilotCursorFor(domain.GateGates)
}

// autopilotPlan is the concrete, card-specific effect of turning
// autopilot on right now, resolved from f's LIVE state at the moment the
// overlay opens — never from the mode a user later picks in it, which
// only changes how a run behaves, not whether one starts.
type autopilotPlan struct {
	bucket    string         // "todo" | "gate" | "running" — drives the confirm label and the body
	to        domain.Stage   // the stage a non-off mode enters now; "" when bucket == "running"
	remaining []domain.Stage // to and everything after it, short of done
}

// confirmLabel words the confirm button to what pressing it actually
// does to this card, per state (the run/pause card actions already use
// this "name what pressing it does" convention — cardactions.go's
// runLabelWhy/pauseLabelWhy).
func (p autopilotPlan) confirmLabel() string {
	switch p.bucket {
	case "todo":
		return "Start on autopilot"
	case "gate":
		return "Cross the gate and continue"
	default:
		return "Set"
	}
}

// autopilotForward names the single stage a parked gate would move into
// were it crossed, and reports whether that edge is one autopilot may
// take on its own: a critiqued plan into implement, a diagnosed bug into
// fix, a finished implement/fix's first completion into review, a
// finished investigate into shape.
//
// Review and Verify are deliberately excluded even though a parked gate
// can sit at either: a Review gate only ever reaches the inbox by
// escalating (a clean pass already auto-continues on its own today,
// gate mode or not — reviewloop.go's onReviewDone), so crossing it here
// would just be re-running a loop that already gave up. Verify's gate is
// the landing decision — autopilot's own guarantee that it never lands
// on main means that call always stays the human's, parked or not.
func autopilotForward(f domain.Feature) (domain.Stage, bool) {
	switch f.Stage {
	case domain.StagePlan:
		return domain.StageImplement, true
	case domain.StageDiagnose:
		return domain.StageFix, true
	case domain.StageImplement, domain.StageFix:
		return domain.StageReview, true
	case domain.StageInvestigate:
		return domain.StageShape, true
	default:
		return "", false
	}
}

// autopilotHandoverEdge is the edge the `A` switch may cross when a
// person picks it, and it is wider than autopilotForward because the
// warrant is different.
//
// autopilotForward answers "may an unattended loop walk past this on its
// own, because a turn ended". At a design stage the answer is no: an
// architect falling silent is not a finished spec, and only you can say
// it is. Choosing the handover IS that judgment — approving and
// delegating in one act, with the dialog's own confirm as the deliberate
// gesture, which is what §10.17 means by autopilot crossing its own
// design gates.
//
// The card's own sequence supplies the edge rather than a hardcoded
// table, so a quick-route spec leads to implement and a skip-flagged
// card is never walked into a stage it does not have. Verify still
// refuses — landing on main stays a keypress under every mode — and so
// does review, where the loop has already given up and the choice
// between bouncing and overruling is the one thing left that is yours.
func autopilotHandoverEdge(f domain.Feature) (domain.Stage, bool) {
	switch f.Stage {
	case domain.StageReview, domain.StageVerify, domain.StageDone, domain.StageTodo:
		return "", false
	}
	seq := stageSequence(f)
	for i, st := range seq {
		if st != f.Stage {
			continue
		}
		if i+1 >= len(seq) || seq[i+1] == domain.StageDone {
			return "", false
		}
		return seq[i+1], true
	}
	return "", false
}

// remainingStages is stageSequence's (thread.go) ordered stage list for
// f's own workflow, truncated to start at `from` (inclusive) and to
// exclude domain.StageDone — the one stop a non-off mode never carries a
// card into by itself.
func remainingStages(f domain.Feature, from domain.Stage) []domain.Stage {
	seq := stageSequence(f)
	idx := -1
	for i, st := range seq {
		if st == from {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil
	}
	out := append([]domain.Stage(nil), seq[idx:]...)
	if n := len(out); n > 0 && out[n-1] == domain.StageDone {
		out = out[:n-1]
	}
	return out
}

// planAutopilot resolves f's autopilotPlan: a todo card's first real
// stage, a parked gate's forward edge (only when autopilotForward allows
// one), or — for everything else, including a card already running and a
// parked gate autopilot never crosses on its own — the "running" bucket,
// where the switch only ever writes the mode.
func (m *Shell) planAutopilot(f domain.Feature) autopilotPlan {
	if f.Stage == domain.StageTodo {
		// workflow.Initial names the stage every item is *created* in
		// (domain.StageTodo itself), not the one to run — that is the
		// next stop on f's own sequence (thread.go's stageSequence),
		// which already resolves brainstorm vs. spec vs. plan for a
		// skip-flagged card the same way advanceStage does.
		if seq := stageSequence(f); len(seq) > 1 {
			to := seq[1]
			return autopilotPlan{bucket: "todo", to: to, remaining: remainingStages(f, to)}
		}
		return autopilotPlan{bucket: "todo"}
	}
	// A card sitting at a gate is one with a forward edge and nothing
	// working on it — which is exactly the state the thread renders a
	// decision in. It used to be read off an inbox item instead, and a
	// design stage whose architect had simply stopped talking has no
	// inbox item: the decision there comes from the card's own state, not
	// the queue. So the switch read "already underway", wrote the mode
	// and moved nothing, directly under a row promising that gates cross
	// themselves from here.
	if to, ok := autopilotHandoverEdge(f); ok && m.atGate(f.ID) {
		return autopilotPlan{bucket: "gate", to: to, remaining: remainingStages(f, to)}
	}
	return autopilotPlan{bucket: "running"}
}

// atGate reports whether the card is sitting at a decision a person would
// cross right now: its stage has run and has stopped.
//
// A parked inbox gate is the obvious case and the only one this used to
// read. It misses the one the thread renders most: a design stage whose
// architect has simply stopped talking leaves no inbox item at all,
// because its decision comes from the card's own state rather than the
// queue — so the switch called that card "already underway", wrote the
// mode and moved nothing, under a row promising the opposite.
//
// A stage that has never started is deliberately not a gate. Nothing has
// been done there yet, so handing over means running it, not walking
// past it — crossing here would carry the card into review over an
// implement stage that never ran.
func (m *Shell) atGate(id domain.FeatureID) bool {
	if it, ok := m.inbox.get(id); ok && it.Kind == attnGate {
		return true
	}
	s := m.sessionFor(id)
	if s == nil {
		return false
	}
	if st := s.State(); st == engine.StateRunning || st == engine.StateQueued {
		return false
	}
	if s.Snapshot().Busy {
		return false // mid-turn: something is already going
	}
	// a finished autonomous run, or an interactive stage whose agent has
	// stopped: either way the work happened and the next move is a
	// person's. A paused one is neither — it is unfinished.
	return s.State() == engine.StateDone || s.Interactive
}

// englishList joins stage names as "brainstorm, spec, plan, implement,
// review and verify" — comma-separated with a bare "and" before the
// last, no serial comma.
func englishList(stages []domain.Stage) string {
	if len(stages) == 0 {
		return ""
	}
	names := make([]string, len(stages))
	for i, st := range stages {
		names[i] = string(st)
	}
	if len(names) == 1 {
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// autopilotHeader is the rule line naming the card's current situation —
// the thing "cross the gate" or "start on autopilot" is relative to.
func autopilotHeader(f domain.Feature, plan autopilotPlan) string {
	switch plan.bucket {
	case "todo":
		return "this card is in todo"
	case "gate":
		return "this card is parked at " + string(f.Stage)
	default:
		return string(f.ID) + " is already underway"
	}
}

// autopilotBody names the concrete consequence of setting mode on f
// right now — never the mode's abstract definition, which the stop list
// above it already gives. off never starts anything, so it gets no
// stage list, no budget, and no envelope: there is nothing concrete to
// name. Every other mode says which stages run next, the shared
// corrective budget (verdict.MaxRounds, never hardcoded), the card's own
// envelope when it has one, and — unconditionally, because these are the
// two guarantees that make the switch safe to use at all — that it parks
// to the inbox if it can't finish and that it never lands on main.
func autopilotBody(f domain.Feature, plan autopilotPlan, mode string) []string {
	if mode == domain.GateOff {
		return []string{"off never starts anything on its own — every gate, including this one, waits for you."}
	}

	verb := "starting"
	if plan.bucket == "gate" {
		verb = "crossing"
	}
	if plan.bucket != "todo" && plan.bucket != "gate" {
		return []string{
			fmt.Sprintf("setting %s doesn't change what %s is doing right now — only how its next gate is handled.", mode, f.ID),
			"it parks to the inbox if it can't finish, and it never lands on main.",
		}
	}

	// The two modes promise different things, so they must not share a
	// sentence. full is the only one that runs a card unattended end to
	// end, and the only one the corrective budget applies to; gates still
	// stops the moment the agent needs an answer, and saying otherwise
	// would be the one lie this dialog cannot afford — it is what someone
	// reads before leaving the room.
	envelope := ""
	if f.Budget.Envelope > 0 {
		envelope = fmt.Sprintf(", inside a %d credit envelope", f.Budget.Envelope)
	}
	var lead string
	if mode == domain.GateFull {
		lead = fmt.Sprintf("%s it on full runs %s without you — up to %d corrections%s.",
			verb, englishList(plan.remaining), verdict.MaxRounds(domain.RoundKindCorrective), envelope)
	} else {
		lead = fmt.Sprintf("%s it on gates runs %s, crossing each design gate for you%s — but it still stops whenever the agent needs an answer.",
			verb, englishList(plan.remaining), envelope)
	}
	return []string{lead, "it parks to the inbox if it can't finish, and it never lands on main."}
}

// autopilotAnswers reports whether mode answers a decision of kind on
// its own — §10.17's rule table ("Autopilot may redo its own work. It
// may never widen its own reach.") turned into code, so nothing else in
// the TUI is free to re-derive (or drift from) this table.
//
//   - decisionBudget is refused under every mode, full included. It is
//     the one decision the design names explicitly as autopilot's own
//     refusal: topping up an exhausted envelope enlarges what the card
//     may spend, which is the definition of widening its reach rather
//     than redoing its work, and `u` (the top-up key) never silently
//     restarts a run on its own.
//   - domain.GateOff never answers anything, by the stop's own words
//     (autopilotStops above: "every gate stops for you").
//   - domain.GateFull answers every kind but budget — "it runs to a
//     verified branch on its own" is the promise, and a card that must
//     stop for its own design gates or its own questions is not that.
//     Reporting true here for decisionVerify is the rule table's own
//     word for what full may do (bounce a failed verify); it is not a
//     license to flip gatepolicy.Input.VerifyMayBounce on, which stays
//     false everywhere in this change — that switch is a behavior change
//     of its own the design reserves for later, not a side effect of
//     this table.
//   - domain.GateGates, and the empty default that reads as it
//     (autopilotCursorFor's own rule), answers only decisionGate and
//     decisionIdle. The stop's own words are "design gates cross
//     themselves; questions still stop for you" — decisionAsk is
//     refused for exactly that reason, and decisionVerify with it: a
//     failed verify is a question about what to do next (bounce, or
//     hand it to a human), not a design gate crossing itself.
func autopilotAnswers(mode string, kind decisionKind) bool {
	if kind == decisionBudget {
		return false
	}
	switch mode {
	case domain.GateFull:
		return true
	case domain.GateGates, "":
		return kind == decisionGate || kind == decisionIdle
	default: // domain.GateOff and anything unrecognized: off's own guarantee
		return false
	}
}

// autopilotCrossGate is the two live raise sites' (shell.go's
// EventIdle, reviewloop.go's onPlanDone) shared attempt to cross a
// parked design gate as autopilot (DESIGN §10.17) instead of parking it:
// a mode that answers gate decisions on its own (autopilotAnswers) and
// an edge autopilot may take on its own (autopilotForward). Neither
// check writes anything, so a card that fails either falls straight back
// to the caller's own raiseAttention — ok reports which.
//
// When both hold, the decision row is opened here, before Advance runs,
// through the same m.logDecision seam raiseAttention itself uses: the
// stop still leaves a row per §10.18 even though it is about to close.
// Store.Transition correlates the crossing's gate event to the newest
// open gate decision for the card inside its own transaction, so a
// successful crossing closes the very row this call just opened. If
// Advance instead reports the gate is blocked (an open %%/diff thread, an
// unmet dependency), advanceStageAs's actor-aware mapping returns
// autopilotGateBlockedMsg rather than a plain error notice; shell.go's
// Update handles that by parking the card through parkAttentionItem, not
// raiseAttention, so the decision opened here isn't logged a second time
// for the one stop.
//
// Crossing always runs through advanceStageAs — never autoStep or
// m.store.Transition directly — so the same blocker checks a human's own
// g would hit apply here too: autopilot may never cross a gate a human
// could not.
func (m *Shell) autopilotCrossGate(f domain.Feature, text string) (tea.Cmd, bool) {
	// autopilotModeFor (shell.go, beside stageOf), not f.GateApproval:
	// f comes from the just-finished session's own snapshot, which can be
	// a stage or more stale than the board's row — a mode set through the
	// `A` overlay while that session was already running would not be
	// reflected on it until the next stage's session is created. The row
	// is the same source every other live mode read in this package
	// treats as authoritative.
	if !autopilotAnswers(m.autopilotModeFor(f.ID), decisionGate) {
		return nil, false
	}
	if _, ok := autopilotForward(f); !ok {
		return nil, false
	}
	m.logDecision(f.ID, state.DecisionKindGate, text)
	// the crossing runs in a command, so the gate is open and already
	// spoken for until it lands — the pinned decision says so rather than
	// letting it read as waiting for you (decision.go).
	m.markAutopilotAnswering(f.ID)
	return autopilotSettled(f.ID, m.advanceStageAs(f.ID, state.ActorAutopilot)), true
}

// autopilotSettledMsg wraps whatever an autopilot-dispatched command
// returned, so the answering mark is dropped before the message is
// handled.
type autopilotSettledMsg struct {
	id    domain.FeatureID
	inner tea.Msg
}

// autopilotSettled wraps a command autopilot dispatched so the answering
// mark comes off however that command ends — including through exits
// added later. Enumerating the outcomes instead was wrong the first time
// it was tried: a crossing onto an interactive stage returns a plain
// notice, which cleared nothing, and the card then advertised "autopilot
// is taking this one" over a decision autopilot had already finished
// with, for the rest of the session.
func autopilotSettled(id domain.FeatureID, cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return nil
	}
	return func() tea.Msg { return autopilotSettledMsg{id: id, inner: cmd()} }
}

// autopilotRun starts the stage autopilot's own crossing just opened —
// the idle decision that crossing created, answered by the only answer
// an idle card offers. It re-reads the card rather than trusting the
// stage the crossing aimed at: between the transition and this command
// something else (a bounce, another process) may have moved it, and
// starting a stage the card is no longer at would be running something
// nobody decided on.
func (m *Shell) autopilotRun(id domain.FeatureID, to domain.Stage) tea.Cmd {
	return func() tea.Msg {
		if m.engine == nil {
			return noticeMsg{text: "no agent configured (set a model/provider to enable agents)", isErr: true}
		}
		f, err := m.store.GetFeature(context.Background(), id)
		if err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		if f.Stage != to {
			return nil
		}
		if !autopilotAnswers(f.GateApproval, decisionIdle) {
			// the mode was turned off between the crossing and here; the
			// card keeps the stage it gained and stops, which is what off
			// means.
			return nil
		}
		if err := m.engine.Run(f); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		return noticeMsg{text: string(id) + ": autopilot started " + string(to), reload: true}
	}
}

const autopilotDialogWidth = 62

// autopilotDialog is the `A` overlay: the three gate-approval stops,
// cursor pre-set to the card's current mode, and a confirm button worded
// to this card's live state.
type autopilotDialog struct {
	feature  domain.Feature
	plan     autopilotPlan
	cursor   int
	onSubmit func(mode string) tea.Cmd
}

func newAutopilotDialog(f domain.Feature, plan autopilotPlan, onSubmit func(string) tea.Cmd) *autopilotDialog {
	return &autopilotDialog{feature: f, plan: plan, cursor: autopilotCursorFor(f.GateApproval), onSubmit: onSubmit}
}

// openAutopilot pushes the overlay for f, computing its plan once so the
// body it shows and the run it starts on confirm can't disagree. The
// sole entry point for both `A` (shell.go's boardVerb) and the "gate"
// card action it replaces (boardactions.go's runCardAction).
func (m *Shell) openAutopilot(f domain.Feature) tea.Cmd {
	plan := m.planAutopilot(f)
	m.Overlay.Push(newAutopilotDialog(f, plan, func(mode string) tea.Cmd {
		return m.startAutopilot(f, mode, plan)
	}))
	return nil
}

// startAutopilot persists mode on f and, when it names anything other
// than off, actually moves the card the way the overlay promised:
// plan.to — resolved when the overlay opened, from f's live state — is
// where a todo card's initial stage or a parked gate's forward edge
// leads. autoStep (reviewloop.go) runs it when it's autonomous;
// autoStepStage just clears the way to an interactive one, the same
// split reviewloop.go's own continuations use (its onAutonomousDone,
// e.g. the investigate→shape re-entry). A card with nothing safe to
// cross on its own (plan.bucket == "running", including a parked Review/
// Verify gate — see autopilotForward) is left exactly where it is: the
// mode write alone is the whole effect for it.
func (m *Shell) startAutopilot(f domain.Feature, mode string, plan autopilotPlan) tea.Cmd {
	return func() tea.Msg {
		msg := m.setGateApproval(f.ID, mode)()
		if nm, ok := msg.(noticeMsg); ok && nm.isErr {
			return msg
		}
		if mode == domain.GateOff || plan.to == "" {
			// nothing to start: the plain "gate approval now …" notice
			// already says the whole of what changed.
			return msg
		}
		if autonomousStage(plan.to) && m.engine == nil {
			return noticeMsg{text: "no agent configured (set a model/provider to enable agents)", isErr: true}
		}
		if plan.bucket == "gate" {
			// A gate is crossed through the engine's own advance floor, as
			// autopilot's own crossing is, so every blocker a person would
			// hit holds here too — an open %% thread, an unresolved diff
			// comment, an unmet dependency. autoStep below transitions the
			// card directly and would walk straight past all three.
			// advanceStageAs also carries the continuation: crossing onto an
			// autonomous stage starts it, which is what "let autopilot
			// finish" is promising, and a blocked gate parks instead.
			return m.advanceStageAs(f.ID, state.ActorAutopilot)()
		}
		note := "autopilot: entering " + string(plan.to)
		var cmd tea.Cmd
		if autonomousStage(plan.to) {
			cmd = m.autoStep(f.ID, plan.to, note)
		} else {
			cmd = m.autoStepStage(f.ID, plan.to, note)
		}
		return cmd()
	}
}

// ID implements overlay.Dialog.
func (d *autopilotDialog) ID() string { return "autopilot" }

// HandleKey implements overlay.Dialog.
func (d *autopilotDialog) HandleKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	switch key.String() {
	case "esc":
		return true, nil
	case "up", "k":
		d.cursor = max(d.cursor-1, 0)
		return false, nil
	case "down", "j":
		d.cursor = min(d.cursor+1, len(autopilotStops)-1)
		return false, nil
	case "enter":
		return true, d.onSubmit(autopilotStops[d.cursor].mode)
	}
	return false, nil
}

// autopilotStopLines renders one stop row, wrapped and hanging-indented
// under its label when its "why" text doesn't fit width — "gates"'s does
// not, in the overlay's usual size.
func autopilotStopLines(s *theme.Styles, st autopilotStop, selected bool, labelWidth, width int) []string {
	marker := "  "
	style := s.Faint
	if selected {
		marker = "▸ "
		style = s.Selection
	}
	indent := ansi.StringWidth(marker) + labelWidth + 2
	wrapped := strings.Split(wrapText(st.why, max(width-indent, 8)), "\n")
	label := padRight(st.label, labelWidth)
	lines := make([]string, len(wrapped))
	lines[0] = style.Render(marker + label + "  " + wrapped[0])
	for i := 1; i < len(wrapped); i++ {
		lines[i] = style.Render(strings.Repeat(" ", indent) + wrapped[i])
	}
	return lines
}

// dashRule renders "── label ────…" filled to width, the same dash-fill
// shape as thread.go's boundaryRule without the trailing timestamp.
func dashRule(label string, width int) string {
	head := "── " + label + " "
	fill := max(width-ansi.StringWidth(head), 0)
	return head + strings.Repeat("─", fill)
}

// View implements overlay.Dialog.
func (d *autopilotDialog) View(s *theme.Styles, w, h int) string {
	width := min(autopilotDialogWidth, max(w-8, 30))

	var b strings.Builder
	title := "autopilot · " + string(d.feature.ID)
	if d.feature.Title != "" {
		title += " · " + d.feature.Title
	}
	b.WriteString(s.DialogTitle.Render(title) + "\n\n")

	labelWidth := 0
	for _, st := range autopilotStops {
		labelWidth = max(labelWidth, ansi.StringWidth(st.label))
	}
	for i, st := range autopilotStops {
		for _, l := range autopilotStopLines(s, st, i == d.cursor, labelWidth, width) {
			b.WriteString(l + "\n")
		}
	}
	b.WriteString("\n")

	b.WriteString(s.Faint.Render(dashRule(autopilotHeader(d.feature, d.plan), width)) + "\n")
	mode := autopilotStops[d.cursor].mode
	for _, l := range autopilotBody(d.feature, d.plan, mode) {
		for _, wl := range strings.Split(wrapText(l, width), "\n") {
			b.WriteString(s.Subtle.Render(wl) + "\n")
		}
	}
	b.WriteString("\n")

	buttons := newButtonRow(button{label: "Cancel"}, button{label: d.plan.confirmLabel()})
	buttons.SetCursor(1)
	b.WriteString(buttons.View(s, true) + "\n")
	b.WriteString("\n" + s.Faint.Render("↑↓ choose · enter set · esc cancel"))
	return s.DialogFrame.Render(b.String())
}
