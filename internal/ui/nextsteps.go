package ui

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/verdict"
	"github.com/morphis/gummi/internal/worktree"
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
	// exited is whether the current stage already finished a run, read
	// from the event log rather than from any live session (featureRow.
	// Exited). It is the third way a stage counts as finished, and the
	// only one that survives a restart.
	exited bool
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

	reviewRound   int    // automatic review→fix rounds burned so far
	verifyBounces int    // verify→work bounces already burned (each one a failed verify)
	failedCheck   string // first failing manual `v` check, "" if none
	// backendNeverStarted marks the failure a first-time user hits most
	// and can act on least: the coding CLI died before this stage's first
	// turn (agent.RunFailure.FirstTurn), so nothing is wrong with the
	// card — the backend is not set up. Re-running is the one thing
	// guaranteed not to help, and it used to be the only thing offered.
	backendNeverStarted bool
	openSpecQs          int // open user %% threads in the artifact (block gates)
	openDiffComments    int // unresolved diff annotations (block gates)
	// undrafted names the required section(s) the departing stage left
	// blank — the artifact half of the same gate, resolved through the
	// engine's own predicate so the panel can name what is missing before
	// an approve that cannot cross is tried.
	undrafted []string

	pullRequest domain.PullRequestRef // the card's linked outbound PR, empty when unlinked

	// excusedChecks names the repo checks that were already failing when
	// the branch was cut, which verify writes off rather than failing on
	// (state.ExcusedChecks). Read from the shell's per-card cache, never
	// from the store — this is assembled on the render path.
	excusedChecks []string

	// base is the branch this card lands on, resolved once at attach
	// (Shell.baseBranch). The landing row used to write the literal
	// "main": round 3 drove a `master` repo and got "land on main" on the
	// decision block and in the inbox while the help overlay and the merge
	// dialog on the same screen said master — the one row where the name
	// of the branch is the consequence. Empty only in a test scaffold,
	// where baseStep falls back to the default name rather than emitting a
	// sentence with a hole in it.
	base string
	// branch is the card's own git branch, carried for the one row whose
	// whole point is what happens to it: hand-off keeps this, by name, so
	// a reader about to go push something is not left to derive it. Empty
	// only in a test scaffold, where keptBranch() says "the branch".
	branch string
}

// keptBranch names the branch a hand-off keeps, falling back to the
// generic noun rather than emitting a sentence with a hole in it — the
// same contract landBase() has for the trunk's name.
func (in nextInput) keptBranch() string {
	if in.branch == "" {
		return "the branch"
	}
	return in.branch
}

// landBase names the branch a landing row merges onto. nextInput carries
// it, so the row is built from the card's own repository rather than from
// a literal that happens to be right most of the time.
func (in nextInput) landBase() string {
	if in.base == "" {
		return worktree.DefaultBaseBranchName
	}
	return in.base
}

// finished reports whether the card's current stage has produced its
// result and is waiting on a person: a gate item is up, the session is
// done, or — after a restart, when neither survives — the log carries
// the stage's exit. Every arm that offers "send it back" rather than
// "run the stage" asks this, so the three sources cannot disagree about
// whether there is anything to send back.
func (in nextInput) finished() bool {
	return in.attn == attnGate || in.sess == engine.StateDone || in.exited
}

