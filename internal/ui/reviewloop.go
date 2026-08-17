package ui

import (
	"context"
	"strconv"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/planround"
	"github.com/morphis/gummi/internal/reviewround"
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

const (
	maxReviewRounds = verdict.MaxReviewRounds
	maxPlanRounds   = verdict.MaxPlanRounds
)

const (
	replanNote     = verdict.ReplanNote
	reCritiqueNote = verdict.ReCritiqueNote
)

func parseVerdict(text string) reviewVerdict { return verdict.Parse(text) }

func sessionVerdict(snap engine.Snapshot) reviewVerdict { return verdict.SessionVerdict(snap) }

func lastAssistant(snap engine.Snapshot) string { return verdict.LastAssistant(snap) }

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
		if m.reviewRounds[id] > 0 {
			return true, m.autoStep(id, domain.StageReview, "re-reviewing")
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
	switch sessionVerdict(s.Snapshot()) {
	case verdictPass:
		// clear the persisted count so the next review loop starts fresh.
		if err := reviewround.Reset(context.Background(), m.reviewStore, id); err != nil {
			return m.writeHalt(id, err)
		}
		m.reviewRounds[id] = 0
		return m.autoStep(id, domain.StageVerify, "review passed → verify")
	case verdictChanges:
		if m.reviewRounds[id] >= maxReviewRounds {
			if err := reviewround.Reset(context.Background(), m.reviewStore, id); err != nil {
				return m.writeHalt(id, err)
			}
			m.reviewRounds[id] = 0
			m.raiseEscalation(id, "review still requesting changes after "+itoa(maxReviewRounds)+" rounds — needs you")
			m.notice = noticeMsg{text: string(id) + " review escalated after " + itoa(maxReviewRounds) + " rounds", isErr: true}
			return nil
		}
		// persist the burned round before it lands in the fast path, so a
		// mid-loop resume observes it.
		if err := reviewround.Bump(context.Background(), m.reviewStore, id); err != nil {
			return m.writeHalt(id, err)
		}
		m.reviewRounds[id]++
		return m.autoStep(id, workflow.WorkStage(id.Kind()), "review requested changes → fixing (round "+itoa(m.reviewRounds[id])+")")
	default:
		// no clear verdict: don't guess — reset the loop and hand it to
		// the human.
		if err := reviewround.Reset(context.Background(), m.reviewStore, id); err != nil {
			return m.writeHalt(id, err)
		}
		m.reviewRounds[id] = 0
		m.raiseEscalation(id, "review finished with no clear verdict — review manually")
		return nil
	}
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
	switch sessionVerdict(s.Snapshot()) {
	case verdictPass:
		m.raiseAttention(id, attnGate, "verify passed — review & land on main")
	case verdictBlocked:
		m.raiseEscalation(id, "verify BLOCKED — the environment can't run the verification plan; "+
			"the missing prerequisites are in the "+artifactNoun(id.Kind())+". Fix the environment or tag the plan — re-implementing won't help")
	case verdictFail, verdictChanges:
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
	default:
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
		if err := planround.Reset(context.Background(), m.planStore, id); err != nil {
			return m.writeHalt(id, err)
		}
		m.planRounds[id] = 0
		m.raiseAttention(id, attnGate, "plan critiqued: clean — review & approve")
		return nil
	case verdictChanges:
		if m.planRounds[id] >= maxPlanRounds {
			if err := planround.Reset(context.Background(), m.planStore, id); err != nil {
				return m.writeHalt(id, err)
			}
			m.planRounds[id] = 0
			m.raiseEscalation(id, "plan critique still requesting changes after "+itoa(maxPlanRounds)+" rounds — review the plan manually")
			m.notice = noticeMsg{text: string(id) + " plan critique escalated after " + itoa(maxPlanRounds) + " rounds", isErr: true}
			return nil
		}
		// persist the burned round before it lands in the fast path, so a
		// mid-loop resume observes it.
		if err := planround.Bump(context.Background(), m.planStore, id); err != nil {
			return m.writeHalt(id, err)
		}
		m.planRounds[id]++
		return m.planStep(id, false, "critique requested changes → replanning (round "+itoa(m.planRounds[id])+")")
	default:
		// no clear verdict: don't guess — reset the loop and hand it to
		// the human.
		if err := planround.Reset(context.Background(), m.planStore, id); err != nil {
			return m.writeHalt(id, err)
		}
		m.planRounds[id] = 0
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
	if critique && m.planRounds[id] > 0 {
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
