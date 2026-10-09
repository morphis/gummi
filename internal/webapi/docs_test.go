package webapi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/exp/golden"
)

func TestLiveShape(t *testing.T) {
	golden.RequireEqual(t, marshal(t, Live{
		Busy: true, Verb: "implementing", Since: at, Streaming: "Running the te",
		Tool:  &ToolCall{Tool: "Bash", Label: "Bash  go test ./...", Detail: "go test ./...", Status: "running"},
		Spent: 1.25, State: "running", Stage: "implement", Role: "implementer", Model: "gpt-5", Session: at,
		Turns: []Turn{
			{Author: "gummi", Text: "Implement the plan."},
			{Author: "tool", Tool: &ToolCall{Tool: "Read", Label: "Read  main.go", Detail: "main.go", Status: "ok"}},
			{Author: "you", Text: "keep it small"},
		},
		Consult: &Conversation{
			Busy: true, Notice: "consult is not confined on copilot: it can write to the checkout",
			Verb: "thinking", Turns: []Turn{{Author: "you", Text: "why two funcs?"}, {Author: "thinking", Text: "one per backend"}},
			Tool:  &ToolCall{Tool: "Read", Label: "Read  main.go", Detail: "main.go", Status: "running"},
			Spent: 0.5, Model: "claude-opus", Context: &AgentContext{Tokens: 41000, Limit: 200000},
			Tasks:  []Task{{Text: "read the adapter", Status: "completed"}, {Text: "Fixing the leak", Status: "in_progress"}},
			Queued: []string{"also fix the docs"}, Watches: []string{"w1 · go test ./... | grep FAIL"},
			Objective: &Objective{Text: "the parser is under 10ms", Check: "go test ./parser", State: "active", Turns: 3, Cap: 20, Note: "the lexer is still slow", Auditing: true},
		},
		Elsewhere: &Elsewhere{PID: 4411, Stage: "verify", Role: "gummi", Since: at, Busy: true, Watching: true, Note: "read-only: another gummi process owns this run"},
	}))
}

func TestSpecShape(t *testing.T) {
	golden.RequireEqual(t, marshal(t, Spec{
		Path: ".gummi/specs/FD-012-dark-mode.md", Rev: "3f2a1bc0000000000000000000000000000000000", Title: "Dark mode",
		Markdown:     "# Dark mode\n\n## Problem\n\nToo bright.\n%% @user(2026-09-27, Simon): only at night?\n",
		Sections:     []SpecSection{{Name: "Problem", Line: 3}},
		Notes:        []SpecNote{{Line: 6, Anchor: 5, Author: "user", By: "Simon", Date: "2026-09-27", Text: "only at night?"}},
		OpenComments: 1,
		Checks: []SpecCheck{
			{Name: "build", Cmd: "go build ./...", Last: &CheckOutcome{OK: true, At: at}},
			{Name: "lint", Cmd: "golangci-lint run", Excused: true, ExcusedOn: "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c"},
		},
	}))
}

func TestDiffShape(t *testing.T) {
	golden.RequireEqual(t, marshal(t, Diff{
		Base: "main", BaseRev: "1111111", Rev: "2222222", Since: "1111111",
		Files: []DiffFile{
			{Path: "main.go", Status: "modified", Add: 1, Del: 1, Since: true, Hunks: []Hunk{{
				Header: "@@ -1,3 +1,3 @@",
				Lines: []DiffLine{
					{T: " ", Old: 1, New: 1, Text: "package main", Idx: 5},
					{T: "-", Old: 2, Text: "func a() {}", Idx: 6},
					{T: "+", New: 2, Text: "func a() { b() }", Idx: 7, Since: true},
				},
			}}},
			{Path: "b.md", OldPath: "a.md", Status: "renamed", Hunks: []Hunk{}},
			{Path: "logo.png", Status: "modified", Binary: true, Hunks: []Hunk{}},
		},
		Annotations: []Annotation{
			{ID: 3, File: "main.go", Idx: 7, Excerpt: "+func a() { b() }", Comment: "why?", Source: "gummi", At: at},
			{ID: 4, File: "main.go", Idx: -1, Excerpt: "x", Comment: "@octo: stale", By: "octo", Source: "pr", Resolved: true},
		},
		PendingComments: 1,
	}))
}

func TestPRShape(t *testing.T) {
	golden.RequireEqual(t, marshal(t, PR{
		Linked: true, Ref: "octo/demo#12", URL: "https://github.com/octo/demo/pull/12", State: "OPEN",
		Threads:     []PRThread{{Path: "main.go", Line: 3, Notes: []PRNote{{Author: "octo", Body: "name it better"}}}},
		Comments:    []PRNote{{Author: "octo", Body: "thanks"}},
		PushCommand: "git push origin fd-012-dark-mode", CommentCount: 2, HeadSHA: "0123abc", Fetched: at,
	}))
}

