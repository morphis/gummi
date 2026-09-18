package ui

// Goals on the board. A goal's implement stage is conducted by the engine
// (Engine.GoalTick); the board's part is to tick it when something that
// matters happened — the goal asked (EventGoal), one of its cards
// stopped, or time passed — and to start the cards each tick names,
// through the same run path every autopilot card takes. A goal's cards
// never reach your inbox: their stops wake the goal instead, and the goal
// reaches you when it is ready.

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/gatepolicy"
	"github.com/morphis/gummi/internal/rounds"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/verdict"
)

// goalPollInterval re-ticks running goals with nothing else to wake them:
// the conductor's idle detection and its catch-up need time to pass.
const goalPollInterval = 10 * time.Second

// goalOf names the goal a card belongs to, from the loaded rows ("" for a
// card on the open board, or one the board has not loaded).
func (m *Shell) goalOf(id domain.FeatureID) domain.FeatureID {
	for _, r := range m.rows {
		if r.F.ID == id {
			return r.F.GoalID
		}
	}
	if m.store != nil && id != "" {
		if f, err := m.store.GetFeature(context.Background(), id); err == nil {
			return f.GoalID
		}
	}
	return ""
}

// queueGoalTick asks for goal to be ticked once this update finishes.
// Callers deep in a handler that returns no command queue here; Update
// drains the queue into the commands it returns.
func (m *Shell) queueGoalTick(goal domain.FeatureID) {
	if goal == "" || m.engine == nil {
		return
	}
	if m.goalTickQueue == nil {
		m.goalTickQueue = map[domain.FeatureID]bool{}
	}
	m.goalTickQueue[goal] = true
}

// drainGoalTicks turns the queued goal ticks into commands, coalescing a
// tick for a goal whose previous tick is still running into one more tick
// when it lands.
func (m *Shell) drainGoalTicks() tea.Cmd {
	if len(m.goalTickQueue) == 0 {
		return nil
	}
	var cmds []tea.Cmd
	for goal := range m.goalTickQueue {
		cmds = append(cmds, m.goalTickCmd(goal))
	}
	m.goalTickQueue = nil
	return tea.Batch(cmds...)
}

type goalTickedMsg struct {
	goal domain.FeatureID
	res  engine.GoalTickResult
	err  error
}

// goalTickCmd runs one conductor tick for goal off the update loop.
func (m *Shell) goalTickCmd(goal domain.FeatureID) tea.Cmd {
	if m.engine == nil {
		return nil
	}
	if m.goalTicking == nil {
		m.goalTicking = map[domain.FeatureID]bool{}
	}
	if m.goalTicking[goal] {
		if m.goalTickAgain == nil {
			m.goalTickAgain = map[domain.FeatureID]bool{}
		}
		m.goalTickAgain[goal] = true
		return nil
	}
	m.goalTicking[goal] = true
	eng := m.engine
	return func() tea.Msg {
		res, err := eng.GoalTick(context.Background(), goal)
		return goalTickedMsg{goal: goal, res: res, err: err}
	}
}

// goalStartMsg starts one goal card at its current stage.
type goalStartMsg struct {
	f    domain.Feature
	note string
}

type goalPollMsg struct{}

func goalPollTick() tea.Cmd {
	return subscription(tea.Tick(goalPollInterval, func(time.Time) tea.Msg { return goalPollMsg{} }))
}

