package ui

import (
	"context"
	"strconv"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/gatepolicy"
	"github.com/morphis/gummi/internal/rounds"
	"github.com/morphis/gummi/internal/verdict"
	"github.com/morphis/gummi/internal/workflow"
)

// The verdict grammar — the type, both regexes, parse, the session
// helpers, the round caps, and the kickoff notes — lives in the shared
// internal/verdict package (it is the seam the headless driver uses
// too). The unexported names below are thin aliases and one-line
// wrappers so the review-loop call sites and tests read unchanged; they
// carry no logic of their own.
type reviewVerdict = verdict.Verdict

const (
	verdictUnclear = verdict.Unclear
	verdictPass    = verdict.Pass
	verdictChanges = verdict.Changes
	verdictFail    = verdict.Fail
	verdictBlocked = verdict.Blocked
)

var (
	maxReviewRounds = verdict.MaxRounds(domain.RoundKindReview)
	maxPlanRounds   = verdict.MaxRounds(domain.RoundKindPlan)
)

const (
	replanNote     = verdict.ReplanNote
	reCritiqueNote = verdict.ReCritiqueNote
)

func parseVerdict(text string) reviewVerdict { return verdict.Parse(text) }

func sessionVerdict(snap engine.Snapshot) reviewVerdict { return verdict.SessionVerdict(snap) }

// onAutonomousDone drives the review loop when an autonomous session
// finishes. It returns (handled, cmd): handled means the loop consumed
// this completion (so the caller must not also raise a generic gate),
// and cmd is any automatic follow-up (bounce+re-implement, re-review, or
// advance), which may be nil (e.g. an escalation with no follow-up).
func (m *Shell) onAutonomousDone(id domain.FeatureID, stage domain.Stage) (bool, tea.Cmd) {
	switch stage {
	case domain.StageReview:
		return true, m.onReviewDone(id)
	case domain.StagePlan:
		return true, m.onPlanDone(id)
	case domain.StageVerify:
		return true, m.onVerifyDone(id)
	case domain.StageImplement, domain.StageFix:
		// only auto-continue work stages that are part of a review loop
		if m.round(id, domain.RoundKindReview) > 0 {
			return true, m.autoStep(id, domain.StageReview, "re-reviewing")
		}
	case domain.StageInvestigate:
		// research's work leg: mirrors Implement/Fix, but the forward edge
		// is to shape (never straight to review — research has no direct
		// investigate→review edge), and shape is interactive (a chat stage,
		// unlike review), so the stage only steps forward — the human's
		// chat turn opens it via the normal Enter, not an auto-run. Only
		// auto-continue when this completion is part of a review loop (a
		// review→investigate bounce burned a round); a fresh, loop-free
		// investigate still raises the generic gate, matching Implement/Fix.
		if m.round(id, domain.RoundKindReview) > 0 {
			return true, m.autoStepStage(id, domain.StageShape, "re-shaping")
		}
	}
	return false, nil
}

// onReviewDone reads the review verdict and either advances (pass),
// bounces to implement for another round (changes, under the cap), or
// escalates to the human (changes past the cap, or an unclear verdict).
func (m *Shell) onReviewDone(id domain.FeatureID) tea.Cmd {
	s := m.engine.Get(id)
	if s == nil {
		return nil
	}
	out := gatepolicy.Decide(gatepolicy.Input{
		Stage:         domain.StageReview,
		Kind:          id.Kind(),
		Verdict:       sessionVerdict(s.Snapshot()),
		Corrective:    m.round(id, domain.RoundKindReview),
		CorrectiveMax: maxReviewRounds,
		WorkStage:     workflow.WorkStage(id.Kind()),
	})
	switch out.Action {
	case gatepolicy.Advance:
		// clear the persisted count so the next review loop starts fresh.
		if err := rounds.Reset(context.Background(), m.roundStore, id, domain.RoundKindReview); err != nil {
			return m.writeHalt(id, err)
		}
		m.setRound(id, domain.RoundKindReview, 0)
		return m.autoStep(id, out.Stage, "review passed → verify")
	case gatepolicy.BounceToWork:
		// persist the burned round before it lands in the fast path, so a
		// mid-loop resume observes it.
		if err := rounds.Bump(context.Background(), m.roundStore, id, domain.RoundKindReview); err != nil {
			return m.writeHalt(id, err)
		}
		m.setRound(id, domain.RoundKindReview, m.round(id, domain.RoundKindReview)+1)
		m.burnCorrective(id, out)
		return m.autoStep(id, out.Stage, "review requested changes → fixing (round "+itoa(m.round(id, domain.RoundKindReview))+")")
	default: // gatepolicy.Park: the cap was hit, or the verdict was unclear
		if err := rounds.Reset(context.Background(), m.roundStore, id, domain.RoundKindReview); err != nil {
			return m.writeHalt(id, err)
		}
		m.setRound(id, domain.RoundKindReview, 0)
		if out.Reason == "review-changes-cap" {
			m.raiseEscalation(id, "review still requesting changes after "+itoa(maxReviewRounds)+" rounds — needs you")
			m.notice = noticeMsg{text: string(id) + " review escalated after " + itoa(maxReviewRounds) + " rounds", isErr: true}
			return nil
		}
		// no clear verdict: don't guess — reset the loop and hand it to
		// the human.
		m.raiseEscalation(id, "review finished with no clear verdict — review manually")
		return nil
	}
}

