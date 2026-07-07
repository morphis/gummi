package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// sampleProposalJSON is a well-formed decomposition the fake architect
// returns, exercising titles, deps, skip flags, seed content, and a
// coverage map with a mapped/out-of-scope/unmapped mix.
const sampleProposalJSON = `{
  "features": [
    {"title": "Payment webhooks", "one_liner": "receive provider callbacks",
     "source_refs": ["Payments", "Webhooks"], "depends_on": [],
     "skip": {"brainstorm": true, "plan": false},
     "problem": "We miss async payment state changes.",
     "constraints": "Verify signatures; idempotent.",
     "acceptance": "A signed test event flips the order to paid.",
     "open_questions": ["which providers first?", ""]},
    {"title": "Webhook retries", "one_liner": "retry failed deliveries",
     "source_refs": ["Reliability"], "depends_on": ["Payment webhooks"],
     "problem": "Failed deliveries are dropped.", "open_questions": []},
    {"title": "   ", "one_liner": "dropped: no title"}
  ],
  "coverage": [
    {"requirement": "receive callbacks", "feature": "Payment webhooks", "status": "mapped"},
    {"requirement": "analytics", "status": "out-of-scope", "note": "later"},
    {"requirement": "gdpr export", "status": "unmapped", "note": "unclear owner"},
    {"requirement": "", "status": "mapped"}
  ]
}`

