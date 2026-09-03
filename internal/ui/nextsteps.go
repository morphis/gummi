package ui

import (
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/workflow"
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
}

func nextStep(id, key, label, detail string) nextAction {
	return nextAction{id: id, key: key, label: label, why: detail, detail: detail}
}

// nextInput is the in-memory state the suggestions derive from. All
// fields come from what the Update loop already tracks.
type nextInput struct {
	stage  domain.Stage
	kind   domain.Kind
	landed bool
	quick  bool // the quick route: one-pass spec, approval goes straight to implement

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

	pullRequest domain.PullRequestRef // the card's linked outbound PR, empty when unlinked
}

// verifyBounces counts verify→work bounce edges in a feature's history:
// each one is a verify failure someone sent back for rework. Derived
// from the transitions table, so the count survives restarts. Verify
// re-runs without a bounce leave no transition, making this a floor —
// it can undercount, never over-warn.
func verifyBounces(hist []state.TransitionRecord, kind domain.Kind) int {
	work := workflow.WorkStage(kind)
	n := 0
	for _, tr := range hist {
		if tr.From == domain.StageVerify && tr.To == work {
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
		quick:            r.F.Skip.Quick,
		reviewRound:      m.round(r.F.ID, domain.RoundKindReview),
		verifyBounces:    verifyBounces(r.History, r.F.Kind),
		openSpecQs:       r.OpenSpecQs,
		openDiffComments: r.OpenDiffComments,
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

// blockedGate returns the resolve-first action when open review
// comments block the gate (DESIGN §6.1), or nil when g is clear.
func blockedGate(in nextInput) *nextAction {
	if in.openSpecQs > 0 {
		a := nextStep("spec", "s", "resolve open comments",
			itoa(in.openSpecQs)+" open in the "+artifactNoun(in.kind)+" block the gate — R requests changes")
		return &a
	}
	if in.openDiffComments > 0 {
		a := nextStep("diff", "d", "resolve diff comments",
			itoa(in.openDiffComments)+" open block the gate — R requests changes, x resolves")
		return &a
	}
	return nil
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
// autopilotAction is the "hand the rest to autopilot" row the design
// puts at the foot of a gate: the same `A` overlay the accelerator opens,
// offered where handing over is a real answer to the decision on screen
// rather than a key you have to already know about. why says what
// handing over means at this particular stop, since that is the part
// that differs — gates crossing themselves is not the same promise as
// taking the remaining correction rounds alone.
func autopilotAction(why string) nextAction {
	return nextStep("gate", "A", "let autopilot finish", why)
}

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

// nextActions derives the ranked suggestion list — the first entry is
// the recommendation, at most three entries total. Empty means the
// state speaks for itself (an agent mid-run, a done feature).
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
	if in.stage != domain.StageReview && in.stage != domain.StageVerify {
		return acts
	}
	return append(acts, nextStep("prpull", "", "pull PR review", "read the PR's review comments back onto the diff"))
}

// stageActions is nextActions' own stage-by-stage derivation, factored out
// so the PR-pull nudge above can post-process its result uniformly instead
// of being threaded into every one of its early returns.
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
			return []nextAction{
				nextStep("run", "enter", "answer the agent", "it asked a question and is blocked on your reply"),
				nextStep("pause", "p", "pause", "free the slot — enter re-runs the stage later"),
			}
		}
		return nil
	case engine.StatePaused:
		return []nextAction{
			nextStep("run", "enter", "re-run "+string(in.stage), "the run is paused — a fresh run picks the stage back up"),
			nextStep("attach", "a", "attach the agent CLI", "work in the worktree by hand instead"),
		}
	}

	// failures, budget stops, and questions override stage guidance.
	switch in.attn {
	case attnFailure:
		return []nextAction{
			nextStep("run", "enter", "re-run "+string(in.stage), "the session errored — a fresh run retries the stage"),
			nextStep("attach", "a", "attach the agent CLI", "debug it by hand in the worktree"),
		}
	case attnBudget:
		return []nextAction{nextStep("inbox", "i", "open the inbox", "the stage hit its budget — top up (u) or park (x) from there")}
	case attnQuestion:
		return []nextAction{nextStep("run", "enter", "attach & answer", "the agent asked a question and is waiting")}
	}

	work := workflow.WorkStage(in.kind) // implement (feature) or fix (bug)
	finished := in.attn == attnGate || in.sess == engine.StateDone

	switch in.stage {
	case domain.StageTodo:
		return []nextAction{nextStep("advance", "g", "start", "advance into the design flow")}

	case domain.StageInvestigate:
		return append(
			talkAction(in, "the researcher", "explore the question and shape the doc"),
			nextStep("advance", "g", "advance", "move on to "+string(domain.StageShape)),
		)

	case domain.StageShape:
		return append(
			talkAction(in, "the researcher", "converge the findings into the answer"),
			nextStep("advance", "g", "advance", "move on to "+string(domain.StageReview)),
		)

	case domain.StageBrainstorm, domain.StageTriage:
		return append(
			talkAction(in, "the architect", "explore the problem and candidate approaches"),
			nextStep("advance", "g", "advance", "converged? move on to the "+artifactNoun(in.kind)),
		)

	case domain.StageSpec, domain.StageDiagnose:
		acts := talkAction(in, "the architect", "shape the "+artifactNoun(in.kind)+" until it convinces you")
		if in.quick && len(acts) > 0 {
			acts[0].why = "quick route — it drafts the whole spec in one pass; steer and refine"
		}
		if b := blockedGate(in); b != nil {
			return append([]nextAction{*b}, acts...)
		}
		gate := nextStep("advance", "g", "approve", "creates the worktree and starts the agent stages")
		if in.quick {
			gate.why = "creates the worktree and starts implementing — P first if it outgrew quick"
		}
		acts = append(acts, gate)
		// Sending it back is an answer in its own right, not just
		// something typing happens to do. It is the option that consumes
		// the composer's words (decision.go's wordConsumer), so typing
		// aims at it and enter delivers the line as the turn that asks
		// for the changes. Only worth offering while the architect is
		// here to receive it — with no session the "start" row above is
		// the way in.
		if in.live {
			acts = append(acts, nextStep("changes", "", "request changes",
				"send it back with what's wrong — your line goes with it"))
		}
		// reading the thing you are about to approve is an option in its
		// own right, the same way the plan stage offers reading the plan.
		// It goes after the gate rather than before it: the recommendation
		// leads, and this is what you reach for when you are not ready to
		// take it yet.
		acts = append(acts, nextStep("spec", "s", "read the "+artifactNoun(in.kind)+" first",
			"it is what approving signs off on"))
		return append(acts, autopilotAction("gates cross themselves from here"))

	case domain.StagePlan:
		if !finished {
			return []nextAction{nextStep("run", "enter", "run the planner", "no active run — writes the line-level plan into the spec")}
		}
		acts := []nextAction{nextStep("spec", "s", "read the plan", "it lives in the spec's Implementation notes")}
		if b := blockedGate(in); b != nil {
			return append(acts, *b)
		}
		why := "plan critiqued clean — start implementing"
		if in.escalated {
			why = "the critique loop gave up — judge the plan yourself before approving"
			acts = append(acts, nextStep("bounce", "b", "bounce for another plan round",
				"send it back for a fresh, human-triggered replan round"))
		}
		return append(acts, nextStep("advance", "g", "approve & "+string(work), why))

	case domain.StageImplement, domain.StageFix:
		if !finished {
			return []nextAction{nextStep("run", "enter", "run "+string(in.stage), "no active run — start (or restart) the stage")}
		}
		acts := []nextAction{nextStep("diff", "d", "review the diff", "spot-check what the "+string(in.stage)+" run produced")}
		if b := blockedGate(in); b != nil {
			return append(acts, *b)
		}
		return append(acts, nextStep("advance", "g", "advance to review", "hand it to the fresh-context reviewer"))

	case domain.StageReview:
		if !finished {
			return []nextAction{nextStep("run", "enter", "run review", "no active run — start the fresh-context review")}
		}
		// a review gate is always an escalation: clean verdicts advance
		// automatically, so this review gave up (round cap or no verdict).
		why := "the review loop gave up — read its findings in the " + artifactNoun(in.kind)
		if in.reviewRound >= maxReviewRounds {
			why = "still requesting changes after " + itoa(maxReviewRounds) + " rounds — read the findings yourself"
		}
		acts := []nextAction{nextStep("spec", "s", "read the findings", why)}
		if b := blockedGate(in); b != nil {
			return append(acts, *b)
		}
		return append(acts,
			nextStep("bounce", "b", "bounce to "+string(work), "send the findings back for another round"),
			nextStep("advance", "g", "advance to verify", "overrule the reviewer if the findings don't hold"),
			// no round count here: nextInput carries the review loop's own
			// counter, not the corrective budget this would be spending,
			// and the overlay the row opens states that budget exactly.
			autopilotAction("it takes the remaining correction rounds alone"))

	case domain.StageVerify:
		if !finished {
			return []nextAction{nextStep("run", "enter", "run verify", "no active run — runs the checks and the verification plan")}
		}
		if b := blockedGate(in); b != nil {
			return []nextAction{
				*b,
				nextStep("bounce", "b", "bounce to "+string(work), "or send the open items back as rework"),
			}
		}
		if in.failedCheck != "" {
			return []nextAction{
				nextStep("verify", "v", "re-run checks", "'"+in.failedCheck+"' failed on the last manual run"),
				nextStep("run", "enter", "re-run verify", "let the agent chase the failure and update the "+artifactNoun(in.kind)),
				nextStep("bounce", "b", "bounce to "+string(work), "if the failure is the implementation's fault"),
			}
		}
		// blocked: the environment can't run the plan, so rework can't
		// help — steer at the environment, not the bounce. (Session gone
		// drops this to the fail arm below, same degradation as fail; the
		// inbox gate text still says BLOCKED.)
		if in.verdict == verdictBlocked {
			// name the blocker when gummi's own floor recorded one: after
			// BG-086 that reason survives a restart, so the card can say
			// what to fix rather than only that something is wrong.
			blocked := "verify is blocked on the environment — the missing prerequisites are in the " + artifactNoun(in.kind)
			if in.verdictFloorReason != "" {
				blocked = sanitize(in.verdictFloorReason) + " — fix that, or tag the plan's env-bound steps"
			}
			return []nextAction{
				nextStep("spec", "s", "read the blockers", blocked),
				nextStep("run", "enter", "re-run verify", "after fixing the environment or tagging the plan's env-bound steps"),
				nextStep("advance", "g", "land on main", "only if you verified it by hand — verify never proved this build"),
			}
		}
		// a verify gate carries a verdict (or, session gone, at least the
		// escalation flag): recommend landing only on a clean pass.
		if in.verdict == verdictFail || in.verdict == verdictChanges ||
			(in.verdict == verdictUnclear && in.escalated) {
			why := "verify reported failure — the evidence is in the " + artifactNoun(in.kind)
			if in.verdict == verdictUnclear {
				why = "verify gave no clear verdict — judge the results in the " + artifactNoun(in.kind)
			}
			// repeat failures: each bounce already bought a full
			// implement→review→verify round that changed nothing, so the
			// bounce drops to last with a warning — the FD-004 loop-breaker.
			if in.verifyBounces >= 1 {
				n := itoa(in.verifyBounces + 1)
				return []nextAction{
					nextStep("spec", "s", "read the verify results", "verify has failed "+n+" times — the evidence is in the "+artifactNoun(in.kind)),
					nextStep("advance", "g", "land on main", "overrule if the failures don't hold up"),
					nextStep("bounce", "b", "bounce to "+string(work), "unlikely to help after "+n+" failed verifies — check the environment and the verification plan first"),
				}
			}
			return []nextAction{
				nextStep("spec", "s", "read the verify results", why),
				nextStep("bounce", "b", "bounce to "+string(work), "send the failures back as rework"),
				nextStep("advance", "g", "land on main", "overrule if the failure doesn't hold up"),
			}
		}
		if in.kind == domain.KindResearch {
			return []nextAction{
				nextStep("advance", "g", "mark done", "verify passed — advance to done"),
				nextStep("bounce", "b", "bounce to investigate", "not convinced — send it back with comments"),
			}
		}
		why := "squash-merge the branch and mark the " + noun(in.kind) + " done"
		if in.verdict == verdictPass {
			why = "verify passed — " + why
		}
		gate := nextStep("advance", "g", "land on main", why)
		if hint := in.pullRequest.NextStepsHint(true); hint != "" {
			gate = nextStep("advance", "g", "merge the PR", hint)
		}
		return []nextAction{
			gate,
			nextStep("diff", "d", "final read of the diff", "one last look before it lands"),
			nextStep("bounce", "b", "bounce to "+string(work), "not convinced — send it back with comments"),
		}
	}
	return nil
}

// noun names the work item kind for prose.
func noun(k domain.Kind) string {
	if k == domain.KindBug {
		return "bug"
	}
	return "feature"
}
