// Package gatepolicy is the single decision seam for what an autonomous
// stage's finished verdict means for the loop: advance, bounce back to
// work for another round, raise a gate for a human/caller to see, or
// park. The TUI's review loop (internal/ui/reviewloop.go) and the
// headless driver's loop (internal/driver/driver.go) both drive the same
// review→fix→review and verify floors; before this package the rules
// were scattered across the two, and drift between them was a standing
// risk. Decide is pure: no I/O, no store, no session mutation. Callers
// perform the actual transition/session work and use Outcome.Burns to
// decide whether to bump a persisted round counter — that side effect
// stays theirs.
//
// gatepolicy sits below both callers: it imports only internal/domain and
// internal/verdict, never internal/ui, internal/engine, internal/driver,
// internal/state, or any package with side effects. A caller that needs a
// work stage to bounce to supplies it via Input.WorkStage rather than
// gatepolicy importing internal/workflow to compute it.
package gatepolicy

import (
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/verdict"
)

// Action is what a caller should do with a finished autonomous stage.
type Action int

const (
	// Advance moves the card forward in the workflow — an unattended
	// step, with no human decision in the way (e.g. review pass → verify).
	Advance Action = iota
	// BounceToWork rewinds to Outcome.Stage (the caller's WorkStage) for
	// another corrective round. Always paired with Burns == true.
	BounceToWork
	// RaiseGate presents the result at a human/caller checkpoint without
	// advancing past it — the landing gate, or any gate an open thread or
	// comment holds shut.
	RaiseGate
	// Park stops the automatic loop and hands off: an unclear verdict,
	// a cap hit, an environment blocker, or anything else no further
	// automatic round can resolve.
	Park
)

// String names an Action for log lines and test failure output.
func (a Action) String() string {
	switch a {
	case Advance:
		return "advance"
	case BounceToWork:
		return "bounce-to-work"
	case RaiseGate:
		return "raise-gate"
	case Park:
		return "park"
	default:
		return "unknown"
	}
}

// HaltReason names an environment condition that pre-empts ordinary
// verdict reasoning: none of these are a content failure a corrective
// round could fix.
type HaltReason int

const (
	// HaltNone: no environment halt; decide from the verdict as usual.
	HaltNone HaltReason = iota
	// HaltSandboxRefusal: the sandbox refused to start the backend for
	// this stage. Parks at every gate mode — reconfiguring the sandbox,
	// not retrying, is the only way through.
	HaltSandboxRefusal
	// HaltEnvelopeExhausted: the stage ran out of its credit envelope
	// before finishing. A caller tops up and re-runs the stage itself;
	// Decide is not consulted with the exhausted turn's non-verdict, so
	// this exists for completeness rather than a wired call site today.
	HaltEnvelopeExhausted
	// HaltRebaseConflict: the worktree needs a rebase and hit a
	// conflict. Handed to the conflict-resolution session, then
	// re-verified — a real corrective round, so it burns one.
	HaltRebaseConflict
)

// Input is everything Decide needs to resolve one finished autonomous
// stage. It carries no session or store handle — a caller reads whatever
// it needs (verdict, round counts, gate mode, open-thread/comment counts)
// before calling Decide, and performs the resulting side effects itself.
type Input struct {
	// Stage is the autonomous stage that just finished (StageReview or
	// StageVerify drive real branches below; anything else parks).
	Stage domain.Stage
	// Kind distinguishes feature/bug/research — consulted where a rule is
	// kind-specific (e.g. research's decompose checkpoint never auto-
	// mints, so its verify-pass outcome is asserted RaiseGate the same as
	// any other kind, never Advance, regardless of gate mode).
	Kind domain.Kind
	// Verdict is the parsed outcome of the finished session.
	Verdict verdict.Verdict
	// Corrective is the count of corrective rounds already spent in the
	// current review/verify loop (the shared review↔fix↔verify budget —
	// see MaxRounds(domain.RoundKindReview)).
	Corrective int
	// CorrectiveMax is the cap on Corrective; at or past it, a Changes/
	// Fail verdict parks instead of bouncing.
	CorrectiveMax int
	// PlanRounds/PlanMax mirror Corrective/CorrectiveMax for the plan→
	// critique→replan loop's own, separate cap. The plan loop is not
	// routed through Decide (see onPlanDone) — these exist so the Input
	// shape has a place for it without conflating the two budgets, not
	// because Decide consults them today.
	PlanRounds int
	PlanMax    int
	// Gate is the card's stored gate-approval mode (domain.GateGates /
	// domain.GateOff). Decide does not branch on it: which design
	// gates auto-cross versus checkpoint for a caller is decided
	// downstream (engine.Advance / the driver's crossGate), not here.
	// It is carried on Input for callers that want to log/assert it
	// alongside a decision, and so a future rule that does need it has
	// a place to read it from.
	Gate string
	// OpenThreads is the number of open `%%` spec threads blocking a
	// gate; OpenComments is the analogous count of unresolved diff
	// annotations. Either being nonzero holds every gate open — Decide
	// never advances or bounces past one.
	OpenThreads  int
	OpenComments int
	// Halt reports an environment condition that pre-empts the verdict
	// (see HaltReason). HaltNone (the zero value) means "decide from the
	// verdict as usual."
	Halt HaltReason
	// WorkStage is where a BounceToWork outcome sends the card — the
	// caller supplies it (workflow.WorkStage(kind)) rather than
	// gatepolicy importing internal/workflow to compute it.
	WorkStage domain.Stage
	// VerifyMayBounce gates whether a failed verify under the corrective
	// cap may auto-bounce to WorkStage instead of escalating to a human.
	// It exists so the rule table can state that row once, in code and
	// in tests, without switching it on: both callers pass false in this
	// phase, so a failed verify keeps escalating exactly as it does
	// today. Do not flip this to true as a side effect of an unrelated
	// change — turning it on is a real behaviour change of its own.
	VerifyMayBounce bool
}

