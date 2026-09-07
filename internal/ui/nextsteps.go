package ui

import (
	"strings"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
)

// The status bar answers "what keys exist"; this file answers "what
// should I do with this feature right now, and why". nextActions is a
// pure function over nextInput so the whole state → guidance mapping
// lives (and is table-tested) in one place; assembling nextInput does
// no IO, so the dashboard renders the block every frame.

// nextAction is one option in a workflow decision. The key is only its
// current accelerator; id is the stable semantic identity carried into
// decision answers, so two options that happen to share a key do not
// become the same choice.
type nextAction struct {
	id     string // stable option id, matching cardAction.id
	key    string // the key to press, as shown in the status bar
	label  string // what pressing it does
	why    string // why gummi recommends it for the current state
	detail string // option detail rendered beside the label
	danger bool   // the option crosses a destructive boundary
	// sendBack marks the answer set's "send it back" row, whichever id
	// it happens to wear at this stage. It is asked instead of the id
	// because the id is the DELIVERY (bounce rewinds, run re-runs in
	// place, changes turns) and the answer is one answer: a typed line
	// aimed at this row is routed by internal/reentry rather than by the
	// stage's fixed rule, and there is no other way to tell "send it
	// back" apart from the plain "run implement" row, which is also a
	// run and must not be classified — there is nothing to send back
	// before a stage has produced anything.
	sendBack bool
}

func nextStep(id, key, label, detail string) nextAction {
	return nextAction{id: id, key: key, label: label, why: detail, detail: detail}
}

// sendBackStep is the answer set's "send it back" row. One label, one
// flag, three deliveries — the whole point of merging bounce, "request
// changes" and re-run-with-a-note into one answer (PROPOSAL §4) is that
// a reader never has to know which edge it takes, so the row is built
// once here rather than spelled out at each of the arms that offer it.
func sendBackStep(id, key, detail string) nextAction {
	a := nextStep(id, key, "send it back", detail)
	a.sendBack = true
	return a
}

// nextInput is the in-memory state the suggestions derive from. All
// fields come from what the Update loop already tracks.
type nextInput struct {
	stage  domain.Stage
	kind   domain.Kind
	landed bool
	// hasWorktree is whether the card's worktree exists on disk right
	// now — the same question cardactions' own attach row asks, and the
	// only thing that makes attaching a raw agent CLI possible.
	hasWorktree bool

	sess   engine.SessionState // "" when the feature has no live session
	live   bool                // sess.Live(): a backend is genuinely attached, not just persisted as interactive
	busy   bool                // agent mid-turn
	hasAsk bool                // agent blocked on a structured ask

	attn      attnKind // "" when the feature has no attention item
	escalated bool     // the gate is a loop give-up, not a clean finish
	// cardOpen is whether the reader is on the card page itself rather
	// than the board — the same test talkAction makes for the
	// conversation, for the actions that would only re-open the surface
	// already under the cursor.
	cardOpen bool

	// verdict is the finished session's outcome (verify: pass/fail),
	// verdictUnclear when none or when the session is gone — then the
	// escalated flag on the gate still carries pass vs not-pass.
	verdict reviewVerdict

	// verdictFloorReason is why gummi overruled the agent's own verdict
	// — the failing check, or the environment gate's sentence. It
	// survives a restart now (BG-086), so the blocked arm can name the
	// blocker instead of pointing vaguely at the artifact.
	verdictFloorReason string

	reviewRound      int    // automatic review→fix rounds burned so far
	verifyBounces    int    // verify→work bounces already burned (each one a failed verify)
	failedCheck      string // first failing manual `v` check, "" if none
	openSpecQs       int    // open user %% threads in the artifact (block gates)
	openDiffComments int    // unresolved diff annotations (block gates)
	// undrafted names the required section(s) the departing stage left
	// blank — the artifact half of the same gate, resolved through the
	// engine's own predicate so the panel can name what is missing before
	// an approve that cannot cross is tried.
	undrafted []string

	pullRequest domain.PullRequestRef // the card's linked outbound PR, empty when unlinked
}

// verifyBounces counts verify→work bounce edges in a feature's history:
// each one is a verify failure someone sent back for rework. Derived
// from the transitions table, so the count survives restarts. Verify
// re-runs without a bounce leave no transition, making this a floor —
// it can undercount, never over-warn.
func verifyBounces(hist []state.TransitionRecord) int {
	n := 0
	for _, tr := range hist {
		if tr.From == domain.StageVerify && tr.To == domain.StageImplement {
			n++
		}
	}
	return n
}

