package engine

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// usageFrom reconstructs a spend total from a persisted snapshot.
func usageFrom(snap state.SessionSnapshot) agent.Usage {
	return agent.Usage{
		Credits:      snap.SpendCredits,
		InputTokens:  snap.SpendIn,
		OutputTokens: snap.SpendOut,
		Model:        snap.SpendModel,
	}
}

// persist writes a session's current state to the store (best-effort:
// persistence failures never break the live session). No-op unless
// Config.Persist is set.
func (e *Engine) persist(s *Session) {
	if !e.cfg.Persist || e.cfg.Store == nil {
		return
	}
	// Serialize the finalized-check-and-write against persistDelete: without
	// this a save that passed the check before Drop finalized the session
	// could land its upsert after the delete and resurrect the row.
	e.persistMu.Lock()
	defer e.persistMu.Unlock()
	// A finalized (stopped/dropped) session must not write — a late
	// pump-persist would otherwise resurrect a deleted row.
	if s.finalizedState() {
		return
	}
	snap := s.Snapshot()
	rec := state.SessionSnapshot{
		Feature:      snap.Feature.ID,
		Stage:        snap.Feature.Stage,
		Role:         string(snap.Role),
		Flavor:       flavorString(s.flavor()),
		State:        string(snap.State),
		AgentSession: snap.AgentSessionID,
		SpendCredits: snap.Spend.Credits,
		SpendIn:      snap.Spend.InputTokens,
		SpendOut:     snap.Spend.OutputTokens,
		SpendModel:   snap.Spend.Model,
		Activity:     snap.Activity,
		Verdict:      snap.Verdict,
		// gummi's own ceiling on that verdict travels with it: a verdict
		// saved without the floor that overruled it reads, on the next
		// process, as the agent's unchallenged word.
		VerdictFloor:       snap.VerdictFloor,
		VerdictFloorReason: snap.VerdictFloorReason,
		// And so does the budget stop. A session that ran out is saved as
		// StateDone like any other finished one, so without this a new
		// process cannot tell a stage that finished from a stage that was
		// cut off — and a resume spends the top-up critiquing work that
		// was never written.
		Exhausted: s.isExhausted(),
		StartedAt: s.startedAt.UTC().Format(time.RFC3339Nano),
	}
	if snap.Err != nil {
		rec.Error = snap.Err.Error()
	}
	for _, m := range snap.Transcript {
		rec.Transcript = append(rec.Transcript, state.SessionMessage{
			Author: string(m.Author), Content: m.Content,
			ToolStatus: string(m.ToolStatus), ToolOutput: m.ToolOutput,
			// the ask-answerer stamp rides the round trip: an echo
			// skipped here but unstamped after restore would be
			// mirrored by the first post-restore save, reintroducing
			// the user-message row this stamp exists to keep out.
			AnsweredBy: m.AnsweredBy,
		})
	}
	_ = e.cfg.Store.SaveSession(context.Background(), rec)
	_ = e.mirrorEvents(s, snap)
}

