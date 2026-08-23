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

// nextAction is one entry in the dashboard's "next" block.
type nextAction struct {
	key   string // the key to press, as shown in the status bar
	label string // what pressing it does
	why   string // why gummi suggests it for the current state
}

// nextInput is the in-memory state the suggestions derive from. All
// fields come from what the Update loop already tracks.
type nextInput struct {
	stage  domain.Stage
	kind   domain.Kind
	landed bool
	quick  bool // the quick route: one-pass spec, approval goes straight to implement

	sess   engine.SessionState // "" when the feature has no live session
	busy   bool                // agent mid-turn
	hasAsk bool                // agent blocked on a structured ask

	attn      attnKind // "" when the feature has no attention item
	escalated bool     // the gate is a loop give-up, not a clean finish

	// verdict is the finished session's outcome (verify: pass/fail),
	// verdictUnclear when none or when the session is gone — then the
	// escalated flag on the gate still carries pass vs not-pass.
	verdict reviewVerdict

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
	if sess := m.sessionFor(r.F.ID); sess != nil && !sess.Interactive {
		in.sess = sess.State()
		snap := sess.Snapshot()
		in.busy = snap.Busy
		in.hasAsk = snap.PendingAsk != nil
		if in.sess == engine.StateDone {
			in.verdict = sessionVerdict(snap)
		}
	}
	for _, res := range m.checks[r.F.ID] {
		if !res.OK {
			in.failedCheck = res.Name
			break
		}
	}
	return in
}

// blockedGate returns the resolve-first action when open review
// comments block the gate (DESIGN §6.1), or nil when g is clear.
func blockedGate(in nextInput) *nextAction {
	if in.openSpecQs > 0 {
		return &nextAction{
			"s", "resolve open comments",
			itoa(in.openSpecQs) + " open in the " + artifactNoun(in.kind) + " block the gate — R requests changes",
		}
	}
	if in.openDiffComments > 0 {
		return &nextAction{
			"d", "resolve diff comments",
			itoa(in.openDiffComments) + " open block the gate — R requests changes, x resolves",
		}
	}
	return nil
}

