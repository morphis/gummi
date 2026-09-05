package driver

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/worktree"
)

// harness wires a driver over an in-process fake agent and a throwaway
// git repo — the driver equivalent of engine_test's newRepo. The fake's
// per-stage script decides what each stage does; the driver's NDJSON goes
// to buf so tests can assert the stream.
type harness struct {
	t     *testing.T
	store *state.Store
	ws    state.Workspace
	wt    *worktree.Manager
	pool  *worktree.Pool
	// byName maps a configured named repo to its resolved root (nil for the
	// single-repo harness); the multi-repo harness fills it.
	byName map[string]string
	fake   *agent.Fake
	eng    *engine.Engine
	buf    *bytes.Buffer
	root   string

	// noDraft switches off the fake agent's stand-in drafting, so a test can
	// exercise the stage-produced-nothing failure the undrafted-sections
	// gate exists to catch.
	noDraft bool

	mu    sync.Mutex
	calls map[domain.Stage]int
}

// stageFn scripts one turn of a stage. n is how many times this stage has
// been entered (0-based), so a review that changes then passes can return
// different turns. opts.WorkDir is the worktree for autonomous stages, so
// a stage can write files that the engine's checkpoint then commits.
type stageFn func(h *harness, n int, opts agent.SessionOpts, msg string) []agent.Event

func newHarness(t *testing.T, clientTools bool, script map[domain.Stage]stageFn) *harness {
	t.Helper()
	root := gitRepo(t)
	return newHarnessRoots(t, clientTools, script, root, root)
}

// newHarnessRoots wires a harness over an explicit workspace root and repo
// root, so a test can drive the nested layout (.gummi at ws, repo at a
// nested subdirectory) end-to-end.
func newHarnessRoots(t *testing.T, clientTools bool, script map[domain.Stage]stageFn, wsRoot, repoRoot string) *harness {
	t.Helper()
	ws, err := state.Init(wsRoot, repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	wt, err := worktree.NewManager(context.Background(), wsRoot, repoRoot, store)
	if err != nil {
		t.Fatal(err)
	}

	h := &harness{t: t, store: store, ws: ws, wt: wt, buf: &bytes.Buffer{}, root: repoRoot, calls: map[domain.Stage]int{}}
	fake := agent.NewFake("")
	fake.Caps = agent.Capabilities{Resume: true, UsageEvents: true, Interrupt: true, ClientTools: clientTools}
	fake.Responder = func(opts agent.SessionOpts, msg string) []agent.Event {
		stage := h.scriptStage(opts)
		if f, err := h.store.GetFeature(context.Background(), h.only()); err == nil {
			h.draftRequiredSections(f)
		}
		fn := script[stage]
		if fn == nil {
			return []agent.Event{{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 1, Model: opts.Model}}, {Kind: agent.EventIdle}}
		}
		h.mu.Lock()
		n := h.calls[stage]
		h.calls[stage]++
		h.mu.Unlock()
		return fn(h, n, opts, msg)
	}
	h.fake = fake
	h.eng = engine.New(engine.Config{
		Agents: map[string]agent.Agent{"": fake, fake.Name(): fake},
		Store:  store, Worktrees: wt, Workspace: ws,
		Persist: true, Model: "test-model",
	})
	t.Cleanup(func() { h.eng.Close(); fake.Close() })
	return h
}

