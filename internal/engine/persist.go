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
		SpendCredits: snap.Spend.Credits,
		SpendIn:      snap.Spend.InputTokens,
		SpendOut:     snap.Spend.OutputTokens,
		SpendModel:   snap.Spend.Model,
		Activity:     snap.Activity,
	}
	for _, m := range snap.Transcript {
		rec.Transcript = append(rec.Transcript, state.SessionMessage{
			Author: string(m.Author), Content: m.Content,
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
		// A plan-stage session persisted with the reviewer role was the
		// plan-critique pass (the plan writer is the architect); the flag
		// itself isn't persisted, so recover it from that pairing.
		critique := f.Stage == domain.StagePlan && snap.Role == string(agent.RoleReviewer)
		if critique {
			role = agent.RoleReviewer
		}
		s := &Session{
			Feature:     f,
			Role:        role,
			Interactive: interactiveStage(f.Stage),
			Critique:    critique,
			state:       restoredState(snap.State),
			done:        make(chan struct{}),
		}
		for _, m := range snap.Transcript {
			s.transcript = append(s.transcript, Message{
				Author: Author(m.Author), Content: m.Content,
			})
		}
		s.activity = append(s.activity, snap.Activity...)
		s.spend = usageFrom(snap)
		e.stampSpawnInfo(s)
		e.live[snap.Feature] = s
	}
	return nil
}

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
