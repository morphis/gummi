package driver

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/worktree"
)

// harness is one throwaway repository with a gummi workspace, and the
// engine a `gummi run` process would build over it.
type harness struct {
	workflows bool // the workspace setting the engine is built with
	t         *testing.T
	root      string
	ws        state.Workspace
	store     *state.Store
	wt        *worktree.Manager
	fake      *sessionFake
	eng       *engine.Engine
	buf       *bytes.Buffer
}

func newHarness(t *testing.T, fake *sessionFake) *harness {
	t.Helper()
	return newHarnessWorkflows(t, fake, true)
}

// newHarnessWorkflows is newHarness with the workspace's workflows setting
// as asked.
func newHarnessWorkflows(t *testing.T, fake *sessionFake, workflows bool) *harness {
	t.Helper()
	root := gitRepo(t)
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
	h := &harness{workflows: workflows, t: t, root: root, ws: ws, store: store, wt: wt, fake: fake, buf: &bytes.Buffer{}}
	fake.h = h
	h.newEngine()
	t.Cleanup(func() { fake.Close() })
	return h
}

// newEngine builds the engine a run process would.
func (h *harness) newEngine() {
	h.eng = engine.New(engine.Config{
		Agents: map[string]agent.Agent{"": h.fake, h.fake.Name(): h.fake},
		Store:  h.store, Worktrees: h.wt, Workspace: h.ws,
		Persist: true, Model: "test-model", Workflows: h.workflows,
	})
	eng := h.eng
	h.t.Cleanup(func() { eng.Close() })
}

func (h *harness) driver(opts Options) *Driver {
	if opts.Timeout == 0 {
		opts.Timeout = 30 * time.Second
	}
	d := New(h.eng, h.store, h.ws, h.buf, opts)
	d.poll = 50 * time.Millisecond
	return d
}

// run is `gummi run`.
func (h *harness) run(opts Options, prompt string) (Outcome, domain.Feature) {
	h.t.Helper()
	out, err := h.driver(opts).Run(context.Background(), prompt)
	if err != nil && out.Status != StatusError {
		h.t.Fatalf("Run: %v", err)
	}
	var f domain.Feature
	if out.ID != "" {
		if f, err = h.store.GetFeature(context.Background(), out.ID); err != nil {
			h.t.Fatal(err)
		}
	}
	return out, f
}

func (h *harness) events() []map[string]any {
	var out []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(h.buf.Bytes()), []byte("\n")) {
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

func (h *harness) eventKinds() []string {
	var out []string
	for _, e := range h.events() {
		out = append(out, e["event"].(string))
	}
	return out
}

// last returns the newest event of kind, or nil.
func (h *harness) last(kind string) map[string]any {
	evs := h.events()
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i]["event"] == kind {
			return evs[i]
		}
	}
	return nil
}

// runStates lists the states the stream's workflow events reported, in
// order.
func (h *harness) runStates() []string {
	var out []string
	for _, e := range h.events() {
		if e["event"] == "workflow" {
			out = append(out, e["state"].(string))
		}
	}
	return out
}

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

// --- the fake agent -----------------------------------------------------

const wfSpec = "# Workflow test\n\n## Problem\n\nA file is missing.\n\n## Approach\n\nWrite it.\n\n" +
	"## Done when\n\n```gummi-done-when\n- id: DW-1\n  says: out.txt exists\n  check: test -f out.txt\n```\n\n" +
	"## Limits\n\nNothing else.\n\n## Notes\n\nnone\n"

// wfRecipe is the recipe the session proposes: an implementer writes
// out.txt, and a reviewer validates it.
const wfRecipe = `name: make-file
description: write the file and validate it
steps:
  - id: make
    role: implementer
    prompt: |
      Write out.txt for the spec below.

      {{spec}}
  - id: validate
    role: reviewer
    prompt: |
      Read {{run.diff}} against the spec. End with MET: DW-1 or UNMET: DW-1.

      {{spec}}
`

var stepRe = regexp.MustCompile(`You are step (\S+) of workflow run`)

// sessionFake answers a session and its workflow run's steps. The
// session's turns are scripted by first: what the first turn does (reply,
// ask, propose a run, spend); later session turns just acknowledge. A
// step is told apart by its brief: make writes out.txt — asking the
// person first when stepAsks is set — and validate says MET.
type sessionFake struct {
	*agent.Fake
	h *harness

	mu       sync.Mutex
	first    string // "reply" | "ask" | "workflow" | "spend"
	stepAsks bool
	cardStep map[string]string
	session  []string
	steps    []string
	briefs   map[string]string // each step's first brief, whole
}