// mirrorEvents appends this save's new card-event-log entries: the
// generation's stage_enter (once, via dedupe), any transcript entries
// that have settled since the last save, and — once the session reaches
// StateDone — the stage_exit that closes the generation out and prunes
// the stage's successful tool output. Best-effort, like persist itself:
// a mirror failure must never break the live session.
func (e *Engine) mirrorEvents(s *Session, snap Snapshot) error {
	// prefix discriminates this session generation's events from any
	// other generation's on the same stage (a review bounce, a resumed
	// card) so their dedupe keys never collide. It is the same key this
	// generation's realized spend is filed under (Session.generation),
	// so the log and the meter agree on what one run of a stage is.
	prefix := s.generation()

	var evs []state.CardEvent

	stageEnterPayload, _ := json.Marshal(map[string]string{
		"role": string(snap.Role), "model": snap.Model,
		"flavor": flavorString(s.flavor()),
	})
	evs = append(evs, state.CardEvent{
		Feature: snap.Feature.ID, Stage: snap.Feature.Stage,
		Kind: state.EventStageEnter, At: s.startedAt,
		Payload: string(stageEnterPayload), Dedupe: prefix + ":stage_enter",
	})

	for i, m := range snap.Transcript {
		// A transcript entry's ord is stable while its content is still
		// growing (a streaming assistant reply, an unresolved tool call
		// awaiting its result): mirroring it now would freeze the
		// truncated in-progress text under a dedupe key that can never
		// be overwritten. So an entry is mirrored only once it has
		// settled — a later save picks up anything skipped here, once
		// it has.
		if m.Author == AuthorTool {
			evs = append(evs, toolEvents(snap, m, prefix, i)...)
			continue
		}
		if m.Streaming {
			continue
		}
		// The echo of an answer the unattended loop took by itself:
		// AnswerAs records the exchange in the transcript as a
		// user-authored turn so a restored session reads as a
		// conversation, but mirrored verbatim it lands here as a plain
		// user message — and a user message is what the stretch
		// derivation reads as a person taking the card back, closing the
		// autopilot period the same second the machine answered its own
		// question. The ask event beside it is the durable record of the
		// exchange and already carries the true answerer, so the echo is
		// not mirrored at all: the transcript keeps it, this log does
		// not. Only the autopilot stamp is skipped — a person's answer
		// still mirrors, and its echo closing a period is harmless (the
		// ask row filed under the user's name already closed it) — and a
		// plain typed send is never stamped in the first place.
		if m.Author == AuthorUser && m.AnsweredBy == state.ActorAutopilot {
			continue
		}
		payload, _ := json.Marshal(map[string]string{
			"author": string(m.Author), "content": m.Content,
		})
		evs = append(evs, state.CardEvent{
			Feature: snap.Feature.ID, Stage: snap.Feature.Stage,
			Kind: state.EventMessage, At: eventTime(m.At), Payload: string(payload),
			Dedupe: prefix + ":message:" + strconv.Itoa(i),
		})
	}

	done := snap.State == StateDone
	if done {
		// ctx_peak/ctx_limit are the only facts written here that are not
		// derivable from the log: the session row holding them is deleted
		// as the stage completes, and nothing else ever saw them. Everything
		// else a reader wants about this session — its turns, its tools, how
		// long it ran — stays derived from the events above, so this payload
		// never becomes a second source of truth for a fact that already has
		// one (DESIGN §6.3).
		peak := s.contextPeak()
		payload, _ := json.Marshal(map[string]any{
			"verdict": snap.Verdict, "credits": snap.Spend.Credits,
			"ctx_peak": peak.Tokens, "ctx_limit": peak.Limit,
		})
		evs = append(evs, state.CardEvent{
			Feature: snap.Feature.ID, Stage: snap.Feature.Stage,
			Kind: state.EventStageExit, At: time.Now(),
			Payload: string(payload), Dedupe: prefix + ":stage_exit",
		})
	}

	if err := e.cfg.Store.AppendEvents(context.Background(), evs); err != nil {
		return err
	}
	if done {
		return e.cfg.Store.PruneStageOutput(context.Background(), snap.Feature.ID, snap.Feature.Stage)
	}
	return nil
}

// toolEvents mirrors one AuthorTool transcript entry as the one or two
// rows the durable log wants for it.
//
// A call gummi ran itself — a check, a budget nudge — knew its outcome
// before it was ever recorded, so it is one row and that row carries
// everything. A call an agent made is two: the call, written the moment
// it happened, and its outcome, written when it arrives.
//
// The split is what makes the tool record exist at all. Waiting for an
// outcome before recording anything means recording nothing on every
// backend that does not report one — and three of gummi's six do not
// (agent.EventToolResult's own doc says so). A call whose result never
// comes now leaves a row saying it was called, which is the truth, in
// place of the silence that used to read as "this card used no tools".
//
// Both dedupe keys are derived from the entry's index within this
// generation, so a save that re-walks a transcript it has already
// mirrored is a no-op, and a call that settles between two saves adds its
// outcome without disturbing the call.
func toolEvents(snap Snapshot, m Message, prefix string, i int) []state.CardEvent {
	payload, _ := json.Marshal(state.ToolPayload{
		Label: m.Content, Tool: m.Tool, Detail: m.Detail, Call: m.CallID,
	})
	call := state.CardEvent{
		Feature: snap.Feature.ID, Stage: snap.Feature.Stage,
		Kind: state.EventTool, At: eventTime(m.At), Payload: string(payload),
		Dedupe: prefix + ":tool:" + strconv.Itoa(i),
	}
	// gummi's own line: no call id to correlate on, and the outcome (if
	// it has one) was known when it was appended. One row, as before.
	if m.CallID == "" {
		call.Status = string(m.ToolStatus)
		call.Output = m.ToolOutput
		return []state.CardEvent{call}
	}
	if m.ToolStatus == "" {
		return []state.CardEvent{call}
	}
	var ms int64
	if !m.DoneAt.IsZero() && !m.At.IsZero() {
		ms = m.DoneAt.Sub(m.At).Milliseconds()
	}
	result, _ := json.Marshal(state.ToolPayload{Label: m.Content, Call: m.CallID, MS: ms})
	return []state.CardEvent{call, {
		Feature: snap.Feature.ID, Stage: snap.Feature.Stage,
		Kind: state.EventToolResult, Status: string(m.ToolStatus),
		At: eventTime(m.DoneAt), Payload: string(result), Output: m.ToolOutput,
		Dedupe: prefix + ":tool_result:" + strconv.Itoa(i),
	}}
}

