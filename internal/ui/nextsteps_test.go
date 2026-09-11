package ui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/ui/theme"
	"github.com/morphis/gummi/internal/verify"
	"github.com/morphis/gummi/internal/worktree"
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
		// attach is plumbing, not an answer: it is in the inventory and
		// answers /attach, and it no longer takes a row in the set beside
		// the one thing the card is actually waiting to be told.
		{"paused picks the stage back up", nextInput{stage: domain.StageVerify, kind: feat, sess: engine.StatePaused, hasWorktree: true}, "enter"},
		{"failure retries", nextInput{stage: domain.StageVerify, kind: feat, attn: attnFailure, hasWorktree: true}, "enter"},
		{"paused with no worktree offers only the re-run", nextInput{stage: domain.StagePlan, kind: feat, sess: engine.StatePaused}, "enter"},
		// §1.1a: pausing a card AFTER its gate already fired is a stop
		// mid-decision, not a reason to hide the decision. BG-002 passed
		// verify, was parked, and lost "land on main" from the page
		// entirely — the paused branch used to return a single row before
		// ever reaching the stage switch below. Now a paused card whose
		// stage has already finished (attnGate here) still gets that
		// stage's own answer set, with the resume row riding where "stop
		// here" would otherwise sit — an already-stopped card has no more
		// use for a second way to stop.
		{"paused after a passed verify gate keeps the landing decision", nextInput{stage: domain.StageVerify, kind: feat, sess: engine.StatePaused, attn: attnGate, verdict: verdictPass, hasWorktree: true}, "g b enter"},
		{"paused after an approved plan keeps the decision, not just the resume", nextInput{stage: domain.StagePlan, kind: feat, sess: engine.StatePaused, attn: attnGate}, "g enter"},
		// the budget stop's two answers are keyless: top up (no
		// accelerator — u is the envelope dialog) and, with no session to
		// stop, nothing else.
		{"budget stop tops up", nextInput{stage: domain.StageImplement, kind: feat, attn: attnBudget}, ""},
		{"question routes to attach", nextInput{stage: domain.StagePlan, kind: feat, attn: attnQuestion}, "enter"},
		{"todo starts the flow", nextInput{stage: domain.StageTodo, kind: feat}, "g"},
		// the design stage: get the conversation going, then approve.
		// Reading the artifact is the page's own tab, and the autopilot
		// switch is a card setting — neither is an answer to "what now",
		// and both used to take a row here.
		{"design stage talks and approves", nextInput{stage: domain.StagePlan, kind: feat}, "enter g"},
		// §3.3: a FINISHED design gate leads with the decision (approve),
		// not the paid re-run of the architect — the narration's own
		// sentence here reads "ready for your decision", and the verify
		// gate already leads with its decision the same way. An
		// unfinished stop (the case just above) still talks first,
		// because there approve has nothing to approve yet — this test
		// used to expect "enter g" for both, which was the bug the finding
		// named: the pre-selected action at a finished gate was the
		// expensive one.
		{"design gate offers the same approval", nextInput{stage: domain.StagePlan, kind: feat, attn: attnGate}, "g enter"},
		// "send it back" appears at the design stage only while the
		// architect is live to receive the turn that carries it.
		{"live design stage can send it back", nextInput{stage: domain.StagePlan, kind: feat, sess: engine.StateInteractive, live: true}, "g "},
		{"design stage with open questions blocks approve", nextInput{stage: domain.StagePlan, kind: feat, attn: attnGate, openSpecQs: 2}, "s enter"},
		// …but only once there is a gate to block. A comment written
		// before the stage ran is context for the agent about to read it,
		// not an objection to work it has not done.
		{"a comment before the first run is not a blocker", nextInput{stage: domain.StagePlan, kind: feat, openSpecQs: 2}, "enter g"},
		// a gate blocked on a section the stage never drafted leads with
		// the writer re-run that unblocks it, not with approve
		// the gate marker matters: only a stage that RAN can have left a
		// section blank, and before the first run the row is suppressed
		// (blockedGate) so it cannot appear as a second, identical-looking
		// way to say "start the architect".
		{"design gate with a blank section leads with the redraft", nextInput{stage: domain.StagePlan, kind: feat, attn: attnGate, undrafted: []string{"Chosen approach"}}, "enter enter"},
		{"a blank section before the first run is not a blocker", nextInput{stage: domain.StagePlan, kind: feat, undrafted: []string{"Chosen approach"}}, "enter g"},
		// nothing has been produced yet, so there is nothing to send back:
		// the rewind to plan is /bounce, in the inventory.
		{"implement idle runs the stage", nextInput{stage: domain.StageImplement, kind: feat}, "enter"},
		// advance, then the merged send-it-back (keyless: at implement it
		// re-runs the stage in place with the line as its note).
		{"implement gate advances or sends it back", nextInput{stage: domain.StageImplement, kind: feat, attn: attnGate}, "g "},

		{"verify gate clean lands", nextInput{stage: domain.StageVerify, kind: feat, attn: attnGate}, "g b"},
		{"verify pass verdict lands", nextInput{stage: domain.StageVerify, kind: feat, attn: attnGate, verdict: verdictPass}, "g b"},
		// send it back leads, land-anyway follows. Reading the evidence is
		// the artifact tab, and the repeated-failure guard is a sentence in
		// the narration now rather than a re-ranking of these two rows.
		{"verify fail verdict sends it back", nextInput{stage: domain.StageVerify, kind: feat, attn: attnGate, escalated: true, verdict: verdictFail}, "b g"},
		{"escalated verify without a session sends it back", nextInput{stage: domain.StageVerify, kind: feat, attn: attnGate, escalated: true}, "b g"},
		// re-running the checks alone is /verify: it re-evaluates the gate
		// rather than answering it.
		{"verify gate with failed check sends it back", nextInput{stage: domain.StageVerify, kind: feat, attn: attnGate, failedCheck: "unit tests"}, "b g"},
		{"verify gate with open comments resolves", nextInput{stage: domain.StageVerify, kind: feat, attn: attnGate, openDiffComments: 1}, "d b"},
		// a settled session is one "stop here" can still park, so this is
		// the one verify row that carries the third answer.
		{"cleared inbox still reads as finished", nextInput{stage: domain.StageVerify, kind: feat, sess: engine.StateDone}, "g b p"},
		{"bug verify sends it back", nextInput{stage: domain.StageVerify, kind: bug, attn: attnGate}, "g b"},
		// the trailing empty key is prpull, which nextActions appends for a
		// linked card. It is not in the answer set — stageActions does not
		// return it — but it still rides above the fold in the inventory.
		{"verify linked lands on PR", nextInput{stage: domain.StageVerify, kind: feat, attn: attnGate, pullRequest: linkedRef}, "g b "},
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
		{stage: domain.StageVerify, kind: domain.KindFeature, attn: attnGate, escalated: true},
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
	// find returns the action with this id, so the assertions name what
	// they are about rather than indexing a list whose order is exactly
	// the thing most likely to change under them.
	find := func(acts []nextAction, id string) nextAction {
		t.Helper()
		for _, a := range acts {
			if a.id == id {
				return a
			}
		}
		t.Fatalf("no %q action in %v", id, keysOf(acts))
		return nextAction{}
	}

	// the merged answer wears one label wherever it appears, whichever
	// edge it happens to take underneath
	for _, in := range []nextInput{
		{stage: domain.StageVerify, kind: domain.KindBug, attn: attnGate},
		{stage: domain.StageVerify, kind: domain.KindFeature, attn: attnGate, verdict: verdictFail},
		{stage: domain.StageImplement, kind: domain.KindFeature, attn: attnGate},
	} {
		acts := nextActions(in)
		var back int
		for _, a := range acts {
			if a.label == "send it back" {
				back++
			}
		}
		if back != 1 {
			t.Errorf("%s/%s: %d rows labelled \"send it back\", want exactly 1 — bounce, changes and re-run-with-note are one answer now (keys %q)",
				in.stage, in.verdict, back, keysOf(acts))
		}
	}

	// a failed manual check is named in the why of the answer it argues for
	acts := nextActions(nextInput{stage: domain.StageVerify, kind: domain.KindFeature, attn: attnGate, failedCheck: "unit tests"})
	if !strings.Contains(find(acts, "bounce").why, "implementation's fault") {
		t.Errorf("failed-check send-it-back why = %q", find(acts, "bounce").why)
	}
	// open comment counts surface in the blocker why
	acts = nextActions(nextInput{stage: domain.StagePlan, kind: domain.KindFeature, attn: attnGate, openSpecQs: 2})
	if !strings.Contains(acts[0].why, "2 open") {
		t.Errorf("blocked-gate why = %q, want the count", acts[0].why)
	}
	// a blank required section is named in the blocker why, on the run
	// action that redrafts it — approve is not even offered
	acts = nextActions(nextInput{stage: domain.StagePlan, kind: domain.KindFeature, attn: attnGate, undrafted: []string{"Chosen approach", "Implementation notes"}})
	if acts[0].id != "run" || acts[0].label != "draft the missing sections" {
		t.Errorf("undrafted-gate lead = %s/%q, want the redraft run action", acts[0].id, acts[0].label)
	}
	if !strings.Contains(acts[0].why, "Chosen approach, Implementation notes") {
		t.Errorf("undrafted-gate why = %q, want the blank sections named", acts[0].why)
	}
	acts = nextActions(nextInput{stage: domain.StagePlan, kind: domain.KindBug, attn: attnGate, undrafted: []string{"Root cause"}})
	if acts[0].label != "draft the missing section" || !strings.Contains(acts[0].why, "Root cause") {
		t.Errorf("single undrafted-gate lead = %q/%q, want the section named", acts[0].label, acts[0].why)
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
		Undrafted:        []string{"Verification plan"},
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
		undrafted: []string{"Verification plan"},
		// base is resolved at assembly now, so the landing row can name the
		// branch the card actually merges onto instead of the literal "main"
		// (round 3 §5.2). An unattached Shell answers with the default name.
		base: worktree.DefaultBaseBranchName,
	}
	// undrafted is a slice (one blocker per blank section), so the struct
	// no longer compares with ==; DeepEqual keeps the assembly pinned.
	if !reflect.DeepEqual(in, want) {
		t.Errorf("nextInputFor = %+v, want %+v", in, want)
	}
}

