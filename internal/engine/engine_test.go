package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/worktree"
)

// testWaitTimeout bounds the poll loops that wait for an engine event or
// state change. It only ever runs out on a broken test, so it is sized for
// the slowest CI shape (race detector plus coverage on a loaded shared
// runner), not for the happy path — those loops return as soon as the
// condition holds. Negative assertions ("nothing more arrives") keep their
// own short windows.
const testWaitTimeout = 30 * time.Second

func newRepo(t *testing.T) (state.Workspace, *state.Store, *worktree.Manager) {
	t.Helper()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.CommandContext(context.Background(), "git",
			append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.name", "t")
	git("config", "user.email", "t@e.invalid")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-q", "-m", "init")

	ws, err := state.Init(root, root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	wt, err := worktree.NewManager(context.Background(), root, root, store)
	if err != nil {
		t.Fatal(err)
	}
	return ws, store, wt
}

func feature(num int, title string, stage domain.Stage) domain.Feature {
	id, _ := domain.NewFeatureID(num)
	slug, _ := domain.Slugify(title)
	now := time.Now()
	return domain.Feature{
		ID: id, Num: num, Title: title, Slug: slug, Stage: stage,
		CreatedAt: now, UpdatedAt: now,
	}
}

func waitFor(t *testing.T, e *Engine, kind EventKind) Event {
	t.Helper()
	deadline := time.After(testWaitTimeout)
	for {
		select {
		case ev := <-e.Events():
			if ev.Kind == kind {
				return ev
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s", kind)
		}
	}
}

// waitIdle waits for a card's session to finish the turn it is on.
func waitIdle(t *testing.T, e *Engine, id domain.FeatureID) {
	t.Helper()
	deadline := time.After(testWaitTimeout)
	for {
		if s := liveSession(e, id); s != nil && !s.Busy() && len(s.Snapshot().Transcript) > 0 {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("%s never went idle", id)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// waitTurn waits until every session the engine holds has finished the
// turn it is on: what a stage's EventIdle used to announce, which a
// session reports only as a not-busy session with something said.
func waitTurn(t *testing.T, e *Engine) {
	t.Helper()
	deadline := time.After(testWaitTimeout)
	for {
		e.mu.Lock()
		ffs := make([]*FreeformSession, 0, len(e.freeform))
		for _, ff := range e.freeform {
			ffs = append(ffs, ff)
		}
		e.mu.Unlock()
		idle := len(ffs) > 0
		for _, ff := range ffs {
			s := ff.Session()
			if s == nil || s.Busy() || len(s.Snapshot().Transcript) == 0 {
				idle = false
			}
		}
		if idle {
			return
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for the turn to end")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// waitErr waits until a card's session records an error.
func waitErr(t *testing.T, e *Engine, id domain.FeatureID) {
	t.Helper()
	deadline := time.After(testWaitTimeout)
	for {
		if s := liveSession(e, id); s != nil && s.Snapshot().Err != nil {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("%s never recorded an error", id)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// liveSession is the session a card's conversation runs on, nil before
// its first turn starts one.
func liveSession(e *Engine, id domain.FeatureID) *Session {
	if ff := e.Freeform(id); ff != nil {
		return ff.Session()
	}
	return nil
}

// attach mints f in the engine's store when it is not there yet, opens
// its session and sends its opening turn, and returns the session that
// turn runs on.
func attach(t *testing.T, e *Engine, f domain.Feature) *Session {
	t.Helper()
	ctx := context.Background()
	if _, err := e.cfg.Store.GetFeature(ctx, f.ID); err != nil {
		createFeature(t, e.cfg.Store, f)
	}
	ff, err := e.OpenFreeform(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := ff.Kickoff(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(testWaitTimeout)
	for ff.Session() == nil {
		select {
		case <-deadline:
			t.Fatalf("%s never started its session", f.ID)
		case <-time.After(5 * time.Millisecond):
		}
	}
	return ff.Session()
}

// newEngine builds an engine over one fake agent. Tests that need more
// than one run construct New directly.
func newEngine(t *testing.T, ag agent.Agent) *Engine {
	t.Helper()
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "fake-model", Workflows: true})
	t.Cleanup(func() { e.Close() })
	return e
}

// withWorktree creates a feature's worktree so autonomous stages locate it.
func withWorktree(t *testing.T, wt *worktree.Manager, f domain.Feature) {
	t.Helper()
	if _, err := wt.Create(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
}

func TestWorktreeStageLocatesWorktree(t *testing.T) {
	ws, store, wt := newRepo(t)
	rec := recordingAgent()
	e := New(Config{Agents: singleAgent(rec), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
	t.Cleanup(func() { e.Close() })

	f := freeformCard(1, "impl me")
	withWorktree(t, wt, f)
	attach(t, e, f)
	waitIdle(t, e, "FF-001")

	wantDir := filepath.Join(wt.Root(), f.WorktreePath())
	if rec.opts().WorkDir != wantDir {
		t.Errorf("workdir = %s, want worktree %s", rec.opts().WorkDir, wantDir)
	}
	if rec.opts().Role != agent.RoleImplementer {
		t.Errorf("role = %s, want implementer", rec.opts().Role)
	}
}

// recorder is an Agent that captures the last SessionOpts and counts
// how many sessions it was asked to start — enough to assert that a
// profile's backend field routed to the right adapter. name overrides
// the underlying Fake's default "fake" identity when a test needs it to
// match a specific backend key ("copilot", "headless").
type recorder struct {
	*agent.Fake
	mu       sync.Mutex
	lastOpts agent.SessionOpts
	sessions int
	name     string
}

func recordingAgent() *recorder {
	f := agent.NewFake("ok")
	// research sessions are read-only; advertise the fake can enforce it,
	// or the engine's fail-closed gate would refuse the session outright.
	f.Caps.ReadOnlyEnforce = true
	return &recorder{Fake: f}
}

func (r *recorder) Name() string {
	if r.name != "" {
		return r.name
	}
	return r.Fake.Name()
}

func (r *recorder) NewSession(ctx context.Context, opts agent.SessionOpts) (agent.Session, error) {
	r.mu.Lock()
	r.lastOpts = opts
	r.sessions++
	r.mu.Unlock()
	return r.Fake.NewSession(ctx, opts)
}

func (r *recorder) opts() agent.SessionOpts {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastOpts
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessions
}

// Every session must bind an inbound MCP endpoint and carry the feature
// id on SessionOpts, for both a ClientTools backend and an MCPTools
// (opencode-style) backend. The MCPTools backend must also still receive
// the session's client tools and tool hint, since it consumes the tools
// over MCP rather than through opts.Tools.
func TestNewAgentSessionAlwaysBindsMCPAndFeatureID(t *testing.T) {
	for name, caps := range map[string]agent.Capabilities{
		"clientTools": {ClientTools: true},
		"mcpTools":    {MCPTools: true},
	} {
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			var got agent.SessionOpts
			ag := &agent.Fake{Caps: caps, Responder: func(opts agent.SessionOpts, _ string) []agent.Event {
				mu.Lock()
				got = opts
				mu.Unlock()
				return []agent.Event{{Kind: agent.EventIdle}}
			}}
			ws, store, wt := newRepo(t)
			e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
			t.Cleanup(func() { e.Close() })

			f := freeformCard(1, "x")
			attach(t, e, f)
			waitTurn(t, e)
			mu.Lock()
			defer mu.Unlock()
			if got.MCPSockPath == "" {
				t.Errorf("%s: MCPSockPath empty", name)
			}
			if got.FeatureID != string(f.ID) {
				t.Errorf("%s: FeatureID = %q, want %q", name, got.FeatureID, string(f.ID))
			}
			if len(got.Tools) == 0 {
				t.Errorf("%s: Tools empty", name)
			}
			if !strings.Contains(strings.Join(got.SystemHints, "\n"), freeformToolHint) {
				t.Errorf("%s: SystemHints missing the session's tool hint", name)
			}
		})
	}
}

// nestedRepo builds a workspace whose .gummi sits at ws while the git repo
// lives in a nested subdirectory ws/git/lxd — the motivating layout.
func nestedRepo(t *testing.T) (ws state.Workspace, store *state.Store, wt *worktree.Manager) {
	t.Helper()
	wsRoot := t.TempDir()
	wsRoot, err := filepath.EvalSymlinks(wsRoot)
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(wsRoot, "git", "lxd")
	if err := os.MkdirAll(repo, 0o750); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.CommandContext(context.Background(), "git",
			append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.name", "t")
	git("config", "user.email", "t@e.invalid")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-q", "-m", "init")

	ws, err = state.Init(wsRoot, repo)
	if err != nil {
		t.Fatal(err)
	}
	store, err = state.OpenStore(ws.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	wt, err = worktree.NewManager(context.Background(), wsRoot, repo, store)
	if err != nil {
		t.Fatal(err)
	}
	return ws, store, wt
}

// TestNestedArtifactJoinsResolveUnderWorkspace locks in the engine's root
// discipline: .gummi-relative artifact joins use the workspace root, while
// the repo root is exposed separately for agent workdirs.
func TestNestedArtifactJoinsResolveUnderWorkspace(t *testing.T) {
	ws, store, wt := nestedRepo(t)
	fake := agent.NewFake("")
	eng := New(Config{
		Agents: map[string]agent.Agent{"": fake, fake.Name(): fake},
		Store:  store, Worktrees: wt, Workspace: ws,
		Persist: true, Model: "test-model",
	})
	t.Cleanup(func() { eng.Close(); fake.Close() })

	if got := wt.Root(); got != ws.Root {
		t.Errorf("Worktrees.Root() = %q, want workspace root %q", got, ws.Root)
	}
	if got := wt.RepoRoot(); got != ws.RepoRoot {
		t.Errorf("Worktrees.RepoRoot() = %q, want repo root %q", got, ws.RepoRoot)
	}

	f := feature(99, "Nested artifact", domain.StagePlan)
	// the artifact workspace-home join must resolve under the workspace
	// root, never the repo root.
	got := filepath.Join(eng.cfg.Worktrees.Root(), f.ArtifactPath())
	if !strings.HasPrefix(got, ws.Root+string(filepath.Separator)) {
		t.Errorf("artifact join %q does not resolve under workspace root %q", got, ws.Root)
	}
	if strings.HasPrefix(got, ws.RepoRoot+string(filepath.Separator)) {
		t.Errorf("artifact join %q resolves under the repo root; it must use the workspace root", got)
	}
}

// TestConsultAndFreeformSessionsCarryTheCardScratchDir: every long-lived
// session kind stamps the card's scratch-files dir on SessionOpts — the
// anchor a backend that derives a per-card config home needs — matching
// what the stage sessions have always stamped. The one-shot kinds
// deliberately carry none (they never resume; their fallback is a temp
// home).
func TestConsultAndFreeformSessionsCarryTheCardScratchDir(t *testing.T) {
	ctx := context.Background()

	r := recordingAgent()
	e := newEngine(t, r)
	f := freeformCard(3, "scratch anchor")
	createFeature(t, e.cfg.Store, f)
	if _, err := e.OpenConsult(ctx, f); err != nil {
		t.Fatal(err)
	}
	if want := e.cfg.Workspace.ScratchFilesDir(f.ID); r.opts().ScratchDir != want {
		t.Fatalf("consult ScratchDir = %q, want the card's scratch dir %q", r.opts().ScratchDir, want)
	}

	r2 := recordingAgent()
	e2 := newEngine(t, r2)
	f2 := freeformCard(4, "freeform scratch anchor")
	createFeature(t, e2.cfg.Store, f2)
	if _, err := e2.OpenFreeform(ctx, f2); err != nil {
		t.Fatal(err)
	}
	if want := e2.cfg.Workspace.ScratchFilesDir(f2.ID); r2.opts().ScratchDir != want {
		t.Fatalf("freeform ScratchDir = %q, want the card's scratch dir %q", r2.opts().ScratchDir, want)
	}
}