// Outcome is Decide's answer: what to do, where (for BounceToWork/
// RaiseGate/Park, the stage the caller should act on or report), a
// machine-readable Reason a caller can use to build its own human-facing
// message (Decide never composes user-visible text itself), and whether
// the outcome spends a corrective round.
type Outcome struct {
	Action Action
	Stage  domain.Stage
	Reason string
	Burns  bool
}

// Decide resolves one finished autonomous stage per the rules shared by
// the TUI review loop and the headless driver's loop. It performs no I/O
// and mutates nothing — callers apply the round-counter and transition
// side effects themselves, keyed off the returned Outcome.
func Decide(in Input) Outcome {
	// Environment halts pre-empt any verdict-based reasoning: neither of
	// these is a content failure a retry could fix.
	switch in.Halt {
	case HaltSandboxRefusal:
		// Refused at every gate mode — reconfiguring the sandbox is the
		// only way through, not another corrective round.
		return Outcome{Action: Park, Stage: in.Stage, Reason: "sandbox-refusal"}
	case HaltRebaseConflict:
		// Handed to the conflict session, then re-verified: a real
		// corrective round, so it burns one like any other bounce.
		return Outcome{Action: BounceToWork, Stage: in.WorkStage, Reason: "rebase-conflict", Burns: true}
	}

	// An open spec thread or diff comment holds every gate shut,
	// independent of verdict or gate mode: never auto-cross past one.
	if in.OpenThreads > 0 || in.OpenComments > 0 {
		return Outcome{Action: RaiseGate, Stage: in.Stage, Reason: "open-threads"}
	}

	switch in.Stage {
	case domain.StageReview:
		return decideReview(in)
	case domain.StageVerify:
		return decideVerify(in)
	default:
		// Neither loop stage Decide knows how to drive: nothing to do but
		// hand it back.
		return Outcome{Action: Park, Stage: in.Stage, Reason: "unhandled-stage"}
	}
}

// decideReview resolves a finished review session: pass advances to
// verify; changes bounces to work under the corrective cap, or parks
// (escalated) at it; anything else is an unclear verdict — never guess,
// park.
func decideReview(in Input) Outcome {
	switch in.Verdict {
	case verdict.Pass:
		return Outcome{Action: Advance, Stage: domain.StageVerify, Reason: "review-pass"}
	case verdict.Changes:
		if in.Corrective >= in.CorrectiveMax {
			return Outcome{Action: Park, Stage: in.Stage, Reason: "review-changes-cap"}
		}
		return Outcome{Action: BounceToWork, Stage: in.WorkStage, Reason: "review-changes", Burns: true}
	default:
		return Outcome{Action: Park, Stage: in.Stage, Reason: "review-unclear"}
	}
}

// decideVerify resolves a finished verify session. Blocked is kept fully
// separate from Fail: it means the environment can't run the
// verification plan, not that the work is wrong, so it always parks and
// never burns a corrective round against a problem no retry fixes.
// Fail/Changes only auto-bounces when VerifyMayBounce is set — no
// caller sets it today, so this path always parks, matching the
// escalate-only behaviour both loops have — and only while the
// corrective cap isn't already spent. A research card's verify-pass checkpoint (its decompose
// gate never auto-mints) is still just RaiseGate here, the same as any
// other kind — the "never auto-mint" invariant lives downstream, but it
// depends on this layer never returning Advance for it.
func decideVerify(in Input) Outcome {
	switch in.Verdict {
	case verdict.Pass:
		return Outcome{Action: RaiseGate, Stage: in.Stage, Reason: "verify-pass"}
	case verdict.Blocked:
		return Outcome{Action: Park, Stage: in.Stage, Reason: "verify-blocked"}
	case verdict.Fail, verdict.Changes:
		if in.VerifyMayBounce && in.Corrective < in.CorrectiveMax {
			return Outcome{Action: BounceToWork, Stage: in.WorkStage, Reason: "verify-fail-bounce", Burns: true}
		}
		return Outcome{Action: Park, Stage: in.Stage, Reason: "verify-fail"}
	default:
		return Outcome{Action: Park, Stage: in.Stage, Reason: "verify-unclear"}
	}
}
