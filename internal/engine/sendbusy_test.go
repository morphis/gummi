package engine

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// A line typed while the agent is mid-turn, or while it is blocked on an
// ask_user question, must be refused — and refused WITHOUT touching the
// transcript and WITHOUT failing the run.
//
// Both used to reach the backend, come back as "a turn is already in
// progress", and get routed through deliverTurn's failRun. The line had
// already been echoed as a "you" message by then, so the reader saw their
// own sentence on screen, believed it delivered, and watched the stage
// die: "The plan session errored before it finished." Typing a second
// thought while the spinner was up cost both the thought and the stage.
func TestSendRefusesWithoutFailingTheRun(t *testing.T) {
	// count the echo itself, not the transcript length: the turn that
	// refused this one is still streaming, so its own output lands in
	// the same window and would move any total.
	echoes := func(s *Session, text string) int {
		n := 0
		for _, m := range s.Snapshot().Transcript {
			if m.Author == AuthorUser && m.Content == text {
				n++
			}
		}
		return n
	}

	t.Run("the backend refuses the turn", func(t *testing.T) {
		ctx := context.Background()
		f := feature(1, "Dark mode", domain.StagePlan)
		ag := agent.NewFake("ack")
		e := newEngine(t, ag)
		s, err := e.Attach(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		// the backend is the authority on whether it can take a turn;
		// every real adapter refuses a second one mid-stream.
		ag.SendErr = agent.ErrBusy

		const line = "also keep it simple"
		err = e.Send(ctx, f.ID, line)
		if !errors.Is(err, agent.ErrBusy) {
			t.Fatalf("a refused turn returned %v, want ErrBusy", err)
		}
		if n := echoes(s, line); n != 0 {
			t.Fatalf("a refused line is echoed %d time(s) in the transcript as if it had been delivered", n)
		}
		if s.Snapshot().Err != nil {
			t.Fatalf("a refused line failed the run: %v", s.Snapshot().Err)
		}
	})

	t.Run("blocked on a question", func(t *testing.T) {
		ctx := context.Background()
		f := feature(2, "Dark mode", domain.StagePlan)
		e := newEngine(t, agent.NewFake("ack"))
		s, err := e.Attach(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		args := json.RawMessage(`{"question":"Which?","options":[{"label":"a"},{"label":"b"}]}`)
		e.handleClientTool(s, &agent.ToolCall{ID: "c1", Name: askToolName, Args: args})
		if s.Snapshot().PendingAsk == nil {
			t.Fatal("the ask did not install")
		}
		// handleAsk drops the spinner while a human is being waited on, so
		// the session reports itself not-busy here — but the backend's
		// turn is still open inside the tool call, and a second turn is
		// exactly what it cannot take.
		if s.Busy() {
			t.Fatal("precondition: the session should not report busy while an ask is pending")
		}
		const line = "yes, and cover rm too"
		err = e.Send(ctx, f.ID, line)
		if !errors.Is(err, agent.ErrBusy) {
			t.Fatalf("Send with a pending ask returned %v, want ErrBusy", err)
		}
		if n := echoes(s, line); n != 0 {
			t.Fatalf("a refused line is echoed %d time(s) in the transcript as if it had been delivered", n)
		}
		if s.Snapshot().Err != nil {
			t.Fatalf("a refused line failed the run: %v", s.Snapshot().Err)
		}
		if s.Snapshot().PendingAsk == nil {
			t.Fatal("the refusal dropped the question it was refusing on behalf of")
		}
	})
}