// updateGoal handles the goal loop's own messages. ok reports whether msg
// was one of them.
func (m *Shell) updateGoal(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case goalTickedMsg:
		delete(m.goalTicking, msg.goal)
		var cmds []tea.Cmd
		if msg.err != nil {
			m.notice = noticeMsg{text: string(msg.goal) + ": " + sanitize(msg.err.Error()), isErr: true, id: msg.goal}
		}
		if msg.res.Stalled != "" {
			// The backend could not serve the goal. The conductor dropped
			// nothing and started nothing; saying so once is the whole of
			// what the board can do, and the goal picks itself back up on
			// the next tick after the backend returns.
			m.notice = noticeMsg{text: string(msg.goal) + ": waiting on the agent backend — " + sanitize(msg.res.Stalled), isErr: true, id: msg.goal}
		}
		for _, st := range msg.res.Start {
			cmds = append(cmds, m.goalStartCmd(st))
		}
		if msg.res.Again || m.goalTickAgain[msg.goal] {
			delete(m.goalTickAgain, msg.goal)
			cmds = append(cmds, m.goalTickCmd(msg.goal))
		}
		if len(msg.res.Actions) > 0 {
			cmds = append(cmds, m.loadRows)
			if m.cardOpen && m.selectedID() == msg.goal {
				cmds = append(cmds, m.loadCardEvents(msg.goal))
			}
		}
		return tea.Batch(cmds...), true

	case goalStartMsg:
		// The start moved the card off todo behind the board's back; reload
		// so its row stops reading as waiting.
		return tea.Batch(m.loadRows, m.runStageWithNote(msg.f, msg.note)), true

	case goalReadyMsg:
		return m.goalReady(msg.id), true

	case goalPageLoadedMsg:
		return m.goalPageLoaded(msg), true

	case statsLoadedMsg:
		return m.statsLoaded(msg), true

	case reverseDialogMsg:
		m.Overlay.Push(&reverseDialog{f: msg.f, decisions: msg.decisions, eng: msg.eng})
		return nil, true

	case goalPollMsg:
		for _, r := range m.rows {
			if r.F.IsGoal() && r.F.Stage == domain.StageImplement && !r.DrivenAbroad {
				m.queueGoalTick(r.F.ID)
			}
		}
		return goalPollTick(), true

	case goalPlanCheckedMsg:
		m.clearAutopilotAnswering(msg.f.ID)
		if !msg.approve {
			m.notice = noticeMsg{text: string(msg.f.ID) + ": the goal's lead sent the plan back"}
			return m.runStageWithNote(msg.f, "The goal's lead sent this plan back before implementation: "+msg.note), true
		}
		m.markAutopilotAnswering(msg.f.ID)
		return autopilotSettled(msg.f.ID, m.advanceStageAs(msg.f.ID, state.ActorAutopilot)), true
	}
	return nil, false
}

// goalStartCmd moves a waiting goal card onto its first stage, then hands
// it to the ordinary run path with the start's note.
func (m *Shell) goalStartCmd(st engine.GoalStart) tea.Cmd {
	store := m.store
	return func() tea.Msg {
		ctx := context.Background()
		f, err := store.GetFeature(ctx, st.ID)
		if err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		if f.Stage == domain.StageTodo {
			if f, err = store.Transition(ctx, f.ID, domain.StagePlan, engine.ActorGoal); err != nil {
				return noticeMsg{text: sanitize(err.Error()), isErr: true}
			}
		}
		return goalStartMsg{f: f, note: st.Note}
	}
}

// goalCardEvent wakes a card's goal when the event is one a goal acts on:
// the card finished a turn, stopped, failed or ran dry.
func (m *Shell) goalCardEvent(ev engine.Event) {
	switch ev.Kind {
	case engine.EventIdle, engine.EventExhausted, engine.EventError, engine.EventStopped:
		if g := m.goalOf(ev.Feature); g != "" {
			m.queueGoalTick(g)
		}
	}
}

// goalAnswerAsk is a goal card's question: its goal's lead answers it (or,
// when the lead cannot, the recommended option does), and the answer is
// delivered as autopilot's.
func (m *Shell) goalAnswerAsk(id domain.FeatureID, ask *engine.Ask) tea.Cmd {
	m.markAutopilotAnswering(id)
	eng := m.engine
	return func() tea.Msg {
		ctx := context.Background()
		answer, ok, err := eng.GoalAnswer(ctx, id, ask)
		if err != nil || !ok || answer == "" {
			answer = engine.RecommendedOption(ask)
		}
		if err := eng.AnswerAs(ctx, id, answer, state.ActorAutopilot); err != nil {
			return autopilotAnsweredMsg{id: id, notice: noticeMsg{text: sanitize(err.Error()), isErr: true}, park: "asks: " + ask.Question}
		}
		return autopilotAnsweredMsg{id: id, notice: noticeMsg{text: string(id) + ": the goal answered: " + answer}}
	}
}

type goalPlanCheckedMsg struct {
	f       domain.Feature
	approve bool
	note    string
}

// goalPlanCheck runs a goal card's plan past its goal's lead before the
// card crosses into implement.
func (m *Shell) goalPlanCheck(f domain.Feature) tea.Cmd {
	m.markAutopilotAnswering(f.ID)
	eng := m.engine
	return func() tea.Msg {
		approve, note, ok, err := eng.GoalPlanCheck(context.Background(), f.ID)
		if err != nil || !ok {
			approve = true
		}
		return goalPlanCheckedMsg{f: f, approve: approve, note: note}
	}
}