// The thread is the conversation: its input sits at the bottom of the
// same page. So once a session is live, offering "chat with the
// architect" as a next action points at the surface you are already
// looking at, and spends a row of the one block whose job is to tell you
// something you did not already know.
func TestNextCardDropsTheChatActionWhenTheChatIsLive(t *testing.T) {
	for _, stage := range []domain.Stage{domain.StagePlan, domain.StagePlan, domain.StagePlan} {
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

	for _, stage := range []domain.Stage{domain.StageVerify, domain.StageVerify} {
		acts := nextActions(nextInput{stage: stage, kind: domain.KindFeature, attn: attnGate, pullRequest: linked})
		if !strings.Contains(nextActionIDs(acts), "prpull") {
			t.Errorf("%s linked: ids = %q, want prpull ranked", stage, nextActionIDs(acts))
		}
	}

	// unlinked: never ranked, in either stage.
	for _, stage := range []domain.Stage{domain.StageVerify, domain.StageVerify} {
		acts := nextActions(nextInput{stage: stage, kind: domain.KindFeature, attn: attnGate})
		if strings.Contains(nextActionIDs(acts), "prpull") {
			t.Errorf("%s unlinked: ids = %q, want no prpull", stage, nextActionIDs(acts))
		}
	}

	// linked but with nothing to review yet: not ranked — the loop this
	// nudges toward only exists once there is a diff. Review is no longer
	// one of those stages, so the work stage is: its critique is what
	// review was, and a PR's comments belong on the diff it just produced.
	acts := nextActions(nextInput{stage: domain.StagePlan, kind: domain.KindFeature, attn: attnGate, pullRequest: linked})
	if strings.Contains(nextActionIDs(acts), "prpull") {
		t.Errorf("spec linked: ids = %q, want no prpull before there is a diff", nextActionIDs(acts))
	}
}

// TestCardActionsForPromotesPullReview proves the id lands where the board
// actually shows it: cardActionsFor promotes any ranked id out of the
// fold, so a linked card in review shows "pull PR review" on screen rather
// than one keystroke away.
func TestCardActionsForPromotesPullReview(t *testing.T) {
	linked := domain.PullRequestRef{Repo: "o/r", Number: 42, URL: "https://github.com/o/r/pull/42"}
	in := nextInput{stage: domain.StageVerify, kind: domain.KindFeature, attn: attnGate, escalated: true, pullRequest: linked}
	r := cardRow(domain.KindFeature, domain.StageVerify, false, true)
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

// TestWhyItStoppedNamesEveryBlocker is §1.3: a stop with more than one
// kind of blocker used to name only the first one whyItStopped checked
// (spec, then diff, then undrafted) — a user resolved the one spec
// comment the page named, and a diff comment they were never told about
// immediately took its place. With two kinds present the sentence now
// names both, in the priority order blockedGate still uses to pick which
// ACTION to offer; with only one kind present (the ordinary case) the
// sentence is unchanged.
func TestWhyItStoppedNamesEveryBlocker(t *testing.T) {
	only := nextInput{stage: domain.StageVerify, kind: domain.KindFeature, attn: attnGate, openSpecQs: 1}
	if got, want := whyItStopped(only), "1 open comment in the spec is holding the gate shut."; got != want {
		t.Errorf("single blocker = %q, want %q (unchanged)", got, want)
	}

	both := nextInput{stage: domain.StageVerify, kind: domain.KindFeature, attn: attnGate, openSpecQs: 1, openDiffComments: 1}
	if got, want := whyItStopped(both), "1 comment in the spec and 1 on the diff are holding the gate shut."; got != want {
		t.Errorf("spec+diff blocker = %q, want %q", got, want)
	}

	// the action row underneath is still ONE row (s resolves the spec,
	// d resolves the diff — there is no single key for both), but its why
	// now says there is more to it than what pressing s alone will clear.
	acts := nextActions(both)
	if !strings.Contains(acts[0].why, "plus 1 on the diff") {
		t.Errorf("blocked-gate why with a second blocker = %q, want it to name the diff too", acts[0].why)
	}

	all3 := nextInput{stage: domain.StagePlan, kind: domain.KindFeature, attn: attnGate,
		openSpecQs: 2, openDiffComments: 1, undrafted: []string{"Chosen approach"}}
	got := whyItStopped(all3)
	if !strings.Contains(got, "2 comments in the spec") || !strings.Contains(got, "1 on the diff") ||
		!strings.Contains(got, "Chosen approach left blank in the spec") || !strings.HasSuffix(got, "are holding the gate shut.") {
		t.Errorf("three-way blocker sentence = %q", got)
	}
}

// TestImplementRunWhyDropsRestartOnAFirstArrival is §3.2's second half:
// "(or restart)" only reads true once implement has actually run before.
// verifyBounces is the one edge that lands a card back in this branch a
// second time (a failed verify sent back for rework); the very first
// arrival, straight off approving the plan, has nothing to restart yet.
func TestImplementRunWhyDropsRestartOnAFirstArrival(t *testing.T) {
	first := nextActions(nextInput{stage: domain.StageImplement, kind: domain.KindFeature})
	if strings.Contains(first[0].why, "restart") {
		t.Errorf("first arrival at implement = %q, want no mention of restarting", first[0].why)
	}
	restart := nextActions(nextInput{stage: domain.StageImplement, kind: domain.KindFeature, verifyBounces: 1})
	if !strings.Contains(restart[0].why, "restart") {
		t.Errorf("implement after a verify bounce = %q, want it to offer a restart too", restart[0].why)
	}
}

// TestTodoAndApproveRowsPromiseOnlyWhatTheyDo covers §3.1 and §3.2's
// first half: both rows' arm is advance, which only moves the stage
// marker — no agent runs on that keypress. The old wording ("start …
// the agent reads the card", "hands the card to the agent stages")
// promised the read/handoff would happen on THIS keypress; live, the
// next screen's own row ("start the architect" / "run implement") was
// the actual start, one keypress later. Neither row may claim what only
// the next screen's talk/run row actually does, and "agent stages" is
// gone — the implementer is named instead.
func TestTodoAndApproveRowsPromiseOnlyWhatTheyDo(t *testing.T) {
	todo := nextActions(nextInput{stage: domain.StageTodo, kind: domain.KindFeature})[0]
	if strings.Contains(todo.why, "reads the card") {
		t.Errorf("todo row why = %q, want it to promise only the move into plan", todo.why)
	}
	if !strings.Contains(todo.why, "moves the card into plan") {
		t.Errorf("todo row why = %q, want it to say what advance actually does", todo.why)
	}

	approve := nextActions(nextInput{stage: domain.StagePlan, kind: domain.KindFeature, attn: attnGate})[0]
	if approve.label != "approve" {
		t.Fatalf("finished design gate lead = %q, want approve leading (§3.3)", approve.label)
	}
	if strings.Contains(approve.why, "agent stages") {
		t.Errorf("approve row why = %q, want the jargon gone", approve.why)
	}
	if !strings.Contains(approve.why, "implementer") {
		t.Errorf("approve row why = %q, want it to name the implementer", approve.why)
	}
}
