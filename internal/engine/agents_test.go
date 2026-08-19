package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// singleAgent wraps one adapter into the map shape Config.Agents wants,
// aliasing it under both its Name() and the "" default key so the
// engine's per-role lookup resolves whether or not a profile mentions a
// backend. Used by tests that don't set up a multi-backend fleet.
func singleAgent(a agent.Agent) map[string]agent.Agent {
	return map[string]agent.Agent{"": a, a.Name(): a}
}

// TestResearchReadOnlyRefusedOnNonEnforcingBackend: an autonomous
// research stage on a backend that cannot structurally strip its write
// tools (ReadOnlyEnforce=false) is refused before any session is created
// — the engine fails closed, so the "documented no-op" can never
// silently downgrade the read-only guarantee to the tripwire alone. The
// refusal names the backend and no session is spawned.
func TestResearchReadOnlyRefusedOnNonEnforcingBackend(t *testing.T) {
	rec := &recorder{Fake: agent.NewFake("ok")} // Caps omit ReadOnlyEnforce
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(rec), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "rs investigate", domain.StageInvestigate)
	f.ID = domain.FeatureID("RS-001")
	f.Kind = domain.KindResearch
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	if err := e.Run(f); err != nil {
		t.Fatalf("Run returned %v, want nil (the refusal surfaces on the session)", err)
	}
	waitState(t, e, "RS-001", StatePaused)
	if s := e.Get("RS-001"); s == nil || s.Snapshot().Err == nil {
		t.Fatal("refused run recorded no session error")
	} else if msg := s.Snapshot().Err.Error(); !strings.Contains(msg, "cannot enforce a read-only") {
		t.Errorf("refusal error = %q, want a fail-closed read-only refusal", msg)
	}
	if rec.count() != 0 {
		t.Fatalf("session count = %d, want 0 (no session created before the refusal)", rec.count())
	}
}
