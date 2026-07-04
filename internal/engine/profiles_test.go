package engine

import (
	"context"
	"testing"

	"github.com/morphia/gummi/internal/agent"
	"github.com/morphia/gummi/internal/config"
	"github.com/morphia/gummi/internal/domain"
)

func profilesFixture() config.Profiles {
	return config.Profiles{
		Default: "thrifty",
		Profiles: map[string]config.Profile{
			"premium": {
				"architect":   {Model: "claude-opus"},
				"implementer": {Model: "claude-sonnet"},
				"reviewer":    {Model: "gpt-5-codex"},
			},
			"thrifty": {
				"architect": {Model: "claude-sonnet"},
				"implementer": {
					Model:    "qwen",
					Provider: &config.ProviderConfig{Type: "openai", BaseURL: "http://127.0.0.1:8080/v1", APIKeyEnv: "LLAMA_API_KEY"},
				},
			},
		},
	}
}

func TestResolveRolePerProfile(t *testing.T) {
	ws, store, wt := newRepo(t)
	rec := recordingAgent()
	e := New(Config{
		Agent: rec, Store: store, Worktrees: wt, Workspace: ws,
		Model: "fallback-model", MaxActive: 1, Profiles: profilesFixture(),
	})
	t.Cleanup(func() { e.Close() })

	// a premium feature at review → the reviewer's model
	f := feature(1, "review me", domain.StageReview)
	f.Profile = "premium"
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-001", StateDone)
	if got := rec.opts(); got.Model != "gpt-5-codex" || got.Role != agent.RoleReviewer {
		t.Errorf("premium review model = %q (role %s), want gpt-5-codex reviewer", got.Model, got.Role)
	}
}

func TestResolveRoleBYOKProvider(t *testing.T) {
	ws, store, wt := newRepo(t)
	rec := recordingAgent()
	e := New(Config{
		Agent: rec, Store: store, Worktrees: wt, Workspace: ws,
		Model: "fallback", MaxActive: 1, Profiles: profilesFixture(),
	})
	t.Cleanup(func() { e.Close() })

	f := feature(2, "impl", domain.StageImplement)
	f.Profile = "thrifty"
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-002", StateDone)
	got := rec.opts()
	if got.Model != "qwen" {
		t.Errorf("thrifty implement model = %q, want qwen", got.Model)
	}
	if got.Provider.BaseURL != "http://127.0.0.1:8080/v1" || got.Provider.APIKeyEnv != "LLAMA_API_KEY" {
		t.Errorf("byok provider not routed: %+v", got.Provider)
	}
}

func TestResolveRoleFallback(t *testing.T) {
	ws, store, wt := newRepo(t)
	rec := recordingAgent()
	e := New(Config{
		Agent: rec, Store: store, Worktrees: wt, Workspace: ws,
		Model: "fallback-model", MaxActive: 1, Profiles: profilesFixture(),
	})
	t.Cleanup(func() { e.Close() })

	// premium has no scribe role; verify (scribe) falls back to Model
	f := feature(3, "verify", domain.StageVerify)
	f.Profile = "premium"
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-003", StateDone)
	if got := rec.opts(); got.Model != "fallback-model" {
		t.Errorf("uncovered role model = %q, want the fallback", got.Model)
	}
}

func TestResolveRoleUnknownProfileUsesDefault(t *testing.T) {
	ws, store, wt := newRepo(t)
	rec := recordingAgent()
	e := New(Config{
		Agent: rec, Store: store, Worktrees: wt, Workspace: ws,
		Model: "fallback", MaxActive: 1, Profiles: profilesFixture(),
	})
	t.Cleanup(func() { e.Close() })

	// a feature with an unknown profile name → the file's default (thrifty)
	f := feature(4, "impl", domain.StageImplement)
	f.Profile = "does-not-exist"
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-004", StateDone)
	if got := rec.opts(); got.Model != "qwen" { // thrifty implementer
		t.Errorf("unknown profile model = %q, want the default profile's (qwen)", got.Model)
	}
}

// resolveRole is also exercised directly for the no-profiles case.
func TestResolveRoleNoProfiles(t *testing.T) {
	e := &Engine{cfg: Config{Model: "only-model", Provider: agent.Provider{BaseURL: "http://x/v1"}}}
	m, p := e.resolveRole("anything", agent.RoleArchitect)
	if m != "only-model" || p.BaseURL != "http://x/v1" {
		t.Errorf("no-profiles resolve = %q/%+v, want the config fallback", m, p)
	}
	_ = context.Background()
}