// goalVerifyNotPassed handles a goal whose verify did not pass, the way the
// headless driver does: a goal already partial, or out of rework rounds,
// stops ready for you with what was not met on its report; a whole goal
// with rounds left goes back to its cards with the evidence.
func (m *Shell) goalVerifyNotPassed(id domain.FeatureID, reason string) tea.Cmd {
	store, eng, rs := m.store, m.engine, m.roundStore
	return func() tea.Msg {
		ctx := context.Background()
		f, err := store.GetFeature(ctx, id)
		if err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		n, err := rounds.Load(ctx, rs, id, domain.RoundKindCorrective)
		if err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		why := "verify did not pass (" + reason + ")"
		if f.Goal.Partial != "" || n >= verdict.MaxRounds(domain.RoundKindCorrective) {
			if f.Goal.Partial == "" {
				_ = store.SetGoalPartial(ctx, id, why+" after its rework rounds")
			}
			return goalReadyMsg{id: id}
		}
		_ = rounds.Bump(ctx, rs, id, domain.RoundKindCorrective)
		if _, err := store.Transition(ctx, id, domain.StageImplement, state.ActorAutopilot); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		cur, err := store.GetFeature(ctx, id)
		if err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		if err := eng.RunWith(cur, "The goal's verify failed: read the goal doc's Verification plan for the evidence and fix what is not met."); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		return noticeMsg{text: string(id) + ": verify did not pass — back to its cards", reload: true}
	}
}

// goalReadyMsg reports a goal that finished and is waiting for you.
type goalReadyMsg struct{ id domain.FeatureID }

// goalReady raises a goal's hand-over: the one stop of a running goal that
// reaches you. Its report is written into the goal doc first.
func (m *Shell) goalReady(id domain.FeatureID) tea.Cmd {
	text := "ready for you — read the report, then land it, send it back or hand it off"
	if f, err := m.store.GetFeature(context.Background(), id); err == nil && f.Goal.Partial != "" {
		text = "ready for you, partial: " + f.Goal.Partial
	}
	m.raiseAttention(id, attnGate, text)
	eng := m.engine
	return tea.Batch(m.markVerified(id), func() tea.Msg {
		if eng != nil {
			_, _ = eng.WriteGoalReport(context.Background(), id)
		}
		return nil
	}, m.loadRows)
}

// topUpGoalAndContinue answers a goal that stopped on a card it cannot
// fund: raise the envelope by what the card asked for, then send the goal
// back to its cards so the waiting one carries on from where it stopped.
//
// One gesture, because it is one decision. The two halves already existed
// — RaiseGoalBudget and SendBackGoal — but reaching them separately meant
// answering "it needs 600 more credits" with arithmetic, a dialog and a
// second verb, on a card page that had not said what the number was.
func (m *Shell) topUpGoalAndContinue(f domain.Feature, need engine.GoalNeedsBudget) tea.Cmd {
	eng, store := m.engine, m.store
	m.inbox.remove(f.ID)
	return func() tea.Msg {
		ctx := context.Background()
		if eng == nil {
			return noticeMsg{text: "no engine to raise the budget with", isErr: true}
		}
		cur, err := store.GetFeature(ctx, need.Card)
		if err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		// what the card asked for, over and above what it already holds
		to := f.Budget.Envelope + max(need.Needs-cur.Budget.Envelope, domain.TurnReserveCredits)
		if err := eng.RaiseGoalBudget(ctx, f.ID, to); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		if err := eng.SendBackGoal(ctx, f.ID, "", "user"); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		return noticeMsg{text: fmt.Sprintf("%s topped up to %d credits — %s carries on", f.ID, to, need.Card),
			reload: true, clearInbox: f.ID}
	}
}

// goalReviewUnactionable ends a goal's own review loop on this board, the
// counterpart of the driver's arm of the same name. The review asked for
// changes the goal cannot make — it has wrapped up or already gave an
// item up, or it spent its rework rounds proving the same thing — so the
// request is recorded as what makes the result partial and the goal goes
// on to its verify and its hand-over.
//
// This is what the loop used to be missing entirely: the rounds burned
// against a conductor that had finished, and the cap raised an escalation
// on a card whose picker has no row that answers one. A goal's stops
// belong on its report.
func (m *Shell) goalReviewUnactionable(id domain.FeatureID, out gatepolicy.Outcome) tea.Cmd {
	store := m.store
	why := goalReviewPartial(out.Reason)
	// One command, not a sequence: the reason must be recorded before the
	// step that moves the goal, so a reader who arrives between them never
	// sees a goal at verify with nothing saying why it is partial. The
	// step is autoStep's, which also drops the finished review session —
	// the one that would otherwise leave the conductor reading itself as
	// busy.
	step := m.autoStep(id, domain.StageVerify, "review not actionable ("+out.Reason+") → verify", "review")
	return func() tea.Msg {
		ctx := context.Background()
		f, err := store.GetFeature(ctx, id)
		if err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		if f.Goal.Partial == "" {
			if serr := store.SetGoalPartial(ctx, id, why); serr != nil {
				return noticeMsg{text: sanitize(serr.Error()), isErr: true}
			}
		}
		return step()
	}
}

// goalReviewPartial is the sentence a hand-over carries when the goal's
// own review is what made it partial. The driver has the same table; a
// goal that ends on one loop and is read on the other must say the same
// thing.
func goalReviewPartial(reason string) string {
	switch reason {
	case "goal-review-cap":
		return "its review kept asking for changes its cards could not make"
	case "goal-review-unclear":
		return "its review finished with no clear verdict"
	default:
		return "its review asked for changes with no card left to make them"
	}
}