func newSessionFake(first string) *sessionFake {
	sf := &sessionFake{Fake: agent.NewFake(""), first: first, cardStep: map[string]string{}, briefs: map[string]string{}}
	sf.Caps = agent.Capabilities{ClientTools: true, UsageEvents: true, Interrupt: true}
	sf.Responder = sf.respond
	return sf
}

func usage() agent.Event {
	return agent.Event{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 2, Model: "m"}}
}

func toolCall(id, name string, args any) agent.Event {
	raw, _ := json.Marshal(args)
	return agent.Event{Kind: agent.EventClientToolCall, ToolCall: &agent.ToolCall{ID: id, Name: name, Args: raw}}
}

func say(text string) []agent.Event {
	return []agent.Event{usage(), {Kind: agent.EventMessage, Text: text}, {Kind: agent.EventIdle}}
}

func (sf *sessionFake) respond(opts agent.SessionOpts, msg string) []agent.Event {
	sf.mu.Lock()
	step := ""
	if m := stepRe.FindStringSubmatch(msg); m != nil {
		step = m[1][strings.LastIndex(m[1], "/")+1:]
		sf.cardStep[opts.FeatureID] = step
	} else if s, ok := sf.cardStep[opts.FeatureID]; ok {
		step = s + "#"
	}
	if step != "" {
		sf.steps = append(sf.steps, step+": "+firstLine(msg))
		if _, ok := sf.briefs[step]; !ok {
			sf.briefs[step] = msg
		}
		sf.mu.Unlock()
		return sf.respondStep(opts, step, msg)
	}
	sf.session = append(sf.session, msg)
	turn := len(sf.session)
	sf.mu.Unlock()
	if turn > 1 {
		return say("ok: " + firstLine(msg))
	}
	switch sf.first {
	case "hang":
		// spends, then never goes idle: the drive times out on it
		return []agent.Event{usage()}
	case "spend":
		return []agent.Event{
			{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 5000, Model: "m"}},
			{Kind: agent.EventMessage, Text: "that was expensive"},
			{Kind: agent.EventIdle},
		}
	case "ask":
		return []agent.Event{
			usage(),
			toolCall("a1", "ask_user", map[string]any{
				"question": "Which format?",
				"options":  []map[string]any{{"label": "json"}, {"label": "yaml"}},
			}),
			{Kind: agent.EventIdle},
		}
	case "workflow":
		sess := sf.sessionCard()
		ctx := context.Background()
		if _, _, err := sf.h.eng.StartSessionSpec(ctx, sess, "Workflow test", "agent"); err != nil {
			return say("spec_start failed: " + err.Error())
		}
		if _, err := sf.h.eng.WriteSessionSpec(ctx, sess, 1, wfSpec, "agent"); err != nil {
			return say("spec write failed: " + err.Error())
		}
		return []agent.Event{
			usage(),
			toolCall("w1", "workflow_run", map[string]any{"recipe_yaml": wfRecipe}),
			{Kind: agent.EventMessage, Text: "a workflow is on it"},
			{Kind: agent.EventIdle},
		}
	}
	return say("done: " + firstLine(msg))
}

// respondStep answers a step's turn: its brief, or (step ends in #) a
// later turn on its card, such as the person's answer to its question.
func (sf *sessionFake) respondStep(opts agent.SessionOpts, step, msg string) []agent.Event {
	switch step {
	case "make":
		if sf.stepAsks {
			return []agent.Event{
				usage(),
				toolCall("s1", "ask_user", map[string]any{
					"question": "Plain text or markdown?",
					"options":  []map[string]any{{"label": "plain"}, {"label": "markdown"}},
				}),
				{Kind: agent.EventIdle},
			}
		}
		fallthrough
	case "make#":
		_ = os.WriteFile(filepath.Join(opts.WorkDir, "out.txt"), []byte("made by the run\n"), 0o600)
		return say("Wrote it.\nDECISION: plain text")
	case "validate":
		return say("It is there.\nMET: DW-1")
	case "reproduce":
		return say("Reproduced it in parser.go.\nOPEN: whether 1.3 has it too")
	case "assess":
		return say("```markdown\nVerdict: a real bug, severity high.\n```\nDECISION: severity high")
	}
	return say("ok")
}

// sessionCard is the run's session: the one freeform card that is no
// session's child.
func (sf *sessionFake) sessionCard() domain.Feature {
	all, _ := sf.h.store.ListFeatures(context.Background())
	for _, f := range all {
		if f.IsFreeform() && !f.IsChild() {
			return f
		}
	}
	sf.h.t.Error("no session card")
	return domain.Feature{}
}