func TestCardStatsShape(t *testing.T) {
	golden.RequireEqual(t, marshal(t, CardStats{
		ID: "FD-012", Title: "Dark mode", Kind: "feature", Stage: "verify",
		Sessions: []StatSession{
			{
				Stage: "plan", Role: "architect", Flavor: "stage", Model: "gpt-5",
				Started: at, Ended: at, Turns: 4, Tools: 9, Credits: 3.5, Verdict: "approve",
				Tokens: Tokens{Input: 1200, Cached: 800, Output: 300}, ContextPeak: 41000, ContextLimit: 200000,
			},
			{Stage: "implement", Role: "implementer", Started: at, Turns: 2, Tools: 5, ToolFails: 1, Credits: 1.25, Redo: true, RedoReason: "corrected"},
		},
		Money: StatMoney{
			Credits: 4.75, FirstPass: 3.5, Rework: 1.25, Corrected: 1.25,
			ByStage: []Bucket{{Name: "plan", Credits: 3.5}, {Name: "implement", Credits: 1.25}},
			ByRole:  []Bucket{{Name: "architect", Credits: 3.5}}, ByModel: []Bucket{{Name: "gpt-5", Credits: 4.75}},
		},
		Clock: StatClock{AgentMs: 60000, OnYouMs: 120000, IdleMs: 30000, ElapsedMs: 210000, ToFirstGateMs: 180000, ToVerifiedMs: 210000},
		Hands: StatHands{
			Turns: 6, ToolCalls: 14, ToolFails: 1,
			Tools: []StatToolUse{
				{Name: "read", Calls: 9, TotalMs: 40},
				{Name: "run", Calls: 5, Fails: 1, Detail: "go test ./...", TotalMs: 2100},
			},
			Skills:    []StatToolUse{{Name: "skill", Calls: 2, Detail: "gummi-go-verify"}},
			Subagents: []StatToolUse{{Name: "task", Calls: 1, Detail: "where do the stats render"}},
			Checks:    []StatCheckRun{{Name: "build", Runs: 3, Fails: 1, Excused: true}},
		},
		Judgment: StatJudgment{
			Gates: StatAnswered{Total: 2, ByYou: 1, ByMachine: 1},
			Asks:  StatAnswered{Total: 1, ByYou: 1},
			Parks: []StatPark{{Reason: "verify failed", Detail: "1 of 3 checks failed", At: at}},
		},
		Envelope: StatEnvelope{Credits: 2000, Left: 1995.25},
	}))
}

// The nil-vs-empty distinction on hands.tools must survive the wire: a
// nil Tools marshals absent — the backend records no tool calls — and an
// empty one marshals []. omitempty would collapse the two, so the field
// is omitzero and this is the test that holds it to that.
func TestCardStatsToolsNilVsEmpty(t *testing.T) {
	var hands map[string]json.RawMessage
	if err := json.Unmarshal(marshal(t, CardStats{ID: "FD-012", Hands: StatHands{Turns: 2}}), &hands); err != nil {
		t.Fatal(err)
	}
	var h map[string]json.RawMessage
	if err := json.Unmarshal(hands["hands"], &h); err != nil {
		t.Fatal(err)
	}
	if _, ok := h["tools"]; ok {
		t.Errorf("a nil Tools must marshal absent, got %s", hands["hands"])
	}
	empty := marshal(t, CardStats{ID: "FD-012", Hands: StatHands{Tools: []StatToolUse{}}})
	if !strings.Contains(string(empty), `"tools": []`) {
		t.Errorf("an empty non-nil Tools must marshal as [], got %s", empty)
	}
}

func TestFleetShape(t *testing.T) {
	golden.RequireEqual(t, marshal(t, Fleet{
		From: at, To: at, Credits: 12, Rework: 2, Corrected: 2,
		ByStage: []Bucket{{Name: "implement", Credits: 12}}, ByModel: []Bucket{{Name: "gpt-5", Credits: 12}},
		AgentMs: 3600000, OnYouMs: 600000, IdleMs: 60000, ElapsedMs: 4260000, Running: 1, PeakLanes: 2,
		Tokens:  Tokens{Input: 1200, Cached: 800, Output: 300},
		Busiest: &Busiest{From: at, LenMs: 3600000, AgentMs: 3000000},
		Lanes: []Lane{{
			ID: "FD-012", Title: "Dark mode", Kind: "feature", Credits: 12, Redo: 2, Running: true, OpenWaitFrom: at,
			Tokens: Tokens{Input: 1200, Cached: 800, Output: 300}, Note: "sent back once after a verdict",
			Blocks: []Span{{From: at, Stage: "implement"}}, Waits: []Span{{From: at, To: at}}, Gates: []time.Time{at},
		}},
		AllTimeCredits: 40, AllTimeCards: 5,
	}))
}

func TestLogShapes(t *testing.T) {
	log := Log{
		Base: "main", Head: "bbbbbbb2", Rewritable: true, PushCommand: "git push --force-with-lease origin feat/x",
		Commits: []LogCommit{
			{SHA: "aaaaaaa1", Short: "aaaaaaa", Subject: "FD-012: implement checkpoint", Author: "gummi", At: at, Files: 2, Add: 10, Del: 1, Checkpoint: true, Pushed: true},
			{SHA: "bbbbbbb2", Short: "bbbbbbb", Subject: "feat: x", Body: "why", Author: "Simon", At: at, Files: 1, Add: 3, Warning: "Co-Authored-By: Claude"},
		},
	}
	golden.RequireEqual(t, marshal(t, struct {
		Log     Log
		Request RewriteRequest
		Preview RewritePreview
		Result  RewriteResult
	}{
		Log:     log,
		Request: RewriteRequest{Head: "bbbbbbb2", AcknowledgePushed: true, Groups: []RewriteGroup{{Commits: []string{"aaaaaaa1", "bbbbbbb2"}, Message: "feat: x"}}},
		Preview: RewritePreview{Commits: log.Commits[1:], Changed: 1, Pushed: true, PushCommand: log.PushCommand},
		Result:  RewriteResult{Head: "ccccccc3", PushCommand: log.PushCommand, Log: log},
	}))
}