// goalVerifyOutcome routes a goal's finished verify: a pass is ready for
// you, anything else goes through goalVerifyNotPassed.
func (m *Shell) goalVerifyOutcome(id domain.FeatureID, out gatepolicy.Outcome) tea.Cmd {
	if out.Action == gatepolicy.RaiseGate {
		return m.goalReady(id)
	}
	return m.goalVerifyNotPassed(id, out.Reason)
}

// landGoal lands a verified goal on main through the engine: a last
// catch-up, a fresh run of its checks when that brought anything in, and
// one merge commit over its cards' commits.
func (m *Shell) landGoal(f domain.Feature, message string) tea.Cmd {
	eng := m.engine
	return m.cardLocked(f.ID, func() tea.Msg {
		if eng == nil {
			return noticeMsg{text: "no engine to land the goal with", isErr: true}
		}
		sha, err := eng.LandGoal(context.Background(), f.ID, message, "user")
		if err != nil {
			return noticeMsg{text: sanitize(string(f.ID) + ": " + err.Error()), isErr: true, reload: true}
		}
		where := m.baseBranch(f)
		// A goal across repositories landed once in each; naming one
		// branch would say a third of what happened.
		if r, rerr := eng.GoalReport(context.Background(), f.ID); rerr == nil && len(r.Repos) > 1 {
			names := make([]string, 0, len(r.Repos))
			for _, rp := range r.Repos {
				if rp.Name != "" {
					names = append(names, rp.Name)
				}
			}
			where = strings.Join(names, " and ")
		}
		return noticeMsg{text: string(f.ID) + " landed on " + where + " as " + shortHash(sha) + " → done", reload: true, clearInbox: f.ID}
	})
}

func shortHash(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// sendBackGoal returns a goal to its cards with your line as notes for its
// lead.
func (m *Shell) sendBackGoal(f domain.Feature, note string) tea.Cmd {
	eng := m.engine
	return func() tea.Msg {
		if eng == nil {
			return noticeMsg{text: "no engine to send the goal back with", isErr: true}
		}
		if err := eng.SendBackGoal(context.Background(), f.ID, note, "user"); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		text := string(f.ID) + " sent back to its cards"
		if note != "" {
			text += " — your line goes to its lead"
		}
		return noticeMsg{text: text, reload: true, clearInbox: f.ID}
	}
}

// goalNote hands a line typed into a running goal to its lead.
func (m *Shell) goalNote(f domain.Feature, note string) tea.Cmd {
	eng := m.engine
	return func() tea.Msg {
		if eng == nil {
			return noticeMsg{text: "no engine to take the note", isErr: true}
		}
		if err := eng.GoalNote(context.Background(), f.ID, note); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		return noticeMsg{text: string(f.ID) + ": note added — the lead reads it next", reload: true}
	}
}

// confirmStopGoal asks before telling a goal to finish now.
func (m *Shell) confirmStopGoal(f domain.Feature) tea.Cmd {
	eng := m.engine
	m.Overlay.Push(&confirmDialog{
		id: "confirm-stop-goal", question: "Stop " + string(f.ID) + "?", confirmLabel: "Stop",
		detail: "Nothing new starts. Verified cards land on the goal branch, the rest are dropped, and the goal comes back to you partial.",
		onConfirm: func() tea.Cmd {
			return func() tea.Msg {
				if err := eng.StopGoal(context.Background(), f.ID); err != nil {
					return noticeMsg{text: sanitize(err.Error()), isErr: true}
				}
				return noticeMsg{text: string(f.ID) + " is wrapping up", reload: true}
			}
		},
	})
	return nil
}

// confirmAbandonGoal asks before closing a goal that is not ready: its
// unfinished cards are dropped and its branch is kept until you clean it.
func (m *Shell) confirmAbandonGoal(f domain.Feature) tea.Cmd {
	eng := m.engine
	m.Overlay.Push(&confirmDialog{
		id: "confirm-abandon-goal", question: "Abandon " + string(f.ID) + "?", confirmLabel: "Abandon",
		detail: "Its unfinished cards are dropped and the goal closes without landing anything. Its branch stays until you clean it up.",
		onConfirm: func() tea.Cmd {
			return func() tea.Msg {
				if _, err := eng.AbandonGoal(context.Background(), f.ID, "user"); err != nil {
					return noticeMsg{text: sanitize(err.Error()), isErr: true}
				}
				return noticeMsg{text: string(f.ID) + " abandoned — its branch is kept", reload: true, clearInbox: f.ID}
			}
		},
	})
	return nil
}
