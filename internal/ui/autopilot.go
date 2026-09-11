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
	"github.com/morphis/gummi/internal/worktree"
)

// The `A` dialog: point autopilot at a card and the card runs — not at
// the next gate, now, from wherever it currently sits.
//
// It used to be a picker over three stored stops. There are two modes
// now, and a binary does not need a list: the dialog states what handing
// this card over will actually do — resolved from the card's LIVE state,
// not described in the abstract — and its confirm is the whole choice.
// Choosing it IS the judgment that the gate in front of it may be
// crossed, which is why the deliberate gesture stays even though the
// list is gone.
//
// Both entry points — the `A` key (shell.go's boardVerb) and the "gate"
// card action it supersedes (boardactions.go's runCardAction) — go
// through openAutopilot, so the plan they show can never drift apart.

// autopilotModeFor reads a card's stored mode, treating empty as
// attended like everywhere else the field is interpreted
// (domain.Feature.GateApproval's own doc comment).
func autopilotModeFor(mode string) string {
	if mode == domain.GateAutopilot {
		return domain.GateAutopilot
	}
	return domain.GateAttended
}

// autopilotPlan is the concrete, card-specific effect of turning
// autopilot on right now, resolved from f's LIVE state at the moment the
// overlay opens — never from the mode a user later picks in it, which
// only changes how a run behaves, not whether one starts.
type autopilotPlan struct {
	bucket    string         // "todo" | "gate" | "running" — drives the confirm label and the body
	to        domain.Stage   // the stage a non-off mode enters now; "" when bucket == "running"
	remaining []domain.Stage // to and everything after it, short of done
	// working is whether something is driving the card at this moment, as
	// opposed to a session merely existing for it. The "running" bucket
	// is the switch's catch-all — a card actually running, one whose run
	// is paused, one parked at a gate autopilot may not cross — and only
	// the first of those is underway. Saying so of the others put the
	// dialog's own header at odds with the card page underneath it.
	working bool
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
	case domain.StageImplement:
		return domain.StageVerify, true
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
	case domain.StageVerify, domain.StageDone, domain.StageTodo:
		return "", false
	}
	seq := stageSequence()
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

// remainingStages is stageSequence's (thread.go) ordered stage list,
// truncated to start at `from` (inclusive) and to exclude
// domain.StageDone — the one stop a non-off mode never carries a card
// into by itself.
func remainingStages(from domain.Stage) []domain.Stage {
	seq := stageSequence()
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
		if seq := stageSequence(); len(seq) > 1 {
			to := seq[1]
			return autopilotPlan{bucket: "todo", to: to, remaining: remainingStages(to)}
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
		return autopilotPlan{bucket: "gate", to: to, remaining: remainingStages(to)}
	}
	return autopilotPlan{bucket: "running", working: m.sessionWorking(f.ID)}
}

// sessionWorking reports whether something is driving the card at this
// moment. It is the question "is there a session for this card" is
// repeatedly mistaken for, and the two are not the same: the engine
// keeps a session after its run ends, a restart restores one for a card
// that has been waiting for hours, and both answer yes to the lookup
// while nothing at all is happening.
//
// Busy() is asked as well as the scheduling state, matching cardBusy
// (board.go): an interactive session mid-reply is working just as much
// as an autonomous one, and a card's row, its thread and this switch
// must not disagree about whether it is.
func (m *Shell) sessionWorking(id domain.FeatureID) bool {
	s := m.sessionFor(id)
	if s == nil {
		return false
	}
	if st := s.State(); st == engine.StateRunning || st == engine.StateQueued {
		return true
	}
	return s.Snapshot().Busy
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
		if plan.working {
			return string(f.ID) + " is already underway"
		}
		return "nothing is running on " + string(f.ID)
	}
}