// nextInputFor assembles a feature's nextInput from board and session
// state already held in memory.
func (m *Shell) nextInputFor(r featureRow) nextInput {
	in := nextInput{
		stage:            r.F.Stage,
		kind:             r.F.Kind,
		landed:           r.Landed,
		hasWorktree:      r.HasWorktree,
		reviewRound:      m.round(r.F.ID, domain.RoundKindReview),
		verifyBounces:    verifyBounces(r.History),
		openSpecQs:       r.OpenSpecQs,
		openDiffComments: r.OpenDiffComments,
		undrafted:        r.Undrafted,
		pullRequest:      r.F.PullRequest,
	}
	if it, ok := m.inbox.get(r.F.ID); ok {
		in.attn, in.escalated = it.Kind, it.Escalated
	}
	in.cardOpen = m.cardOpen
	// An attached interactive session counts too. It used to be excluded,
	// which left talkAction's own engine.StateInteractive branch — "the
	// architect is already here, do not offer to start it" — unreachable,
	// so a spec you were in the middle of still advertised starting the
	// conversation you were having.
	if sess := m.sessionFor(r.F.ID); sess != nil {
		in.sess = sess.State()
		in.live = sess.Live()
		snap := sess.Snapshot()
		in.busy = snap.Busy
		in.hasAsk = snap.PendingAsk != nil
		if in.sess == engine.StateDone {
			in.verdict = sessionVerdict(snap)
		}
		in.verdictFloorReason = snap.VerdictFloorReason
	}
	for _, res := range m.checksFor(r.F) {
		if !res.OK {
			in.failedCheck = res.Name
			break
		}
	}
	in.verdict = escalatedGateVerdict(in.verdict, in.escalated)
	return in
}

// escalatedGateVerdict refuses to read an escalated gate as a clean pass.
//
// A gate the loop escalated is, by construction, not one: a clean verify
// raises the plain gate (raiseAttention, recorded as DecisionKindGate),
// and only a give-up escalates (raiseEscalation, recorded as
// DecisionKindVerify). So a pass alongside an escalation is one the
// engine already overruled — its verdict floor (setVerdictFloor, stamped
// when a live gummi-check fails) lives on the session and is not
// persisted, so after a restart verdict.SessionVerdict drops back to
// parsing the agent's own "VERDICT: pass" out of the transcript and
// resurrects the claim the floor existed to refuse. The card then read
// as verified and recommended landing the branch on main.
//
// Unclear is what is actually known at that point, and it is the
// fallback nextInput.verdict's own comment already describes: "the
// escalated flag on the gate still carries pass vs not-pass".
func escalatedGateVerdict(v reviewVerdict, escalated bool) reviewVerdict {
	if escalated && v == verdictPass {
		return verdictUnclear
	}
	return v
}

// blockedGate returns the resolve-first action when something blocks the
// gate (DESIGN §6.1), or nil when g is clear. The undrafted-sections case
// leads with the same lever the gate itself demands: the stage's writer
// run again, now that the panel knows what is missing before approve is
// tried and refused.
func blockedGate(in nextInput) *nextAction {
	if in.openSpecQs > 0 {
		a := nextStep("spec", "s", "resolve open comments",
			itoa(in.openSpecQs)+" open in the "+artifactNoun(in.kind)+" "+blockVerb(in.openSpecQs)+
				" the gate — R requests changes")
		return &a
	}
	if in.openDiffComments > 0 {
		a := nextStep("diff", "d", "resolve diff comments",
			itoa(in.openDiffComments)+" open "+blockVerb(in.openDiffComments)+
				" the gate — R requests changes, x resolves")
		return &a
	}
	if len(in.undrafted) > 0 {
		blank := strings.Join(in.undrafted, ", ")
		pronoun, be, label := "they", "are", "draft the missing sections"
		if len(in.undrafted) == 1 {
			pronoun, be, label = "it", "is", "draft the missing section"
		}
		a := nextStep("run", "enter", label,
			blank+" "+be+" required in the "+artifactNoun(in.kind)+" and still blank — the gate stays shut until "+
				pronoun+" "+be+" drafted; enter re-runs the stage to write "+pronoun)
		return &a
	}
	return nil
}

// blockVerb agrees the blocker count's verb. One open comment blocks the
// gate; two block it. The row sits directly under the narration sentence
// stating the same count (narration.go), so a disagreement between the
// two is read as one of them being wrong about the card rather than
// about grammar.
func blockVerb(n int) string {
	if n == 1 {
		return "blocks"
	}
	return "block"
}