// nextActions derives the ranked suggestion list — the first entry is
// the recommendation, at most three entries total. Empty means the
// state speaks for itself (an agent mid-run, a done feature).
func nextActions(in nextInput) []nextAction {
	if in.landed {
		return []nextAction{{"c", "clean up", "branch landed on main — remove the worktree and branch"}}
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
				{"enter", "answer the agent", "it asked a question and is blocked on your reply"},
				{"p", "pause", "free the slot — enter re-runs the stage later"},
			}
		}
		return nil
	case engine.StatePaused:
		return []nextAction{
			{"enter", "re-run " + string(in.stage), "the run is paused — a fresh run picks the stage back up"},
			{"a", "attach the agent CLI", "work in the worktree by hand instead"},
		}
	}

	// failures, budget stops, and questions override stage guidance.
	switch in.attn {
	case attnFailure:
		return []nextAction{
			{"enter", "re-run " + string(in.stage), "the session errored — a fresh run retries the stage"},
			{"a", "attach the agent CLI", "debug it by hand in the worktree"},
		}
	case attnBudget:
		return []nextAction{
			{"i", "open the inbox", "the stage hit its budget — top up (u) or park (x) from there"},
		}
	case attnQuestion:
		return []nextAction{
			{"enter", "attach & answer", "the agent asked a question and is waiting"},
		}
	}

	work := workflow.WorkStage(in.kind) // implement (feature) or fix (bug)
	finished := in.attn == attnGate || in.sess == engine.StateDone

	switch in.stage {
	case domain.StageTodo:
		return []nextAction{{"g", "start", "advance into the design flow"}}

	case domain.StageInvestigate:
		return []nextAction{
			{"enter", "chat with the researcher", "explore the question and shape the doc"},
			{"g", "advance", "move on to " + string(domain.StageShape)},
		}

	case domain.StageShape:
		return []nextAction{
			{"enter", "shape the doc", "converge the findings into the answer"},
			{"g", "advance", "move on to " + string(domain.StageReview)},
		}

	case domain.StageBrainstorm, domain.StageTriage:
		return []nextAction{
			{"enter", "chat with the architect", "explore the problem and candidate approaches"},
			{"g", "advance", "converged? move on to the " + artifactNoun(in.kind)},
		}

	case domain.StageSpec, domain.StageDiagnose:
		acts := []nextAction{
			{"enter", "chat with the architect", "shape the " + artifactNoun(in.kind) + " until it convinces you"},
		}
		if in.quick {
			acts[0].why = "quick route — it drafts the whole spec in one pass; steer and refine"
		}
		if b := blockedGate(in); b != nil {
			return append([]nextAction{*b}, acts...)
		}
		gate := nextAction{"g", "approve", "creates the worktree and starts the agent stages"}
		if in.quick {
			gate.why = "creates the worktree and starts implementing — P first if it outgrew quick"
		}
		return append(acts, gate)

	case domain.StagePlan:
		if !finished {
			return []nextAction{{"enter", "run the planner", "no active run — writes the line-level plan into the spec"}}
		}
		acts := []nextAction{{"s", "read the plan", "it lives in the spec's Implementation notes"}}
		if b := blockedGate(in); b != nil {
			return append(acts, *b)
		}
		why := "plan critiqued clean — start implementing"
		if in.escalated {
			why = "the critique loop gave up — judge the plan yourself before approving"
		}
		return append(acts, nextAction{"g", "approve & " + string(work), why})

	case domain.StageImplement, domain.StageFix:
		if !finished {
			return []nextAction{{"enter", "run " + string(in.stage), "no active run — start (or restart) the stage"}}
		}
		acts := []nextAction{{"d", "review the diff", "spot-check what the " + string(in.stage) + " run produced"}}
		if b := blockedGate(in); b != nil {
			return append(acts, *b)
		}
		return append(acts, nextAction{"g", "advance to review", "hand it to the fresh-context reviewer"})

	case domain.StageReview:
		if !finished {
			return []nextAction{{"enter", "run review", "no active run — start the fresh-context review"}}
		}
		// a review gate is always an escalation: clean verdicts advance
		// automatically, so this review gave up (round cap or no verdict).
		why := "the review loop gave up — read its findings in the " + artifactNoun(in.kind)
		if in.reviewRound >= maxReviewRounds {
			why = "still requesting changes after " + itoa(maxReviewRounds) + " rounds — read the findings yourself"
		}
		acts := []nextAction{{"s", "read the findings", why}}
		if b := blockedGate(in); b != nil {
			return append(acts, *b)
		}
		return append(acts,
			nextAction{"b", "bounce to " + string(work), "send the findings back for another round"},
			nextAction{"g", "advance to verify", "overrule the reviewer if the findings don't hold"})

	case domain.StageVerify:
		if !finished {
			return []nextAction{{"enter", "run verify", "no active run — runs the checks and the verification plan"}}
		}
		if b := blockedGate(in); b != nil {
			return []nextAction{
				*b,
				{"b", "bounce to " + string(work), "or send the open items back as rework"},
			}
		}
		if in.failedCheck != "" {
			return []nextAction{
				{"v", "re-run checks", "'" + in.failedCheck + "' failed on the last manual run"},
				{"enter", "re-run verify", "let the agent chase the failure and update the " + artifactNoun(in.kind)},
				{"b", "bounce to " + string(work), "if the failure is the implementation's fault"},
			}
		}
		// blocked: the environment can't run the plan, so rework can't
		// help — steer at the environment, not the bounce. (Session gone
		// drops this to the fail arm below, same degradation as fail; the
		// inbox gate text still says BLOCKED.)
		if in.verdict == verdictBlocked {
			return []nextAction{
				{"s", "read the blockers", "verify is blocked on the environment — the missing prerequisites are in the " + artifactNoun(in.kind)},
				{"enter", "re-run verify", "after fixing the environment or tagging the plan's env-bound steps"},
				{"g", "land on main", "only if you verified it by hand — verify never proved this build"},
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
					{"s", "read the verify results", "verify has failed " + n + " times — the evidence is in the " + artifactNoun(in.kind)},
					{"g", "land on main", "overrule if the failures don't hold up"},
					{"b", "bounce to " + string(work), "unlikely to help after " + n + " failed verifies — check the environment and the verification plan first"},
				}
			}
			return []nextAction{
				{"s", "read the verify results", why},
				{"b", "bounce to " + string(work), "send the failures back as rework"},
				{"g", "land on main", "overrule if the failure doesn't hold up"},
			}
		}
		if in.kind == domain.KindResearch {
			return []nextAction{
				{"g", "mark done", "verify passed — advance to done"},
				{"b", "bounce to investigate", "not convinced — send it back with comments"},
			}
		}
		why := "squash-merge the branch and mark the " + noun(in.kind) + " done"
		if in.verdict == verdictPass {
			why = "verify passed — " + why
		}
		gate := nextAction{"g", "land on main", why}
		if hint := in.pullRequest.NextStepsHint(true); hint != "" {
			gate = nextAction{"g", "merge the PR", hint}
		}
		return []nextAction{
			gate,
			{"d", "final read of the diff", "one last look before it lands"},
			{"b", "bounce to " + string(work), "not convinced — send it back with comments"},
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
