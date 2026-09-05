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
	case domain.StagePlan:
		return true, m.onCritiqueStageDone(id, stage)
	case domain.StageVerify:
		return true, m.onVerifyDone(id)
	case domain.StageImplement, domain.StageFix, domain.StageInvestigate:
		// the work stage ends with its own critique pass — what the Review
		// stage used to be, minus the transition.
		return true, m.onCritiqueStageDone(id, stage)
	}
	return false, nil
}

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
		m.raiseAttention(id, attnGate, verifyGateReason(id.Kind()))
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

// onCritiqueStageDone drives the critique loop for any stage that ends
// with one: the plan stage, and — since Review stopped being a stage —
// the work stage. A stage that has just written its output gets
// critiqued; a stage whose critique has landed gets judged, and the
// verdict either raises the gate, re-runs the stage under its cap, or
// escalates.
//
// The round kind comes from the stage (engine.CritiqueRoundKind), so the
// plan critique burns plan rounds and the work stage's critique burns
// review rounds — the same budgets each loop had when the second one was
// a stage of its own.
func (m *Shell) onCritiqueStageDone(id domain.FeatureID, stage domain.Stage) tea.Cmd {
	kind, ok := engine.CritiqueRoundKind(stage)
	if !ok {
		return nil
	}
	maxRounds := verdict.MaxRounds(kind)
	noun := critiqueNoun(stage)

	s := m.engine.Get(id)
	if s == nil {
		return nil
	}
	snap := s.Snapshot()
	if !snap.Critique {
		// the output was just written (or reworked): critique it before
		// raising the gate.
		return m.critiqueStep(id, stage, true, noun+" written → critiquing")
	}

	// gatepolicy owns the rule, so this loop and the headless driver's
	// judgeCritique can never drift into two ideas of what a verdict means.
	out := gatepolicy.Decide(gatepolicy.Input{
		Stage:         stage,
		Forward:       forwardEdge(snap.Feature),
		Kind:          id.Kind(),
		Verdict:       sessionVerdict(snap),
		Corrective:    m.round(id, kind),
		CorrectiveMax: maxRounds,
		WorkStage:     workflow.WorkStage(id.Kind()),
	})
	switch out.Action {
	case gatepolicy.RaiseGate:
		// the plan gate: a person crosses it (or autopilot does)
		if err := rounds.Reset(context.Background(), m.roundStore, id, kind); err != nil {
			return m.writeHalt(id, err)
		}
		m.setRound(id, kind, 0)
		text := noun + " critiqued: clean — review & approve"
		if cmd, attempted := m.autopilotCrossGate(snap.Feature, text); attempted {
			return cmd
		}
		m.raiseAttention(id, attnGate, text)
		return nil
	case gatepolicy.Advance:
		if err := rounds.Reset(context.Background(), m.roundStore, id, kind); err != nil {
			return m.writeHalt(id, err)
		}
		m.setRound(id, kind, 0)
		// Shape is interactive, so research's crossing steps the stage and
		// stops rather than running what is behind it; every other forward
		// edge here lands on an autonomous stage that may start at once.
		if workflow.Interactive(out.Stage) {
			return m.autoStepStage(id, out.Stage, "critique passed → "+string(out.Stage), "review")
		}
		return m.autoStep(id, out.Stage, "critique passed → "+string(out.Stage), "review")
	case gatepolicy.BounceToWork:
		// persist the burned round before it lands in the fast path, so a
		// mid-loop resume observes it.
		if err := rounds.Bump(context.Background(), m.roundStore, id, kind); err != nil {
			return m.writeHalt(id, err)
		}
		m.setRound(id, kind, m.round(id, kind)+1)
		m.burnCorrective(id, out)
		return m.critiqueStep(id, stage, false, "critique requested changes → reworking (round "+itoa(m.round(id, kind))+")")
	default: // Park: the cap was hit, or the verdict was unclear
		if err := rounds.Reset(context.Background(), m.roundStore, id, kind); err != nil {
			return m.writeHalt(id, err)
		}
		m.setRound(id, kind, 0)
		if out.Reason == "critique-changes-cap" {
			m.raiseEscalation(id, noun+" critique still requesting changes after "+itoa(maxRounds)+" rounds — review it manually")
			m.notice = noticeMsg{text: string(id) + " " + noun + " critique escalated after " + itoa(maxRounds) + " rounds", isErr: true}
			return nil
		}
		m.raiseEscalation(id, noun+" critique finished with no clear verdict — review it manually")
		return nil
	}
}

