package webapi

import (
	"testing"

	"github.com/charmbracelet/x/exp/golden"

	"github.com/morphis/gummi/internal/engine"
)

func TestGoalShape(t *testing.T) {
	n := 3000
	golden.RequireEqual(t, marshal(t, struct {
		List   Goals             `json:"list"`
		Page   Goal              `json:"page"`
		Create GoalCreateRequest `json:"create"`
		Action GoalActionRequest `json:"action"`
		Done   Outcome           `json:"done"`
	}{
		List: Goals{Goals: []GoalSummary{{
			Row:   Row{ID: "GL-001", Kind: "goal", Title: "Stable board", Stage: "implement", Status: StatusRunning, Spend: 410, Envelope: 3000},
			State: "running", Met: 1, DoneWhen: 3, Landed: 2, Cards: 4, Spent: 410,
		}}},
		Page: Goal{
			Report: engine.GoalReport{
				ID: "GL-001", Title: "Stable board", Stage: "implement", Lanes: 2,
				Budget:   engine.GoalReportBudget{Envelope: 3000, Total: 410},
				DoneWhen: []engine.DoneWhenStatus{{ID: "DW-1", Says: "the board loads", How: "check: go test ./...", Status: engine.DoneWhenMet}},
				Cards:    []engine.GoalReportCard{{ID: "FD-004", Kind: "feature", Title: "Loader", State: "landed", Stage: "done", Envelope: 600, Spent: 212}},
			},
			State: "running",
			Cards: []Row{{ID: "FD-004", Kind: "feature", Title: "Loader", Stage: "done", Status: StatusDone, Goal: &RowGoal{ID: "GL-001", Title: "Stable board"}}},
			Log:   []GoalLogEntry{{Seq: 7, At: at, Action: "decision", Ref: "D-1", Detail: "cache the rows", By: "lead"}},
			Notebook: GoalNotebook{
				References: []GoalReference{{Name: "prd.md"}},
				Findings:   []GoalFinding{{Ref: "F-1", Claim: "rows load in 40ms", Card: "FD-004", Status: "holds"}},
			},
			Actions: []Action{
				{ID: GoalActionNote, Label: "note to the lead", Needs: ActionNeedsMessage},
				{ID: GoalActionReverse, Label: "reverse a decision", Needs: ActionNeedsDecision},
				{ID: GoalActionBudget, Label: "raise the budget", Key: "u", Needs: ActionNeedsNumber, Default: "3000"},
			},
		},
		Create: GoalCreateRequest{Description: "Make the board stable", Envelope: &n, After: "GL-000", References: []string{"docs/prd.md"}, Autopilot: true},
		Action: GoalActionRequest{Text: "focus on the loader", Ref: "D-1", Why: "too slow"},
		Done:   Outcome{OK: true, Text: "GL-001 created", ID: "GL-001"},
	}))
}

func TestStackShape(t *testing.T) {
	pos := 1
	golden.RequireEqual(t, marshal(t, struct {
		List    Stacks       `json:"list"`
		Request StackRequest `json:"request"`
		Restack Restack      `json:"restack"`
	}{
		List: Stacks{Stacks: []Stack{{
			ID: "theme", Name: "theme", Base: "main",
			Members: []StackMember{
				{ID: "FD-010", Title: "Tokens", Pos: 0, Stage: "verify", Branch: "feat/tokens", Tree: true},
				{ID: "FD-011", Title: "Dark mode", Pos: 1, Stage: "implement", Branch: "feat/dark-mode", Below: "FD-010", Tree: true, Stale: true, Running: true, Blocker: "FD-010"},
			},
			Push:     []string{"git push --force-with-lease origin feat/dark-mode"},
			Replayed: []string{"FD-011"}, ReplayedAt: at,
		}}},
		Request: StackRequest{Card: "FD-011", Pos: &pos},
		Restack: Restack{
			Stack:    Stack{ID: "theme", Name: "theme", Members: []StackMember{}, Push: []string{"git push --force-with-lease origin feat/dark-mode"}},
			Replayed: []string{"FD-011"},
			Conflict: &StackConflict{Card: "FD-012", Files: []string{"theme.go"}},
		},
	}))
}