// talkAction is how the next card offers an interactive stage's own
// conversation.
//
// Once a session is live the thread IS that conversation — its input
// sits at the bottom of the very same page — so "chat with the
// architect" would be an action pointing at the surface you are already
// looking at, costing a row of the one block that exists to tell you
// something you did not know. It appears only when there is nobody to
// talk to yet, and then it says what enter actually does: start them.
func talkAction(in nextInput, who, why string) []nextAction {
	// Only an attached conversation is the one already on screen. A row
	// whose state merely persisted as interactive — a card rehydrated
	// after a restart, or one a headless run seeded — reports
	// StateInteractive with no backend behind it, and dropping the row
	// there leaves the stage's gate as the card's recommendation:
	// "approve — creates the worktree and starts the agent stages" as the
	// answer to a conversation that went away. decisionQuestion learned
	// this distinction as BG-043; this is the same in.live test.
	if in.sess == engine.StateInteractive && in.live {
		return nil
	}
	verb := "start"
	// paused and rehydrated-interactive both have a transcript waiting:
	// the run action attaches to the card's chat (decision.go's "run" →
	// attachChatWith) rather than opening a blank one.
	if in.sess == engine.StatePaused || in.sess == engine.StateInteractive {
		verb = "resume"
	}
	return []nextAction{nextStep("run", "enter", verb+" "+who, why)}
}

// nextActions is the ACTION INVENTORY's ranking feed — the answer set,
// plus the per-card nudges that are not workflow answers but should
// still ride above the fold when they apply.
//
// It is deliberately NOT what the decision picker renders. That is
// stageActions, the answer set itself (decision.go's openDecision): a
// "pull PR review" row is a reading surface for a linked card, and
// putting it in the picker would reopen exactly the blur this change
// closes — a view rendered as an equal-weight option beside "land on
// main". cardActionsFor promotes whatever this ranks, so the nudge keeps
// its place above the fold in the inventory without becoming an answer
// to "what now".
func nextActions(in nextInput) []nextAction {
	return appendPullReviewSuggestion(stageActions(in), in)
}

// appendPullReviewSuggestion adds the "pull PR review" nudge whenever the
// card is linked and sitting in review or verify — the loop this feature
// exists to keep on the board. It applies uniformly across every sub-state
// of those two stages (mid-run, blocked, gated, failed) rather than only
// the "everything finished cleanly" path, since pulling fresh PR comments
// is a legitimate move throughout review and verify, not just at the end
// of them. cardActionsFor promotes any action whose key nextActions ranks
// (folded = false), so this is what lets prpull rise out of the fold
// without spending an accelerator on it.
//
// It is deliberately exempt from stageActions' own four-suggestion cap
// (TestNextActionsCapAndRanking): that cap bounds the base recommendation
// table, and a linked PR is a per-card fact on top of it, not another
// stage-derived option competing for the same four slots.
func appendPullReviewSuggestion(acts []nextAction, in nextInput) []nextAction {
	if in.pullRequest.Empty() {
		return acts
	}
	if in.stage != domain.StageImplement && in.stage != domain.StageVerify {
		return acts
	}
	return append(acts, nextStep("prpull", "", "pull PR review", "read the PR's review comments back onto the diff"))
}

