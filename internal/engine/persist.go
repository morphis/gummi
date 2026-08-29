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
		StartedAt:    s.startedAt.UTC().Format(time.RFC3339Nano),
	}
	if snap.Err != nil {
		rec.Error = snap.Err.Error()
	}
	for _, m := range snap.Transcript {
		rec.Transcript = append(rec.Transcript, state.SessionMessage{
			Author: string(m.Author), Content: m.Content,
			ToolStatus: string(m.ToolStatus), ToolOutput: m.ToolOutput,
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
	// card) so their dedupe keys never collide.
	prefix := strconv.FormatInt(s.startedAt.UnixNano(), 10)

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
			if m.ToolStatus == "" {
				continue
			}
			payload, _ := json.Marshal(map[string]string{"label": m.Content})
			evs = append(evs, state.CardEvent{
				Feature: snap.Feature.ID, Stage: snap.Feature.Stage,
				Kind: state.EventTool, Status: string(m.ToolStatus),
				At: time.Now(), Payload: string(payload), Output: m.ToolOutput,
				Dedupe: prefix + ":tool:" + strconv.Itoa(i),
			})
			continue
		}
		if m.Streaming {
			continue
		}
		payload, _ := json.Marshal(map[string]string{
			"author": string(m.Author), "content": m.Content,
		})
		evs = append(evs, state.CardEvent{
			Feature: snap.Feature.ID, Stage: snap.Feature.Stage,
			Kind: state.EventMessage, At: time.Now(), Payload: string(payload),
			Dedupe: prefix + ":message:" + strconv.Itoa(i),
		})
	}

	done := snap.State == StateDone
	if done {
		payload, _ := json.Marshal(map[string]any{
			"verdict": snap.Verdict, "credits": snap.Spend.Credits,
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
		role, ok := roleForStage(f.Stage)
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
			Interactive: interactiveStage(f.Stage),
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
			})
		}
		s.activity = append(s.activity, snap.Activity...)
		s.spend = usageFrom(snap)
		if snap.Error != "" {
			s.err = restoredErr(snap.Error)
		}
		s.verdict = snap.Verdict
		s.setAgentSessionID(snap.AgentSession)
		e.stampSpawnInfo(s)
		// A resumable live session is being rehydrated (a parked interactive
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
