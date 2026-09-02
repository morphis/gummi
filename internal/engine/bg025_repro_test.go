package engine

import (
	"context"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// TestHeadlessGenerationsMintCollidingMCPCallIDs: each headless `gummi
// resume` is a fresh process with a fresh Engine, so a client-tool ask's
// decision id (minted from Engine.mcpSeq) always starts back at "mcp-1". A
// second generation's decision_open then collides with the first
// generation's already-answered "mcp-1" row and is silently deduped away.
func TestHeadlessGenerationsMintCollidingMCPCallIDs(t *testing.T) {
	ws, store, wt := newRepo(t)
	f := feature(1, "Dark mode", domain.StageBrainstorm)
	_ = wt
	ctx := context.Background()
	if err := store.CreateFeature(ctx, &f); err != nil {
		t.Fatal(err)
	}

	// Generation 1: a fresh Engine (as a headless `resume` process
	// constructs), asks one question over the MCP bridge path, and gets
	// answered — mirroring the first `resume`/`--answer` round trip.
	e1 := New(Config{Agents: singleAgent(agent.NewFake("hi")), Store: store, Worktrees: wt, Workspace: ws, Model: "fake-model", MaxActive: 1})
	s1, err := e1.Attach(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, e1, EventIdle) // kickoff turn
	go e1.DispatchClientTool(ctx, s1, "ask_user", askArgs(t, Ask{Question: "Persist where?", Options: []AskOption{{Label: "per-device"}}}))
	waitFor(t, e1, EventQuestion)
	if err := e1.Answer(ctx, f.ID, "per-device"); err != nil {
		t.Fatal(err)
	}
	e1.Close()

	// Generation 2: a brand-new Engine against the same store and card,
	// exactly what the next headless `gummi resume` constructs. Its
	// mcpSeq starts back at zero, so its first ask mints "mcp-1" again.
	e2 := New(Config{Agents: singleAgent(agent.NewFake("hi")), Store: store, Worktrees: wt, Workspace: ws, Model: "fake-model", MaxActive: 1})
	defer e2.Close()
	s2, err := e2.Attach(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, e2, EventIdle) // kickoff turn
	ctx2, cancel := context.WithCancel(ctx)
	defer cancel()
	go e2.DispatchClientTool(ctx2, s2, "ask_user", askArgs(t, Ask{Question: "Second question?", Options: []AskOption{{Label: "yes"}}}))
	waitFor(t, e2, EventQuestion)

	// The card is genuinely blocked on a second, different question — but
	// because "mcp-1" already exists (closed) in card_events for this
	// feature, the dedupe key collides and the new decision_open never
	// lands.
	open, err := store.OpenDecisions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	decs := open[f.ID]
	if len(decs) != 1 || decs[0].Question != "Second question?" {
		t.Fatalf("generation 2's decision_open was deduped away by mcp-1 colliding with generation 1's already-answered row; store.OpenDecisions()[%s] = %+v, want [Second question?]", f.ID, decs)
	}
}