// stageActions is the card's ANSWER SET: the workflow answers to "what
// now", and nothing else.
//
// Four answers cover every stop (PROPOSAL-card-surface §4), and the
// label a row wears is the arm's own name — "approve", "land on main",
// "send it back" — because the category is what makes the set legible,
// not what the reader presses:
//
//   - GO ON — every arm of advance (start / approve / advance to verify /
//     land on main / merge the PR / mark done / clean up), plus the run
//     that gets a stage moving when nothing is running yet.
//   - SEND IT BACK — one answer, not three. bounce, "request changes"
//     and re-run-with-a-note all meant *not right, try again*, and the
//     reader re-derived the difference on every visit. Which edge it
//     takes is a fixed rule in this phase: at verify it bounces to
//     implement, at implement it re-runs in place, at the design stage it
//     is the turn that asks the architect for the changes. The typed
//     line rides along in all three (decision.go's wordConsumer, which
//     still keys on these same three ids).
//   - STOP HERE — pause and park, which were already one action wearing
//     two words (pauseLabelWhy); the detail line still says which of the
//     two this card's state means.
//   - ANSWER IT — a pending ask_user, which replaces the rest rather
//     than joining it: a blocked agent's question is not one option
//     among four.
//
// What is NOT here is as load-bearing as what is. Reading surfaces
// (the artifact, the diff, the thread) are tabs on the card page now,
// not rows in this list, and card housekeeping (the envelope, the
// autopilot switch, attach, rebase, merge, deps, the PR verbs) lives in
// the action inventory and the "/" menu. Both were rendered here as
// equal-weight options beside the actual decision, which is what made a
// reader sweep all 22 every visit to be sure the fold hid nothing.
//
// It stays a pure function of nextInput — table-tested, free, no agent
// anywhere near it (DESIGN §6.3: the options are deterministic even
// though the narration above them is not).
func stageActions(in nextInput) []nextAction {
	if in.landed {
		a := nextStep("clean", "c", "clean up", "branch landed on main — remove the worktree and branch")
		a.danger = true
		return []nextAction{a}
	}
	if in.stage == domain.StageDone {
		return nil
	}

	// a scheduled or running agent owns the screen; only a blocking
	// question needs the user before it finishes.
	switch in.sess {
	case engine.StateQueued:
		return nil
	case engine.StateRunning:
		if in.hasAsk {
			return append([]nextAction{answerIt()}, stopHere(in)...)
		}
		return nil
	case engine.StatePaused:
		// already stopped by hand: picking it back up is the only answer,
		// and "stop here" would be a row offering what has already
		// happened. attach is plumbing — it is in the inventory and
		// answers /attach, it is not one of the four.
		return []nextAction{nextStep("run", "enter", "pick it back up",
			"the run is paused — a fresh run picks "+string(in.stage)+" back up")}
	}

	// failures, budget stops, and questions override stage guidance.
	switch in.attn {
	case attnFailure:
		return append([]nextAction{nextStep("run", "enter", "try again",
			"the session errored — a fresh run retries "+string(in.stage))}, stopHere(in)...)
	case attnBudget:
		// the two honest answers to an exhausted envelope, offered where
		// the stop is rather than as a pointer at the inbox tab: raise it
		// and carry on, or stop here. The inbox reaches the same top-up
		// with u; this is the same act, not a second one.
		return append([]nextAction{nextStep("topup", "", "top up and go on",
			"raise the envelope — "+string(in.stage)+" picks up where it stopped")}, stopHere(in)...)
	case attnQuestion:
		return append([]nextAction{answerIt()}, stopHere(in)...)
	}

	finished := in.attn == attnGate || in.sess == engine.StateDone

	switch in.stage {
	case domain.StageTodo:
		return []nextAction{nextStep("advance", "g", "start", "advance into the design flow")}

	case domain.StagePlan:
		// The design stage. Its answers are: get the conversation going,
		// approve what it wrote, send it back, or stop. Reading the
		// artifact it wrote — and the code a design stage may already have
		// put on the card's branch — are the artifact and diff tabs.
		acts := talkAction(in, designPartner(in.kind), "shape the "+artifactNoun(in.kind)+" until it convinces you")
		if b := blockedGate(in); b != nil {
			// "you cannot cross yet" is a different sentence, not another
			// way forward: it leads, and the rest still follows it.
			return append(append([]nextAction{*b}, acts...), stopHere(in)...)
		}
		acts = append(acts, nextStep("advance", "g", "approve", "hands the card to the agent stages"))
		// Only worth offering while the architect is here to receive it —
		// with no session the "start" row above is the way in.
		if in.live {
			acts = append(acts, sendBackStep("changes", "",
				"say what is wrong — your line goes to the architect as the turn asking for it"))
		}
		return append(acts, stopHere(in)...)

	case domain.StageImplement:
		if !finished {
			// nothing has been produced yet, so there is nothing to send
			// back: the rewind to plan is /bounce, in the inventory.
			return append([]nextAction{nextStep("run", "enter", "run "+string(in.stage),
				"no active run — start (or restart) the stage")}, stopHere(in)...)
		}
		if b := blockedGate(in); b != nil {
			return append([]nextAction{*b}, stopHere(in)...)
		}
		acts := []nextAction{
			nextStep("advance", "g", "advance to verify", "the critique passed — run the checks"),
			// re-runs the stage in place rather than rewinding to it: the
			// work stage's critique iterates the stage, so there is no edge
			// to take. The bigger hammer — the whole plan, not this pass —
			// is /bounce.
			sendBackStep("run", "",
				"re-runs "+string(in.stage)+" with what is wrong — your line goes with it"),
		}
		return append(acts, stopHere(in)...)

	case domain.StageVerify:
		if !finished {
			return append([]nextAction{nextStep("run", "enter", "run verify",
				"no active run — runs the checks and the verification plan")}, stopHere(in)...)
		}
		if b := blockedGate(in); b != nil {
			return append([]nextAction{
				*b,
				sendBackStep("bounce", "b", "or send the open items back as rework"),
			}, stopHere(in)...)
		}
		if in.failedCheck != "" {
			// re-running the checks alone is /verify: it re-evaluates the
			// gate rather than answering it, so it is not one of the four.
			return append([]nextAction{
				sendBackStep("bounce", "b", "the failure is the implementation's fault — your line goes with it"),
				nextStep("advance", "g", "land anyway", "overrule if the failure does not hold up"),
			}, stopHere(in)...)
		}
		// blocked: the environment can't run the plan, so rework can't
		// help — steer at the environment, not the bounce. The blocker
		// itself is the narration's first sentence now (narration.go).
		if in.verdict == verdictBlocked {
			return append([]nextAction{
				nextStep("run", "enter", "re-run verify", "after fixing the environment or tagging the plan's env-bound steps"),
				nextStep("advance", "g", "land anyway", "only if you verified it by hand — verify never proved this build"),
			}, stopHere(in)...)
		}
		// a verify gate carries a verdict (or, session gone, at least the
		// escalation flag): recommend landing only on a clean pass.
		//
		// The FD-004 loop-breaker used to live here as an ORDERING — after
		// a repeated failed verify, bounce dropped to last with a warning.
		// It cannot: "send it back" now carries the in-place re-run too,
		// and de-ranking the merged row would demote an answer the guard
		// was never about. The warning moved into the narration, where it
		// is a sentence rather than a rank (narration.go's loopBreaker).
		if in.verdict == verdictFail || in.verdict == verdictChanges ||
			(in.verdict == verdictUnclear && in.escalated) {
			return append([]nextAction{
				sendBackStep("bounce", "b", "send the failures back as rework — your line goes with them"),
				nextStep("advance", "g", "land anyway", "overrule if the failures do not hold up"),
			}, stopHere(in)...)
		}
		if in.kind == domain.KindResearch {
			return append([]nextAction{
				nextStep("advance", "g", "mark done", "verify passed — advance to done"),
				sendBackStep("bounce", "b", "not convinced — your line goes back with it"),
			}, stopHere(in)...)
		}
		why := "squash-merge the branch and mark the " + noun(in.kind) + " done"
		if in.verdict == verdictPass {
			why = "verify passed — " + why
		}
		gate := nextStep("advance", "g", "land on main", why)
		if hint := in.pullRequest.NextStepsHint(true); hint != "" {
			gate = nextStep("advance", "g", "merge the PR", hint)
		}
		return append([]nextAction{
			gate,
			sendBackStep("bounce", "b", "not convinced — your line goes back with it"),
		}, stopHere(in)...)
	}
	return nil
}

