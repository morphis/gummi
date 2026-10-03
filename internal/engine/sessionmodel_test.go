package engine

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/domain"
)

// sessionModelEngine is a board whose profiles only ever name backend
// "claude", plus a StartAgent that can start "codex" on demand — the shape
// that makes "a session may pick any installed agent" observable: codex is
// reachable only through the start, never through the profiles.
func sessionModelEngine(t *testing.T) (*Engine, *recorder, *recorder, *atomic.Int32) {
	t.Helper()
	ws, store, wt := newRepo(t)
	claude := recordingAgent()
	claude.name = "claude"
	codex := recordingAgent()
	codex.name = "codex"
	var starts atomic.Int32
	e := New(Config{
		Agents: map[string]agent.Agent{"": claude, "claude": claude},
		StartAgent: func(name string) (agent.Agent, error) {
			if name != "codex" {
				return nil, errors.New("not installed")
			}
			starts.Add(1)
			return codex, nil
		},
		Store: store, Worktrees: wt, Workspace: ws, Model: "fallback",
		Profiles: config.Profiles{Default: "alpha", Profiles: map[string]config.Profile{
			"alpha": {"implementer": {Backend: "claude", Model: "claude-sonnet-5-5"}},
		}},
	})
	t.Cleanup(func() { e.Close() })
	return e, claude, codex, &starts
}

// TestASessionRunsOnTheModelItNamed: a freeform card that names its own
// agent and model runs on them, not on its profile's implementer — and an
// agent the board did not start is started for it, once.
func TestASessionRunsOnTheModelItNamed(t *testing.T) {
	e, claude, codex, starts := sessionModelEngine(t)
	ctx := context.Background()

	f := freeformCard(1, "tidy the help text")
	f.Profile = "alpha"
	f.SessionBackend, f.SessionModel = "codex", "gpt-5"
	createFeature(t, e.cfg.Store, f)
	if _, err := e.OpenFreeform(ctx, f); err != nil {
		t.Fatal(err)
	}
	if got := codex.opts().Model; got != "gpt-5" {
		t.Errorf("the session ran on model %q, want the gpt-5 it named", got)
	}
	if claude.count() != 0 {
		t.Error("the profile's implementer was started for a session that named its own agent")
	}

	g := freeformCard(2, "a second codex session")
	g.SessionBackend, g.SessionModel = "codex", "gpt-5"
	createFeature(t, e.cfg.Store, g)
	if _, err := e.OpenFreeform(ctx, g); err != nil {
		t.Fatal(err)
	}
	if n := starts.Load(); n != 1 {
		t.Errorf("codex was started %d times for two sessions, want once", n)
	}
	if backend, model := e.SessionModel(g); backend != "codex" || model != "gpt-5" {
		t.Errorf("SessionModel = %s/%s, want codex/gpt-5", backend, model)
	}

	// A card that names nothing still runs on its profile's implementer.
	h := freeformCard(3, "a profile session")
	h.Profile = "alpha"
	if backend, model := e.SessionModel(h); backend != "claude" || model != "claude-sonnet-5-5" {
		t.Errorf("SessionModel for a card naming nothing = %s/%s, want the profile's claude/claude-sonnet-5-5", backend, model)
	}
}

