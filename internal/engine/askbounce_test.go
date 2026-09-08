package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// TestBouncedAskIsVisibleOnTheCard locks the rule that a question the
// engine refuses must not vanish. Both bounce paths in handleAsk answer
// the model with a tool error and let it carry on; without a trace on the
// card, the reader sees a bare ask_user tool line, no question, and a run
// that looks hung for no stated reason.
func TestBouncedAskIsVisibleOnTheCard(t *testing.T) {
	activityHas := func(t *testing.T, s *Session, want string) {
		t.Helper()
		for _, m := range s.Snapshot().Transcript {
			if m.Author == AuthorTool && strings.Contains(m.Content, want) {
				return
			}
		}
		t.Errorf("no activity line containing %q; transcript=%+v", want, s.Snapshot().Transcript)
	}

	t.Run("malformed payload", func(t *testing.T) {
		ctx := context.Background()
		f := feature(1, "Dark mode", domain.StagePlan)
		e := newEngine(t, agent.NewFake("ack"))
		s, err := e.Attach(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		// an option with no label: parseAsk rejects it at the boundary
		args := json.RawMessage(`{"question":"Which?","options":[{"detail":"no label here"}]}`)
		e.handleClientTool(s, &agent.ToolCall{ID: "c1", Name: askToolName, Args: args})

		if s.Snapshot().PendingAsk != nil {
			t.Fatal("a malformed ask installed a pending question")
		}
		activityHas(t, s, AskBouncedNote)
		activityHas(t, s, "no label")
	})

	t.Run("second ask while one is pending", func(t *testing.T) {
		ctx := context.Background()
		f := feature(2, "Dark mode", domain.StagePlan)
		e := newEngine(t, agent.NewFake("ack"))
		s, err := e.Attach(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		first := json.RawMessage(`{"question":"First?","options":[{"label":"a"},{"label":"b"}]}`)
		e.handleClientTool(s, &agent.ToolCall{ID: "c1", Name: askToolName, Args: first})
		if s.Snapshot().PendingAsk == nil {
			t.Fatal("the first ask did not install")
		}
		second := json.RawMessage(`{"question":"Second?","options":[{"label":"x"},{"label":"y"}]}`)
		e.handleClientTool(s, &agent.ToolCall{ID: "c2", Name: askToolName, Args: second})

		if got := s.Snapshot().PendingAsk.Question; got != "First?" {
			t.Errorf("pending ask = %q, want the first one still installed", got)
		}
		activityHas(t, s, AskBouncedNote)
		activityHas(t, s, "one question at a time")
	})
}
