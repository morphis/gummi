package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/worktree"
)

// draftingAgent answers every scribe turn with one fenced landing message
// carrying subject, so a test can tell which pass produced the text in
// hand. It counts the turns it served: the whole point of a pre-draft is
// that the second reader of the same message does not buy a second pass.
type draftingAgent struct {
	*agent.Fake
	turns int
}

func newDraftingAgent(subject string) *draftingAgent {
	d := &draftingAgent{}
	d.Fake = &agent.Fake{Responder: func(_ agent.SessionOpts, _ string) []agent.Event {
		d.turns++
		return []agent.Event{
			{Kind: agent.EventMessage, Text: "```gummi-commit\n" + subject + "\n\n- a rationale bullet\n```"},
			{Kind: agent.EventIdle},
		}
	}}
	return d
}

// predraftCard builds a card that is in the state a pre-draft is fired
// from: a row in the store, a worktree, and a commit of its own on the
// branch (without one there is no landing to describe, and the pass is
// skipped).
func predraftCard(t *testing.T, store *state.Store, wt *worktree.Manager) domain.Feature {
	t.Helper()
	ctx := context.Background()
	f := feature(1, "prefill the merge dialog", domain.StageVerify)
	if err := store.CreateFeature(ctx, &f); err != nil {
		t.Fatal(err)
	}
	withWorktree(t, wt, f)
	path, err := wt.Path(&f)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "work.txt"), []byte("the work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.CommitAll(ctx, &f, "FD-001: the work"); err != nil {
		t.Fatal(err)
	}
	return f
}

// commit adds another commit to the card's branch, moving the tip out
// from under whatever was drafted against it.
func commitMore(t *testing.T, wt *worktree.Manager, f domain.Feature, name string) {
	t.Helper()
	path, err := wt.Path(&f)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, name), []byte("more\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.CommitAll(context.Background(), &f, "FD-001: "+name); err != nil {
		t.Fatal(err)
	}
}

func predraftEngine(t *testing.T, ag agent.Agent) (*Engine, *state.Store, *worktree.Manager) {
	t.Helper()
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })
	return e, store, wt
}