// TestSwitchingASessionsModelCarriesTheConversation: switching mid-session
// stores the new pair on the card, and the next turn runs on it with the
// conversation so far replayed rather than resumed, since the old
// backend's conversation id is not the new one's.
func TestSwitchingASessionsModelCarriesTheConversation(t *testing.T) {
	e, _, codex, _ := sessionModelEngine(t)
	ctx := context.Background()

	f := freeformCard(4, "fix the flaky retry")
	f.Profile = "alpha"
	createFeature(t, e.cfg.Store, f)
	ff, err := e.OpenFreeform(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := ff.Send(ctx, "find out why TestRetry flakes"); err != nil {
		t.Fatal(err)
	}
	waitFreeformIdle(t, ff)

	if err := e.SwitchSessionModel(ctx, f.ID, "codex", "gpt-5"); err != nil {
		t.Fatal(err)
	}
	row, err := e.cfg.Store.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.SessionBackend != "codex" || row.SessionModel != "gpt-5" {
		t.Errorf("the card holds %q/%q after the switch, want codex/gpt-5", row.SessionBackend, row.SessionModel)
	}
	if !strings.Contains(transcriptText(ff.Snapshot()), "Switched to gpt-5 on codex") {
		t.Errorf("the thread does not say the model changed:\n%s", transcriptText(ff.Snapshot()))
	}

	if err := ff.Send(ctx, "now make the cap configurable"); err != nil {
		t.Fatal(err)
	}
	waitFreeformIdle(t, ff)
	opts := codex.opts()
	if opts.Model != "gpt-5" {
		t.Errorf("the turn after the switch ran on %q, want gpt-5", opts.Model)
	}
	if opts.ResumeID != "" {
		t.Errorf("the new backend was asked to resume %q, a conversation that belongs to the old one", opts.ResumeID)
	}
	if hints := strings.Join(opts.SystemHints, "\n"); !strings.Contains(hints, "find out why TestRetry flakes") {
		t.Errorf("the conversation so far did not go with the switch:\n%s", hints)
	}
}

// TestASessionSwitchIsRefusedWhereItCannotApply: a card in the workflow
// takes its agents from its profile, a pair no session could run is
// refused before it is stored, and a turn in flight keeps the model it
// started on.
func TestASessionSwitchIsRefusedWhereItCannotApply(t *testing.T) {
	ag := &agent.Fake{Responder: func(agent.SessionOpts, string) []agent.Event {
		// No idle: the turn is still in flight when the switch arrives.
		return []agent.Event{{Kind: agent.EventTextDelta, Text: "working"}}
	}}
	ag.Caps = agent.Capabilities{UsageEvents: true, Interrupt: true}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
	t.Cleanup(func() { e.Close() })
	ctx := context.Background()

	stage := feature(5, "a stage card", domain.StageImplement)
	createFeature(t, store, stage)
	if err := e.SwitchSessionModel(ctx, stage.ID, "codex", "gpt-5"); err == nil {
		t.Error("a card in the workflow was given a session model")
	}

	f := freeformCard(6, "busy session")
	createFeature(t, store, f)
	for _, bad := range [][2]string{{"nonesuch", "m"}, {"opencode", ""}, {"claude", "gpt-5"}, {"claude", "claude-haiku-4.5"}} {
		if err := e.SwitchSessionModel(ctx, f.ID, bad[0], bad[1]); err == nil {
			t.Errorf("switching to %s/%q was accepted", bad[0], bad[1])
		}
	}
	ff, err := e.OpenFreeform(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := ff.Send(ctx, "go slowly"); err != nil {
		t.Fatal(err)
	}
	if err := e.SwitchSessionModel(ctx, f.ID, "codex", "gpt-5"); !errors.Is(err, ErrSessionBusy) {
		t.Errorf("a switch mid-turn = %v, want ErrSessionBusy", err)
	}
	row, err := store.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.SessionBackend != "" || row.SessionModel != "" {
		t.Errorf("a refused switch was stored anyway: %q/%q", row.SessionBackend, row.SessionModel)
	}
}

// TestASessionStartsFromItsWholeOpeningMessage: the message a session was
// started with is its first turn verbatim, every line of it — the card
// itself keeps only a title, which is what a multi-line opening used to be
// cut down to.
func TestASessionStartsFromItsWholeOpeningMessage(t *testing.T) {
	r := recordingAgent()
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(r), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
	t.Cleanup(func() { e.Close() })
	ctx := context.Background()

	opening := "The retry test flakes on CI.\n\nFind out why, and keep the fix small."
	f := freeformCard(7, "The retry test flakes on CI")
	createFeature(t, store, f)
	ff, err := e.OpenFreeform(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := ff.KickoffWith(ctx, opening); err != nil {
		t.Fatal(err)
	}
	waitFreeformIdle(t, ff)
	if got := transcriptText(ff.Snapshot()); !strings.Contains(got, "keep the fix small") {
		t.Errorf("the opening message was cut short:\n%s", got)
	}
}

// TestAClosedSessionKeepsItsConversation: a session that ended — landed,
// handed off, continued as a spec — is still the record of what was said
// about the work on its branch, so its conversation stays readable on the
// closed card, with what became of it, here and after a restart.
func TestAClosedSessionKeepsItsConversation(t *testing.T) {
	ws, store, wt := newRepo(t)
	ctx := context.Background()
	f := freeformCard(30, "closed but not forgotten")
	createFeature(t, store, f)

	e1 := New(Config{Agents: singleAgent(agent.NewFake("Added pty.go.")), Store: store, Worktrees: wt, Workspace: ws, Model: "m", Persist: true})
	ff, err := e1.OpenFreeform(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := ff.Send(ctx, "drop the leaked pty fd"); err != nil {
		t.Fatal(err)
	}
	waitFreeformIdle(t, ff)
	if err := ff.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CloseFreeform(ctx, f.ID, "t"); err != nil {
		t.Fatal(err)
	}
	if e1.Freeform(f.ID) != nil {
		t.Fatal("a closed session is still open in the engine")
	}
	e1.NoteClosedFreeform(f.ID, "Continued as the spec FD-031.")
	snap, ok := e1.FreeformHistory(f.ID)
	if !ok || !strings.Contains(transcriptText(snap), "drop the leaked pty fd") || !strings.Contains(transcriptText(snap), "Continued as the spec FD-031.") {
		t.Fatalf("the closed session's conversation is not readable (ok=%v):\n%s", ok, transcriptText(snap))
	}
	if err := e1.Close(); err != nil {
		t.Fatal(err)
	}

	e2 := New(Config{Agents: singleAgent(recordingAgent()), Store: store, Worktrees: wt, Workspace: ws, Model: "m", Persist: true})
	t.Cleanup(func() { e2.Close() })
	if err := e2.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	if e2.Freeform(f.ID) != nil {
		t.Error("a closed session came back open after a restart")
	}
	snap, ok = e2.FreeformHistory(f.ID)
	if !ok || !strings.Contains(transcriptText(snap), "Added pty.go.") || !strings.Contains(transcriptText(snap), "Continued as the spec FD-031.") {
		t.Errorf("the closed session's conversation did not survive a restart (ok=%v):\n%s", ok, transcriptText(snap))
	}
}

// catalogAgent wraps a fake with its own model answer and counts the
// asks, so a test can watch the catalog cache work.
type catalogAgent struct {
	*agent.Fake
	calls int
}

func (c *catalogAgent) ModelCatalog(context.Context) ([]string, error) {
	c.calls++
	return c.Models, nil
}

// TestSessionModelChoicesMergesTheCatalogWithTheProfiles: a picker offers
// the agent's own answer about itself plus the ids the workspace's
// profiles run, deduplicated and sorted — the one merged view both faces
// show. A backend the agent cannot enumerate keeps the profile ids only.
func TestSessionModelChoicesMergesTheCatalogWithTheProfiles(t *testing.T) {
	e, claude, _, _ := sessionModelEngine(t)
	claude.Models = []string{"claude-opus-5-5", "claude-sonnet-5-5"}
	e.cfg.Profiles.Profiles["beta"] = config.Profile{
		"reviewer": {Backend: "codex", Model: "gpt-5"},
	}
	ctx := context.Background()

	got := e.SessionModelChoices(ctx, "claude")
	want := []string{"claude-opus-5-5", "claude-sonnet-5-5"}
	if !slices.Equal(got, want) {
		t.Errorf("claude choices = %v, want %v (deduped, sorted)", got, want)
	}
	if got := e.SessionModelChoices(ctx, "codex"); !slices.Equal(got, []string{"gpt-5"}) {
		t.Errorf("codex choices = %v, want the profile id only (it cannot enumerate)", got)
	}
	if got := e.SessionModelChoices(ctx, "pi"); got != nil {
		t.Errorf("pi choices = %v, want none (no agent, no probe, no profile)", got)
	}
}

// TestSessionModelChoicesWithoutAnAgent: nil-safe on every axis — no
// profiles, no agent, no probe.
func TestSessionModelChoicesWithoutAnAgent(t *testing.T) {
	e := &Engine{cfg: Config{Model: "fallback"}}
	if got := e.SessionModelChoices(context.Background(), "claude"); got != nil {
		t.Errorf("choices = %v, want nil", got)
	}
}

// TestSessionModelCatalogCachesTheAsk: the probe is a subprocess or RPC
// read a picker repeats, so the answer — including a backend's refusal to
// answer — is trusted for its TTL, and a caller whose context was already
// gone writes nothing (its cancelled ask must not suppress the next
// caller's good one).
func TestSessionModelCatalogCachesTheAsk(t *testing.T) {
	e, _, _, _ := sessionModelEngine(t)
	ag := &catalogAgent{Fake: agent.NewFake("ok")}
	ag.Caps = agent.Capabilities{ReadOnlyEnforce: true}
	ag.Models = []string{"fake-large"}
	e.cfg.Agents["fake"] = ag
	e.cfg.Agents[""] = ag
	ctx := context.Background()

	if _, ok := e.SessionModelCatalog(ctx, "fake"); !ok {
		t.Fatal("the staged agent's catalog was not offered")
	}
	if ag.calls != 1 {
		t.Fatalf("the agent was asked %d times for the first read", ag.calls)
	}
	if _, ok := e.SessionModelCatalog(ctx, "fake"); !ok || ag.calls != 1 {
		t.Errorf("a repeat ask re-probed (ok=%v, calls=%d): the cache is not doing its job", ok, ag.calls)
	}

	// past the TTL the agent is asked again — and a backend that cannot
	// enumerate stays cached negative for the same span
	ag.Models = nil
	oldNow := catalogNow
	catalogNow = func() time.Time { return oldNow().Add(6 * time.Minute) }
	t.Cleanup(func() { catalogNow = oldNow })
	if _, ok := e.SessionModelCatalog(ctx, "fake"); ok || ag.calls != 2 {
		t.Errorf("past the TTL the ask was not re-paid (ok=%v, calls=%d)", ok, ag.calls)
	}
	if _, ok := e.SessionModelCatalog(ctx, "fake"); ok || ag.calls != 2 {
		t.Errorf("a refusal was not cached (ok=%v, calls=%d)", ok, ag.calls)
	}

	// a cancelled caller gets its answer and writes nothing: the clock
	// moves past the cached refusal, the probe runs against a context
	// that is already gone, and the cache still holds the old refusal —
	// so the next good ask probes again rather than finding the
	// cancelled call's non-answer.
	ag.Models = []string{"fake-large"}
	catalogNow = func() time.Time { return oldNow().Add(12 * time.Minute) }
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	// the fake answers regardless of the gone context, so the ask reads
	// as ok — what the test pins is that it did not write the cache
	if _, ok := e.SessionModelCatalog(cancelled, "fake"); !ok || ag.calls != 3 {
		t.Errorf("a cancelled ask (ok=%v, calls=%d) did not reach the probe", ok, ag.calls)
	}
	e.catalogMu.Lock()
	stamp, had := e.modelCatalog["fake"]
	e.catalogMu.Unlock()
	if had && stamp.at.Equal(catalogNow()) {
		t.Errorf("the cancelled ask wrote the cache: %v", stamp.at)
	}
	if _, ok := e.SessionModelCatalog(ctx, "fake"); !ok || ag.calls != 4 {
		t.Errorf("the cancelled ask suppressed the next good one (ok=%v, calls=%d)", ok, ag.calls)
	}
}

// TestSessionModelCatalogAsksOpencodeWithoutStartingIt: opencode answers
// without an adapter to start — its CLI is asked directly, through the
// same *_BIN override every probe honors — so a picker offers its catalog
// on a board that runs none of it.
func TestSessionModelCatalogAsksOpencodeWithoutStartingIt(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	var starts atomic.Int32
	e := New(Config{
		Agents: map[string]agent.Agent{},
		StartAgent: func(string) (agent.Agent, error) {
			starts.Add(1)
			return nil, errors.New("not installed")
		},
		Model: "fallback",
	})
	t.Cleanup(func() { e.Close() })
	bin := filepath.Join(t.TempDir(), "opencode")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho opencode/claude-sonnet-5-5\necho opencode/gpt-5\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GUMMI_OPENCODE_BIN", bin)

	ids, ok := e.SessionModelCatalog(context.Background(), "opencode")
	if !ok || !slices.Equal(ids, []string{"opencode/claude-sonnet-5-5", "opencode/gpt-5"}) {
		t.Fatalf("opencode catalog = %v (ok=%v), want the CLI's own ids", ids, ok)
	}
	if starts.Load() != 0 {
		t.Errorf("the probe started a backend to ask it")
	}
}