// burnCorrective records an outcome that spent a corrective round against
// the card's unified budget — the running total of everything that is the
// same work done again: review bounces, verify bounces, conflict
// handoffs. It is deliberately separate from each loop's own cap, which
// still governs that loop: this counter is what says how much rework a
// card has cost overall, and it is what an unattended run is finally
// stopped by. It never resets mid-run, because "total across" is the
// whole point of it.
//
// Best-effort: the loop's own persisted cap is the one that must not
// drift, and it is written above. Failing to tally here miscounts a
// report; it never re-grants budget.
func (m *Shell) burnCorrective(id domain.FeatureID, out gatepolicy.Outcome) {
	if !out.Burns {
		return
	}
	_ = rounds.Bump(context.Background(), m.roundStore, id, domain.RoundKindCorrective)
	m.setRound(id, domain.RoundKindCorrective, m.round(id, domain.RoundKindCorrective)+1)
}

// onVerifyDone reads the verify verdict and raises the landing gate.
// Verify never auto-advances — landing on main is the human's call —
// but the gate says whether verification held up: a clean pass is
// ready-to-approve, a fail or a shrug is an escalation.
func (m *Shell) onVerifyDone(id domain.FeatureID) tea.Cmd {
	s := m.engine.Get(id)
	if s == nil {
		return nil
	}
	out := gatepolicy.Decide(gatepolicy.Input{
		Stage:     domain.StageVerify,
		Kind:      id.Kind(),
		Verdict:   sessionVerdict(s.Snapshot()),
		WorkStage: workflow.WorkStage(id.Kind()),
		// verify never auto-bounces here: a failed verify always escalates
		// to a human today (gatepolicy documents the eligible-to-bounce
		// rule as dormant; this keeps it switched off).
		VerifyMayBounce: false,
	})
	switch {
	case out.Action == gatepolicy.RaiseGate:
		m.raiseAttention(id, attnGate, "verify passed — review & land on main")
	case out.Reason == "verify-blocked":
		m.raiseEscalation(id, "verify BLOCKED — the environment can't run the verification plan; "+
			"the missing prerequisites are in the "+artifactNoun(id.Kind())+". Fix the environment or tag the plan — re-implementing won't help")
	case out.Reason == "verify-fail":
		// repeat failures warn off the bounce: each prior one bought a
		// full rework round that changed nothing (m.rows is at most one
		// bounce stale here — fine for a warning).
		bounces := 0
		for _, r := range m.rows {
			if r.F.ID == id {
				bounces = verifyBounces(r.History, r.F.Kind)
				break
			}
		}
		if bounces >= 1 {
			m.raiseEscalation(id, "verify FAILED for the "+ordinal(bounces+1)+" time — "+
				"re-implementing is unlikely to help; check the environment and the verification plan before bouncing")
		} else {
			m.raiseEscalation(id, "verify FAILED — read the evidence and bounce or overrule")
		}
	default: // verify-unclear
		m.raiseEscalation(id, "verify finished with no clear verdict — check the results manually")
	}
	// the session edited the artifact and committed; reload so the gate's
	// row state (landed, open-comment counts) is fresh
	return m.loadRows
}

// onPlanDone drives the plan-critique loop when a Plan-stage session
// finishes. A finished plan writer triggers the critique pass; a
// finished critique either clears the gate (pass), bounces to a replan
// round (changes, under the cap), or escalates to the human (changes
// past the cap, or an unclear verdict). The feature never leaves the
// Plan stage — the loop is invisible to the state machine, and the
// human gate stays at the end of it.

// writeHalt raises a needs-attention notice for a failed plan-rounds
// write-through and stops the loop leg, so the in-memory counter cannot
// drift from the store's record and silently re-grant budget on a later
// resume.
func (m *Shell) writeHalt(id domain.FeatureID, err error) tea.Cmd {
	m.notice = noticeMsg{text: sanitize(err.Error()), isErr: true}
	m.raiseAttention(id, attnFailure, sanitize(err.Error()))
	return nil
}

