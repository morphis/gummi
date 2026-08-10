package engine

import (
	"context"

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
		State:        string(snap.State),
		AgentSession: snap.AgentSessionID,
		SpendCredits: snap.Spend.Credits,
		SpendIn:      snap.Spend.InputTokens,
		SpendOut:     snap.Spend.OutputTokens,
		SpendModel:   snap.Spend.Model,
		Activity:     snap.Activity,
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
		// The pass flags aren't persisted; recover them from role/stage
		// pairings the stage's own run can't produce. A plan-stage session
		// with the reviewer role was the plan-critique pass (the plan
		// writer is the architect); an implementer-role session on a stage
		// whose own role isn't implementer was the rebase-resolve pass.
		critique := f.Stage == domain.StagePlan && snap.Role == string(agent.RoleReviewer)
		if critique {
			role = agent.RoleReviewer
		}
		rebase := snap.Role == string(agent.RoleImplementer) && role != agent.RoleImplementer
		if rebase {
			role = agent.RoleImplementer
			// a crash mid-session can strand the worktree mid-rebase; abort
			// so it comes back clean (best-effort, like the live settle).
			_, _ = e.cfg.Worktrees.AbortRebase(ctx, &f)
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
		s.setAgentSessionID(snap.AgentSession)
		e.stampSpawnInfo(s)
		// A resumable live session is being rehydrated (a parked interactive
		// question, an autonomous run picked up again): stop the prior one,
		// or its pump would outlive Restore and, unjoined by Close, leak.
		// Mirrors the old.stop() both replace and RunWith do on overwrite.
		if old := e.live[snap.Feature]; old != nil {
			old.stop()
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
