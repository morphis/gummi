package cardrun

import (
	"encoding/json"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// A tool whose name merely mentions subagents delegated nothing: only the
// delegation tools themselves count.
func TestReportDoesNotCountAToolNamedForSubagentsAsADelegation(t *testing.T) {
	p, _ := json.Marshal(state.ToolPayload{
		Label: "list_subagents", Tool: "list_subagents", Call: "c1", MS: 10,
	})
	evs := []state.CardEvent{{
		Stage: domain.StageImplement, Kind: state.EventTool, Status: state.StatusOK,
		At: base, Payload: string(p),
	}}
	run := Report(Input{Feature: card(1, 100), Events: evs})
	if len(run.Hands.Subagents) != 0 {
		t.Fatalf("subagents = %+v, want none for list_subagents", run.Hands.Subagents)
	}
}