// answerIt is the row a pending ask_user gets in the workflow answer set.
//
// It is only ever reached from the two arms that have an ask without a
// live Ask object to render (a running session blocked on one, and the
// attnQuestion inbox item). Once the session hands over the Ask itself,
// decision.go renders the question's own options instead and this list
// is not consulted at all — which is the settled answer to "does a
// pending ask keep the full answer set": it replaces it.
func answerIt() nextAction {
	return nextStep("run", "enter", "answer it", "the agent asked a question and is blocked on your reply")
}

// stopHere is the "stop here" answer, or nothing when there is nothing
// to stop.
//
// pause and park were never two actions — both are boardVerb's p, which
// is engine.Pause either way (shell.go's pauseRun). Only the word
// differed, by session state, which is a distinction the detail line can
// carry without spending a second row on it. A session already paused
// gets no row: it has already stopped.
//
// An INTERACTIVE session gets none either, and that is the lockstep rule
// rather than a nicety. boardVerb's p pauses only a non-interactive
// session (pauseRun refuses one outright); with an interactive session it
// falls through to the dependency picker. So a "stop here" row keyed p on
// a live design chat would name one act and perform an unrelated other —
// exactly the divergence between the offered list and the key handler
// that foreignBlockedKeys exists to prevent, and now stated for every
// card rather than only for a foreign-driven one.
func stopHere(in nextInput) []nextAction {
	switch in.sess {
	case "", engine.StatePaused, engine.StateInteractive:
		return nil
	}
	why := "park it — nothing runs until you come back"
	if in.sess == engine.StateRunning || in.sess == engine.StateQueued {
		why = "free the slot — enter re-runs the stage later"
	}
	return []nextAction{nextStep("pause", "p", "stop here", why)}
}

// noun names the work item kind for prose.
func noun(k domain.Kind) string {
	if k == domain.KindBug {
		return "bug"
	}
	return "feature"
}

// designPartner names who the design stage's chat is with, by kind. One
// stage, but a research topic is shaped by a researcher and a feature by
// an architect, and the row should say which.
func designPartner(kind domain.Kind) string {
	if kind == domain.KindResearch {
		return "the researcher"
	}
	return "the architect"
}
