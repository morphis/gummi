package engine

import (
	"context"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/domain"
)

// changeProfileFixture declares two profiles, each routing every stage
// role to its own backend/model, so a restart under a different profile
// is provable by which backend received the new session — not just by
// what the store now says.
func changeProfileFixture() config.Profiles {
	return config.Profiles{
		Default: "alpha",
		Profiles: map[string]config.Profile{
			"alpha": {
				"architect":   {Backend: "one", Model: "alpha-architect"},
				"implementer": {Backend: "one", Model: "alpha-model"},
				"reviewer":    {Backend: "one", Model: "alpha-review"},
			},
			"beta": {
				"architect":   {Backend: "two", Model: "beta-architect"},
				"implementer": {Backend: "two", Model: "beta-model"},
				"reviewer":    {Backend: "two", Model: "beta-review"},
			},
		},
	}
}

// TestChangeProfileUnknownNameErrors: a name absent from profiles.yaml is
// refused before any store write or session touch.
func TestChangeProfileUnknownNameErrors(t *testing.T) {
	ws, store, wt := newRepo(t)
	one := recordingAgent()
	one.name = "one"
	e := New(Config{
		Agents: map[string]agent.Agent{"": one, "one": one}, Store: store, Worktrees: wt, Workspace: ws,
		Model: "fallback", MaxActive: 1, Profiles: changeProfileFixture(),
	})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "impl", domain.StageImplement)
	f.Profile = "alpha"
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}

	if err := e.ChangeProfile(context.Background(), f.ID, "does-not-exist"); err == nil {
		t.Fatal("ChangeProfile with an unknown name = nil, want an error")
	}
	got, err := store.GetFeature(context.Background(), f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Profile != "alpha" {
		t.Errorf("persisted profile = %q, want unchanged alpha", got.Profile)
	}
	if e.Get(f.ID) != nil {
		t.Error("a session was created for an unknown-profile ChangeProfile call")
	}
}

// TestChangeProfilePersistsWithNoLiveSession: no live session means
// nothing to restart — the new profile just lands in the store.
func TestChangeProfilePersistsWithNoLiveSession(t *testing.T) {
	ws, store, wt := newRepo(t)
	one := recordingAgent()
	one.name = "one"
	e := New(Config{
		Agents: map[string]agent.Agent{"": one, "one": one}, Store: store, Worktrees: wt, Workspace: ws,
		Model: "fallback", MaxActive: 1, Profiles: changeProfileFixture(),
	})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "impl", domain.StageImplement)
	f.Profile = "alpha"
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}

	if err := e.ChangeProfile(context.Background(), f.ID, "beta"); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetFeature(context.Background(), f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Profile != "beta" {
		t.Errorf("persisted profile = %q, want beta", got.Profile)
	}
	if e.Get(f.ID) != nil {
		t.Error("no session should have been started for an idle card")
	}
	if one.count() != 0 {
		t.Errorf("backend saw %d sessions, want 0 (nothing was running)", one.count())
	}
}

// TestChangeProfileRestartsLiveAutonomousSession: a running autonomous
// stage is stopped via the interrupted-session path (Pause), the new
// profile is persisted, and a fresh session starts under it, resolving
// the new profile's backend/model rather than the old one's.
func TestChangeProfileRestartsLiveAutonomousSession(t *testing.T) {
	release := make(chan struct{})
	one := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		<-release
		return []agent.Event{{Kind: agent.EventIdle}}
	}}
	two := recordingAgent()
	two.name = "two"
	ws, store, wt := newRepo(t)
	e := New(Config{
		Agents: map[string]agent.Agent{"": one, "one": one, "two": two}, Store: store, Worktrees: wt, Workspace: ws,
		Model: "fallback", MaxActive: 1, Profiles: changeProfileFixture(),
	})
	t.Cleanup(func() {
		close(release)
		e.Close()
	})

	f := feature(1, "impl", domain.StageImplement)
	f.Profile = "alpha"
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, f.ID, StateRunning)

	if err := e.ChangeProfile(context.Background(), f.ID, "beta"); err != nil {
		t.Fatal(err)
	}

	got, err := store.GetFeature(context.Background(), f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Profile != "beta" {
		t.Errorf("persisted profile = %q, want beta", got.Profile)
	}
	waitState(t, e, f.ID, StateDone)
	if two.count() != 1 {
		t.Errorf("new-profile backend saw %d sessions, want 1", two.count())
	}
	if opts := two.opts(); opts.Model != "beta-model" {
		t.Errorf("restarted session model = %q, want beta-model", opts.Model)
	}
}