// eventTime prefers when the thing actually happened over when the
// mirror got around to writing it down. A zero time means the entry
// predates the stamp — a transcript restored from a previous process,
// which carries its messages but not their clocks — and the write time
// is then the closest honest answer available.
func eventTime(at time.Time) time.Time {
	if at.IsZero() {
		return time.Now()
	}
	return at
}

// persistDelete removes a feature's persisted session.
func (e *Engine) persistDelete(id domain.FeatureID) {
	if !e.cfg.Persist || e.cfg.Store == nil {
		return
	}
	e.persistMu.Lock()
	defer e.persistMu.Unlock()
	_ = e.cfg.Store.DeleteSession(context.Background(), id)
}

// Restore reloads persisted sessions into the engine as paused,
// transcript-carrying sessions the user can re-run or re-attach. It
// must be called once, before the UI starts. Features whose stage no
// longer matches the persisted session are skipped (the store is the
// source of truth for the current stage).
func (e *Engine) Restore(ctx context.Context) error {
	if !e.cfg.Persist || e.cfg.Store == nil {
		return nil
	}
	snaps, err := e.cfg.Store.LoadSessions(ctx)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, snap := range snaps {
		f, err := e.cfg.Store.GetFeature(ctx, snap.Feature)
		if err != nil || f.Stage != snap.Stage {
			continue // stale session for a since-advanced feature
		}
		role, ok := roleForStage(f)
		if !ok {
			continue
		}
		// The pass flavor is persisted on the session row, so a restored
		// session keeps its identity (stage / critique / rebase) whatever
		// stage it borrowed — a rebase pass on an implementer-owned stage
		// must not be mistaken for the stage's own run. A legacy row
		// predating the flavor column falls back to the role/stage
		// inference the column replaced.
		critique, rebase := parseFlavor(snap.Flavor)
		if snap.Flavor == "" {
			critique = f.Stage == domain.StagePlan && snap.Role == string(agent.RoleReviewer)
			rebase = snap.Role == string(agent.RoleImplementer) && role != agent.RoleImplementer
		}
		if critique {
			role = agent.RoleReviewer
		}
		if rebase {
			role = agent.RoleImplementer
			// a crash mid-session can strand the worktree mid-rebase; abort
			// so it comes back clean (best-effort, like the live settle).
			if wt, merr := e.mgr(ctx, &f); merr == nil {
				_, _ = wt.AbortRebase(ctx, &f)
			}
		}
		// A restored session must keep its original startedAt where one
		// exists, or its already-mirrored transcript would re-mirror
		// under a new generation prefix and duplicate. Legacy rows
		// predating the column (empty or unparseable) fall back to now.
		startedAt, serr := time.Parse(time.RFC3339Nano, snap.StartedAt)
		if serr != nil {
			startedAt = time.Now()
		}
		ctx, cancel := context.WithCancel(context.Background())
		s := &Session{
			Feature:     f,
			Role:        role,
			Interactive: restoredState(snap.State) == StateInteractive,
			Critique:    critique,
			Rebase:      rebase,
			state:       restoredState(snap.State),
			done:        make(chan struct{}),
			ctx:         ctx,
			cancel:      cancel,
			startedAt:   startedAt,
		}
		for _, m := range snap.Transcript {
			s.transcript = append(s.transcript, Message{
				Author: Author(m.Author), Content: m.Content,
				ToolStatus: ToolStatus(m.ToolStatus), ToolOutput: m.ToolOutput,
				// the stamp comes back with the echo it rides on, so
				// the mirror's skip holds across the restart instead
				// of failing open on the first post-restore save.
				AnsweredBy: m.AnsweredBy,
			})
		}
		s.activity = append(s.activity, snap.Activity...)
		s.spend = usageFrom(snap)
		if snap.Error != "" {
			s.err = restoredErr(snap.Error)
		}
		s.verdict = snap.Verdict
		// restored alongside the verdict it overrules, so the rehydrated
		// session judges the stage the way the live one did — and can still
		// say which check made it say so.
		s.verdictFloor = snap.VerdictFloor
		s.verdictFloorReason = snap.VerdictFloorReason
		// and the budget stop, so the restored session still knows it was
		// cut off rather than finished
		s.exhausted = snap.Exhausted
		s.setAgentSessionID(snap.AgentSession)
		e.stampSpawnInfo(s)
		// An ask that was open when the process died is re-armed from its
		// durable decision row (DESIGN §6.3's reopen path): the blocked
		// tool call and its resolver died with the process, but the record
		// of the question did not. Free-form only — the recorded options
		// are never stored — so the answer is prose, riding a fresh turn.
		if open := e.openAskFor(ctx, snap.Feature, snap.Stage); open != nil {
			s.setPendingAsk(open)
		}
		// A resumable live session is being rehydrated (a paused interactive
		// question, an autonomous run picked up again): stop the prior one,
		// or its pump would outlive Restore and, unjoined by Close, leak.
		// Mirrors the old.stop() both replace and RunWith do on overwrite.
		if old := e.live[snap.Feature]; old != nil {
			// ...unless it is still in flight in THIS process, where the
			// live session is by definition newer than the row it was
			// persisted into. Rehydrating over it would stop a working
			// agent mid-turn and hand the driver a paused snapshot, which
			// reads as "died mid-turn, re-dispatch" — the double-spawn a
			// resume onto an in-flight stage must not do (DESIGN §4.2).
			// Nothing enforced this but the attention-slot cap, which is
			// off by default now.
			if st := old.State(); st == StateRunning || st == StateQueued {
				continue
			}
			old.stop()
			// a paused/done session freed its slot on the way out; release
			// defensively so a replaced holder can never leak the count
			// (Restore runs under e.mu, so freeSlot's own lock is out).
			if held, p := old.releaseSlot(); held && e.lanes[p].running > 0 {
				e.lanes[p].running--
			}
		}
		e.live[snap.Feature] = s
	}
	return nil
}