// critiqueNoun names what a stage's critique is judging, for the notices
// and inbox lines the reader sees.
func critiqueNoun(stage domain.Stage) string {
	switch stage {
	case domain.StagePlan:
		return "plan"
	case domain.StageInvestigate:
		return "investigation"
	}
	return "diff"
}

// critiqueStep re-runs a stage as the loop's next leg — the critique pass
// (critique=true) or the rework addressing its findings — with no stage
// transition; autoStep's analog inside a single stage.
func (m *Shell) critiqueStep(id domain.FeatureID, stage domain.Stage, critique bool, note string) tea.Cmd {
	kind, ok := engine.CritiqueRoundKind(stage)
	if !ok {
		return nil
	}
	// the kickoff is decided here, on the update loop, not in the cmd
	// goroutine: a burned round means this critique follows a rework, so
	// it burns down the prior threads instead of re-judging from scratch.
	var kickoff string
	if critique && m.round(id, kind) > 0 {
		kickoff = reCritiqueNote
	}
	rework := replanNote
	if stage != domain.StagePlan {
		rework = verdict.ReworkNote
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
			err = m.engine.RunWith(f, rework)
		}
		if err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		return noticeMsg{text: string(id) + ": " + note}
	}
}

// autoStep transitions a feature to the next loop stage and auto-runs
// it, all in one command.
//
// actor names who is crossing, and it is a parameter rather than the
// constant "review" it used to hardcode. The review→fix loop's own
// continuations are genuinely the loop's, and still pass "review". But
// startAutopilot (autopilot.go) reaches this same helper to run a card a
// person has just handed over with `A`, and filing those crossings under
// the review loop misattributed them twice over: the audit trail named a
// loop that had nothing to do with it, and every reader of the actor —
// the thread's own history line among them — repeated the mistake. Rows
// written before this keep saying "review"; recorded history is not
// rewritten to match a later correction.
func (m *Shell) autoStep(id domain.FeatureID, to domain.Stage, note, actor string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		m.dropSession(id) // the completed session is stale
		if _, err := m.store.Transition(ctx, id, to, actor); err != nil {
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
//
// actor carries the same meaning, and exists for the same reason, as
// autoStep's above.
func (m *Shell) autoStepStage(id domain.FeatureID, to domain.Stage, note, actor string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		m.dropSession(id) // the completed session is stale
		if _, err := m.store.Transition(ctx, id, to, actor); err != nil {
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

// verifyGateReason is what a clean verify asks the reader to do, in the
// words of the act itself.
//
// It is not only the inbox line: raiseAttention writes the reason into
// the card's events as the park receipt, so it stays in the card's
// history. A research card carries no branch and has nothing to land —
// the picker's own row already says "mark done" — so a fixed "land on
// main" left the card permanently recorded as having been asked to do
// something it cannot.
func verifyGateReason(k domain.Kind) string {
	if k == domain.KindResearch {
		return "verify passed — review & mark it done"
	}
	return "verify passed — review & land on main"
}

// forwardEdge is the primary forward stage out of f's current stage — the
// same edge engine.Advance would take, and the same one the headless
// driver's own forwardEdge names. A critique's pass advances along it
// rather than along a stage hardcoded here, because the answer differs by
// kind: implement passes to verify, investigate passes to shape.
func forwardEdge(f domain.Feature) domain.Stage {
	nexts := workflow.Next(f.Kind, f.Stage, f.Skip)
	if len(nexts) == 0 {
		return f.Stage
	}
	return nexts[len(nexts)-1]
}
