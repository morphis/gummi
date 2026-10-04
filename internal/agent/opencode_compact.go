package agent

import (
	"context"
	"errors"
	"fmt"
)

// Compact implements Compactor. The session's server compacts: one
// summarize call against the server already running for this session —
// the per-compaction serve spawn is gone with the per-turn CLI process.
// The next turn then continues from the summary.
func (s *opencodeSession) Compact(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("session closed")
	}
	if s.turn != nil || s.cancel != nil {
		return ErrBusy
	}
	if s.sessionID == "" {
		go func() {
			s.emit(Event{Kind: EventMessage, Text: "Nothing to compact yet: this conversation has not started."})
			s.emit(Event{Kind: EventIdle})
		}()
		return nil
	}
	provider, model, err := SplitOpencodeModel(s.model)
	if err != nil {
		return err
	}
	cctx, cancel := context.WithCancel(s.sctx)
	s.cancel = cancel
	go s.runCompact(cctx, s.srv, s.sessionID, provider, model)
	return nil
}

func (s *opencodeSession) runCompact(ctx context.Context, srv opencodeServer, id, provider, model string) {
	err := srv.summarize(ctx, id, provider, model)
	s.mu.Lock()
	s.cancel = nil
	closed := s.closed
	aborted := s.interrupted // stopped by Interrupt: the call was cancelled
	s.interrupted = false
	s.mu.Unlock()
	switch {
	case closed:
		return
	case aborted:
		s.emit(Event{Kind: EventMessage, Text: "Compaction stopped; the conversation is as it was."})
	case err != nil:
		s.emit(Event{Kind: EventError, Err: &RunFailure{
			Backend: "opencode",
			Err:     fmt.Errorf("compacting the session: %w", err),
		}})
		return
	default:
		s.emit(Event{Kind: EventMessage, Text: "Compacted the conversation: the agent now carries a summary of it instead of the whole history."})
	}
	s.emit(Event{Kind: EventIdle})
}
