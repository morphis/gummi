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
	fake  *agent.Fake
	eng   *engine.Engine
	buf   *bytes.Buffer
	root  string

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
	ws, err := state.Init(root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	wt, err := worktree.NewManager(context.Background(), root, store)
	if err != nil {
		t.Fatal(err)
	}

	h := &harness{t: t, store: store, ws: ws, wt: wt, buf: &bytes.Buffer{}, root: root, calls: map[domain.Stage]int{}}
	fake := agent.NewFake("")
	fake.Caps = agent.Capabilities{Resume: true, UsageEvents: true, Interrupt: true, ClientTools: clientTools}
	fake.Responder = func(opts agent.SessionOpts, msg string) []agent.Event {
		stage := h.stageFromWorkDir(opts.WorkDir)
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
		Agents:  map[string]agent.Agent{"": fake, fake.Name(): fake},
		Store:   store, Worktrees: wt, Workspace: ws,
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
