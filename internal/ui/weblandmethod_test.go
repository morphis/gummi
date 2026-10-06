package ui

import (
	"context"
	"io"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/webapi"
)

// bridgeOver runs m under a Bridge, as the web face does, and stops it when
// the test ends.
func bridgeOver(t *testing.T, m *Shell) *Bridge {
	t.Helper()
	b := NewHeadless(m, tea.WithOutput(io.Discard))
	go func() { _ = b.Run() }()
	t.Cleanup(b.Stop)
	return b
}

// pinnedAgainst reads the revision of the decision the card FD-001 is
// waiting on, as the page does before it acts (ActionRequest.Against).
func pinnedAgainst(t *testing.T, b *Bridge) string {
	t.Helper()
	cur, err := b.Card(context.Background(), "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	if cur.Decision == nil {
		return ""
	}
	return cur.Decision.Against.Token
}

// TestWebLandingWithMergeMethod: a landing requested from the page with the
// merge-commit method lands the branch as a merge commit, moves the card to
// done, and says so in its own record.
func TestWebLandingWithMergeMethod(t *testing.T) {
	m, root, _ := mergeFixture(t)
	b := bridgeOver(t, m)
	ctx := context.Background()
	before := gitOut(t, root, "rev-parse", "HEAD")
	message := "FD-001: rebase me\n\nKeeps the feature commit on main."

	card, err := b.Action(ctx, "FD-001", "merge", webapi.ActionRequest{Message: message, Method: "merge", Against: pinnedAgainst(t, b)}, "Simon")
	if err != nil {
		t.Fatalf("merge action: %v", err)
	}
	if card == nil || card.Stage != string(domain.StageDone) {
		t.Fatalf("card after merge landing = %+v, want done", card)
	}
	if parents := strings.Fields(gitOut(t, root, "log", "-1", "--format=%P")); len(parents) != 2 || parents[0] != before {
		t.Fatalf("main tip parents = %v, want a merge commit on %s", parents, before)
	}
	if got := gitOut(t, root, "log", "-1", "--format=%B"); got != message {
		t.Errorf("merge commit message = %q, want %q", got, message)
	}
}

// TestWebAnswerAdvanceWithMergeMethod: the verify decision's advance option
// offers the choice, and an answer that picks merge with the message the
// person read lands the branch as a merge commit on the base.
func TestWebAnswerAdvanceWithMergeMethod(t *testing.T) {
	m, root, _ := mergeFixture(t)
	b := bridgeOver(t, m)
	ctx := context.Background()
	if err := b.Do(ctx, func(m *Shell) tea.Cmd { m.raiseAttention("FD-001", attnGate, "verify passed"); return nil }); err != nil {
		t.Fatal(err)
	}
	c, err := b.Card(ctx, "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	if c.Decision == nil {
		t.Fatal("no decision at verify")
	}
	var offered bool
	for _, o := range c.Decision.Options {
		if o.ID == "advance" {
			offered = strings.Join(o.Methods, ",") == "squash,merge"
		}
	}
	if !offered {
		t.Fatalf("advance option does not offer the method choice: %+v", c.Decision.Options)
	}
	before := gitOut(t, root, "rev-parse", "HEAD")
	message := "FD-001: rebase me\n\nKeeps the feature commit on main."

	card, err := b.Answer(ctx, "FD-001", webapi.AnswerRequest{Ref: c.Decision.Ref, Option: "advance", Words: message, Method: "merge", Against: c.Decision.Against.Token}, "Simon")
	if err != nil {
		t.Fatalf("advance answer with merge: %v", err)
	}
	if card.Stage != string(domain.StageDone) {
		t.Fatalf("card after merge answer = %+v, want done", card)
	}
	if parents := strings.Fields(gitOut(t, root, "log", "-1", "--format=%P")); len(parents) != 2 || parents[0] != before {
		t.Fatalf("main tip parents = %v, want a merge commit on %s", parents, before)
	}
	if got := gitOut(t, root, "log", "-1", "--format=%B"); got != message {
		t.Errorf("merge commit message = %q, want %q", got, message)
	}
}

// TestWebSquashInPlaceRefusesMergeMethod: squash in place is not a landing
// and offers no method; a merge request on it is refused with the base unmoved.
func TestWebSquashInPlaceRefusesMergeMethod(t *testing.T) {
	m, root, _ := mergeFixture(t)
	b := bridgeOver(t, m)
	before := gitOut(t, root, "rev-parse", "HEAD")

	_, err := b.Action(context.Background(), "FD-001", "squash", webapi.ActionRequest{Message: "FD-001: x", Method: "merge", Against: pinnedAgainst(t, b)}, "Simon")
	if we, ok := IsWebError(err); !ok || !strings.Contains(we.Text, "merge commit") {
		t.Fatalf("err = %v, want the merge-commit refusal", err)
	}
	if got := gitOut(t, root, "rev-parse", "HEAD"); got != before {
		t.Errorf("main HEAD moved by a refused request: %s -> %s", before, got)
	}
}

// TestWebLandingRejectsUnknownMethod: a method that is neither squash nor
// merge is a bad request, refused before the card is touched.
func TestWebLandingRejectsUnknownMethod(t *testing.T) {
	m, root, _ := mergeFixture(t)
	b := bridgeOver(t, m)
	before := gitOut(t, root, "rev-parse", "HEAD")

	_, err := b.Action(context.Background(), "FD-001", "merge", webapi.ActionRequest{Message: "FD-001: x", Method: "rebase", Against: pinnedAgainst(t, b)}, "Simon")
	if we, ok := IsWebError(err); !ok || we.Code != WebBadRequest {
		t.Fatalf("err = %v, want a bad request", err)
	}
	if got := gitOut(t, root, "rev-parse", "HEAD"); got != before {
		t.Errorf("main HEAD moved by a refused request: %s -> %s", before, got)
	}
}