// TestPredraftStoresTheMessageAgainstTheTip is the whole mechanism in one
// pass: the message is composed at the gate, stored, and stamped with the
// tip it describes — which is what lets a later reader trust it without
// re-running the scribe.
func TestPredraftStoresTheMessageAgainstTheTip(t *testing.T) {
	ag := newDraftingAgent("feat(ui): open the dialog on a message")
	e, store, wt := predraftEngine(t, ag)
	f := predraftCard(t, store, wt)
	ctx := context.Background()

	draft, err := e.PredraftCommitMessage(ctx, f, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(draft, "open the dialog on a message") {
		t.Fatalf("pre-draft = %q, want the scribe's fenced body", draft)
	}
	cur, err := store.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cur.CommitDraft != draft {
		t.Errorf("stored draft = %q, want %q", cur.CommitDraft, draft)
	}
	tip, err := wt.Head(ctx, &f)
	if err != nil {
		t.Fatal(err)
	}
	if cur.CommitDraftSHA != tip {
		t.Errorf("stored SHA = %q, want the branch tip %q", cur.CommitDraftSHA, tip)
	}
	if got := e.PendingCommitDraft(ctx, f); got != draft {
		t.Errorf("PendingCommitDraft = %q, want the stored draft", got)
	}
}

// TestPredraftPaysForOnePass covers the two ways the same message gets
// asked for twice: re-reaching the gate (a restart replaying the verify
// completion) and the dialog opening on it. Neither may buy a second
// scribe turn — a pre-draft that re-drafts is just the old latency moved
// somewhere the reader cannot see it.
func TestPredraftPaysForOnePass(t *testing.T) {
	ag := newDraftingAgent("feat(ui): compose once")
	e, store, wt := predraftEngine(t, ag)
	f := predraftCard(t, store, wt)
	ctx := context.Background()

	if _, err := e.PredraftCommitMessage(ctx, f, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.PredraftCommitMessage(ctx, f, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.LandingMessage(ctx, f, false); err != nil {
		t.Fatal(err)
	}
	if ag.turns != 1 {
		t.Errorf("scribe ran %d times, want 1: the stored draft answers every later reader", ag.turns)
	}
}

// TestPredraftGoesStaleWithTheBranch is the guard that makes an early
// draft safe at all. A branch that moved after the draft was written —
// the merge flow's own final checkpoint is the common case — describes
// work the draft never saw, so the stored one must be ignored and the
// live pass must run, exactly as it did before pre-drafting existed.
func TestPredraftGoesStaleWithTheBranch(t *testing.T) {
	ag := newDraftingAgent("feat(ui): the first tree")
	e, store, wt := predraftEngine(t, ag)
	f := predraftCard(t, store, wt)
	ctx := context.Background()

	if _, err := e.PredraftCommitMessage(ctx, f, ""); err != nil {
		t.Fatal(err)
	}
	commitMore(t, wt, f, "afterthought.txt")

	if got := e.PendingCommitDraft(ctx, f); got != "" {
		t.Errorf("PendingCommitDraft = %q, want none: the branch moved under the draft", got)
	}
	if _, err := e.LandingMessage(ctx, f, false); err != nil {
		t.Fatal(err)
	}
	if ag.turns != 2 {
		t.Errorf("scribe ran %d times, want 2: a stale draft must be re-composed", ag.turns)
	}
}

// TestRedraftNeverAnswersFromTheStore pins the one case that must always
// cost a pass: a reader pressing Redraft is asking for a different
// message, and handing back the stored one — instantly, unchanged —
// reads as a broken button.
func TestRedraftNeverAnswersFromTheStore(t *testing.T) {
	ag := newDraftingAgent("feat(ui): compose again")
	e, store, wt := predraftEngine(t, ag)
	f := predraftCard(t, store, wt)
	ctx := context.Background()

	if _, err := e.PredraftCommitMessage(ctx, f, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.LandingMessage(ctx, f, true); err != nil {
		t.Fatal(err)
	}
	if ag.turns != 2 {
		t.Errorf("scribe ran %d times, want 2: Redraft always composes", ag.turns)
	}
}

// TestPredraftRecordsWhyThereIsNoDraft: a failed pre-draft is not a
// silent one. The card's durable reason is what the dashboard renders and
// what tells the reader who lands to a live pass that this was not the
// first attempt.
func TestPredraftRecordsWhyThereIsNoDraft(t *testing.T) {
	// an unparseable reply is a guard rejection, not a transport fault
	ag := &agent.Fake{Responder: func(_ agent.SessionOpts, _ string) []agent.Event {
		return []agent.Event{
			{Kind: agent.EventMessage, Text: "Sure! Here's a commit message for you."},
			{Kind: agent.EventIdle},
		}
	}}
	e, store, wt := predraftEngine(t, ag)
	f := predraftCard(t, store, wt)
	ctx := context.Background()

	if _, err := e.PredraftCommitMessage(ctx, f, ""); err == nil {
		t.Fatal("PredraftCommitMessage accepted an unfenced reply")
	}
	cur, err := store.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cur.CommitDraftFail == "" {
		t.Error("a failed pre-draft recorded no reason")
	}
	if cur.CommitDraft != "" {
		t.Errorf("stored draft = %q, want none after a failed pass", cur.CommitDraft)
	}
	if got := e.PendingCommitDraft(ctx, f); got != "" {
		t.Errorf("PendingCommitDraft = %q, want none", got)
	}
}

// TestPredraftSkipsCardsNobodyWillLand: every exclusion costs a scribe
// turn if it is wrong, and buys nothing if it is right — the landing
// message for each of these cards is either written by something else or
// never read at all.
func TestPredraftSkipsCardsNobodyWillLand(t *testing.T) {
	goal := feature(1, "a goal", domain.StageVerify)
	goal.Kind = domain.KindGoal
	research := feature(2, "a study", domain.StageVerify)
	research.Kind = domain.KindResearch
	goalCard := feature(3, "a goal's card", domain.StageVerify)
	goalCard.GoalID = domain.FeatureID("GL-001")
	linked := feature(4, "a linked card", domain.StageVerify)
	linked.PullRequest = domain.PullRequestRef{Repo: "o/r", Number: 7, URL: "https://x/7"}
	handedOff := feature(5, "a handed-off card", domain.StageVerify)
	handedOff.HandedOffAt = time.Now().UTC()
	dropped := feature(6, "a dropped card", domain.StageVerify)
	dropped.GoalDroppedAt = time.Now().UTC()

	for _, tc := range []struct {
		name string
		f    domain.Feature
	}{
		{"goal lands as a merge commit gummi writes", goal},
		{"research carries no branch", research},
		{"a goal's card is landed by its goal, at once", goalCard},
		{"a linked card refuses a local landing", linked},
		{"a handed-off card already ended", handedOff},
		{"a dropped card already ended", dropped},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if PredraftEligible(tc.f) {
				t.Errorf("PredraftEligible(%s) = true, want false: %s", tc.f.ID, tc.name)
			}
		})
	}

	ordinary := feature(7, "an ordinary card", domain.StageVerify)
	if !PredraftEligible(ordinary) {
		t.Error("predraftEligible = false for the card the whole mechanism is for")
	}
}

// TestPredraftSkipsSkipsTheScribeEntirely proves the exclusion is a
// refusal to spend, not a spend whose result is thrown away.
func TestPredraftSkipsSkipsTheScribeEntirely(t *testing.T) {
	ag := newDraftingAgent("feat(ui): never asked for")
	e, store, wt := predraftEngine(t, ag)
	f := predraftCard(t, store, wt)
	f.PullRequest = domain.PullRequestRef{Repo: "o/r", Number: 7, URL: "https://x/7"}

	if _, err := e.PredraftCommitMessage(context.Background(), f, ""); err != nil {
		t.Fatal(err)
	}
	if ag.turns != 0 {
		t.Errorf("scribe ran %d times for an excluded card, want 0", ag.turns)
	}
}

// TestPredraftCarriesWhatVerifyFound: the pre-draft's one advantage over
// the pass it replaces is timing — it runs while the verify report still
// exists. If that report does not reach the prompt, the earlier moment
// buys only latency, not a better message.
func TestPredraftCarriesWhatVerifyFound(t *testing.T) {
	var prompt string
	ag := &agent.Fake{Responder: func(_ agent.SessionOpts, msg string) []agent.Event {
		prompt = msg
		return []agent.Event{
			{Kind: agent.EventMessage, Text: "```gummi-commit\nfeat(ui): x\n\n- y\n```"},
			{Kind: agent.EventIdle},
		}
	}}
	e, store, wt := predraftEngine(t, ag)
	f := predraftCard(t, store, wt)

	const report = "ran the fractional-size suite: 22/22 pass, no regressions"
	if _, err := e.PredraftCommitMessage(context.Background(), f, "%% reviewer\n"+report+"\nVERDICT: pass"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, report) {
		t.Errorf("prompt never carried the verify report:\n%s", prompt)
	}
	if !strings.Contains(prompt, "## What verify found") {
		t.Error("the verify report arrived without its heading")
	}
	if strings.Contains(prompt, "%% reviewer") {
		t.Error("marker lines are collaboration scaffolding and must not reach the scribe")
	}
}

// TestCommitmsgVerifyNoteKeepsTheConclusion: a verify report that overruns
// the cap is trimmed from the FRONT. The front is setup and command
// output; the end is what the pass concluded, which is the half a landing
// message is made of.
func TestCommitmsgVerifyNoteKeepsTheConclusion(t *testing.T) {
	const tail = "every invariant held; the branch is landable"
	note := commitmsgVerifyNote(strings.Repeat("noise noise noise\n", 400) + tail)
	if len(note) > commitmsgVerifyNoteCap+len("[report truncated]\n\n") {
		t.Errorf("note is %d bytes, want it capped near %d", len(note), commitmsgVerifyNoteCap)
	}
	if !strings.HasSuffix(note, tail) {
		t.Errorf("note lost its conclusion: %q", note[max(0, len(note)-120):])
	}
	if !strings.HasPrefix(note, "[report truncated]") {
		t.Error("a clipped report must say so rather than pass as whole")
	}
	if got := commitmsgVerifyNote("  \n%% marker\n  \n"); got != "" {
		t.Errorf("commitmsgVerifyNote(markers only) = %q, want empty", got)
	}
}
