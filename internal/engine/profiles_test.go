package engine

import (
	"context"
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

// TestResolveRolePropagatesProvider proves a zz-backed role's provider:
// field reaches the adapter's SessionOpts — guarding the plumbing from
// silently regressing to always-empty.
func TestResolveRolePropagatesProvider(t *testing.T) {
	ws, store, wt := newRepo(t)
	rec := recordingAgent()
	rec.name = "zz"
	agents := map[string]agent.Agent{"": rec, "zz": rec}
	e := New(Config{
		Agents: agents, Store: store, Worktrees: wt, Workspace: ws,
		Model: "fallback", MaxActive: 1,
		Profiles: config.Profiles{
			Default: "mixed",
			Profiles: map[string]config.Profile{
				"mixed": {
					"implementer": {Backend: "zz", Model: "m", Provider: "mab"},
				},
			},
		},
	})
	t.Cleanup(func() { e.Close() })

	f := feature(6, "impl", domain.StageImplement)
	f.Profile = "mixed"
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-006", StateDone)
	if got := rec.opts().Provider; got != "mab" {
		t.Errorf("SessionOpts.Provider = %q, want mab", got)
	}
}

// TestResolveRolePropagatesThink proves a zz-backed role's think: field
// reaches the adapter's SessionOpts — guarding the plumbing from silently
// regressing to always-empty, the same way TestResolveRolePropagatesProvider
// guards Provider.
func TestResolveRolePropagatesThink(t *testing.T) {
	ws, store, wt := newRepo(t)
	rec := recordingAgent()
	rec.name = "zz"
	agents := map[string]agent.Agent{"": rec, "zz": rec}
	e := New(Config{
		Agents: agents, Store: store, Worktrees: wt, Workspace: ws,
		Model: "fallback", MaxActive: 1,
		Profiles: config.Profiles{
			Default: "mixed",
			Profiles: map[string]config.Profile{
				"mixed": {
					"implementer": {Backend: "zz", Model: "m", Think: "high"},
				},
			},
		},
	})
	t.Cleanup(func() { e.Close() })

	f := feature(7, "impl", domain.StageImplement)
	f.Profile = "mixed"
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-007", StateDone)
	if got := rec.opts().Think; got != "high" {
		t.Errorf("SessionOpts.Think = %q, want high", got)
	}
}

// TestScribeSessionCarriesThink guards the transient-session sites (this
// one via DraftCommitMessage's scribe role): every callsite that copies
// rc.Provider onto SessionOpts must also copy rc.Think, not just the
// stage seam TestResolveRolePropagatesThink covers.
func TestScribeSessionCarriesThink(t *testing.T) {
	rec := &recorder{Fake: agent.NewFake("```gummi-commit\nfeat(ui): prefill the merge dialog\n\n- drafts from the spec\n```")}
	rec.name = "zz"
	ws, store, wt := newRepo(t)
	agents := map[string]agent.Agent{"": rec, "zz": rec}
	e := New(Config{
		Agents: agents, Store: store, Worktrees: wt, Workspace: ws,
		Model: "fallback", MaxActive: 1,
		Profiles: config.Profiles{
			Default: "mixed",
			Profiles: map[string]config.Profile{
				"mixed": {
					"scribe": {Backend: "zz", Model: "m", Think: "low"},
				},
			},
		},
	})
	t.Cleanup(func() { e.Close() })
	f := feature(8, "impl", domain.StageImplement)
	f.Profile = "mixed"
	withWorktree(t, wt, f)
	if _, err := e.DraftCommitMessage(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	if got := rec.opts().Think; got != "low" {
		t.Errorf("SessionOpts.Think = %q, want low", got)
	}
}

// resolveRole is also exercised directly for the no-profiles case.
func TestResolveRoleNoProfiles(t *testing.T) {
	e := &Engine{cfg: Config{Model: "only-model"}}
	rc, backend := e.resolveRole("anything", agent.RoleArchitect)
	if rc.Model != "only-model" || backend != "" || rc.Provider != "" {
		t.Errorf("no-profiles resolve = (%+v, %q), want (RoleConfig{Model: only-model}, \"\")", rc, backend)
	}
}

// TestBoardProfilesOrderAndResolution pins BoardProfiles to
// config.Profiles.Names' order (declared default first, rest sorted)
// and to resolveBoardRole's actual resolution — premium here has no
// board role and must borrow the architect's, exactly as
// TestBoardRolePairsModelAndBackend pins for resolveBoardRole itself.
func TestBoardProfilesOrderAndResolution(t *testing.T) {
	e := &Engine{cfg: Config{Profiles: profilesFixture()}}
	got := e.BoardProfiles()
	want := []BoardProfile{
		{Name: "thrifty", Backend: "", Model: "claude-sonnet"}, // Default, first
		{Name: "premium", Backend: "", Model: "claude-opus"},   // borrows architect
	}
	if len(got) != len(want) {
		t.Fatalf("BoardProfiles() = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestBoardProfilesReportsEmptyBackendAsEmpty: a resolved backend of ""
// (the engine's default) must be reported as-is, never papered over with
// a placeholder string — wording that for a human is the UI's job.
func TestBoardProfilesReportsEmptyBackendAsEmpty(t *testing.T) {
	e := &Engine{cfg: Config{Profiles: config.Profiles{
		Default: "p",
		Profiles: map[string]config.Profile{
			"p": {"architect": {Model: "m"}}, // no backend: declared
		},
	}}}
	got := e.BoardProfiles()
	if len(got) != 1 || got[0].Backend != "" {
		t.Errorf("BoardProfiles() = %+v, want one entry with an empty Backend", got)
	}
}

// TestBoardProfilesNoProfiles: an engine with no profiles.yaml returns
// nil rather than panicking on a nil Profiles map.
func TestBoardProfilesNoProfiles(t *testing.T) {
	e := &Engine{cfg: Config{Model: "only-model"}}
	if got := e.BoardProfiles(); got != nil {
		t.Errorf("BoardProfiles() = %+v, want nil", got)
	}
}

// cardRoleFixture declares one profile whose implementer and reviewer
// roles diverge from each other and from the architect — so a picker
// reading the wrong resolver (resolveBoardRole, which borrows the
// architect's for any role it doesn't itself declare) is caught
// red-handed rather than accidentally agreeing.
func cardRoleFixture() config.Profiles {
	return config.Profiles{
		Default: "mixed",
		Profiles: map[string]config.Profile{
			"mixed": {
				"architect":   {Model: "architect-model"},
				"implementer": {Backend: "impl-backend", Model: "impl-model"},
				"reviewer":    {Backend: "review-backend", Model: "review-model"},
			},
		},
	}
}

// TestCardProfilesResolvesPerCardRole pins CardProfiles to the card's own
// role — implement and review resolve to their own distinct
// backend/model, not the board/architect one BoardProfiles reports for
// the same profile.
func TestCardProfilesResolvesPerCardRole(t *testing.T) {
	e := &Engine{cfg: Config{Profiles: cardRoleFixture()}}

	implement := e.CardProfiles(domain.StageImplement)
	if len(implement) != 1 || implement[0].Backend != "impl-backend" || implement[0].Model != "impl-model" {
		t.Errorf("CardProfiles(implement) = %+v, want impl-backend/impl-model", implement)
	}
	review := e.CardProfiles(domain.StageReview)
	if len(review) != 1 || review[0].Backend != "review-backend" || review[0].Model != "review-model" {
		t.Errorf("CardProfiles(review) = %+v, want review-backend/review-model", review)
	}
	// sanity: BoardProfiles borrows the architect's, proving the two
	// resolvers genuinely disagree for this fixture.
	board := e.BoardProfiles()
	if len(board) != 1 || board[0].Model != "architect-model" {
		t.Errorf("BoardProfiles() = %+v, want architect-model", board)
	}
}

// TestCardProfilesFallsBackForUndeclaredRole: a stage with no agent
// action (roleForStage's ok=false) leaves the role at its zero value, and
// resolveRole's existing undeclared-role fallback — the engine's
// single-model config — answers it, the same fallback every other
// undeclared-role lookup gets.
func TestCardProfilesFallsBackForUndeclaredRole(t *testing.T) {
	e := &Engine{cfg: Config{Model: "only-model", Profiles: cardRoleFixture()}}
	got := e.CardProfiles(domain.StageDone)
	if len(got) != 1 || got[0].Backend != "" || got[0].Model != "only-model" {
		t.Errorf("CardProfiles(done) = %+v, want one entry falling back to only-model with an empty backend", got)
	}
}

// TestCardProfilesNoProfiles: nil-safe like BoardProfiles, for the
// identical reason.
func TestCardProfilesNoProfiles(t *testing.T) {
	e := &Engine{cfg: Config{Model: "only-model"}}
	if got := e.CardProfiles(domain.StageImplement); got != nil {
		t.Errorf("CardProfiles() = %+v, want nil", got)
	}
}

// TestKnownModelsDedupsAndSorts: a model reused across profiles/roles
// collapses into one KnownModel entry naming every use, both the model
// list and each entry's Uses sorted deterministically.
func TestKnownModelsDedupsAndSorts(t *testing.T) {
	e := &Engine{cfg: Config{Profiles: config.Profiles{
		Default: "a",
		Profiles: map[string]config.Profile{
			"a": {
				"architect":   {Model: "shared-model"},
				"implementer": {Model: "a-only-model"},
			},
			"b": {
				"reviewer": {Model: "shared-model"},
			},
		},
	}}}
	got := e.KnownModels()
	want := []KnownModel{
		{Model: "a-only-model", Uses: []string{"a · implementer"}},
		{Model: "shared-model", Uses: []string{"a · architect", "b · reviewer"}},
	}
	if len(got) != len(want) {
		t.Fatalf("KnownModels() = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i].Model != want[i].Model {
			t.Errorf("entry %d Model = %q, want %q", i, got[i].Model, want[i].Model)
		}
		if len(got[i].Uses) != len(want[i].Uses) {
			t.Fatalf("entry %d Uses = %v, want %v", i, got[i].Uses, want[i].Uses)
		}
		for j := range want[i].Uses {
			if got[i].Uses[j] != want[i].Uses[j] {
				t.Errorf("entry %d Uses[%d] = %q, want %q", i, j, got[i].Uses[j], want[i].Uses[j])
			}
		}
	}
}

// TestKnownModelsIgnoresEmptyModel: a role with no model set (never
// valid per ParseProfiles, but this package builds Profiles by literal
// too, e.g. profilesFixture) must not surface as a KnownModel named "".
func TestKnownModelsIgnoresEmptyModel(t *testing.T) {
	e := &Engine{cfg: Config{Profiles: config.Profiles{
		Profiles: map[string]config.Profile{
			"a": {"architect": {Backend: "claude"}}, // no Model
		},
	}}}
	got := e.KnownModels()
	if got != nil {
		t.Errorf("KnownModels() = %+v, want nil (no non-empty model declared)", got)
	}
}

// TestKnownModelsNoProfiles: an engine with no profiles.yaml returns nil
// rather than panicking on a nil Profiles map.
func TestKnownModelsNoProfiles(t *testing.T) {
	e := &Engine{cfg: Config{Model: "only-model"}}
	if got := e.KnownModels(); got != nil {
		t.Errorf("KnownModels() = %+v, want nil", got)
	}
}
