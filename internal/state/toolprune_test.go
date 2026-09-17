package state

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
)

// toolCall appends a call row and, when status is non-empty, the result
// row that settles it — the two-row shape the mirror writes for an
// agent's tool call.
func toolCall(t *testing.T, s *Store, id domain.FeatureID, stage domain.Stage, call, tool, detail, status, output string) {
	t.Helper()
	ctx := context.Background()
	p, _ := json.Marshal(ToolPayload{Label: tool + "  " + detail, Tool: tool, Detail: detail, Call: call})
	if err := s.AppendEvent(ctx, CardEvent{
		Feature: id, Stage: stage, Kind: EventTool, At: time.Now(),
		Payload: string(p), Dedupe: call + ":call",
	}); err != nil {
		t.Fatal(err)
	}
	if status == "" {
		return
	}
	rp, _ := json.Marshal(ToolPayload{Label: tool + "  " + detail, Call: call, MS: 120})
	if err := s.AppendEvent(ctx, CardEvent{
		Feature: id, Stage: stage, Kind: EventToolResult, Status: status, At: time.Now(),
		Payload: string(rp), Output: output, Dedupe: call + ":result",
	}); err != nil {
		t.Fatal(err)
	}
}

// The retention rule, at the grain the settled proposal chose: a stage
// that has gone by keeps every event, keeps the name, outcome and
// duration of every call it made, and keeps the arguments and output only
// of the calls that failed. Nothing a metric is computed from decays;
// what a long-lived card accumulates is bounded by how much of it went
// wrong rather than by how much of it happened.
func TestPruneStageKeepsTheRecordAndDropsTheBulk(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := feat(1, "noisy stage")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}

	toolCall(t, s, f.ID, domain.StageImplement, "c1", "Bash", "go test ./...", StatusOK, "ok output")
	toolCall(t, s, f.ID, domain.StageImplement, "c2", "Bash", "go vet ./...", StatusFail, "vet output")
	toolCall(t, s, f.ID, domain.StageImplement, "c3", "Read", "internal/secret.go", "", "")
	// another stage's call must be left entirely alone
	toolCall(t, s, f.ID, domain.StageVerify, "c4", "Bash", "make ci", StatusOK, "ci output")

	if err := s.PruneStageOutput(ctx, f.ID, domain.StageImplement); err != nil {
		t.Fatal(err)
	}

	evs, err := s.Events(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]ToolPayload{}
	out := map[string]string{}
	for _, ev := range FoldToolResults(evs) {
		if ev.Kind != EventTool {
			continue
		}
		var p ToolPayload
		if err := json.Unmarshal([]byte(ev.Payload), &p); err != nil {
			t.Fatal(err)
		}
		got[p.Call] = p
		out[p.Call] = ev.Output
	}
	if len(got) != 4 {
		t.Fatalf("calls after prune = %d, want all 4 kept: %+v", len(got), got)
	}

	// every call keeps what a metric is made of
	for _, c := range []string{"c1", "c2", "c3", "c4"} {
		if got[c].Tool == "" {
			t.Errorf("%s lost its tool name; counts and failure rates decay with it", c)
		}
	}
	if got["c1"].MS != 120 || got["c2"].MS != 120 {
		t.Errorf("durations pruned away: c1=%d c2=%d", got["c1"].MS, got["c2"].MS)
	}

	// the passing call in the pruned stage gives up its argument and output
	if got["c1"].Detail != "" {
		t.Errorf("c1 detail = %q, want dropped: it passed and the stage is done", got["c1"].Detail)
	}
	if out["c1"] != "" {
		t.Errorf("c1 output = %q, want blanked", out["c1"])
	}
	// so does the one that never reported an outcome — nothing says it failed
	if got["c3"].Detail != "" {
		t.Errorf("c3 detail = %q, want dropped: no outcome ever said it failed", got["c3"].Detail)
	}
	// the failure keeps both: it is the forensic case the rule exists for
	if got["c2"].Detail != "go vet ./..." {
		t.Errorf("c2 detail = %q, want kept: it failed", got["c2"].Detail)
	}
	if out["c2"] != "vet output" {
		t.Errorf("c2 output = %q, want kept", out["c2"])
	}
	// and another stage is untouched
	if got["c4"].Detail != "make ci" || out["c4"] != "ci output" {
		t.Errorf("c4 = %+v / %q, want a different stage left alone", got["c4"], out["c4"])
	}
}

// A result whose call is not in the log describes something this card
// cannot show, so folding drops it rather than leaving a headless row a
// reader would have to invent a meaning for.
func TestFoldToolResultsDropsOrphans(t *testing.T) {
	p, _ := json.Marshal(ToolPayload{Call: "gone", MS: 5})
	evs := []CardEvent{
		{Kind: EventMessage, Payload: `{"author":"user","content":"hi"}`},
		{Kind: EventToolResult, Status: StatusOK, Payload: string(p)},
	}
	got := FoldToolResults(evs)
	if len(got) != 1 || got[0].Kind != EventMessage {
		t.Fatalf("folded = %+v, want the orphan result dropped and nothing else touched", got)
	}
}

// A log with no results at all comes back as itself — the common case on
// a card whose backend never reports outcomes, and the one where copying
// would be pure waste.
func TestFoldToolResultsPassesThroughWithoutResults(t *testing.T) {
	evs := []CardEvent{{Kind: EventTool, Payload: `{"label":"Bash  ls","tool":"Bash","call":"c1"}`}}
	got := FoldToolResults(evs)
	if len(got) != 1 || got[0].Status != "" {
		t.Fatalf("folded = %+v, want the unsettled call left exactly as it is", got)
	}
}