// TestChangeProfileRestartsLiveInteractiveSession: an attached chat
// session is stopped and re-attached fresh under the new profile,
// carrying the transcript over (Attach's own restart-reattach path) —
// not left pointing at the old, now-closed backend handle.
func TestChangeProfileRestartsLiveInteractiveSession(t *testing.T) {
	one := recordingAgent()
	one.name = "one"
	two := recordingAgent()
	two.name = "two"
	ws, store, wt := newRepo(t)
	e := New(Config{
		Agents: map[string]agent.Agent{"": one, "one": one, "two": two}, Store: store, Worktrees: wt, Workspace: ws,
		Model: "fallback", MaxActive: 1, Profiles: changeProfileFixture(),
	})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "brainstorm", domain.StageBrainstorm)
	f.Profile = "alpha"
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}

	if _, err := e.Attach(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventStarted)
	if !e.Get(f.ID).Live() {
		t.Fatal("attached session is not live")
	}

	if err := e.ChangeProfile(context.Background(), f.ID, "beta"); err != nil {
		t.Fatal(err)
	}

	got, err := store.GetFeature(context.Background(), f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Profile != "beta" {
		t.Errorf("persisted profile = %q, want beta", got.Profile)
	}
	if s := e.Get(f.ID); !s.Live() {
		t.Fatal("restarted session is not live")
	}
	if two.count() != 1 {
		t.Errorf("new-profile backend saw %d sessions, want 1", two.count())
	}
	if opts := two.opts(); opts.Model != "beta-architect" {
		t.Errorf("restarted session model = %q, want beta-architect", opts.Model)
	}
	if one.count() != 1 {
		t.Errorf("old-profile backend saw %d sessions, want 1 (only the original attach)", one.count())
	}
}

// TestChangeProfilePersistsAcrossNextStage: the changed profile is not a
// one-off override for the session it restarted — a later stage started
// independently still reads it from the store and resolves against it.
func TestChangeProfilePersistsAcrossNextStage(t *testing.T) {
	one := recordingAgent()
	one.name = "one"
	two := recordingAgent()
	two.name = "two"
	ws, store, wt := newRepo(t)
	e := New(Config{
		Agents: map[string]agent.Agent{"": one, "one": one, "two": two}, Store: store, Worktrees: wt, Workspace: ws,
		Model: "fallback", MaxActive: 1, Profiles: changeProfileFixture(),
	})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "impl", domain.StageImplement)
	f.Profile = "alpha"
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}

	if err := e.ChangeProfile(context.Background(), f.ID, "beta"); err != nil {
		t.Fatal(err)
	}

	next, err := store.GetFeature(context.Background(), f.ID)
	if err != nil {
		t.Fatal(err)
	}
	next.Stage = domain.StageReview
	withWorktree(t, wt, next)
	if err := e.Run(next); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, next.ID, StateDone)
	if two.count() != 1 {
		t.Errorf("next stage used the new-profile backend %d times, want 1", two.count())
	}
	if opts := two.opts(); opts.Model != "beta-review" {
		t.Errorf("next stage model = %q, want beta-review", opts.Model)
	}
	if one.count() != 0 {
		t.Errorf("next stage used the old-profile backend %d times, want 0", one.count())
	}
}