// stageExited reads a stage's finished run out of the log: the newest
// stage_exit event for stage, provided it is not older than the newest
// transition INTO stage in the card's history. The second clause is the
// generation check — a card that finished verify, was sent back, and
// came round to verify again carries the old exit in its log, and that
// exit belongs to a stage generation that no longer exists.
//
// It answers with the exit's verdict so a restarted card can still be
// told apart as passed or failed, through the same FromTool mapping the
// tool result itself took.
func stageExited(events []state.CardEvent, hist []state.TransitionRecord, stage domain.Stage) (reviewVerdict, bool) {
	var entered time.Time
	for _, tr := range hist {
		if tr.To == stage && tr.At.After(entered) {
			entered = tr.At
		}
	}
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		if ev.Kind != state.EventStageExit || ev.Stage != stage {
			continue
		}
		if ev.At.Before(entered) {
			return verdictUnclear, false
		}
		var p struct {
			Verdict string `json:"verdict"`
		}
		_ = json.Unmarshal([]byte(ev.Payload), &p)
		return verdict.FromTool(p.Verdict), true
	}
	return verdictUnclear, false
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
		exited:           r.Exited,
		excusedChecks:    m.excusedChecks[r.F.ID],
		base:             m.baseBranch(r.F),
		branch:           r.F.BranchName(),
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
		var rf *agent.RunFailure
		in.backendNeverStarted = errors.As(snap.Err, &rf) && rf.FirstTurn
	}
	// No session at all — a restart took it — but the log still says how
	// the stage ended. The exit's verdict stands in for the session's,
	// and goes through the same escalation guard below, so a restarted
	// escalated gate still refuses to read as a clean pass.
	if in.sess == "" && r.Exited {
		in.verdict = r.ExitVerdict
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
//
// It still returns exactly one row — spec, then diff, then undrafted, in
// that priority — because that is genuinely the one lever this row can
// pull: `s` resolves the spec, `d` resolves the diff, and there is no
// single key that does both. But §1.3 found whyItStopped's sentence
// naming only the first blocker it checked, so a reader who resolved the
// one THIS row named found a second one waiting, never mentioned
// anywhere on the page. otherBlockersNote is the fix at this end of the
// same bug: the row's own why now says when there is more to it, even
// though only one of them is what enter runs next.
func blockedGate(in nextInput) *nextAction {
	// A BLOCKER DESCRIBES A GATE, AND THERE IS NO GATE UNTIL THE STAGE
	// HAS PRODUCED SOMETHING TO APPROVE. Before that, an open comment is
	// what the person wrote for the agent that is about to read it —
	// leading with "resolve open comments" tells them to take back the
	// only thing they have said about the card, and a blank required
	// section is blank because nothing has filled it yet. Both used to
	// show anyway, above (and identical to) the one row that actually
	// moves the card, which is how a comment written at todo turned into
	// a card with nothing to do but undo it.
	if !in.finished() {
		return nil
	}
	if in.openSpecQs > 0 {
		// x FIRST, and named at all. The blockers here are @user markers,
		// and a @user marker closes only under a @user resolution
		// (spec.Parse) — so R, which sends them back to the agent, is the
		// one thing that provably cannot clear this gate no matter how
		// well the agent answers. Round 3 §1.4 walked it: the architect
		// addressed the comment and wrote its own "resolved —" directly
		// beneath, the gate stayed shut, and the only key the row named was
		// the one that would send it round again. The diff row has said
		// "x resolves" all along; this is the row where it is load-bearing.
		a := nextStep("spec", "s", "resolve open comments",
			itoa(in.openSpecQs)+" open in the "+artifactNoun(in.kind)+" "+blockVerb(in.openSpecQs)+
				" the gate"+otherBlockersNote(in, "spec")+" — x resolves one, R sends them back to the agent")
		return &a
	}
	if in.openDiffComments > 0 {
		a := nextStep("diff", "d", "resolve diff comments",
			itoa(in.openDiffComments)+" open "+blockVerb(in.openDiffComments)+
				" the gate"+otherBlockersNote(in, "diff")+" — x resolves one, R sends them back to the agent")
		return &a
	}
	if len(in.undrafted) > 0 {
		blank := strings.Join(in.undrafted, ", ")
		subject, object, be, label := "they", "them", "are", "draft the missing sections"
		if len(in.undrafted) == 1 {
			subject, object, be, label = "it", "it", "is", "draft the missing section"
		}
		a := nextStep("run", "enter", label,
			blank+" "+be+" required in the "+artifactNoun(in.kind)+" and still blank — the gate stays shut until "+
				subject+" "+be+" drafted"+otherBlockersNote(in, "undrafted")+"; enter runs the stage to draft "+object)
		return &a
	}
	return nil
}

// otherBlockersNote names the blockers blockedGate's own row is NOT
// about, so that row's why does not repeat §1.3's mistake at one remove:
// whyItStopped's sentence now names every blocker holding the gate shut,
// but the action row underneath it used to name only its own — resolving
// it could still leave the gate shut on something this row never
// mentioned. which is the kind the row already covers, so its own count
// is not echoed back at it. Empty for the ordinary case, one blocker
// alone, so the row's why is unchanged wherever there is nothing else to
// disclose.
func otherBlockersNote(in nextInput, which string) string {
	var extras []string
	if which != "spec" && in.openSpecQs > 0 {
		extras = append(extras, itoa(in.openSpecQs)+" in the "+artifactNoun(in.kind))
	}
	if which != "diff" && in.openDiffComments > 0 {
		extras = append(extras, itoa(in.openDiffComments)+" on the diff")
	}
	if which != "undrafted" && len(in.undrafted) > 0 {
		extras = append(extras, strings.Join(in.undrafted, ", ")+" still blank")
	}
	if len(extras) == 0 {
		return ""
	}
	return " (plus " + joinList(extras) + ")"
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
	return appendLinkPRSuggestion(appendPullReviewSuggestion(stageActions(in), in), in)
}

// appendLinkPRSuggestion raises "link PR…" out of the fold on a finished,
// unlinked card — the one moment linking is the move, and the one place
// nobody could find it.
//
// Landing through a PR is the third ending a verified card has, but it is
// a ROUTE to an ending rather than an ending itself: linking merges
// nothing, and rendering it beside "land on main" as an equal answer to
// "how does this leave gummi" would reopen exactly the blur
// appendPullReviewSuggestion's own comment argues against. So it rides
// the same seam prpull does — promoted in the action inventory, absent
// from the picker — which also keeps the answer set at the four the
// design gates it to.
func appendLinkPRSuggestion(acts []nextAction, in nextInput) []nextAction {
	if !in.pullRequest.Empty() || !in.hasWorktree || in.landed {
		return acts
	}
	// Offered exactly where hand-off is, by asking the answer set rather
	// than by restating its conditions: both answer "how does this leave
	// gummi", which is a question only a card at the clean end of verify
	// is being asked. A failed verify has not reached it — the answers
	// there are still fix-it or overrule-it — and a linked PR raised on
	// work that did not pass is not a route anyone wants suggested.
	if !hasAction(acts, "handoff") {
		return acts
	}
	return append(acts, nextStep("prlink", "", "link PR…",
		"land it on GitHub instead — gummi follows the PR and waits for "+in.landBase()))
}

// hasAction reports whether an answer set contains the row with this id.
func hasAction(acts []nextAction, id string) bool {
	for _, a := range acts {
		if a.id == id {
			return true
		}
	}
	return false
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
		a := nextStep("clean", "c", "clean up", "branch landed on "+in.landBase()+" — remove the worktree and branch")
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
			return append([]nextAction{answerIt()}, stopOrResume(in)...)
		}
		return nil
	case engine.StatePaused:
		if !in.finished() {
			// stopped by hand before the stage produced anything: picking
			// it back up is the only answer, and "stop here" would be a
			// row offering what has already happened. attach is plumbing
			// — it is in the inventory and answers /attach, it is not one
			// of the four.
			return []nextAction{nextStep("run", "enter", "pick it back up",
				"the run is paused — a fresh run picks "+string(in.stage)+" back up")}
		}
		// §1.1a: pausing stops a RUN. It does not un-pass a verify, or
		// withdraw a design waiting on approval — the stage's own gate
		// already fired, and finished() says so. The old comment above
		// ("picking it back up is the only answer") was true of a paused
		// run and false of a paused card sitting at a finished gate: a
		// card parked right after verify passed used to lose "land on
		// main" from the page entirely, because this case returned before
		// the stage switch below ever ran. Falling through instead lets
		// that switch render the gate's own answer set as it would for
		// any other finished stop; stopOrResume (used throughout that
		// switch in place of stopHere) is what puts "pick it back up"
		// back on the page, as the re-run row, rather than as the only
		// row.
	}

	// failures, budget stops, and questions override stage guidance.
	//
	// attnFailure and attnQuestion keep plain stopHere here, not
	// stopOrResume: each already carries its own id "run" / key "enter"
	// row ("try again", answerIt's "answer it"), so on the rare paused+
	// finished-by-exit combination stopOrResume's "pick it back up" would
	// duplicate it — the same reason StagePlan's talk keeps stopHere,
	// above. attnBudget's own row is keyless ("topup"), so it has nothing
	// to duplicate and keeps stopOrResume.
	switch in.attn {
	case attnFailure:
		// A backend that never produced a turn gets a different why: a
		// retry runs the same command that just failed, and the reason it
		// failed is not on this card. The ANSWER SET is unchanged — the
		// pointer at `gummi doctor` belongs in the narration, which may
		// say anything and change nothing (stageActions' own contract),
		// not in a fifth arm.
		why := "the session errored — a fresh run retries " + string(in.stage)
		if in.backendNeverStarted {
			why = "the backend never started — a retry runs the same command"
		}
		return append([]nextAction{nextStep("run", "enter", "try again", why)}, stopHere(in)...)
	case attnBudget:
		// the two honest answers to an exhausted envelope, offered where
		// the stop is rather than as a pointer at the inbox tab: raise it
		// and carry on, or stop here. The inbox reaches the same top-up
		// with u; this is the same act, not a second one.
		return append([]nextAction{nextStep("topup", "", "top up and go on",
			"raise the budget — "+string(in.stage)+" picks up where it stopped")}, stopOrResume(in)...)
	case attnQuestion:
		return append([]nextAction{answerIt()}, stopHere(in)...)
	}

	finished := in.finished()

	switch in.stage {
	case domain.StageTodo:
		// "the plan stage", the strip's own word for where this goes —
		// not "flow", which is a noun nothing else on the screen uses.
		//
		// §3.1: the row's arm is advance, and advance only moves the stage
		// marker — no agent runs on this keypress. The old label and why
		// ("start — opens the plan stage — the agent reads the card, and
		// any comments on it") promised the read would happen here; live,
		// it produced a screen whose OWN row read "start the architect" —
		// the real start, one keypress later, and the plan stage's own
		// talkAction is what actually promises the agent will read the
		// card. Say only what enter does on THIS screen.
		return []nextAction{nextStep("advance", "g", "open the plan stage",
			"moves the card into plan — start the agent there to have it read the card, and any comments on it")}

	case domain.StagePlan:
		// The design stage. Its answers are: get the conversation going,
		// approve what it wrote, send it back, or stop. Reading the
		// artifact it wrote — and the code a design stage may already have
		// put on the card's branch — are the artifact and diff tabs.
		// talk (below) already carries its own "resume" wording once
		// in.sess is StatePaused (talkAction's own verb switch), so this
		// branch keeps stopHere rather than stopOrResume: stopHere already
		// returns nil for a paused session, and stopOrResume's generic
		// "pick it back up" would otherwise double up with "resume the
		// architect" as two rows for the one same act.
		talk := talkAction(in, designPartner(in.kind), "shape the "+artifactNoun(in.kind)+" until it convinces you")
		if b := blockedGate(in); b != nil {
			// "you cannot cross yet" is a different sentence, not another
			// way forward: it leads, and the rest still follows it.
			return append(append([]nextAction{*b}, talk...), stopHere(in)...)
		}
		// §3.2: approve's arm is also advance — it moves the card into
		// implement and stops there, the same promise-only-what-happens
		// fix as §3.1's todo row above. "hands the card to the agent
		// stages" oversold it (nothing ran until a second keypress
		// started the implementer) and "agent stages" is jargon nothing
		// else on the screen uses; name the agent that actually runs
		// next.
		approve := nextStep("advance", "g", "approve",
			"moves the card into implement — start the implementer there to begin")
		var acts []nextAction
		if finished {
			// §3.3: this stop's own narration reads "plan is ready for
			// your decision" — and the decision is approve (or send it
			// back), not another paid pass through the architect. Leading
			// with talk here put that re-run under the cursor at exactly
			// the stop whose own sentence says the opposite; the verify
			// gate already gets this right (its clean pass leads with
			// "land on main"). A finished design gate is the same shape,
			// so it leads with the decision the same way. Only the ORDER
			// changes, and only in this one state — nothing here is
			// added, dropped, or renamed, and an unfinished stop still
			// talks first, because there is nothing to approve yet.
			acts = append([]nextAction{approve}, talk...)
		} else {
			acts = append(talk, approve)
		}
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
			//
			// §3.2: "(or restart)" used to show unconditionally, including
			// on the very first arrival here straight off approving the
			// plan — where there is nothing yet TO restart. verifyBounces
			// is the one edge that lands a card back in this branch a
			// second time (a failed verify sent back to implement for
			// rework); nothing else that reaches this branch has run
			// implement before, so it is what tells "first run" and
			// "restart" apart.
			why := "no active run — start the stage"
			if in.verifyBounces > 0 {
				why = "no active run — start (or restart) the stage"
			}
			return append([]nextAction{nextStep("run", "enter", "run "+string(in.stage), why)}, stopOrResume(in)...)
		}
		if b := blockedGate(in); b != nil {
			// blockedGate's undrafted-sections row also wears id "run" /
			// key "enter" (it is the redraft run, nextsteps.go's own
			// blockedGate) — stopHere, not stopOrResume, so a paused card
			// blocked on a blank section does not get a second, competing
			// "enter" row underneath its own.
			return append([]nextAction{*b}, stopHere(in)...)
		}
		acts := []nextAction{
			nextStep("advance", "g", "start verify", "the critique passed — run the checks"),
			// re-runs the stage in place rather than rewinding to it: the
			// work stage's critique iterates the stage, so there is no edge
			// to take. The bigger hammer — the whole plan, not this pass —
			// is /bounce.
			sendBackStep("run", "",
				"re-runs "+string(in.stage)+" with what is wrong — your line goes with it"),
		}
		return append(acts, stopOrResume(in)...)

	case domain.StageVerify:
		if !finished {
			return append([]nextAction{nextStep("run", "enter", "run verify",
				"no active run — runs the checks and the verification plan")}, stopOrResume(in)...)
		}
		if b := blockedGate(in); b != nil {
			// same reason as StageImplement's blockedGate branch above:
			// the undrafted-sections row can itself wear id "run" / key
			// "enter", so this stays stopHere rather than stopOrResume.
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
			}, stopOrResume(in)...)
		}
		// blocked: the environment can't run the plan, so rework can't
		// help — steer at the environment, not the bounce. The blocker
		// itself is the narration's first sentence now (narration.go).
		if in.verdict == verdictBlocked {
			// "re-run verify" here is itself id "run" / key "enter" — the
			// same collision the blockedGate branches above guard against
			// — so this stays stopHere rather than stopOrResume too.
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
			}, stopOrResume(in)...)
		}
		if in.kind == domain.KindResearch {
			return append([]nextAction{
				nextStep("advance", "g", "mark done", "verify passed — advance to done"),
				sendBackStep("bounce", "b", "not convinced — your line goes back with it"),
			}, stopOrResume(in)...)
		}
		// The card is finished and the question is no longer "is this
		// good" but "how does this leave gummi". There are three honest
		// answers to that, and this arm used to hold one: landing. The
		// others were a keyless verb nobody finds (link a PR) and nothing
		// at all (keep the branch), which is how a finished card ended up
		// with `D` — delete, which destroys the branch — as the only way
		// to say "I'll take it from here".
		//
		// Landing stays first, and stays what g does: it is still the
		// recommendation on a clean pass. Hand-off is not a warning and is
		// not folded; it is the second answer to the question just asked.
		why := "squash-merge the branch and mark the " + noun(in.kind) + " done"
		if in.verdict == verdictPass {
			why = "verify passed — " + why
		}
		gate := nextStep("advance", "g", "land on "+in.landBase(), why)
		keep := nextStep("handoff", "h", "hand off",
			"close the card and keep "+in.keptBranch()+" — you push, PR or cherry-pick it")
		if hint := in.pullRequest.NextStepsHint(true); hint != "" {
			gate = nextStep("advance", "g", "merge the PR", hint)
			// With a PR open, hand-off is usually the real answer rather
			// than the alternative one: gummi never writes to GitHub, so
			// the landing is already someone else's, and waiting at verify
			// for your own `git pull` is not a workflow step.
			keep = nextStep("handoff", "h", "hand off",
				"close the card now — the PR carries it from here")
		}
		return append([]nextAction{
			gate,
			keep,
			sendBackStep("bounce", "b", "not convinced — your line goes back with it"),
		}, stopOrResume(in)...)
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