func writeSource(t *testing.T, ws interface{ Root() string }, name, body string) string {
	t.Helper()
	p := filepath.Join(ws.Root(), name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// wsRoot adapts state.Workspace to the tiny Root() interface writeSource wants.
type wsRoot struct{ root string }

func (w wsRoot) Root() string { return w.root }

func assertSampleResult(t *testing.T, res domain.IngestResult) {
	t.Helper()
	if len(res.Proposals) != 2 {
		t.Fatalf("got %d proposals, want 2 (blank-title one dropped)", len(res.Proposals))
	}
	p0 := res.Proposals[0]
	if p0.Title != "Payment webhooks" || !p0.Skip.Brainstorm || p0.Skip.Plan {
		t.Errorf("proposal[0] fields wrong: %+v", p0)
	}
	if len(p0.SourceRefs) != 2 || p0.Draft.Problem == "" || len(p0.Draft.OpenQuestions) != 1 {
		t.Errorf("proposal[0] seed/refs wrong: refs=%v seed=%+v", p0.SourceRefs, p0.Draft)
	}
	if got := res.Proposals[1].DependsOn; len(got) != 1 || got[0] != "Payment webhooks" {
		t.Errorf("proposal[1] depends_on = %v", got)
	}
	if len(res.Coverage) != 3 { // the empty-requirement entry is dropped
		t.Fatalf("got %d coverage entries, want 3", len(res.Coverage))
	}
	if u := res.Unmapped(); len(u) != 1 || u[0].Requirement != "gdpr export" {
		t.Errorf("Unmapped() = %+v", u)
	}
	if !strings.HasPrefix(res.SourcePath, filepath.Join(".gummi", "ingest")) {
		t.Errorf("SourcePath = %q, want under .gummi/ingest", res.SourcePath)
	}
}

func TestIngestClientToolPath(t *testing.T) {
	ag := &agent.Fake{
		Caps: agent.Capabilities{ClientTools: true},
		Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
			if opts.Role != agent.RoleArchitect {
				t.Errorf("ingest used role %q, want architect", opts.Role)
			}
			if len(opts.Tools) != 1 || opts.Tools[0].Name != ingestToolName {
				t.Errorf("propose_features tool not offered: %+v", opts.Tools)
			}
			return []agent.Event{
				{Kind: agent.EventClientToolCall, ToolCall: &agent.ToolCall{ID: "c1", Name: ingestToolName, Args: json.RawMessage(sampleProposalJSON)}},
				{Kind: agent.EventIdle},
			}
		},
	}
	ws, store, wt := newRepo(t)
	e := New(Config{Agent: ag, Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	src := writeSource(t, wsRoot{ws.Root}, "prd.md", "# Platform PRD\nlots of requirements\n")
	res, err := e.Ingest(context.Background(), src, "premium", nil)
	if err != nil {
		t.Fatal(err)
	}
	assertSampleResult(t, res)

	// the source was stashed for provenance, and the pass resolved the call.
	if _, err := os.Stat(filepath.Join(ws.Root, res.SourcePath)); err != nil {
		t.Errorf("source not stashed: %v", err)
	}
}

func TestIngestConventionPath(t *testing.T) {
	ag := &agent.Fake{
		Caps: agent.Capabilities{}, // no client tools → convention fallback
		Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
			if len(opts.Tools) != 0 {
				t.Errorf("no tools should be offered without ClientTools: %+v", opts.Tools)
			}
			reply := "Here is the decomposition:\n```gummi-propose\n" + sampleProposalJSON + "\n```\n"
			return []agent.Event{{Kind: agent.EventMessage, Text: reply}, {Kind: agent.EventIdle}}
		},
	}
	ws, store, wt := newRepo(t)
	e := New(Config{Agent: ag, Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	src := writeSource(t, wsRoot{ws.Root}, "design.md", "# Design\nstuff\n")
	res, err := e.Ingest(context.Background(), src, "thrifty", nil)
	if err != nil {
		t.Fatal(err)
	}
	assertSampleResult(t, res)
}

func TestIngestConventionPathStreamedThenCompleted(t *testing.T) {
	// streaming adapters emit deltas AND the completed message they were
	// streaming; the collector must treat the message as authoritative
	// (replacing its deltas) — appending both doubles the fence and the
	// greedy first-to-last-fence regex then captures garbage. Reasoning
	// deltas must stay out of the reply text entirely.
	reply := "Here it is:\n```gummi-propose\n" + sampleProposalJSON + "\n```\n"
	ag := &agent.Fake{
		Caps: agent.Capabilities{}, // no client tools → convention fallback
		Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
			return []agent.Event{
				{Kind: agent.EventReasoningDelta, Text: "thinking about slices"},
				{Kind: agent.EventTextDelta, Text: reply[:len(reply)/2]},
				{Kind: agent.EventTextDelta, Text: reply[len(reply)/2:]},
				{Kind: agent.EventMessage, Text: reply},
				{Kind: agent.EventIdle},
			}
		},
	}
	ws, store, wt := newRepo(t)
	e := New(Config{Agent: ag, Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	src := writeSource(t, wsRoot{ws.Root}, "streamy.md", "# Spec\nthings\n")
	res, err := e.Ingest(context.Background(), src, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	assertSampleResult(t, res)
}

func TestIngestNoProposalIsError(t *testing.T) {
	ag := &agent.Fake{Reply: "I couldn't find anything to split."}
	ws, store, wt := newRepo(t)
	e := New(Config{Agent: ag, Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	src := writeSource(t, wsRoot{ws.Root}, "empty-ish.md", "not really a spec\n")
	if _, err := e.Ingest(context.Background(), src, "", nil); err == nil {
		t.Error("expected an error when the agent returns no proposal")
	}
}

func TestIngestEmptySourceRejected(t *testing.T) {
	ag := agent.NewFake("x")
	ws, store, wt := newRepo(t)
	e := New(Config{Agent: ag, Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	src := writeSource(t, wsRoot{ws.Root}, "blank.md", "   \n")
	if _, err := e.Ingest(context.Background(), src, "", nil); err == nil {
		t.Error("expected empty source to be rejected before spawning a session")
	}
}

func TestIngestStashDoesNotClobberSameBasename(t *testing.T) {
	ag := &agent.Fake{
		Caps: agent.Capabilities{ClientTools: true},
		Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
			return []agent.Event{
				{Kind: agent.EventClientToolCall, ToolCall: &agent.ToolCall{ID: "c", Name: ingestToolName, Args: json.RawMessage(sampleProposalJSON)}},
				{Kind: agent.EventIdle},
			}
		},
	}
	ws, store, wt := newRepo(t)
	e := New(Config{Agent: ag, Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	// two different documents that share a basename must both survive.
	if err := os.MkdirAll(filepath.Join(ws.Root, "a"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(ws.Root, "b"), 0o750); err != nil {
		t.Fatal(err)
	}
	srcA := writeSource(t, wsRoot{ws.Root}, filepath.Join("a", "spec.md"), "AAA content\n")
	srcB := writeSource(t, wsRoot{ws.Root}, filepath.Join("b", "spec.md"), "BBB content\n")

	resA, err := e.Ingest(context.Background(), srcA, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	resB, err := e.Ingest(context.Background(), srcB, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resA.SourcePath == resB.SourcePath {
		t.Fatalf("distinct documents stashed to the same path %q", resA.SourcePath)
	}
	a, _ := os.ReadFile(filepath.Join(ws.Root, resA.SourcePath))
	b, _ := os.ReadFile(filepath.Join(ws.Root, resB.SourcePath))
	if string(a) != "AAA content\n" || string(b) != "BBB content\n" {
		t.Errorf("stashed contents clobbered: a=%q b=%q", a, b)
	}

	// re-ingesting the identical document reuses its existing stash.
	resA2, err := e.Ingest(context.Background(), srcA, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resA2.SourcePath != resA.SourcePath {
		t.Errorf("identical re-ingest made a new file %q (want reuse of %q)", resA2.SourcePath, resA.SourcePath)
	}
}

func TestIngestReportsProgress(t *testing.T) {
	ag := &agent.Fake{
		Caps: agent.Capabilities{ClientTools: true},
		Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
			return []agent.Event{
				{Kind: agent.EventToolCall, Tool: "read prd.md"},
				{Kind: agent.EventReasoningDelta, Text: "weighing the seams"},
				{Kind: agent.EventTextDelta, Text: "splitting into vertical slices"},
				{Kind: agent.EventClientToolCall, ToolCall: &agent.ToolCall{ID: "c1", Name: ingestToolName, Args: json.RawMessage(sampleProposalJSON)}},
				{Kind: agent.EventIdle},
			}
		},
	}
	ws, store, wt := newRepo(t)
	e := New(Config{Agent: ag, Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	src := writeSource(t, wsRoot{ws.Root}, "prd.md", "# PRD\nrequirements\n")
	// progress is invoked synchronously from the pass, so the slice is
	// safe to inspect once Ingest returns.
	var steps []IngestStep
	if _, err := e.Ingest(context.Background(), src, "", func(st IngestStep) { steps = append(steps, st) }); err != nil {
		t.Fatal(err)
	}
	if len(steps) < 4 {
		t.Fatalf("got %d steps, want at least 4: %+v", len(steps), steps)
	}
	if steps[0].Kind != IngestStepNote || !strings.Contains(steps[0].Text, "architect") {
		t.Errorf("first step should be the architect-started note, got %+v", steps[0])
	}
	var sawTool, sawDelta, sawThinking bool
	for _, st := range steps {
		sawTool = sawTool || (st.Kind == IngestStepTool && st.Text == "read prd.md")
		sawDelta = sawDelta || (st.Kind == IngestStepDelta && strings.Contains(st.Text, "slices"))
		sawThinking = sawThinking || (st.Kind == IngestStepDelta && strings.Contains(st.Text, "seams"))
	}
	if !sawTool || !sawDelta || !sawThinking {
		t.Errorf("tool/delta/thinking steps missing (tool=%v delta=%v thinking=%v): %+v",
			sawTool, sawDelta, sawThinking, steps)
	}
	if last := steps[len(steps)-1]; last.Kind != IngestStepNote || !strings.Contains(last.Text, "2 feature(s)") {
		t.Errorf("last step should be the proposal-received note, got %+v", last)
	}
}

func TestProposalFromTextHandlesEmbeddedFence(t *testing.T) {
	// a gummi-propose block whose JSON body contains a ``` must not be
	// truncated at the inner fence.
	body := `{"features":[{"title":"A","open_questions":["keep the ` + "```go build```" + ` step?"]}]}`
	text := "Here you go:\n```gummi-propose\n" + body + "\n```\n"
	res, err := proposalFromText(text)
	if err != nil {
		t.Fatalf("embedded fence truncated the block: %v", err)
	}
	if len(res.Proposals) != 1 || res.Proposals[0].Title != "A" {
		t.Errorf("parsed proposals = %+v", res.Proposals)
	}
}

func TestDecodeProposalStatusFallback(t *testing.T) {
	// a fuzzy/blank status with a named feature reads as mapped; without
	// one it reads as unmapped — never silently covered.
	res, err := decodeProposal([]byte(`{"features":[{"title":"A"}],"coverage":[
	  {"requirement":"x","feature":"A","status":"weird"},
	  {"requirement":"y","status":""},
	  {"requirement":"z","feature":"A","status":"unmapped"}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	// a fuzzy/unrecognized status fails loud (unmapped) even with a feature
	// named, so a not-fully-covered requirement is never silently covered.
	if res.Coverage[0].Status != domain.CoverageUnmapped {
		t.Errorf("named-feature fuzzy status = %q, want unmapped", res.Coverage[0].Status)
	}
	if res.Coverage[1].Status != domain.CoverageUnmapped {
		t.Errorf("blank status/no feature = %q, want unmapped", res.Coverage[1].Status)
	}
	// an explicit "unmapped" stays unmapped even when a feature is named.
	if res.Coverage[2].Status != domain.CoverageUnmapped {
		t.Errorf("explicit unmapped = %q, want unmapped", res.Coverage[2].Status)
	}
}
