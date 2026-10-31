package engine

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/domain"
)

// TestABoardEditRepairsItsProfiles: a board that came up on a profiles
// file it could not read lists what the file holds and why it is refused,
// and the person's delete repairs it — applied at once, with the backend
// the repaired file names started, so no restart is needed.
func TestABoardEditRepairsItsProfiles(t *testing.T) {
	ws, store, wt := newRepo(t)
	path := ws.ProfilesFile()
	if err := os.WriteFile(path, []byte(`Default: claude
profiles:
  antigravity:
    implementer: { backend: antigravity, model: g }
  claude:
    implementer: { backend: claude, model: claude-sonnet-5-5 }
  open:
    implementer: { backend: opencode, model: openrouter/z-ai/glm }
`), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := agent.NewFake("ok")
	var started []string
	e := New(Config{
		Agents: map[string]agent.Agent{"": fake, "claude": fake}, Store: store, Worktrees: wt, Workspace: ws,
		StartAgent: func(name string) (agent.Agent, error) {
			started = append(started, name)
			return agent.NewFake("ok"), nil
		},
	})
	t.Cleanup(func() { e.Close() })

	v := e.ProfilesView()
	if !strings.Contains(v.Problem, `unknown key "Default"`) || len(v.Profiles) != 3 || v.Default != "claude" {
		t.Fatalf("view of the broken file = %+v", v)
	}
	if err := e.DeleteProfile("antigravity"); err != nil {
		t.Fatalf("DeleteProfile: %v", err)
	}
	if v = e.ProfilesView(); v.Problem != "" || len(v.Profiles) != 2 || v.Profiles[0].Name != "claude" {
		t.Fatalf("view after the repair = %+v", v)
	}
	if !slices.Equal(started, []string{"opencode"}) {
		t.Errorf("started %v, want the repaired file's opencode", started)
	}
	if rc, b := e.resolveRole("open", agent.RoleImplementer); b != "opencode" || rc.Model != "openrouter/z-ai/glm" {
		t.Errorf("open's implementer = %q on %q", rc.Model, b)
	}
	if got := e.ProfileNames(); !slices.Equal(got, []string{"claude", "open"}) {
		t.Errorf("ProfileNames = %v", got)
	}
}

func TestSaveProfileChecksEachRole(t *testing.T) {
	ws, store, wt := newRepo(t)
	fake := agent.NewFake("ok")
	e := New(Config{Agents: map[string]agent.Agent{"": fake, "claude": fake}, Store: store, Worktrees: wt, Workspace: ws})
	t.Cleanup(func() { e.Close() })
	ctx := context.Background()

	for _, rc := range []config.RoleConfig{
		{Backend: "claude", Model: "gpt-5"},
		{Backend: "claude", Model: "claude-haiku-4.5"},
		{Backend: "opencode", Model: "bare"},
		{Backend: "claude"},
	} {
		if err := e.SaveProfile(ctx, "p", config.ProfileEdit{Roles: config.Profile{"reviewer": rc}}, false); err == nil {
			t.Errorf("SaveProfile with %+v was written", rc)
		}
	}
	// a backend this board cannot start refuses the save before the write
	err := e.SaveProfile(ctx, "p", config.ProfileEdit{Roles: config.Profile{"reviewer": {Backend: "copilot", Model: "gpt-5"}}}, false)
	if err == nil || !strings.Contains(err.Error(), "copilot") {
		t.Errorf("a backend the board cannot start: %v", err)
	}
	if _, err := os.Stat(ws.ProfilesFile()); !os.IsNotExist(err) {
		t.Fatalf("a refused save wrote the file: %v", err)
	}

	edit := config.ProfileEdit{Roles: config.Profile{
		"implementer": {Backend: "claude", Model: " claude-sonnet-5-5 "},
		"scout":       {Model: "claude-haiku-5-5"},
	}}
	if err := e.SaveProfile(ctx, "mine", edit, false); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if rc, _ := e.stepRole("mine", domain.RoleScout, ""); rc.Model != "claude-haiku-5-5" {
		t.Errorf("a scout step runs %q", rc.Model)
	}
	if rc, b := e.resolveRole("mine", agent.RoleImplementer); rc.Model != "claude-sonnet-5-5" || b != "claude" {
		t.Errorf("the implementer runs %q on %q", rc.Model, b)
	}
	v := e.ProfilesView()
	if v.Default != "mine" || len(v.Profiles) != 1 || v.Profiles[0].Roles[0].Role != "implementer" {
		t.Errorf("view = %+v", v)
	}
	if err := e.SaveProfile(ctx, "other", edit, true); err != nil {
		t.Fatal(err)
	}
	if err := e.SetDefaultProfile("mine"); err != nil {
		t.Fatal(err)
	}
	if got := e.ProfileNames(); !slices.Equal(got, []string{"mine", "other"}) {
		t.Errorf("ProfileNames = %v", got)
	}
}

// TestAStepRunsOnItsRolesSlot: a step's model comes from the slot its
// recipe named, then its role's own, then the architect for design work,
// then the implementer — never from a tier.
func TestAStepRunsOnItsRolesSlot(t *testing.T) {
	fake := agent.NewFake("ok")
	e := New(Config{Agents: map[string]agent.Agent{"": fake}, Model: "fallback", Profiles: config.Profiles{
		Default: "p",
		Profiles: map[string]config.Profile{"p": {
			"implementer":  {Model: "impl"},
			"architect":    {Model: "arch"},
			"reviewer":     {Model: "rev"},
			"reviewer-alt": {Model: "rev-alt"},
			"scribe":       {Model: "scribe"},
			"step-strong":  {Model: "a tier, never read"},
		}},
	}})
	t.Cleanup(func() { e.Close() })
	for _, c := range []struct {
		role domain.WorkflowRole
		slot string
		want string
	}{
		{domain.RoleReviewer, "", "rev"},
		{domain.RoleReviewer, "reviewer-alt", "rev-alt"},
		{domain.RoleReviewer, "reviewer-nope", "rev"},
		{domain.RoleScribe, "", "scribe"},
		{domain.RolePlanner, "", "arch"},
		{domain.RoleContractor, "", "arch"},
		{domain.RoleScout, "", "impl"},
		{domain.RoleTester, "", "impl"},
	} {
		if rc, _ := e.stepRole("p", c.role, c.slot); rc.Model != c.want {
			t.Errorf("%s with slot %q runs %q, want %q", c.role, c.slot, rc.Model, c.want)
		}
	}
	// A named agent and model win over the slot; each stands alone.
	for _, c := range []struct {
		step         domain.Step
		model, agent string
	}{
		{domain.Step{Role: domain.RoleReviewer, Slot: "reviewer-alt", Model: "m1"}, "m1", ""},
		{domain.Step{Role: domain.RoleReviewer, Agent: "claude"}, "", "claude"},
		{domain.Step{Role: domain.RoleReviewer, Agent: "claude", Model: "m2"}, "m2", "claude"},
		{domain.Step{Role: domain.RoleReviewer}, "rev", ""},
	} {
		rc, backend := e.stepRoleFor("p", c.step)
		if rc.Model != c.model || backend != c.agent {
			t.Errorf("%+v runs %q on %q, want %q on %q", c.step, rc.Model, backend, c.model, c.agent)
		}
	}
}

// A step naming an agent gummi has no adapter for is refused with the list
// of those it has, the way a session's picker refuses it.
func TestAStepNamingAnUnknownAgentIsRefused(t *testing.T) {
	es := []domain.Entry{{Step: &domain.Step{ID: "a", Role: domain.RoleReviewer, Agent: "claud"}}}
	p := agentProblems(es)
	if len(p) != 1 || !strings.Contains(p[0], "unknown agent") || !strings.Contains(p[0], "claude") {
		t.Fatalf("problems = %v", p)
	}
	es[0].Step.Agent = "claude"
	if p := agentProblems(es); len(p) != 0 {
		t.Fatalf("problems = %v", p)
	}
}
