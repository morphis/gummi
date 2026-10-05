package webapi

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/charmbracelet/x/exp/golden"
)

var at = time.Date(2026, 9, 27, 9, 12, 0, 0, time.UTC)

func marshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(b, '\n')
}

// The page reads these field names; a rename is a broken page, so it has
// to show up here as a golden diff first.
func TestBoardShape(t *testing.T) {
	golden.RequireEqual(t, marshal(t, Board{
		Repo:    "gummi",
		Head:    "main",
		Today:   Today{Spent: 41.5},
		Counts:  Counts{Needs: 1, Running: 1},
		Viewers: []Viewer{{Person: "Simon", Device: "iPhone · Safari", DeviceID: "a1b2c3d4", Since: at}},
		Rows: []Row{
			{
				ID: "FD-012", Kind: "feature", Title: "Dark mode", Stage: "plan", Status: StatusNeeds,
				Needs: &RowNeeds{Kind: NeedsGate, Color: "warn", Question: "Approve the design?"},
				Spend: 12.3, Envelope: 2000, Profile: "balanced",
				Stack: &RowStack{ID: "ST-1", Name: "theme", Pos: 0, Of: 2},
			},
			{
				ID: "BG-003", Kind: "bug", Title: "Crash on empty board", Stage: "implement", Status: StatusRunning,
				Running: &RowRunning{Verb: "editing board.go", Autopilot: true},
				Spend:   3, Envelope: 500, Severity: "high", Autopilot: true,
				Goal: &RowGoal{ID: "GL-001", Title: "Stable board"}, Waits: []string{"FD-010"},
			},
			{ID: "FD-001", Kind: "feature", Title: "Landed thing", Stage: "done", Status: StatusDone, Landed: true, PR: "#12 merged"},
		},
		Resume: &ResumeOffer{Cards: []CardRef{{ID: "FD-012", Title: "Dark mode", Stage: "plan"}}, Since: "3m ago", At: at},
	}))
}

func TestCardShape(t *testing.T) {
	golden.RequireEqual(t, marshal(t, Card{
		Row:    Row{ID: "FD-012", Kind: "feature", Title: "Dark mode", Stage: "plan", Status: StatusNeeds, Spend: 12.3, Envelope: 2000},
		Branch: "fd-012-dark-mode", Base: "main",
		Decision: &Decision{
			Ref: "d-41", Kind: DecisionGate, Question: "Approve the design?", Anchor: AnchorSpec,
			Against: Against{Token: "d-41@3f2a1bc", Label: "spec 3f2a1bc"},
			Options: []Option{
				{ID: "advance", Label: "Approve, move to implement", Words: false},
				{ID: "changes", Label: "Ask for changes", Words: true, Relabel: "Send back with note"},
			},
		},
		DecisionsMore: 1,
		Actions: []Action{
			{ID: "pause", Label: "Pause", Key: "p"},
			{ID: "topup", Label: "Raise the envelope", Needs: ActionNeedsNumber, Default: "2000"},
			{ID: "delete", Label: "Delete the card", Danger: true, Needs: ActionNeedsConfirm},
		},
		Composer: Composer{Says: "asks the architect, without interrupting", Route: RouteConsult},
		Files:    &Files{Dir: "/repo/.gummi/worktrees/FD-012", URL: "/files/FD-012/k3y/"},
	}))
}

func TestThreadShape(t *testing.T) {
	golden.RequireEqual(t, marshal(t, Thread{
		Items: []Item{
			{Key: "stage-1", Seq: 1, T: ItemStage, Time: at, Stage: "plan", Role: "architect", Model: "gpt-5", Flavor: "work"},
			{Key: "tools-3", Seq: 4, T: ItemTools, Time: at, Tools: []ToolCall{{Tool: "read", Label: "read board.go", Status: "ok", Ms: 12}}},
			{Key: "msg-5", Seq: 5, T: ItemMessage, Time: at, Text: "The plan is **ready**."},
			{Key: "rcpt-6", Seq: 6, T: ItemReceipt, Time: at, Receipt: &Receipt{Kind: "gate", OK: true, Text: "design approved", By: "Simon"}, Supersedes: []string{"ev:5"}},
			{Key: "rcpt-7", Seq: 7, T: ItemReceipt, Time: at, Receipt: &Receipt{Kind: "park", Text: "Simon parked it", By: "Simon"}},
			{Key: "stretch-8", Seq: 8, T: ItemStretch, Time: at, Label: "autopilot", Tally: "2 gates crossed"},
			{Key: "verify-9", Seq: 9, T: ItemVerify, Time: at, Checks: []CheckRun{{Name: "build", Cmd: "go build ./...", OK: false, Ms: 900, Output: "boom"}}},
		},
		Live:    &Live{Busy: true, Verb: "thinking", Since: at, Spent: 1.25, State: "running"},
		LastSeq: 9,
	}))
}