// newMultiRepoHarness wires a harness over a pool whose default repo is the
// workspace root plus two nested named repos ("a", "b"), so a test can drive
// a named-repo card end-to-end. The returned harness resolves cards through
// the pool; byName maps each configured name to its repo root.
func newMultiRepoHarness(t *testing.T, script map[domain.Stage]stageFn) *harness {
	t.Helper()
	wsRoot := gitRepo(t)
	roots := map[string]string{}
	for _, name := range []string{"a", "b"} {
		r := filepath.Join(wsRoot, "git", name)
		if err := os.MkdirAll(r, 0o750); err != nil {
			t.Fatal(err)
		}
		git := func(args ...string) {
			if out, err := exec.CommandContext(context.Background(), "git",
				append([]string{"-C", r}, args...)...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
		git("init", "-q", "-b", "main")
		git("config", "user.name", "t")
		git("config", "user.email", "t@e.invalid")
		if err := os.WriteFile(filepath.Join(r, "README.md"), []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		git("add", ".")
		git("commit", "-q", "-m", "init")
		roots[name] = r
	}
	ws, err := state.Init(wsRoot, wsRoot)
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	var named []worktree.NamedRepo
	for _, name := range []string{"a", "b"} {
		named = append(named, worktree.NamedRepo{Name: name, Root: roots[name]})
	}
	pool, err := worktree.NewPool(context.Background(), ws.Root, ws.Root, named, store, false)
	if err != nil {
		t.Fatal(err)
	}

	h := &harness{t: t, store: store, ws: ws, pool: pool, byName: roots, buf: &bytes.Buffer{}, root: wsRoot, calls: map[domain.Stage]int{}}
	fake := agent.NewFake("")
	fake.Caps = agent.Capabilities{Resume: true, UsageEvents: true, Interrupt: true, ClientTools: true}
	fake.Responder = func(opts agent.SessionOpts, msg string) []agent.Event {
		stage := h.scriptStage(opts)
		if f, err := h.store.GetFeature(context.Background(), h.only()); err == nil {
			h.draftRequiredSections(f)
		}
		fn := script[stage]
		if fn == nil {
			return []agent.Event{{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 1, Model: opts.Model}}, {Kind: agent.EventIdle}}
		}
		h.mu.Lock()
		n := h.calls[stage]
		h.calls[stage]++
		h.mu.Unlock()
		return fn(h, n, opts, msg)
	}
	h.fake = fake
	h.eng = engine.New(engine.Config{
		Agents: map[string]agent.Agent{"": fake, fake.Name(): fake},
		Store:  store, Pool: pool, Workspace: ws,
		Persist: true, Model: "test-model",
	})
	t.Cleanup(func() { h.eng.Close(); fake.Close() })
	return h
}

// stageFromWorkDir recovers the running stage. The driver transitions the
// store before it runs a stage, so the feature's current stored stage is
// the stage whose session is now executing (the plan writer vs its
// critique share the Plan stage — a script tells them apart by opts.Role).
func (h *harness) stageFromWorkDir(_ string) domain.Stage {
	f, err := h.store.GetFeature(context.Background(), h.only())
	if err != nil {
		return ""
	}
	return f.Stage
}

// only returns the single feature id in the store (tests drive one).
func (h *harness) only() domain.FeatureID {
	feats, err := h.store.ListFeatures(context.Background())
	if err != nil || len(feats) == 0 {
		return ""
	}
	return feats[0].ID
}

// driver builds a driver over this harness's engine with the given opts.
// A fresh driver per invocation models a fresh CLI process.
func (h *harness) driver(opts Options) *Driver {
	if opts.Envelope == 0 {
		opts.Envelope = 500
	}
	if opts.StageTimeout == 0 {
		opts.StageTimeout = 5 * time.Second
	}
	return New(h.eng, h.store, h.ws, h.buf, opts)
}

// events parses the NDJSON buffer into a slice of generic maps.
func (h *harness) events() []map[string]any {
	h.t.Helper()
	var out []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(h.buf.Bytes()), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			h.t.Fatalf("bad NDJSON line %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

// eventKinds returns the ordered "event" discriminators in the stream.
func (h *harness) eventKinds() []string {
	var out []string
	for _, e := range h.events() {
		if s, ok := e["event"].(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// has reports whether the stream carries an event of the given kind.
func (h *harness) has(kind string) bool {
	for _, k := range h.eventKinds() {
		if k == kind {
			return true
		}
	}
	return false
}

// stageOf returns the current stored stage of the single feature.
func (h *harness) stageOf(id domain.FeatureID) domain.Stage {
	f, err := h.store.GetFeature(context.Background(), id)
	if err != nil {
		h.t.Fatalf("GetFeature %s: %v", id, err)
	}
	return f.Stage
}

// gitRepo builds a committed throwaway repo (mirrors engine_test.newRepo).
func gitRepo(t *testing.T) string {
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
	return root
}

// --- scripted turns --------------------------------------------------

// msgIdle is a plain assistant turn: a message, a token of spend, idle.
func msgIdle(model, text string) []agent.Event {
	return []agent.Event{
		{Kind: agent.EventMessage, Text: text},
		{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 1, Model: model}},
		{Kind: agent.EventIdle},
	}
}

// toolVerdict is an autonomous verdict turn via the submit_verdict client
// tool (client-tool mode).
func toolVerdict(model, verdict string) []agent.Event {
	args, _ := json.Marshal(map[string]string{"verdict": verdict})
	return []agent.Event{
		{Kind: agent.EventClientToolCall, ToolCall: &agent.ToolCall{ID: "v-" + verdict, Name: "submit_verdict", Args: args}},
		{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 1, Model: model}},
		{Kind: agent.EventIdle},
	}
}

// prosePass is a passing verdict via the VERDICT: convention (the path
// backends without client tools take).
func prosePass(model string) []agent.Event {
	return msgIdle(model, "Reviewed.\nVERDICT: pass")
}

// convAsk is an interactive turn that ends with a gummi-ask fenced block
// (convention path): the engine parses it into a pending question, and
// the answer arrives as a fresh turn.
func convAsk(model, question string, options ...string) []agent.Event {
	opts := make([]map[string]string, 0, len(options))
	for _, o := range options {
		opts = append(opts, map[string]string{"label": o})
	}
	body, _ := json.Marshal(map[string]any{
		"question": question, "options": opts, "allow_free_form": true,
	})
	return msgIdle(model, "Considering.\n```gummi-ask\n"+string(body)+"\n```")
}

// harnessRoot is the repo root the harness resolves card artifacts under:
// the pool's root for a multi-repo harness, the manager's for a single-repo
// one.
func (h *harness) harnessRoot() string {
	if h.pool != nil {
		return h.pool.Root()
	}
	if h.wt != nil {
		return h.wt.Root()
	}
	return h.root
}

// draftRequiredSections fills in the one section the running stage's gate
// will ask for, simulating an agent that actually did its job. The blank
// template leaves every section holding nothing but its `%% @gummi:` prompt,
// which the undrafted-sections gate (correctly) refuses to advance — so a
// fixture whose fake agent only emits chat would otherwise stall at its
// first design gate. It writes only a section that is still undrafted, so a
// stage that wrote its own content (or a discovered gummi-checks block)
// keeps it. A test that wants the empty-artifact failure asserts it against
// Advance directly rather than through this harness.
func (h *harness) draftRequiredSections(f domain.Feature) {
	if h.noDraft {
		return
	}
	var want []string
	switch {
	case f.Kind == domain.KindFeature && f.Stage == domain.StageSpec:
		want = []string{"Chosen approach"}
	case f.Kind == domain.KindFeature && f.Stage == domain.StagePlan:
		want = []string{"Implementation notes"}
	case f.Kind == domain.KindBug && f.Stage == domain.StageDiagnose:
		want = []string{"Root cause"}
	case f.Kind == domain.KindFeature && f.Stage == domain.StageVerify:
		want = []string{"Verification plan"}
	case f.Kind == domain.KindBug && f.Stage == domain.StageVerify:
		want = []string{"Verification"}
	default:
		return
	}
	root := h.harnessRoot()
	path := spec.LocateArtifact(
		filepath.Join(root, f.ArtifactPath()),
		filepath.Join(h.ws.DraftsDir(), spec.DraftFilename(&f)),
		filepath.Join(root, f.WorktreePath(), f.ArtifactPath()),
	)
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	content := string(raw)
	for _, name := range spec.UndraftedSections(content, want) {
		// append, never replace: the section may already hold % markers a
		// test put there on purpose, and an agent drafting its section does
		// not delete the reader's comments.
		body, ok := spec.ViewSection(content, name)
		if !ok {
			return
		}
		next, _, err := spec.ReplaceSection(content, name, body+"drafted by the fake agent.\n\n")
		if err != nil {
			return
		}
		content = next
	}
	if content != string(raw) {
		_ = os.WriteFile(path, []byte(content), 0o600)
	}
}

// scriptStage picks the script entry that answers this session.
//
// Normally that is the card's stored stage. The exception is a critique
// pass on a work stage: Review stopped being a stage, so what used to run
// as a StageReview session now runs as a reviewer-role session borrowing
// implement/fix. A script's StageReview entry still means "what the
// critique says about the diff", so it is still the right answer — the
// stage it is filed under is just historical. Routing here keeps every
// existing script meaning what it meant.
//
// Research is untouched: it still has a real Review stage, so its
// sessions arrive with StageReview stored and never take this branch.
func (h *harness) scriptStage(opts agent.SessionOpts) domain.Stage {
	stage := h.stageFromWorkDir(opts.WorkDir)
	if opts.Role != agent.RoleReviewer {
		return stage
	}
	switch stage {
	case domain.StageImplement, domain.StageFix:
		return domain.StageReview
	}
	return stage
}
