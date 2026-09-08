package ui

import (
	"context"
	"strconv"
	"strings"

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
	case domain.StageImplement:
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
//
// The clean-pass arm is also where the verified marker is stamped, which
// is the TUI's half of a fact the store had only ever learned from
// engine.Advance: a card driven to its verify gate here never passed
// through that code, so two cards in identical states reported opposite
// `verified` values depending only on whether a human or `gummi run` had
// driven them — and a script polling `gummi status --json … .verified` to
// open a PR never fired for the human-driven one.
func (m *Shell) onVerifyDone(id domain.FeatureID) tea.Cmd {
	s := m.engine.Get(id)
	if s == nil {
		return nil
	}
	out := gatepolicy.Decide(gatepolicy.Input{
		Stage:     domain.StageVerify,
		Kind:      id.Kind(),
		Verdict:   sessionVerdict(s.Snapshot()),
		WorkStage: domain.StageImplement,
		// verify never auto-bounces here: a failed verify always escalates
		// to a human today (gatepolicy documents the eligible-to-bounce
		// rule as dormant; this keeps it switched off).
		VerifyMayBounce: false,
	})
	var stamp tea.Cmd
	switch {
	case out.Action == gatepolicy.RaiseGate:
		m.raiseAttention(id, attnGate, gateReason(domain.StageVerify, id.Kind(), true))
		// The excused-checks cache is otherwise filled on a board load, so
		// the frame that first says "verify passed" — the one most likely
		// to be read, because it is the one that just changed — was the
		// only frame missing the clause naming what the pass did not
		// cover. It arrived a navigation later, which is exactly too late.
		stamp = tea.Batch(m.markVerified(id), m.loadExcusedChecks(id))
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
				bounces = verifyBounces(r.History)
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
	return tea.Batch(stamp, m.loadRows)
}

// markVerified stamps the card's verified marker, the store-side twin of
// what engine.Advance does on the branch that returns StatusNeedsMerge.
// Its semantics are copied from there deliberately: stamp only when
// VerifiedAt is still zero, so re-reaching the gate (a re-run, a restart
// that replays the completion) keeps the FIRST pass's time rather than
// sliding the record forward every time the stage is looked at again.
// The zero test reads the store, not a board row: a row is a snapshot
// that can be a beat stale, and a stale zero here is exactly the read
// that would move a timestamp that must not move.
//
// It is a command because it writes: the store's single sqlite
// connection (SetMaxOpenConns(1)) can block, and the render loop is not a
// place to wait on it — the same reason setGateApproval and setEnvelope
// are commands. Like those it is a side-channel write, so it takes no
// card lock and asks for no reload; nothing on screen renders VerifiedAt.
//
// Research cards are deliberately NOT stamped. They reach this arm too —
// verifyGateReason has a wording for them — but they carry no branch, and
// engine.Advance stamps only where a branch exists and is ahead of main,
// so headless never marks one verified. Stamping here would fix the
// divergence for feature and bug cards and open the identical one, in the
// other direction, for research.
func (m *Shell) markVerified(id domain.FeatureID) tea.Cmd {
	if m.store == nil || id.Kind() == domain.KindResearch {
		return nil
	}
	return func() tea.Msg {
		ctx := context.Background()
		f, err := m.store.GetFeature(ctx, id)
		if err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true, id: id}
		}
		if !f.VerifiedAt.IsZero() {
			return nil
		}
		if err := m.store.SetVerifiedAt(ctx, id, m.now().UTC()); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true, id: id}
		}
		return nil
	}
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
		WorkStage:     domain.StageImplement,
	})
	switch out.Action {
	case gatepolicy.RaiseGate:
		// A critique judges what the stage wrote; the gate it feeds also
		// demands that the stage's required sections exist. A pass with a
		// section still blank is not yet a gate for a person to cross —
		// the stage's writer is the missing step, so run it (naming what
		// is blank) instead of re-raising the decision approving cannot
		// cross. This arm also fires unattended from the engine's own
		// event loop, so the redrafts burn the stage's round budget and
		// the cap hands the card to a human instead of looping.
		if names := m.undraftedGate(snap.Feature); len(names) > 0 {
			if m.round(id, kind) >= maxRounds {
				if err := rounds.Reset(context.Background(), m.roundStore, id, kind); err != nil {
					return m.writeHalt(id, err)
				}
				m.setRound(id, kind, 0)
				m.raiseEscalation(id, noun+" critique passed with "+strings.Join(names, ", ")+
					" still blank after "+itoa(maxRounds)+" rounds — review it manually")
				return m.loadRows
			}
			if err := rounds.Bump(context.Background(), m.roundStore, id, kind); err != nil {
				return m.writeHalt(id, err)
			}
			m.setRound(id, kind, m.round(id, kind)+1)
			return m.redraftUndrafted(id, names)
		}
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
		// the session edited the artifact and committed; reload so the
		// gate's row state — the undrafted sections among them — is fresh
		return m.loadRows
	case gatepolicy.Advance:
		if err := rounds.Reset(context.Background(), m.roundStore, id, kind); err != nil {
			return m.writeHalt(id, err)
		}
		m.setRound(id, kind, 0)
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
	if stage == domain.StagePlan {
		return "plan"
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

// redraftUndrafted re-runs a stage's writer because the gate it feeds is
// blocked on required section(s) the departing stage left blank — the
// half-drafted state only a merged stage can produce, where a critique
// passes cleanly on what was written and the gate still refuses the
// crossing. The kickoff names what is missing so the fresh writer's one
// job is to draft it; when the run finishes, the ordinary loop resumes
// (critique, then the judge).
func (m *Shell) redraftUndrafted(id domain.FeatureID, names []string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		m.dropSession(id) // the completed critique is stale; the writer starts fresh
		f, err := m.store.GetFeature(ctx, id)
		if err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		if err := m.engine.RunWith(f, redraftNote(names, id.Kind())); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		return noticeMsg{text: string(id) + " — re-running " + string(f.Stage) + " to draft " + strings.Join(names, ", "), clearInbox: id}
	}
}

// redraftNote is the kickoff note a redraft rides — the counterpart of
// the replan/rework notes, naming the gate's blank sections so the fresh
// writer knows the one job it has.
func redraftNote(names []string, kind domain.Kind) string {
	pronoun, job := "they are", "draft them"
	if len(names) == 1 {
		pronoun, job = "it is", "draft it"
	}
	return "The card is held at its gate: " + strings.Join(names, ", ") + " " + pronoun +
		" required in the " + artifactNoun(kind) + " and still blank — " + job +
		" before the card can move on"
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

// gateReason is what a card's gate is asking the reader to do, for every
// stage that raises one — the single place that wording is written.
//
// The three surfaces that raise gate items (a live stage completion, a
// clean verify, and the startup reconstruction for cards that reached
// their gate while the TUI was closed) each spelled this out for
// themselves, and two of them spelled it "<stage> finished — review &
// advance" for every stage there is. At verify that is not what the gate
// asks: the next keypress opens the landing dialog and puts the branch on
// main. So whether the reader was told they were about to merge depended
// on whether gummi happened to be running when the card got there, and
// one inbox could show both wordings on two cards at the same gate.
//
// It stays short on purpose — inboxview's rows spend their width on this
// text, and inboxRowText trims the leading stage word the row's own label
// has already printed, so every wording here keeps the stage first.
func gateReason(stage domain.Stage, k domain.Kind, verifyPassed bool) string {
	if stage == domain.StageVerify {
		return verifyGateReason(k, verifyPassed)
	}
	return string(stage) + " finished — review & advance"
}

// verifyGateReason is what a clean verify asks the reader to do, in the
// words of the act itself. gateReason routes the verify stage here rather
// than restating it, so the branch-vs-research split below is made once.
//
// It is not only the inbox line: raiseAttention writes the reason into
// the card's events as the park receipt, so it stays in the card's
// history. A research card carries no branch and has nothing to land —
// the picker's own row already says "mark done" — so a fixed "land on
// main" left the card permanently recorded as having been asked to do
// something it cannot.
//
// passed says whether the caller actually knows verify succeeded. The live
// path does — it is on gatepolicy's clean-pass arm. The startup
// reconstruction does NOT: a session's verdict is not durable (the
// sessions row's verdict column is empty in practice, even for a card
// driven headlessly to a verified branch), so it infers from the stage
// alone. Asserting "verify passed" there would put that sentence on a card
// whose verify failed and invite the reader to land a branch verification
// rejected — worse than the vague wording this replaced. So the outcome
// word is the caller's to supply, while the act ("land on main") is
// unconditional, which is the half the reader was missing.
func verifyGateReason(k domain.Kind, passed bool) string {
	lead := "verify finished"
	if passed {
		lead = "verify passed"
	}
	if k == domain.KindResearch {
		return lead + " — review & mark it done"
	}
	return lead + " — review & land on main"
}

// forwardEdge is the primary forward stage out of f's current stage — the
// same edge engine.Advance would take, and the same one the headless
// driver's own forwardEdge names. A critique's pass advances along it
// rather than along a stage hardcoded here, because the answer differs by
// kind: implement passes to verify, investigate passes to shape.
func forwardEdge(f domain.Feature) domain.Stage {
	nexts := workflow.Next(f.Stage)
	if len(nexts) == 0 {
		return f.Stage
	}
	// nexts[0]: forward edges are listed before rerun edges, and this
	// names the forward one. See Engine.nextStage for why it is no longer
	// the last entry.
	return nexts[0]
}