// autopilotBody names the concrete consequence of setting mode on f
// right now — never the mode's abstract definition, which the stop list
// above it already gives. off never starts anything, so it gets no
// stage list and no budget: there is nothing concrete to name. Every
// other mode says which stages run next, the shared corrective-round
// budget (verdict.MaxRounds, never hardcoded) and what happens if it
// runs out before the card is done, the card's own spend budget when it
// has one, and — unconditionally, because this is the other guarantee
// that makes the switch safe to use at all — that it never lands on
// base (the branch f actually lands on; see autopilotDialog.baseBranch,
// resolved by openAutopilot — this used to assert the literal "main",
// which was simply wrong on a repo checked out on anything else:
// REVIEW-ux-drive-2026-09-10-round2.md §3.4).
//
// "envelope" used to be this function's word for the card's own spend
// cap ("inside a %d credit envelope"), and "it parks to the inbox" its
// word for what happens when a run can't finish. Round 2's §5 renamed
// both in every dialog a user reads: the cap is "budget" (matching the
// new-card dialog and cardform.go's own note on the word), and the stop
// is "it stops and leaves the card in the inbox" — plain enough that
// nobody has to already know what "the inbox" is a name for. The
// corrective-round sentence below also used to end at the round count
// alone; §5 flagged that a reader cannot guess what happens at the
// limit, so it now says so in the same sentence that states the number.
func autopilotBody(f domain.Feature, plan autopilotPlan, mode string, base string) []string {
	if autopilotModeFor(mode) == domain.GateAttended {
		return []string{"attended never starts anything on its own — every gate, including this one, waits for you."}
	}

	verb := "starting"
	if plan.bucket == "gate" {
		verb = "crossing"
	}
	if plan.bucket != "todo" && plan.bucket != "gate" {
		lead := fmt.Sprintf("setting %s starts nothing — it decides how %s's next gate is handled.", mode, f.ID)
		if plan.working {
			lead = fmt.Sprintf("setting %s doesn't change what %s is doing right now — only how its next gate is handled.", mode, f.ID)
		}
		return []string{lead, "if it can't finish, it stops and leaves the card in the inbox — and it never lands on " + base + "."}
	}

	// This is what someone reads before leaving the room, so it has to be
	// exactly what autopilot will do — not a description of a mode. It
	// names the stages it will run unattended and, separately, the ones it
	// will only open and hand back.
	budget := ""
	if f.Budget.Envelope > 0 {
		budget = fmt.Sprintf(", inside a %d credit budget", f.Budget.Envelope)
	}
	// Every remaining stage is one autopilot may run: no stage needs a
	// person by nature any more, so the list that used to split in two —
	// the stages it runs, and the ones it would only open and hand back —
	// is one list again.
	//
	// mode is always domain.GateAutopilot below: autopilotModeFor already
	// sent anything else through the GateAttended branch above, and the
	// two-mode switch (autopilotAnswers' own doc) leaves nothing else this
	// could be. There used to be a third, "gates" mode with its own
	// sentence here — "%s it on gates runs %s, crossing each design gate
	// for you" — retired along with the mode itself; the wording below
	// names only vocabulary the rest of the UI actually uses: "autopilot"
	// (this dialog's own header, thread.go's autopilotField) and
	// "corrective rounds" (thread.go's "N of M corrective", quitresume.go's
	// "N of M corrections spent") rather than the bare, unitless
	// "corrections" this used to say.
	runs := plan.remaining
	var out []string
	if len(runs) > 0 {
		out = append(out, fmt.Sprintf("%s it on autopilot runs %s without you — up to %d corrective rounds%s. Run out before it passes and it stops and leaves the card in the inbox for you.",
			verb, englishList(runs), verdict.MaxRounds(domain.RoundKindCorrective), budget))
	}
	return append(out, "it never lands on "+base+".")
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
//   - domain.GateAttended — and the empty default that reads as it —
//     never answers anything. Every gate stops; that is what the mode
//     is. The retired middle mode ("gates") DID answer decisionGate and
//     decisionIdle on the card's behalf, and collapsing three modes into
//     two is exactly the decision not to keep doing that: a card the
//     reader has not handed over stops at its gates, full stop.
//   - domain.GateAutopilot answers every kind but budget — "it runs to a
//     verified branch on its own" is the promise, and a card that must
//     stop for its own gates or its own questions is not that. Reporting
//     true here for decisionVerify is the rule table's own word for what
//     autopilot may do (bounce a failed verify); it is not a license to
//     flip gatepolicy.Input.VerifyMayBounce on, which stays false
//     everywhere — that switch is a behavior change of its own the
//     design reserves for later, not a side effect of this table.
func autopilotAnswers(mode string, kind decisionKind) bool {
	if kind == decisionBudget {
		return false
	}
	// autopilot answers everything but budget; attended (and anything
	// unrecognized, and the empty default that reads as attended) answers
	// nothing.
	return mode == domain.GateAutopilot
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
// for the one stop — and by re-wording that row to the blocker Advance
// named, so the refusal never leaves the inviting wording standing as
// the record the card waits on.
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
	feature domain.Feature
	plan    autopilotPlan
	// baseBranch is the branch feature actually lands on (openAutopilot's
	// m.baseBranch(f)) — the body's "it never lands on main" guarantee
	// has to name the checkout's real trunk (REVIEW-ux-drive-2026-09-10-
	// round2.md §3.4), never a hardcoded literal.
	baseBranch string
	// buttons is the dialog's own row, held rather than rebuilt each
	// frame. It used to be constructed inside View with its cursor forced
	// to the confirm every time, so Cancel was drawn as a control you
	// could reach and no key could ever reach it — while enter submitted
	// regardless of which button looked focused.
	buttons  *buttonRow
	onSubmit func(mode string) tea.Cmd
}

// base is baseBranch with the same fallback Shell.baseBranch and
// featureRow.baseBranch carry, for a dialog a test built without going
// through openAutopilot.
func (d *autopilotDialog) base() string {
	if d.baseBranch != "" {
		return d.baseBranch
	}
	return worktree.DefaultBaseBranchName
}

func newAutopilotDialog(f domain.Feature, plan autopilotPlan, base string, onSubmit func(string) tea.Cmd) *autopilotDialog {
	// the confirm leads: opening this switch is already the intent to
	// change something, so the row starts on the doing button and ←→ is
	// the way back out of it.
	buttons := newButtonRow(button{label: "Cancel"}, button{label: plan.confirmLabel()})
	buttons.SetCursor(1)
	return &autopilotDialog{feature: f, plan: plan, baseBranch: base, buttons: buttons, onSubmit: onSubmit}
}

// openAutopilot pushes the overlay for f, computing its plan once so the
// body it shows and the run it starts on confirm can't disagree. The
// sole entry point for both `A` (shell.go's boardVerb) and the "gate"
// card action it replaces (boardactions.go's runCardAction).
func (m *Shell) openAutopilot(f domain.Feature) tea.Cmd {
	plan := m.planAutopilot(f)
	m.Overlay.Push(newAutopilotDialog(f, plan, m.baseBranch(f), func(mode string) tea.Cmd {
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
		// The switch is where a card changes hands in the TUI, so it is
		// where the card's history records that it did — the resume path
		// (quitresume.go) comes through here too, so both handovers a
		// person can make leave the same row.
		//
		// off is the one mode that gives a card back rather than taking
		// it, and it is written unconditionally: whether a period was
		// actually open is the reader's question, not this one's, and a
		// handback with nothing to close is ignored there.
		//
		// A takeover needs something to have actually happened. plan.to
		// names a card the switch moves — a todo card started, a parked
		// gate crossed — and a working session names one already running
		// that autopilot now owns the gates of, which is the shape of
		// setting a running card to full and going to bed. Neither holds
		// for a card merely sitting at a gate autopilot may not cross on
		// its own, and that is right: the mode changed, nothing was handed
		// over, and claiming a period there would put a stretch around a
		// card that sat still.
		//
		// "working" and "has a session" are different questions, and
		// asking the second one here was how a paused card gained a
		// period: nothing ran, so nothing ever closed it, and the thread
		// drew a run that opened and was immediately reported lost.
		switch {
		case mode == domain.GateAttended:
			m.logAutopilot(f.ID, state.AutopilotHandedBack, "you turned autopilot off", mode)
		case plan.to != "" || m.sessionWorking(f.ID):
			m.logAutopilot(f.ID, state.AutopilotTookOver, "you handed it to autopilot", mode)
		}
		if mode == domain.GateAttended || plan.to == "" {
			// nothing to start: the plain "autopilot <stop>" notice
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
			cmd = m.autoStep(f.ID, plan.to, note, state.ActorAutopilot)
		} else {
			cmd = m.autoStepStage(f.ID, plan.to, note, state.ActorAutopilot)
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
		return true, d.cancelNotice()
	case "left", "h", "shift+tab":
		d.buttons.Move(-1)
		return false, nil
	case "right", "l", "tab":
		d.buttons.Move(1)
		return false, nil
	case "enter":
		// enter activates the focused control, with no exceptions
		// (buttonRow's own contract). There is one thing to confirm now
		// that the modes are a binary: hand this card to autopilot.
		if d.buttons.Cursor() == 0 {
			return true, d.cancelNotice()
		}
		return true, d.onSubmit(domain.GateAutopilot)
	}
	return false, nil
}

// cancelNotice is what closing this dialog on Cancel (or esc) leaves
// behind. It used to leave nothing: driven for real, a stray → off the
// confirm button (buttonRow.Move wrapped before it clamped) landed on
// Cancel, and enter there closed the overlay with no notice at all — the
// status bar kept whatever it said before the dialog opened, and the
// card still read "autopilot: off". The miss was found only two screens
// later. A cancel is a real outcome, not the absence of one, so it gets
// the same notice-on-the-way-out every other abandoned dialog in this
// package leaves (bugingestview.go's "left the issue picker — nothing
// created" is the same convention).
func (d *autopilotDialog) cancelNotice() tea.Cmd {
	id := d.feature.ID
	return func() tea.Msg {
		return noticeMsg{text: string(id) + ": cancelled — nothing started"}
	}
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

	b.WriteString(s.Faint.Render(dashRule(autopilotHeader(d.feature, d.plan), width)) + "\n")
	for _, l := range autopilotBody(d.feature, d.plan, domain.GateAutopilot, d.base()) {
		for _, wl := range strings.Split(wrapText(l, width), "\n") {
			b.WriteString(s.Subtle.Render(wl) + "\n")
		}
	}
	b.WriteString("\n")

	b.WriteString(d.buttons.View(s, true) + "\n")
	b.WriteString("\n" + s.Faint.Render("←/→ move · enter select · esc cancel"))
	return s.DialogFrame.Render(b.String())
}
