package engine

import (
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/domain"
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
				"architect":   {Model: "claude-sonnet"},
				"implementer": {Backend: "headless", Model: "qwen"},
			},
		},
	}
}

func TestResolveRolePerProfile(t *testing.T) {
	ws, store, wt := newRepo(t)
	rec := recordingAgent()
	e := New(Config{
		Agents: singleAgent(rec), Store: store, Worktrees: wt, Workspace: ws,
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

// TestResolveRoleBackend proves the profile's backend field routes the
// session at a specific adapter — thrifty's implementer names `headless`,
// and only that adapter should be handed the session.
func TestResolveRoleBackend(t *testing.T) {
	ws, store, wt := newRepo(t)
	def := recordingAgent()
	def.name = "copilot"
	head := recordingAgent()
	head.name = "headless"
	agents := map[string]agent.Agent{
		"":         def, // default (unspecified backend)
		"copilot":  def,
		"headless": head,
	}
	e := New(Config{
		Agents: agents, Store: store, Worktrees: wt, Workspace: ws,
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
	if got := head.opts(); got.Model != "qwen" {
		t.Errorf("headless implementer model = %q, want qwen", got.Model)
	}
	if def.count() != 0 {
		t.Errorf("default backend saw %d sessions, want 0 (implementer routed elsewhere)", def.count())
	}
}

func TestResolveRoleFallback(t *testing.T) {
	ws, store, wt := newRepo(t)
	rec := recordingAgent()
	e := New(Config{
		Agents: singleAgent(rec), Store: store, Worktrees: wt, Workspace: ws,
		Model: "fallback-model", MaxActive: 1, Profiles: profilesFixture(),
	})
	t.Cleanup(func() { e.Close() })

	// thrifty has no reviewer role; verify (reviewer) falls back to Model
	f := feature(3, "verify", domain.StageVerify)
	f.Profile = "thrifty"
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-003", StateDone)
	if got := rec.opts(); got.Model != "fallback-model" {
		t.Errorf("uncovered role model = %q, want the fallback", got.Model)
	}
}

// Verify is reviewer work (adversarial judgment gating the landing) —
// pin the mapping and that a profile's reviewer model carries over.
func TestVerifyStageUsesReviewerRole(t *testing.T) {
	if role, ok := roleForStage(domain.StageVerify); !ok || role != agent.RoleReviewer {
		t.Fatalf("roleForStage(verify) = %s/%v, want reviewer", role, ok)
	}

	ws, store, wt := newRepo(t)
	rec := recordingAgent()
	e := New(Config{
		Agents: singleAgent(rec), Store: store, Worktrees: wt, Workspace: ws,
		Model: "fallback-model", MaxActive: 1, Profiles: profilesFixture(),
	})
	t.Cleanup(func() { e.Close() })

	f := feature(5, "verify", domain.StageVerify)
	f.Profile = "premium"
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-005", StateDone)
	if got := rec.opts(); got.Model != "gpt-5-codex" || got.Role != agent.RoleReviewer {
		t.Errorf("premium verify = %q (role %s), want gpt-5-codex reviewer", got.Model, got.Role)
	}
}

func TestResolveRoleUnknownProfileUsesDefault(t *testing.T) {
	ws, store, wt := newRepo(t)
	def := recordingAgent()
	def.name = "copilot"
	head := recordingAgent()
	head.name = "headless"
	agents := map[string]agent.Agent{
		"":         def,
		"copilot":  def,
		"headless": head,
	}
	e := New(Config{
		Agents: agents, Store: store, Worktrees: wt, Workspace: ws,
		Model: "fallback", MaxActive: 1, Profiles: profilesFixture(),
	})
	t.Cleanup(func() { e.Close() })

	// a feature with an unknown profile name → the file's default (thrifty).
	// thrifty's implementer names backend: headless.
	f := feature(4, "impl", domain.StageImplement)
	f.Profile = "does-not-exist"
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-004", StateDone)
	if got := head.opts(); got.Model != "qwen" {
		t.Errorf("unknown profile: headless model = %q, want the default profile's (qwen)", got.Model)
	}
}

// resolveRole is also exercised directly for the no-profiles case.
func TestResolveRoleNoProfiles(t *testing.T) {
	e := &Engine{cfg: Config{Model: "only-model"}}
	m, backend, otm := e.resolveRole("anything", agent.RoleArchitect)
	if m != "only-model" || backend != "" || otm != 0 {
		t.Errorf("no-profiles resolve = (%q, %q, %d), want (only-model, \"\", 0)", m, backend, otm)
	}
}
