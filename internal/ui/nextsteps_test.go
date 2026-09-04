package ui

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/ui/theme"
	"github.com/morphis/gummi/internal/verify"
)

// keysOf flattens a suggestion list to its key sequence for compact
// table expectations.
func keysOf(acts []nextAction) string {
	keys := make([]string, len(acts))
	for i, a := range acts {
		keys[i] = a.key
	}
	return strings.Join(keys, " ")
}

func TestNextActionsByState(t *testing.T) {
	feat := domain.KindFeature
	bug := domain.KindBug
	linkedRef := domain.PullRequestRef{Repo: "o/r", Number: 42, URL: "https://github.com/o/r/pull/42"}
	cases := []struct {
		name string
		in   nextInput
		want string // expected key sequence, "" for no suggestions
	}{
		{"landed wins over everything", nextInput{stage: domain.StageVerify, kind: feat, landed: true, attn: attnGate}, "c"},
		{"done and cleaned up is quiet", nextInput{stage: domain.StageDone, kind: feat}, ""},
		{"queued run is quiet", nextInput{stage: domain.StageImplement, kind: feat, sess: engine.StateQueued}, ""},
		{"busy run is quiet", nextInput{stage: domain.StageImplement, kind: feat, sess: engine.StateRunning, busy: true}, ""},
		{"blocking ask interrupts the run", nextInput{stage: domain.StageImplement, kind: feat, sess: engine.StateRunning, hasAsk: true}, "enter p"},
		// hasWorktree: a feature card at review/verify always has one, and
		// the CLI row is offered only where there is something to attach
		// into (BG-089).
		{"paused offers a re-run", nextInput{stage: domain.StageVerify, kind: feat, sess: engine.StatePaused, hasWorktree: true}, "enter a"},
		{"failure offers retry and CLI", nextInput{stage: domain.StageReview, kind: feat, attn: attnFailure, hasWorktree: true}, "enter a"},
		{"paused with no worktree offers only the re-run", nextInput{stage: domain.StageSpec, kind: feat, sess: engine.StatePaused}, "enter"},
		{"budget stop routes to the inbox", nextInput{stage: domain.StageImplement, kind: feat, attn: attnBudget}, "i"},
		{"question routes to attach", nextInput{stage: domain.StageSpec, kind: feat, attn: attnQuestion}, "enter"},
		{"todo starts the flow", nextInput{stage: domain.StageTodo, kind: feat}, "g"},
		{"brainstorm chats first", nextInput{stage: domain.StageBrainstorm, kind: feat}, "enter g"},
		// s reads the spec you are about to sign off on, the way the plan
		// stage already offers reading the plan
		{"spec clean offers approve", nextInput{stage: domain.StageSpec, kind: feat}, "enter g s A"},
		{"spec with open questions blocks approve", nextInput{stage: domain.StageSpec, kind: feat, openSpecQs: 2}, "s enter"},
		{"plan idle runs the planner", nextInput{stage: domain.StagePlan, kind: feat}, "enter"},
		{"plan gate reads then approves", nextInput{stage: domain.StagePlan, kind: feat, attn: attnGate}, "s g"},
		{"escalated plan gate offers a replan bounce, not just override", nextInput{stage: domain.StagePlan, kind: feat, attn: attnGate, escalated: true}, "s b g"},
		{"implement idle runs the stage", nextInput{stage: domain.StageImplement, kind: feat}, "enter"},
		{"implement gate diffs then advances", nextInput{stage: domain.StageImplement, kind: feat, attn: attnGate}, "d g"},
		{"review gate reads findings", nextInput{stage: domain.StageReview, kind: feat, attn: attnGate, escalated: true}, "s b g A"},
		{"verify gate clean lands", nextInput{stage: domain.StageVerify, kind: feat, attn: attnGate}, "g d b"},
		{"verify pass verdict lands", nextInput{stage: domain.StageVerify, kind: feat, attn: attnGate, verdict: verdictPass}, "g d b"},
		{"verify fail verdict reads evidence first", nextInput{stage: domain.StageVerify, kind: feat, attn: attnGate, escalated: true, verdict: verdictFail}, "s b g"},
		{"escalated verify without a session reads first", nextInput{stage: domain.StageVerify, kind: feat, attn: attnGate, escalated: true}, "s b g"},
		{"verify gate with failed check re-checks", nextInput{stage: domain.StageVerify, kind: feat, attn: attnGate, failedCheck: "unit tests"}, "v enter b"},
		{"verify gate with open comments resolves", nextInput{stage: domain.StageVerify, kind: feat, attn: attnGate, openDiffComments: 1}, "d b"},
		{"cleared inbox still reads as finished", nextInput{stage: domain.StageVerify, kind: feat, sess: engine.StateDone}, "g d b"},
		{"bug verify bounces to fix", nextInput{stage: domain.StageVerify, kind: bug, attn: attnGate}, "g d b"},
		{"verify linked lands on PR", nextInput{stage: domain.StageVerify, kind: feat, attn: attnGate, pullRequest: linkedRef}, "g d b "},
	}
	for _, c := range cases {
		if got := keysOf(nextActions(c.in)); got != c.want {
			t.Errorf("%s: keys = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestNextActionsCapAndRanking(t *testing.T) {
	for _, in := range []nextInput{
		{stage: domain.StageVerify, kind: domain.KindFeature, attn: attnGate},
		{stage: domain.StageReview, kind: domain.KindFeature, attn: attnGate, escalated: true},
		{stage: domain.StageVerify, kind: domain.KindFeature, attn: attnGate, failedCheck: "lint"},
	} {
		acts := nextActions(in)
		// four is the design's own gate: the recommendation, the way back,
		// the thing to read first, and handing the rest to autopilot.
		if len(acts) > 4 {
			t.Errorf("stage %s: %d suggestions, cap is 4", in.stage, len(acts))
		}
		for _, a := range acts {
			if a.id == "" || a.label == "" || a.why == "" || a.detail == "" {
				t.Errorf("stage %s: suggestion %q missing id/label/why/detail", in.stage, a.key)
			}
		}
	}
}

func TestNextActionsProseDetails(t *testing.T) {
	// bounce targets name the kind's work stage
	acts := nextActions(nextInput{stage: domain.StageVerify, kind: domain.KindBug, attn: attnGate})
	if !strings.Contains(acts[2].label, "fix") {
		t.Errorf("bug bounce label = %q, want the fix stage named", acts[2].label)
	}
	// a failed manual check is named in the why
	acts = nextActions(nextInput{stage: domain.StageVerify, kind: domain.KindFeature, attn: attnGate, failedCheck: "unit tests"})
	if !strings.Contains(acts[0].why, "unit tests") {
		t.Errorf("failed-check why = %q, want the check named", acts[0].why)
	}
	// the round-capped review gate explains itself
	acts = nextActions(nextInput{stage: domain.StageReview, kind: domain.KindFeature, attn: attnGate, escalated: true, reviewRound: maxReviewRounds})
	if !strings.Contains(acts[0].why, "rounds") {
		t.Errorf("capped review why = %q, want the round cap mentioned", acts[0].why)
	}
	// open comment counts surface in the blocker why
	acts = nextActions(nextInput{stage: domain.StageSpec, kind: domain.KindFeature, openSpecQs: 2})
	if !strings.Contains(acts[0].why, "2 open") {
		t.Errorf("blocked-gate why = %q, want the count", acts[0].why)
	}
	// a verified linked card's top action points at the PR
	linkedRef := domain.PullRequestRef{Repo: "o/r", Number: 42, URL: "https://github.com/o/r/pull/42"}
	acts = nextActions(nextInput{stage: domain.StageVerify, kind: domain.KindFeature, attn: attnGate, pullRequest: linkedRef})
	if want := "PR #42 — merge on GitHub, then pull main"; !strings.Contains(acts[0].why, want) {
		t.Errorf("linked verify-clean why = %q, want it to contain %q", acts[0].why, want)
	}
	// an unlinked verify-clean card keeps its pre-existing prose unchanged
	acts = nextActions(nextInput{stage: domain.StageVerify, kind: domain.KindFeature, attn: attnGate})
	if want := "squash-merge the branch and mark the feature done"; !strings.Contains(acts[0].why, want) {
		t.Errorf("unlinked verify-clean why = %q, want it to contain %q", acts[0].why, want)
	}
}

// nextInputFor pulls together inbox, session, checks, and row state.
func TestNextInputForAssembly(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0-test")
	row := featureRow{
		F:                domain.Feature{ID: "FD-001", Stage: domain.StageVerify},
		Landed:           true,
		OpenSpecQs:       1,
		OpenDiffComments: 2,
	}
	m.inbox.addEscalated("FD-001", attnGate, "escalated")
	m.setRound("FD-001", domain.RoundKindReview, 2)
	m.checks["FD-001"] = stagedChecks{stage: domain.StageVerify, results: []verify.Result{{Name: "lint", OK: true}, {Name: "unit", OK: false}}}

	in := m.nextInputFor(row)
	want := nextInput{
		stage: domain.StageVerify, landed: true,
		attn: attnGate, escalated: true,
		reviewRound: 2, failedCheck: "unit",
		openSpecQs: 1, openDiffComments: 2,
	}
	if in != want {
		t.Errorf("nextInputFor = %+v, want %+v", in, want)
	}
}

// The thread is the conversation: its input sits at the bottom of the
// same page. So once a session is live, offering "chat with the
// architect" as a next action points at the surface you are already
// looking at, and spends a row of the one block whose job is to tell you
// something you did not already know.
func TestNextCardDropsTheChatActionWhenTheChatIsLive(t *testing.T) {
	for _, stage := range []domain.Stage{domain.StageBrainstorm, domain.StageSpec, domain.StageInvestigate} {
		live := nextActions(nextInput{stage: stage, kind: domain.KindFeature, sess: engine.StateInteractive, live: true})
		for _, a := range live {
			if a.key == "enter" {
				t.Errorf("%s with a live session still offers %q — the input is already on screen", stage, a.label)
			}
		}

		// BG-070: a card restored without a backend reports
		// StateInteractive too, and there the row is the only way back
		// into the conversation — without it the stage's gate becomes the
		// card's recommendation, so "approve" answers "my conversation
		// went away".
		rehydrated := nextActions(nextInput{stage: stage, kind: domain.KindFeature, sess: engine.StateInteractive})
		var resume bool
		for _, a := range rehydrated {
			if a.key == "enter" {
				resume = true
				if !strings.HasPrefix(a.label, "resume") {
					t.Errorf("%s, detached: %q does not say the conversation is resumed", stage, a.label)
				}
			}
		}
		if !resume {
			t.Errorf("%s with a detached session offers no way back into the conversation; got %q",
				stage, nextActionIDs(rehydrated))
		}

		// with nobody to talk to yet the action earns its place, and says
		// what enter actually does rather than naming a pane that no
		// longer exists
		cold := nextActions(nextInput{stage: stage, kind: domain.KindFeature})
		var found bool
		for _, a := range cold {
			if a.key == "enter" {
				found = true
				if strings.HasPrefix(a.label, "chat with") {
					t.Errorf("%s: %q names a chat pane the thread replaced", stage, a.label)
				}
			}
		}
		if !found {
			t.Errorf("%s with no session offers no way to start one", stage)
		}
	}
}

// idsOf flattens a suggestion list to its id sequence, mirroring
// cardactions_test.go's idsOf for []cardAction.
func nextActionIDs(acts []nextAction) string {
	ids := make([]string, len(acts))
	for i, a := range acts {
		ids[i] = a.id
	}
	return strings.Join(ids, " ")
}

// TestNextActionsRanksPullReview covers the "pull PR review" nudge
// (nextsteps.go's appendPullReviewSuggestion): it appears in review and
// verify once the card is linked, in no other stage, and never on an
// unlinked card — matching cardActionsFor's rank map, which is what lifts
// prpull out of the fold onto the screen.
func TestNextActionsRanksPullReview(t *testing.T) {
	linked := domain.PullRequestRef{Repo: "o/r", Number: 42, URL: "https://github.com/o/r/pull/42"}

	for _, stage := range []domain.Stage{domain.StageReview, domain.StageVerify} {
		acts := nextActions(nextInput{stage: stage, kind: domain.KindFeature, attn: attnGate, pullRequest: linked})
		if !strings.Contains(nextActionIDs(acts), "prpull") {
			t.Errorf("%s linked: ids = %q, want prpull ranked", stage, nextActionIDs(acts))
		}
	}

	// unlinked: never ranked, in either stage.
	for _, stage := range []domain.Stage{domain.StageReview, domain.StageVerify} {
		acts := nextActions(nextInput{stage: stage, kind: domain.KindFeature, attn: attnGate})
		if strings.Contains(nextActionIDs(acts), "prpull") {
			t.Errorf("%s unlinked: ids = %q, want no prpull", stage, nextActionIDs(acts))
		}
	}

	// linked but outside review/verify: still not ranked — the loop this
	// nudges toward only exists once there is something to review.
	acts := nextActions(nextInput{stage: domain.StageImplement, kind: domain.KindFeature, attn: attnGate, pullRequest: linked})
	if strings.Contains(nextActionIDs(acts), "prpull") {
		t.Errorf("implement linked: ids = %q, want no prpull outside review/verify", nextActionIDs(acts))
	}
}

// TestCardActionsForPromotesPullReview proves the id lands where the board
// actually shows it: cardActionsFor promotes any ranked id out of the
// fold, so a linked card in review shows "pull PR review" on screen rather
// than one keystroke away.
func TestCardActionsForPromotesPullReview(t *testing.T) {
	linked := domain.PullRequestRef{Repo: "o/r", Number: 42, URL: "https://github.com/o/r/pull/42"}
	in := nextInput{stage: domain.StageReview, kind: domain.KindFeature, attn: attnGate, escalated: true, pullRequest: linked}
	r := cardRow(domain.KindFeature, domain.StageReview, false, true)
	r.F.PullRequest = linked

	acts := cardActionsFor(in, r)
	var found *cardAction
	for i := range acts {
		if acts[i].id == "prpull" {
			found = &acts[i]
		}
	}
	if found == nil {
		t.Fatalf("prpull missing from %v", idsOf(acts))
	}
	if found.folded {
		t.Error("prpull should be promoted (unfolded) once nextActions ranks it")
	}
}