// TestWebAnswerRejectsUnknownMethod: the same validation on an answer, which
// names a decision; a malformed method is a bad request even with no
// decision pinned.
func TestWebAnswerRejectsUnknownMethod(t *testing.T) {
	m, _, _ := mergeFixture(t)
	b := bridgeOver(t, m)

	_, err := b.Answer(context.Background(), "FD-001", webapi.AnswerRequest{Ref: "gate:FD-001:verify", Option: "advance", Method: "rebase"}, "Simon")
	if we, ok := IsWebError(err); !ok || we.Code != WebBadRequest {
		t.Fatalf("err = %v, want a bad request", err)
	}
}

// TestWebLandingRefusesMergeForGoalCard: a dialog that offers only squash
// refuses a merge-commit request before any git mutation.
func TestWebLandingRefusesMergeForGoalCard(t *testing.T) {
	var landed bool
	d := newCommitMsgDialog(domain.Feature{ID: "FD-002", Slug: "x", GoalID: "GL-001"}, func(_ string, _ domain.LandMethod) tea.Cmd {
		landed = true
		return nil
	}, nil)
	res := d.webAnswer(nil, &webInput{land: true, message: "FD-002: land it", method: domain.LandMerge})
	if res.refused == "" {
		t.Fatal("a card in a goal accepted a merge-commit landing")
	}
	if landed {
		t.Fatal("the refused landing still submitted")
	}
}

// TestWebLandingWithMergeMethodSetsDialog: an offered merge-commit request
// reaches the dialog's submit with the method, and an absent method is squash.
func TestWebLandingWithMergeMethodSetsDialog(t *testing.T) {
	var got domain.LandMethod
	mk := func() *commitMsgDialog {
		return newCommitMsgDialog(domain.Feature{ID: "FD-001", Slug: "x"}, func(_ string, method domain.LandMethod) tea.Cmd {
			got = method
			return nil
		}, nil)
	}
	d := mk()
	d.webAnswer(nil, &webInput{land: true, message: "FD-001: land it", method: domain.LandMerge})
	if got != domain.LandMerge {
		t.Errorf("merge request submitted %q, want merge", got)
	}
	d = mk()
	d.webAnswer(nil, &webInput{land: true, message: "FD-001: land it"})
	if got != domain.LandSquash {
		t.Errorf("request without a method submitted %q, want squash", got)
	}
}

// TestWebLandMethodsOfferedOnlyWhereChosen: the page is offered the choice
// on a card that may keep its commits, and on no other.
func TestWebLandMethodsOfferedOnlyWhereChosen(t *testing.T) {
	if got := webLandMethods(domain.Feature{ID: "FD-001"}); strings.Join(got, ",") != "squash,merge" {
		t.Errorf("plain card methods = %v, want squash,merge", got)
	}
	if got := webLandMethods(domain.Feature{ID: "FD-002", GoalID: "GL-001"}); got != nil {
		t.Errorf("card in a goal methods = %v, want none", got)
	}
	if got := webLandMethods(domain.Feature{ID: "GL-001", Kind: domain.KindGoal}); got != nil {
		t.Errorf("goal methods = %v, want none", got)
	}
}

// TestWebLandingMergeWithoutMessage: a merge sent with no words lands at
// once with git's own merge message. It is never told to wait on a draft or
// to write a message, and the branch tip becomes the merge's second parent.
func TestWebLandingMergeWithoutMessage(t *testing.T) {
	m, root, _ := mergeFixture(t)
	b := bridgeOver(t, m)
	ctx := context.Background()
	before := gitOut(t, root, "rev-parse", "HEAD")

	card, err := b.Action(ctx, "FD-001", "merge", webapi.ActionRequest{Method: "merge", Against: pinnedAgainst(t, b)}, "Simon")
	if err != nil {
		t.Fatalf("merge action without a message: %v", err)
	}
	if card == nil || card.Stage != string(domain.StageDone) {
		t.Fatalf("card after message-less merge = %+v, want done", card)
	}
	if parents := strings.Fields(gitOut(t, root, "log", "-1", "--format=%P")); len(parents) != 2 || parents[0] != before {
		t.Fatalf("main tip parents = %v, want a merge commit on %s", parents, before)
	}
	if got := gitOut(t, root, "log", "-1", "--format=%s"); !strings.HasPrefix(got, "Merge branch '") {
		t.Errorf("merge commit subject = %q, want git's merge message", got)
	}
}

// TestWebLandingMergeNeverWaitsOnDraft pins the answer itself: a merge with
// no words is not held for a draft (wait) and never asks for a message
// (needs), while a squash with no words still does.
func TestWebLandingMergeNeverWaitsOnDraft(t *testing.T) {
	var submitted []string
	mk := func() *commitMsgDialog {
		return newCommitMsgDialog(domain.Feature{ID: "FD-001", Slug: "x"}, func(msg string, _ domain.LandMethod) tea.Cmd {
			submitted = append(submitted, msg)
			return nil
		}, nil)
	}
	d := mk()
	d.drafting = true
	ans := d.webAnswer(nil, &webInput{land: true, method: domain.LandMerge})
	if ans.wait || ans.needs != "" {
		t.Fatalf("merge without words: wait=%v needs=%q, want it to land", ans.wait, ans.needs)
	}
	if len(submitted) != 1 || submitted[0] != "" {
		t.Fatalf("merge without words submitted %q, want one empty-message landing", submitted)
	}

	d = mk()
	ans = d.webAnswer(nil, &webInput{land: true})
	if ans.needs == "" {
		t.Fatal("squash without words did not ask for a message")
	}
}
