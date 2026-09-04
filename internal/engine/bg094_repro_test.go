package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// TestBG094ReadOnlyRefusalNamesTheBackend: the refusal that tells the
// reader to point a role at a different backend has to say which backend
// it is pointed at now.
//
// It used to format the resolved backend NAME, and an empty name is
// resolveRole's documented fallback to the engine's default — the normal
// state of a workspace with no profiles.yaml, which is most of them — so
// the message read `backend "" cannot enforce a read-only research
// session`. TestResearchReadOnlyRefusedOnNonEnforcingBackend claimed in
// its own doc comment that the refusal names the backend; it never
// checked, so nothing caught it.
func TestBG094ReadOnlyRefusalNamesTheBackend(t *testing.T) {
	rec := &recorder{Fake: agent.NewFake("ok")} // Caps omit ReadOnlyEnforce
	ws, store, wt := newRepo(t)
	// no Profiles: resolveRole falls back to the single-model config and
	// returns an empty backend name, exactly as an unconfigured workspace
	// does.
	e := New(Config{Agents: singleAgent(rec), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "rs investigate", domain.StageInvestigate)
	f.ID = domain.FeatureID("RS-001")
	f.Kind = domain.KindResearch
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "RS-001", StatePaused)
	s := e.Get("RS-001")
	if s == nil || s.Snapshot().Err == nil {
		t.Fatal("refused run recorded no session error")
	}
	msg := s.Snapshot().Err.Error()
	if strings.Contains(msg, `backend ""`) {
		t.Errorf("refusal names no backend at all: %q", msg)
	}
	if !strings.Contains(msg, rec.Name()) {
		t.Errorf("refusal = %q, want it to name the backend that refused (%q)", msg, rec.Name())
	}
}