// stopOrResume is stopHere's answer for every session state except a
// paused one, where "pick it back up" takes its place instead.
//
// stopHere already returns nil for a paused session — it has already
// stopped, so a second "stop here" row would offer what already
// happened. §1.1a found that this meant a paused card at a FINISHED gate
// rode with NOTHING in its place: the paused branch above used to return
// early with a single "pick it back up" row and never reach the stage
// switch at all, so the gate's own decision (land on main, approve, send
// it back) disappeared along with it. Now that the paused branch falls
// through into the same switch every other stop uses, every call site
// below that used to append stopHere's row appends this one instead, so
// a paused-but-finished stop still carries its way back into the run
// beside the decision it is actually waiting on.
//
// Several call sites deliberately keep plain stopHere instead, because
// they already carry their own id "run" / key "enter" row and this one
// would only duplicate it: the design stage (talkAction grows its own
// "resume the architect" wording once in.sess is StatePaused), implement's
// and verify's blockedGate branches (the undrafted-sections row is itself
// id "run" / key "enter" — nextsteps.go's own blockedGate), verify's
// blocked-environment branch ("re-run verify" is the same id and key),
// and attnFailure/attnQuestion ("try again", "answer it").
func stopOrResume(in nextInput) []nextAction {
	if in.sess == engine.StatePaused {
		return []nextAction{nextStep("run", "enter", "pick it back up",
			"the run is paused — a fresh run picks "+string(in.stage)+" back up")}
	}
	return stopHere(in)
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