// TestAttachmentShapes pins the fields the page reads for images: an
// AttachmentRef's own shape, and where it (or just its id) rides on every
// other request/response that carries one.
func TestAttachmentShapes(t *testing.T) {
	golden.RequireEqual(t, marshal(t, struct {
		Ref        AttachmentRef
		Send       SendRequest
		CreateCard CreateCardRequest
		SpecNote   SpecNoteRequest
		Composer   Composer
		ThreadItem Item
	}{
		Ref:  AttachmentRef{ID: "ab12ef34", Name: "shot.png", MediaType: "image/png", Size: 4096},
		Send: SendRequest{Text: "look at this", Attachments: []string{"ab12ef34"}},
		CreateCard: CreateCardRequest{
			Kind: "freeform", Title: "Dark mode", Attachments: []string{"ab12ef34"},
			// the session's "runs in" choice: the main checkout, without a
			// branch or worktree (DESIGN §19)
			MainCheckout: true,
		},
		SpecNote: SpecNoteRequest{Line: 3, Text: "see the mock", Attachments: []string{"ab12ef34"}},
		Composer: Composer{Says: "steers the implementer mid-turn", Route: RouteSteer, Images: true},
		ThreadItem: Item{
			Key: "you-7", Seq: 7, T: ItemYou, Time: at, Author: "you", Text: "look at this",
			Attachments: []AttachmentRef{{ID: "ab12ef34", Name: "shot.png", MediaType: "image/png", Size: 4096}},
		},
	}))
}

func TestComposerCompletionShapes(t *testing.T) {
	golden.RequireEqual(t, marshal(t, Composer{
		Says: "opens the card's menu — or finish a project command: /review /release", Route: RouteMenu,
		Completions: []Completion{
			{Text: "/review ", Detail: "review the diff"},
			{Text: "/release "},
		},
	}))
}

func TestChangeShapes(t *testing.T) {
	golden.RequireEqual(t, marshal(t, []Change{
		{Kind: ChangeBoard},
		{Kind: ChangeCard, ID: "FD-012"},
		{Kind: ChangeCard, ID: "FD-013", Gone: true},
		{Kind: ChangeLive, ID: "FD-012"},
		{Kind: ChangeToast, ID: "FD-012", Text: "FD-012: paused", Err: false},
		{Kind: ChangeViewers, Viewers: []Viewer{{Person: "Simon", Device: "Mac · Firefox", DeviceID: "a1b2c3d4", Since: at}}},
	}))
}

// Pairing and letting a device in: what a waiting browser is told about
// itself, and what a page at the board is shown to decide on.
func TestPairingShapes(t *testing.T) {
	golden.RequireEqual(t, marshal(t, map[string]any{
		"sessionAtTheBoard": Session{Authed: true, Person: "Simon", Device: "Mac · Firefox", DeviceID: "a1b2c3d4", Version: "v1", Repo: "gummi", Host: "box"},
		"sessionWaiting":    Session{Approval: ApprovalPending, ExpiresInSecs: 598, Person: "Ana", Device: "iPhone · Safari", DeviceID: "e5f6a7b8"},
		"pairWaiting":       PairResponse{Person: "Ana", Device: "iPhone · Safari", DeviceID: "e5f6a7b8", Pending: true, ExpiresInSecs: 600},
		"pending": PendingDevices{Devices: []PendingDevice{{
			ID: "e5f6a7b8", Person: "Ana", Device: "iPhone · Safari",
			UserAgent: "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) Safari/604.1",
			Source:    "100.64.0.9", Code: "cli", Via: "via the local CLI (`gummi web pair`)",
			Origin: "gummi.tail1234.ts.net", RequestedAt: at, ExpiresInSecs: 540,
		}}},
		"refusedWaiting": Error{Error: "waiting for approval on a paired device", Approval: ApprovalPending},
		"change":         Change{Kind: ChangePairing, ID: "e5f6a7b8"},
	}))
}