func TestReposShape(t *testing.T) {
	golden.RequireEqual(t, marshal(t, struct {
		List    Repos       `json:"list"`
		Request RepoRequest `json:"request"`
	}{
		List: Repos{
			Discovered: true,
			Ambiguous:  []RepoClash{{Name: "tools", Paths: []string{"a/tools", "b/tools"}}},
			Repos: []Repo{
				{
					Name: "gummi", Path: "git/gummi", Base: "main", Origin: "git@github.com:morphis/gummi.git",
					Remote: true, Dirty: true, Fetched: at, Cards: 3,
					Remotes: []RepoRemote{
						{Name: "origin", URL: "git@github.com:morphis/gummi.git", Tracking: 2},
						{Name: "fork", URL: "https://•••@github.com/simon/gummi.git", PushURL: "git@github.com:simon/gummi.git", Secret: true},
					},
					RemoteBranches: []string{"origin/main", "origin/release"},
					Branches: []RepoBranch{
						{Name: "main", Group: "base", SHA: "7f5dac70a1b2", Subject: "fix(web): keep the rail", At: at, Upstream: "origin/main", Remote: "origin", Ahead: 2, Behind: 3, Push: "git push origin main"},
						{Name: "feat/loader", Group: "cards", SHA: "ace70de9c3d4", AheadBase: 4, BehindBase: 1, Worktree: ".gummi/worktrees/FD-012", Card: &RepoBranchCard{ID: "FD-012", Title: "Loader", Stage: "done", Landed: true, Clean: true}},
						{Name: "release", Group: "unowned", SHA: "9a8ca12ae5f6", Upstream: "origin/release", Gone: true, ForkedBy: []string{"FD-020"}, Why: "FD-020 forks from it"},
						{Name: "wip/simon", Group: "unowned", SHA: "b9128d40a7b8", AheadBase: 2, Delete: "confirm"},
					},
				},
				{Name: "", Path: ".", Branches: []RepoBranch{}, Error: "not the root of a git repository"},
			},
		},
		Request: RepoRequest{
			Repo: "gummi", All: true, Branch: "wip/simon", Force: true,
			Remote: "fork", NewName: "simon", URL: "git@github.com:simon/gummi.git", Upstream: "fork/wip/simon",
		},
	}))
}

func TestIngestShape(t *testing.T) {
	golden.RequireEqual(t, marshal(t, struct {
		Request IngestRequest     `json:"request"`
		Run     IngestRun         `json:"run"`
		Edit    IngestEditRequest `json:"edit"`
	}{
		Request: IngestRequest{Markdown: "# PRD\n", Name: "prd.md", Profile: "balanced"},
		Run: IngestRun{
			ID: "1", State: IngestReview, Source: ".gummi/ingest/prd.md", Profile: "balanced", Envelope: 2000,
			Steps:     []IngestStep{{Kind: "note", Text: "architect reading prd.md"}, {Kind: "tool", Text: "read prd.md"}},
			Proposals: []IngestProposal{{Index: 0, Kind: "feature", Title: "Loader", OneLiner: "load rows", SourceRefs: []string{"§2"}, OpenQuestions: []string{"cache?"}}},
			Coverage:  &IngestCoverage{Mapped: 3, OutOfScope: 1, Unmapped: 1},
			Unmapped:  []string{"offline mode — not covered"},
			Created:   []CardRef{{ID: "FD-020", Title: "Loader", Stage: "todo"}},
		},
		Edit: IngestEditRequest{Index: 0, Op: IngestEditRename, Title: "Row loader"},
	}))
}

func TestBugsShape(t *testing.T) {
	golden.RequireEqual(t, marshal(t, struct {
		List    Bugs        `json:"list"`
		Request BugsRequest `json:"request"`
		Created BugsCreated `json:"created"`
	}{
		List: Bugs{
			Source:    "github",
			Proposals: []BugProposal{{Ref: "https://github.com/o/r/issues/7", Number: 7, Title: "Crash on empty board", Severity: "high", State: "open", Labels: []string{"bug"}, Author: "octo"}},
			Skipped:   []BugSkipped{{Ref: "https://github.com/o/r/issues/3", Card: "BG-001", Title: "Old crash"}},
		},
		Request: BugsRequest{Repo: "o/r", Label: "bug", Limit: 80, Refs: []string{"https://github.com/o/r/issues/7"}},
		Created: BugsCreated{Created: []CardRef{{ID: "BG-002", Title: "Crash on empty board", Stage: "todo"}}},
	}))
}

// TestSessionModelsShape: what a session's model picker is offered, and
// the pair a session card reports it runs on (DESIGN §19.8).
func TestSessionModelsShape(t *testing.T) {
	golden.RequireEqual(t, marshal(t, struct {
		Sessions SessionModels `json:"sessions"`
		Card     Card          `json:"card"`
		Switch   ActionRequest `json:"switch"`
	}{
		Sessions: SessionModels{
			Default: SessionModel{Backend: "claude", Model: "claude-sonnet-5-5"},
			Agents: []SessionAgent{
				{Name: "claude", Installed: true, Models: []string{"claude-opus-5-5", "claude-sonnet-5-5"}, Hint: "versions with dashes", Images: true},
				{Name: "opencode", Installed: false, Models: []string{}, NeedsModel: true, Hint: "provider/model"},
			},
			Recent: []SessionModel{{Backend: "codex", Model: "gpt-5"}},
		},
		Card: Card{
			Row:     Row{ID: "FF-003", Kind: "freeform", Title: "Tidy the help text", Stage: "open", Status: StatusRunning, Spend: 12, Envelope: 150},
			Branch:  "ff/003-tidy-the-help-text",
			Actions: []Action{{ID: "model", Label: "model", Needs: ActionNeedsModel, Default: "codex gpt-5"}},
			Session: &SessionModel{Backend: "codex", Model: "gpt-5"},
		},
		Switch: ActionRequest{Backend: "claude", Model: "claude-sonnet-5-5"},
	}))
}
