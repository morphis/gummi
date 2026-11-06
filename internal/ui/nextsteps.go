package ui

import (
	"context"
	"encoding/json"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
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
// name is how a sentence built from in names another card.
func (in nextInput) name(id domain.FeatureID) string {
	if in.names == nil {
		return id.Ref()
	}
	return in.names(id)
}

type nextAction struct {
	id     string // stable option id, matching cardAction.id
	key    string // the key to press, as shown in the status bar
	label  string // what pressing it does
	why    string // why gummi recommends it for the current state
	detail string // option detail rendered beside the label
	danger bool   // the option crosses a destructive boundary
	// web is detail as the web face shows it, for a row whose detail
	// names terminal keys ("x resolves one"): the same sentence with the
	// act named instead. Empty means detail reads the same on both faces
	// (webDetail). Set through keyed, never by hand.
	web string
}

// webDetail is the option detail in the words the web face shows.
func (a nextAction) webDetail() string {
	if a.web != "" {
		return a.web
	}
	return a.detail
}

func nextStep(id, key, label, detail string) nextAction {
	return nextAction{id: id, key: key, label: label, why: detail, detail: detail}
}

// nextInput is the in-memory state the suggestions derive from. All
// fields come from what the Update loop already tracks.
type nextInput struct {
	// publish is whether this board publishes at all (gh found signed in,
	// Shell.publishOffered) — the publish rows' first condition
	publish bool
	// agentPublish is whether a person allowed the session to publish its
	// own branch (DESIGN §22.14)
	agentPublish bool
	// workflows is whether the session is offered workflows; workflowsSet
	// whether that is its own switch
	workflows, workflowsSet bool
	stage                   domain.Stage
	kind                    domain.Kind
	landed                  bool
	// mergeChoice: the card may land as a merge commit keeping its commits
	// as well as a squash (domain.Feature.Offers), so the landing's wording
	// says both are on offer
	mergeChoice bool
	// mainCheckout is a freeform card minted into the main checkout: no
	// branch, no worktree, its turns loose in the checkout the person
	// works in (DESIGN §19). It is what the answer set reads instead of
	// hasWorktree — which is false for such a card by definition.
	mainCheckout bool
	// home is an agent (domain.Feature.Home): a main-checkout card whose
	// checkout is its own home, with no diff a person reads and nothing
	// a spec could continue.
	home bool
	// hasWorktree is whether the card's worktree exists on disk right
	// now — the same question cardactions' own attach row asks, and the
	// only thing that makes attaching a raw agent CLI possible.
	hasWorktree bool

	attn attnKind // "" when the feature has no attention item
	// freeformBusy is the card's session mid-turn.
	freeformBusy bool
	// cardOpen is whether the reader is on the card page itself rather
	// than the board, for the actions that would only re-open the surface
	// already under the cursor.
	cardOpen bool

	// agentWired is whether this board can run an agent at all (a nil
	// engine cannot). It gates the freeform card's model row: a session
	// switch to another agent or model needs a backend to start, and
	// without one the row would only ever refuse what it offered.
	agentWired bool
	// otherRepos is whether the workspace has more than one repository,
	// which is what makes attaching one to a session an offer at all.
	otherRepos bool

	openDiffComments int // unresolved diff annotations (hold a landing)

	pullRequest domain.PullRequestRef // the card's linked outbound PR, empty when unlinked

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
	// adopted marks a card minted onto a branch gummi did not cut
	// (DESIGN §10 D22), which changes which ending leads: handing an
	// inherited branch back is the default, and squashing somebody else's
	// commits onto the trunk is the deliberate second answer.
	adopted bool

	// --- what a finished card says about itself -------------------------
	//
	// A done card used to say nothing at all: both the answer set and the
	// narration returned early for it, so the surface that holds every
	// one of these facts showed none of them at the moment a reader came
	// looking for exactly this.

	// ending is how the card left gummi, resolved through the card's own
	// predicate so the block, the board badge and `status` cannot
	// disagree.
	ending domain.Ending
	// continuedAs is the spec a freeform session's work went on as
	// (domain.Feature.ContinuedAs): a hand-off whose branch lands through
	// that card, never from this one.
	continuedAs domain.FeatureID
	// endedAt is when the card reached done, read from the transition
	// record rather than from any one ending's stamp — a landing has no
	// stamp of its own, and the transition is the fact all three share.
	endedAt time.Time
	// commit is the squash commit a landing created (LandedSHA). It has
	// been recorded since the first merge and shown nowhere.
	commit string
	// spend is what the card cost.
	spend float64

	// --- answers the server would refuse --------------------------------
	//
	// Each of these names a reason the gate's forward answer cannot be
	// taken right now, so the picker says so instead of offering a row
	// that comes back refused.

	// stackBlocker is the unlanded card below this one in its stack,
	// which has to land first (stack.LandBlocker).
	stackBlocker domain.FeatureID
	// names says another card's name in a sentence (Shell.cardName). Nil
	// in a test scaffold, where name falls back to the card's number.
	names func(domain.FeatureID) string
	// landConflicts are the files the last landing attempt conflicted
	// in; non-nil once a landing hit conflicts, until a rebase clears it.
	landConflicts []string
	// landRefused is why a landing would be refused right now (merge.go's
	// landingRefusal — the floor every landing door reads), "" when the
	// card may land. The action menu offers its landing entries only when
	// it is "" (cardActionsFor), so no door lists a landing the same
	// board would then refuse.
	landRefused string

	// attnText is the attention item's own sentence — for a failure, the
	// cause, which the decision's question names rather than leaving it
	// in the thread alone.
	attnText string
	// drifted is the card's fork drift (featureRow.Drift): its base no
	// longer carries the commit it forked from, so every stage session is
	// refused until a rebase clears it. driftForkedFrom names the unlanded
	// branch the card really forked from, when that is the cause.
	drifted         bool
	driftForkedFrom string
	// driftMissingBase names the base the card was cut from when the
	// repository no longer has that branch.
	driftMissingBase string
}

// closed reports whether the card has ended — landed, or at done by any
// route. It is the one predicate the three surfaces that used to test
// this inline now share (the answer set, the narration, the decision), so
// none of them can decide a finished card is finished and another decide
// it is not.
func (in nextInput) closed() bool {
	return in.landed || in.stage == domain.StageDone
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

// doneAt is when the card reached done, from the transitions table — the
// LAST such edge, because a card can be closed, adopted back and closed
// again, and the ending on screen is the current one.
//
// It is read from history rather than from an ending's own stamp because
// only two of the three endings have one: a hand-off stamps HandedOffAt
// and a drop stamps GoalDroppedAt, while a landing stamps only the
// commit. The transition is the fact all three share. Zero when the
// history does not reach done — a landed card still sitting at verify,
// or a scaffold with no history at all — and every caller prints the
// date only when it is non-zero.
func doneAt(hist []state.TransitionRecord) time.Time {
	var at time.Time
	for _, t := range hist {
		if t.To == domain.StageDone {
			at = t.At
		}
	}
	return at
}

// nextInputFor assembles a feature's nextInput from board and session
// state already held in memory.
func (m *Shell) nextInputFor(r featureRow) nextInput {
	in := nextInput{
		stage:            r.F.Stage,
		kind:             r.F.Kind,
		mergeChoice:      r.F.Offers(domain.LandMerge),
		landed:           r.Landed,
		mainCheckout:     r.F.MainCheckout,
		home:             r.F.Home,
		hasWorktree:      r.HasWorktree,
		openDiffComments: r.OpenDiffComments,
		publish:          m.publishOffered(),
		pullRequest:      r.F.PullRequest,
		base:             m.baseBranch(r.F),
		branch:           r.F.BranchName(),
		adopted:          r.F.Adopted(),
		ending:           r.F.Ending(r.Landed),
		continuedAs:      r.F.ContinuedAs,
		commit:           r.F.LandedSHA,
		spend:            r.F.Spend.Credits,
		endedAt:          doneAt(r.History),
		stackBlocker:     m.stackLandBlocker(r.F.ID),
		names:            m.cardName,
		landConflicts:    m.landConflicts[r.F.ID],
	}
	if it, ok := m.inbox.get(r.F.ID); ok {
		in.attn, in.attnText = it.Kind, it.Text
	}
	// a session's model switch needs an agent to start, and a board with
	// no engine has none to start (cardactions.go's model row).
	in.agentWired = m.engine != nil
	in.agentPublish = m.engine != nil && r.AgentPublish
	in.workflows, in.workflowsSet = r.Workflows, r.WorkflowsSet
	in.otherRepos = m.engine != nil && len(m.repoNames) > 0
	if r.Drift != nil {
		in.drifted, in.driftForkedFrom, in.driftMissingBase = true, r.Drift.ForkedFrom, r.Drift.MissingBase
	}
	in.cardOpen = m.cardOpen
	if r.F.IsFreeform() && m.engine != nil {
		if ff := m.engine.Freeform(r.F.ID); ff != nil {
			in.freeformBusy = ff.Snapshot().Busy
		}
	}
	// last, since the floor reads the answer set this input yields
	in.landRefused = m.landingRefusalIn(r.F, r, true, &in)
	return in
}

// stackLandBlocker is the unlanded card below id in its stack — the one
// stack.LandBlocker names, and merge.go's landing refuses on — read from
// the board's own per-card stack rows rather than asked of the engine on
// the render path. "" when id is at the bottom, in no stack, or every
// card below it has landed.
func (m *Shell) stackLandBlocker(id domain.FeatureID) domain.FeatureID {
	me, ok := m.stackRows[id]
	if !ok || me.Pos <= 1 {
		return ""
	}
	var blocker domain.FeatureID
	pos := me.Pos
	for other, sr := range m.stackRows {
		if sr.ID != me.ID || sr.Pos >= me.Pos || sr.Landed {
			continue
		}
		if blocker == "" || sr.Pos < pos {
			blocker, pos = other, sr.Pos
		}
	}
	return blocker
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
	// Not on a freeform card. Landing through a PR is a route to the
	// verify→done gate (DESIGN §3), and a freeform card has neither — what
	// "gummi follows the PR and waits" would mean for a card with no gate
	// to wait at is a question nobody has answered yet (§19.6), and an
	// unanswered route is worse offered than withheld.
	if in.kind == domain.KindFreeform {
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

// stageActions is the card's ANSWER SET: the answers to "what now", and
// nothing else — read the diff, land it, hand it off, stop a turn, answer
// a question, top up a budget.
//
// What is NOT here is as load-bearing as what is. Reading surfaces (the
// diff, the thread) are tabs on the card page, and card housekeeping (the
// envelope, attach, rebase, the PR verbs) lives in the action inventory
// and the "/" menu.
//
// It stays a pure function of nextInput — table-tested, free, no agent
// anywhere near it.
func stageActions(in nextInput) []nextAction {
	acts := stageAnswers(in)
	if !in.drifted || in.closed() {
		return acts
	}
	if in.freeformBusy {
		return acts
	}
	return driftActions(in, acts)
}

// driftActions leads a drifted card's stop with the one answer that can
// work: the rebase. Every other answer the stop has stays behind it, since
// each is refused or allowed on its own terms.
func driftActions(in nextInput, acts []nextAction) []nextAction {
	why := in.landBase() + " no longer carries the commit this card forked from — nothing runs until this card's own commits are replayed onto it"
	if in.driftForkedFrom != "" {
		why = "this card forked from " + in.driftForkedFrom + ", which has not landed on " + in.landBase() +
			" — nothing runs until it is rebased onto " + in.landBase() + " (or " + in.driftForkedFrom + " lands)"
	}
	if in.driftMissingBase != "" {
		why = "this card was cut from " + in.driftMissingBase + ", which no longer exists, so it is read against " + in.landBase() +
			" — nothing runs until this card's own commits are replayed onto it (or " + in.driftMissingBase + " is brought back)"
	}
	out := []nextAction{nextStep("rebase", "r", "rebase onto "+in.landBase(), why)}
	for _, a := range acts {
		if a.id == "rebase" {
			continue // already leading
		}
		out = append(out, a)
	}
	return out
}

// stageAnswers is stageActions before a drifted card's rebase is put in
// front of it.
func stageAnswers(in nextInput) []nextAction {
	// A card that has ended answers for its ending.
	if in.closed() {
		return closedActions(in)
	}
	// A retired workflow card is read-only: nothing runs it again, and the
	// answers it has left are its endings.
	if in.kind != domain.KindFreeform {
		return legacyActions(in)
	}

	// failures, budget stops, and questions come before the session's own
	// answers.
	switch in.attn {
	case attnFailure:
		why := "the session errored — your next line starts a fresh turn"
		if in.attnText != "" {
			why = "the session errored (" + in.attnText + ") — your next line starts a fresh turn"
		}
		// the session has already stopped: this is the answer that leaves
		// it so, and takes the card off the inbox
		return []nextAction{nextStep("settle", "", "stop here", why)}
	case attnBudget:
		// the honest answer to an exhausted envelope, offered where the
		// stop is rather than as a pointer at the inbox tab: raise it and
		// carry on. The inbox reaches the same top-up with u.
		return []nextAction{nextStep("topup", "", "top up and go on",
			"raise the budget — the session carries on with your next line")}
	case attnQuestion:
		return []nextAction{answerIt()}
	}

	// A turn in flight owns the screen. Stopping it is a real answer here:
	// nothing else on this board would stop an interactive session, and a
	// reader who can see a turn going the wrong way should not have to pay
	// it out.
	if in.freeformBusy {
		return []nextAction{nextStep("pause", "p", "stop this turn",
			"stop it mid-turn — whatever it has written stays in the worktree, uncommitted")}
	}
	// Saying what a session's answers are matters because it has no gate
	// to teach the reader what comes next: the two ends of its review
	// loop, and the ending that keeps the branch.
	//
	// An agent (a home of its own) has no diff a person reads and nothing
	// to land: closing it keeps its home.
	if in.home {
		return []nextAction{nextStep("handoff", "h", "hand off",
			"close the agent — its home stays where it is, with everything in it")}
	}
	// A session minted into the main checkout: there is a diff to read
	// (the loose work in the checkout) and an ending (hand-off keeps it,
	// loose, for the person to commit) — but no landing, because there is
	// no branch to squash.
	if in.mainCheckout {
		return []nextAction{
			nextStep("diff", "d", "read the diff", "what the session has left loose in the main checkout"),
			nextStep("handoff", "h", "hand off",
				"close the card — its work stays loose in the main checkout, yours to commit"),
		}
	}
	// Before anything has run there is nothing to read and nothing to
	// land — the composer's placeholder is what says how to start.
	if !in.hasWorktree {
		return nil
	}
	msgNote := "you review and approve the message"
	if in.mergeChoice {
		msgNote = "a squash has you review and approve the message; a merge commit takes git's own"
	}
	return []nextAction{
		nextStep("diff", "d", "read the diff", "what it has written on this card's branch so far"),
		nextStep("merge", "m", "land it on "+in.landBase(),
			landWhy(in.mergeChoice, "squash-merge the branch", "land the branch")+" — uncommitted work has to be committed first, with your own message; "+msgNote),
		nextStep("handoff", "h", "hand off",
			"close the card without landing — a branch nothing else holds is kept"),
	}
}

// legacyActions is the answer set of a retired workflow card (a feature,
// bug, research or goal card from before sessions): it is never run
// again, so what is left is to put it away — hand it off, keeping its
// branch, or land the branch on a person's read of its diff.
func legacyActions(in nextInput) []nextAction {
	if in.kind == domain.KindResearch || in.kind == domain.KindGoal {
		return nil
	}
	if !in.hasWorktree {
		return []nextAction{nextStep("handoff", "h", "hand off",
			"close the retired card — gummi no longer runs a workflow card")}
	}
	return []nextAction{
		nextStep("diff", "d", "read the diff", "what the card left on its branch"),
		nextStep("handoff", "h", "hand off",
			"close the retired card and keep "+in.keptBranch()+" — gummi no longer runs a workflow card"),
		nextStep("merge", "m", "land it on "+in.landBase(),
			landWhy(in.mergeChoice, "squash-merge the branch", "land the branch")+" on your read of its diff"),
	}
}

// closedActions is the answer set for a card that has ended.
//
// Both halves of this used to be a bare return: a landed card was offered
// clean-up and nothing else, and a card at done was offered nothing at
// all — so the page that holds the spec, the branch, the commit and the
// whole history went silent at the one moment a reader opens it to ask
// what happened. The rows differ by ending because the answers do: a
// landed branch can be tidied away, a kept one can still be landed, and a
// dropped card can be taken back.
//
// Deleting stays out of it: that verb is in the inventory under D, where
// the destructive ones live, and a closing block offering "delete" as a
// peer of "open a bug from this" invites the keypress it should be making
// someone think about. Clean-up keeps the danger paint it has always had.
func closedActions(in nextInput) []nextAction {
	var out []nextAction
	switch in.ending {
	case domain.EndingHandedOff:
		// m has always worked on a handed-off card, and the card it
		// happened to has never said so. Landing it retracts the stamp —
		// except on a session continued as a spec, whose work lands through
		// that spec (domain.Feature.MayLandAfterAll).
		if in.continuedAs == "" {
			out = append(out, nextStep("merge", "m", "land it after all",
				"changed your mind — "+landWhy(in.mergeChoice, "squash-merge "+in.keptBranch()+" onto "+in.landBase(), "land "+in.keptBranch()+" onto "+in.landBase())))
		}
	}
	// The follow-up, on every ending. A landed card that breaks ten
	// minutes later is the most ordinary thing that happens after done,
	// and the answer — a fresh bug card — was reachable only by pressing
	// n and retyping context this card is already holding.
	if in.kind != domain.KindGoal {
		carries := "spec, branch and thread"
		if in.kind == domain.KindFreeform {
			// a session has no spec to carry
			carries = "branch and thread"
		}
		out = append(out, nextStep("newbug", "", "start a follow-up session",
			"a fresh session carrying this card's "+carries))
	}
	// Clean-up last, because it is the destructive one and a destructive
	// action never gets to be what enter runs by default.
	//
	// Gated on landed rather than on the worktree: Landed is only
	// computed where a worktree exists (msgs.go's canHaveLanded), so it
	// drops to false the moment clean-up removes one — which is exactly
	// the moment there is nothing left to clean. A card whose ending is
	// landed only because LandedSHA survives that removal is already
	// tidy, and offering it the row would be offering a refusal.
	if in.landed || (in.hasWorktree && in.stage == domain.StageDone && !in.mainCheckout) {
		help := "closed — remove the worktree, and the branch unless it is the only copy of its work"
		if in.landed {
			help = "branch landed on " + in.landBase() + " — remove the worktree and branch"
		}
		a := nextStep("clean", "c", "clean up", help)
		a.danger = true
		out = append(out, a)
	}
	return out
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

// repeatsLastPark reports whether the card's newest park already says
// detail, with no run, crossing or answer since. Each retry of a stage
// that fails the same way used to append another identical "parked — …"
// row, so the thread filled with copies of one stop.
func (m *Shell) repeatsLastPark(id domain.FeatureID, detail string) bool {
	if m.store == nil {
		return false
	}
	evs, err := m.store.Events(context.Background(), id)
	if err != nil {
		return false
	}
	for i := len(evs) - 1; i >= 0; i-- {
		switch evs[i].Kind {
		case state.EventStageEnter, state.EventGate, state.EventAsk:
			return false
		case state.EventPark:
			var p state.ParkPayload
			if json.Unmarshal([]byte(evs[i].Payload), &p) != nil {
				return false
			}
			return p.Detail == detail
		}
	}
	return false
}