func TestChangeKey(t *testing.T) {
	for _, tc := range []struct {
		c    Change
		want string
	}{
		{Change{Kind: ChangeBoard}, "board"},
		{Change{Kind: ChangeCard, ID: "FD-1"}, "card:FD-1"},
		{Change{Kind: ChangeLive, ID: "FD-1"}, "live:FD-1"},
		{Change{Kind: ChangeViewers}, "viewers"},
		{Change{Kind: ChangeToast, Text: "x"}, ""},
		{Change{Kind: ChangePairing, ID: "e5f6"}, "pairing:e5f6"},
	} {
		if got := tc.c.Key(); got != tc.want {
			t.Errorf("%+v.Key() = %q, want %q", tc.c, got, tc.want)
		}
	}
}

// TestWritespecShapes pins what the writespec flow puts on the wire: the
// request field the edited brief rides in (it has one of its own — Message
// carries the title, and a multi-paragraph brief is not an input any other
// action takes), and the draft the dialog fetches once at open, labeled by
// where it came from.
func TestWritespecShapes(t *testing.T) {
	golden.RequireEqual(t, marshal(t, struct {
		Request ActionRequest
		Live    WritespecDraft
		Cold    WritespecDraft
	}{
		Request: ActionRequest{Message: "Configurable sync retries", Brief: "asked\n- why the retry test flakes\n\ndecided\n- retry twice", Number: &[]int{300}[0]},
		Live:    WritespecDraft{Brief: "asked\n- why the retry test flakes\n\ndecided\n- retry twice\n\ndone\n- the loop retries twice\n\nremaining\n- make the count configurable", Source: "live"},
		Cold:    WritespecDraft{Brief: "asked:\n- rewrite the retry loop", Source: "assembled"},
	}))
}

// TestMemoryShape pins the fields the page reads for a freeform card's
// project memory: the three documents, each a path and its content, and
// the none-answer a workflow card gets.
func TestMemoryShape(t *testing.T) {
	golden.RequireEqual(t, marshal(t, struct {
		Populated Memory
		None      Memory
	}{
		Populated: Memory{
			Dir:      ".gummi/memory",
			Global:   MemoryDoc{Path: ".gummi/memory/global.md", Text: "The repo's checks are make ci."},
			Memory:   MemoryDoc{Path: ".gummi/memory/FF-002/memory.md", Text: "# Plan\n- split the parser"},
			DeadEnds: MemoryDoc{},
		},
		None: Memory{
			None: true,
			Why:  "memory is a freeform session's; this card's documents are its stages'",
		},
	}))
}

// The new-card form's adopt list reads each branch's adoptability beside
// the branch list itself.
func TestFormShape(t *testing.T) {
	golden.RequireEqual(t, marshal(t, Form{
		Kinds:      []Choice{{Value: "feature", Label: "Feature"}},
		Profiles:   []string{"balanced"},
		Repos:      []string{},
		Severities: []string{"high"},
		Branches:   []string{"main", "feat/dark", "fd-001-wave", "stale"},
		Adoptable: []AdoptChoice{
			{Branch: "main", Why: "the branch it lands on"},
			{Branch: "feat/dark"},
			{Branch: "fd-001-wave", Why: "FD-001 has it", Held: "FD-001"},
			{Branch: "stale", Why: "no commits past main"},
		},
		Stackable:  []CardRef{},
		Dependable: []CardRef{},
		Envelope:   2000,
		Sessions:   SessionModels{Default: SessionModel{Backend: "copilot", Model: "gpt-5"}, Agents: []SessionAgent{}, Recent: []SessionModel{}},
	}))
}