func (m *Shell) onPlanDone(id domain.FeatureID) tea.Cmd {
	s := m.engine.Get(id)
	if s == nil {
		return nil
	}
	snap := s.Snapshot()
	if !snap.Critique {
		// the plan was just written (or revised): critique it before
		// raising the approval gate.
		return m.planStep(id, true, "plan written → critiquing")
	}
	switch sessionVerdict(snap) {
	case verdictPass:
		// clear the persisted count so the next plan cycle starts fresh.
		if err := rounds.Reset(context.Background(), m.roundStore, id, domain.RoundKindPlan); err != nil {
			return m.writeHalt(id, err)
		}
		m.setRound(id, domain.RoundKindPlan, 0)
		text := "plan critiqued: clean — review & approve"
		if cmd, attempted := m.autopilotCrossGate(snap.Feature, text); attempted {
			return cmd
		}
		m.raiseAttention(id, attnGate, text)
		return nil
	case verdictChanges:
		if m.round(id, domain.RoundKindPlan) >= maxPlanRounds {
			if err := rounds.Reset(context.Background(), m.roundStore, id, domain.RoundKindPlan); err != nil {
				return m.writeHalt(id, err)
			}
			m.setRound(id, domain.RoundKindPlan, 0)
			m.raiseEscalation(id, "plan critique still requesting changes after "+itoa(maxPlanRounds)+" rounds — review the plan manually")
			m.notice = noticeMsg{text: string(id) + " plan critique escalated after " + itoa(maxPlanRounds) + " rounds", isErr: true}
			return nil
		}
		// persist the burned round before it lands in the fast path, so a
		// mid-loop resume observes it.
		if err := rounds.Bump(context.Background(), m.roundStore, id, domain.RoundKindPlan); err != nil {
			return m.writeHalt(id, err)
		}
		m.setRound(id, domain.RoundKindPlan, m.round(id, domain.RoundKindPlan)+1)
		return m.planStep(id, false, "critique requested changes → replanning (round "+itoa(m.round(id, domain.RoundKindPlan))+")")
	default:
		// no clear verdict: don't guess — reset the loop and hand it to
		// the human.
		if err := rounds.Reset(context.Background(), m.roundStore, id, domain.RoundKindPlan); err != nil {
			return m.writeHalt(id, err)
		}
		m.setRound(id, domain.RoundKindPlan, 0)
		m.raiseEscalation(id, "plan critique finished with no clear verdict — review the plan manually")
		return nil
	}
}

// planStep re-runs the Plan stage as the loop's next leg — the critique
// pass (critique=true) or a replan addressing its findings — with no
// stage transition; autoStep's analog inside a single stage.
func (m *Shell) planStep(id domain.FeatureID, critique bool, note string) tea.Cmd {
	// the kickoff is decided here, on the update loop, not in the cmd
	// goroutine: planRounds > 0 means this critique follows a replan, so
	// it burns down the prior threads instead of re-judging from scratch.
	var kickoff string
	if critique && m.round(id, domain.RoundKindPlan) > 0 {
		kickoff = reCritiqueNote
	}
	return func() tea.Msg {
		ctx := context.Background()
		m.dropSession(id) // the completed session is stale
		f, err := m.store.GetFeature(ctx, id)
		if err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		if critique {
			err = m.engine.RunCritique(f, kickoff)
		} else {
			err = m.engine.RunWith(f, replanNote)
		}
		if err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		return noticeMsg{text: string(id) + ": " + note}
	}
}

// autoStep transitions a feature to the next loop stage and auto-runs
// it, all in one command. The transition actor is "review" so the audit
// trail shows the automatic loop.
func (m *Shell) autoStep(id domain.FeatureID, to domain.Stage, note string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		m.dropSession(id) // the completed session is stale
		if _, err := m.store.Transition(ctx, id, to, "review"); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		nf, err := m.store.GetFeature(ctx, id)
		if err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		if err := m.engine.Run(nf); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		return noticeMsg{text: string(id) + ": " + note, reload: true}
	}
}

// autoStepStage transitions a feature to the next loop stage without
// auto-running it — the counterpart to autoStep for a target that is
// interactive (Shape, research's design leg): an interactive stage needs
// a human's chat turn, so the loop only clears the way to it; opening it
// (attachChat) happens on the same plain Enter as any other interactive
// stage entry, exactly like a design-gate approval elsewhere in the TUI.
func (m *Shell) autoStepStage(id domain.FeatureID, to domain.Stage, note string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		m.dropSession(id) // the completed session is stale
		if _, err := m.store.Transition(ctx, id, to, "review"); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		return noticeMsg{text: string(id) + ": " + note, reload: true}
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// ordinal renders 2 → "2nd", 3 → "3rd" for the repeat-failure warning.
func ordinal(n int) string {
	suffix := "th"
	if n%100 < 11 || n%100 > 13 {
		switch n % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return itoa(n) + suffix
}
