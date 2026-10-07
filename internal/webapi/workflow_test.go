package webapi

import (
	"testing"

	"github.com/charmbracelet/x/exp/golden"
)

// The card workflow's bodies: what an answer, a line, an action and a new
// card send, and what the board says back — the decision kinds and
// refusals beyond the first cut of the contract included. A refusal is a
// 409; a question (IsQuestion) is the same Error body answered with
// StatusQuestion, and the two lists pin which word is which.
func TestCardWorkflowShapes(t *testing.T) {
	n := 750
	refusals := []Error{
		{Error: ConflictAnswered, Text: "Simon advanced plan → implement", By: "Simon", Receipt: "Simon advanced plan → implement"},
		{Error: ConflictMoved, Text: "the card moved since you read it — now spec 3f2a1bc"},
		{Error: ConflictBusy, Text: "use the system theme"},
	}
	questions := []Error{
		{Error: ConflictConfirm, Needs: ActionNeedsConfirm, Text: "delete FD-012?\nDark mode — removes worktree, branch, and record", Confirm: "c1a2b3c4d5e6f7a8b9c0d1e2"},
		{Error: ConflictNeeds, Needs: ActionNeedsMessage, Text: "no landing message was drafted — write one"},
		{Error: ConflictNewCard, Text: "the export needs a CSV mode too"},
	}
	for _, e := range refusals {
		if IsQuestion(e.Error) {
			t.Errorf("%q is a refusal (409), not a question", e.Error)
		}
	}
	for _, e := range questions {
		if !IsQuestion(e.Error) {
			t.Errorf("%q is a question (%d), not a refusal", e.Error, StatusQuestion)
		}
	}
	golden.RequireEqual(t, marshal(t, struct {
		Answer    AnswerRequest     `json:"answer"`
		Send      SendResponse      `json:"send"`
		Action    ActionRequest     `json:"action"`
		Refusals  []Error           `json:"refusals"`
		Questions []Error           `json:"questions"`
		Create    CreateCardRequest `json:"create"`
		Resume    ResumeRequest     `json:"resume"`
		Board     Board             `json:"board"`
	}{
		Answer: AnswerRequest{Ref: "gate:FD-012:plan", Option: "advance", Against: "gate:FD-012:plan#0de111d3@3f2a1bc", Confirm: "c1a2b3c4d5e6f7a8b9c0d1e2"},
		Send: SendResponse{Route: RouteMenu, Card: Card{
			Row: Row{ID: "FD-012", Kind: "feature", Title: "Dark mode", Stage: "verify", Status: StatusNeeds},
			Decision: &Decision{
				Ref: "verify:FD-012:verify", Kind: DecisionVerify, Question: "verification stopped here — choose what happens next.",
				Anchor: AnchorDiff, Against: Against{Token: "verify:FD-012:verify#1a2b3c4d@9f8e7d6", Label: "feat/dark-mode at 9f8e7d6"},
				Options: []Option{
					{ID: "bounce", Label: "send it back", Words: true, Relabel: "send it back with your words", CarriesComments: true},
					{ID: "advance", Label: "land anyway"},
				},
			},
			Actions: []Action{
				{ID: "profile", Label: "profile", Needs: ActionNeedsProfile, Default: "thrifty", Choices: []Choice{{Value: "premium", Label: "premium", Detail: "copilot · gpt-5"}}},
				{ID: "repo", Label: "repository", Key: "o", Needs: ActionNeedsRepo, Choices: []Choice{{Value: "api", Label: "api"}}},
				{ID: "gate", Label: "hand to autopilot", Default: "autopilot"},
			},
			Composer: Composer{Says: "“rebase” is in the card's menu, not one of the answers above", Route: RouteMenu},
		}},
		Action:    ActionRequest{Number: &n, Repo: "api", Mode: "attended", Confirm: "c1a2b3c4d5e6f7a8b9c0d1e2 c9f8e7d6c5b4a3f2e1d0c9b8", Against: "verify:FD-012:verify#1a2b3c4d@9f8e7d6"},
		Refusals:  refusals,
		Questions: questions,
		Create:    CreateCardRequest{Kind: "research:diagnosis", Title: "Slow board", DependsOn: []string{"FD-001"}, StackOn: "FD-002", Autopilot: true},
		Resume:    ResumeRequest{Cards: []string{"FD-004"}},
		Board: Board{Repo: "gummi", Today: Today{Spent: 41.5}, Viewers: []Viewer{}, Rows: []Row{
			{
				ID: "FD-013", Kind: "feature", Title: "Row cache", Stage: "plan", Status: StatusRunning,
				Running: &RowRunning{Verb: "planning", Pausing: true}, Stack: &RowStack{ID: "ST-1", Name: "rows", Pos: 1, Of: 2}, Waits: []string{"FD-012"},
			},
		}, Resume: &ResumeOffer{Cards: []CardRef{{ID: "FD-004", Title: "Export", Stage: "implement"}}, Since: "2h ago"}},
	}))
}