// openAskFor returns the ask a card is durably waiting on at stage,
// re-armed for the session being restored, or nil when no open ask
// decision names it. The newest open ask wins. Best-effort: an unreadable
// log degrades to today's behavior (the ask evaporates) rather than
// failing the restore.
func (e *Engine) openAskFor(ctx context.Context, id domain.FeatureID, stage domain.Stage) *Ask {
	opens, err := e.cfg.Store.OpenDecisions(ctx)
	if err != nil {
		return nil
	}
	for _, d := range opens[id] {
		if d.Kind != state.DecisionKindAsk || d.Stage != stage {
			continue
		}
		// No options: they died with the process and are never stored. The
		// question is still answerable because every ask takes the
		// person's own words (see Ask) — here that channel is the only one
		// left.
		return &Ask{
			Question:   d.Question,
			SpecAnchor: d.Anchor,
			DecisionID: d.ID,
		}
	}
	return nil
}

// restoredErr rebuilds a session error from its persisted text (the
// original error type is not preserved; the message is what surfaces).
type restoredErr string

func (e restoredErr) Error() string { return string(e) }

// restoredState maps a persisted state to the state a reloaded session
// resumes in: a running/queued session was interrupted by the restart,
// so it comes back paused (resumable); done/paused/interactive persist.
func restoredState(st string) SessionState {
	switch SessionState(st) {
	case StateDone:
		return StateDone
	case StateInteractive:
		return StateInteractive
	default:
		return StatePaused
	}
}
