package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/ui/theme"
)

// A clean verify asks how the work leaves gummi, and the branch's own
// name is in the row that keeps it — a reader about to go push something
// should not have to derive it.
func TestVerifyAnswerSetOffersHandOff(t *testing.T) {
	in := nextInput{
		stage: domain.StageVerify, kind: domain.KindFeature,
		attn: attnGate, verdict: verdictPass,
		base: "main", branch: "gummi/FD-042-json-export",
	}
	acts := stageActions(in)
	var keep *nextAction
	for i := range acts {
		if acts[i].id == "handoff" {
			keep = &acts[i]
		}
	}
	if keep == nil {
		t.Fatalf("a clean verify offers no hand-off: %+v", acts)
	}
	if acts[0].id != "advance" {
		t.Errorf("landing lost its place as the recommendation: %q leads", acts[0].id)
	}
	if !strings.Contains(keep.detail, "gummi/FD-042-json-export") {
		t.Errorf("the hand-off row does not name the branch it keeps: %q", keep.detail)
	}
	if keep.danger {
		t.Error("hand-off is an ending, not a destructive act — it must not wear the danger paint")
	}
}

// With a PR open the landing is already someone else's, so the hand-off
// row says the PR carries it rather than offering the reader a branch to
// go push.
func TestVerifyAnswerSetHandOffUnderAnOpenPR(t *testing.T) {
	in := nextInput{
		stage: domain.StageVerify, kind: domain.KindFeature,
		attn: attnGate, verdict: verdictPass,
		base: "main", branch: "gummi/FD-042-json-export",
		pullRequest: domain.PullRequestRef{Repo: "o/r", Number: 42, URL: "https://github.com/o/r/pull/42"},
	}
	for _, a := range stageActions(in) {
		if a.id == "handoff" {
			if !strings.Contains(a.detail, "PR") {
				t.Errorf("the linked card's hand-off row does not mention the PR: %q", a.detail)
			}
			return
		}
	}
	t.Fatal("a linked, verified card offers no hand-off — the only other row is advisory")
}

// A verify that did not pass is not at an ending: the answers there are
// still fix-it or overrule-it, so neither the hand-off row nor the
// link-a-PR route rides above the fold.
func TestFailedVerifyOffersNoEnding(t *testing.T) {
	for _, in := range []nextInput{
		{stage: domain.StageVerify, kind: domain.KindFeature, attn: attnGate, verdict: verdictFail, escalated: true, hasWorktree: true},
		{stage: domain.StageVerify, kind: domain.KindFeature, attn: attnGate, failedCheck: "unit", hasWorktree: true},
		{stage: domain.StageVerify, kind: domain.KindFeature, attn: attnGate, verdict: verdictBlocked, hasWorktree: true},
	} {
		for _, a := range nextActions(in) {
			if a.id == "handoff" || a.id == "prlink" {
				t.Errorf("verdict %v / check %q offers %q", in.verdict, in.failedCheck, a.id)
			}
		}
	}
}

// The confirm names what is kept before it names anything else: keeping
// the branch is the whole reason anyone reaches for this.
func TestHandOffDetailNamesWhatIsKept(t *testing.T) {
	f := domain.Feature{ID: "FD-042", Slug: "json-export", Kind: domain.KindFeature}
	d := handOffDetail(f, "main", nil)
	for _, want := range []string{f.BranchName(), f.WorktreePath(), "kept", "nothing lands"} {
		if !strings.Contains(d, want) {
			t.Errorf("confirm detail does not mention %q:\n%s", want, d)
		}
	}
	if strings.Contains(d, "depends on this") {
		t.Errorf("a card with no dependents warned about them:\n%s", d)
	}
}

// The one consequence nothing else on the board will say: a dependent
// unblocks on Stage == done, so it is free to start coding from a base
// branch that does not contain this work.
func TestHandOffDetailNamesDependents(t *testing.T) {
	f := domain.Feature{ID: "FD-042", Slug: "json-export", Kind: domain.KindFeature}
	one := handOffDetail(f, "main", []domain.FeatureID{"BG-051"})
	if !strings.Contains(one, "BG-051 depends on this and will start from main") {
		t.Errorf("single dependent not named in the singular:\n%s", one)
	}
	two := handOffDetail(f, "main", []domain.FeatureID{"BG-051", "FD-060"})
	if !strings.Contains(two, "BG-051, FD-060 depend on this") {
		t.Errorf("two dependents not named in the plural:\n%s", two)
	}
}

// A handed-off card and an abandoned one both read as "done, never
// landed"; the badge is the whole difference between them.
func TestBoardBadgesTheHandOff(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0.1.0-test")

	handed := row(42, "json export", domain.StageDone, "thrifty", true)
	handed.F.HandedOffAt = time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC)
	if line := m.cardLine(handed, 1, false, true, 100); !strings.Contains(line, "handed off") {
		t.Errorf("handed-off card line = %q, want the handed-off badge", line)
	}

	dropped := row(43, "abandoned spike", domain.StageDone, "thrifty", true)
	if line := m.cardLine(dropped, 1, false, true, 100); strings.Contains(line, "handed off") {
		t.Errorf("a card that simply stopped claims an ending: %q", line)
	}

	// landing is the other ending and keeps the badge it always had: a
	// card that reached main must never read as one that did not.
	landed := row(44, "landed work", domain.StageDone, "thrifty", true)
	landed.Landed = true
	landed.F.HandedOffAt = time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC)
	line := m.cardLine(landed, 1, false, true, 100)
	if !strings.Contains(line, "landed") || strings.Contains(line, "handed off") {
		t.Errorf("a landed card = %q, want the landed badge and not the hand-off one", line)
	}
}

// Hand-off is offered where the card has actually ended. `m` lands a
// branch, which is a thing you might do at any point; this closes the
// card, and there is nothing to close before the work is finished.
func TestHandOffActionOnlyAtVerify(t *testing.T) {
	for _, stage := range []domain.Stage{domain.StageTodo, domain.StagePlan, domain.StageImplement, domain.StageDone} {
		in := nextInput{stage: stage, kind: domain.KindFeature, hasWorktree: true}
		r := cardRow(domain.KindFeature, stage, false, true)
		for _, a := range cardActionsFor(in, r) {
			if a.id == "handoff" {
				t.Errorf("stage %s offers hand-off", stage)
			}
		}
	}
	in := nextInput{stage: domain.StageVerify, kind: domain.KindFeature, hasWorktree: true}
	r := cardRow(domain.KindFeature, domain.StageVerify, false, true)
	var found bool
	for _, a := range cardActionsFor(in, r) {
		if a.id == "handoff" {
			found = true
		}
	}
	if !found {
		t.Error("verify does not offer hand-off")
	}
}

// A research card carries no branch, so there is nothing to keep — the
// same filter that hides diff/rebase/merge/clean hides this.
func TestHandOffAbsentOnResearch(t *testing.T) {
	in := nextInput{stage: domain.StageVerify, kind: domain.KindResearch}
	r := cardRow(domain.KindResearch, domain.StageVerify, false, false)
	for _, a := range cardActionsFor(in, r) {
		if a.id == "handoff" {
			t.Error("a research card offers hand-off")
		}
	}
}

// The clean-up refusal on a handed-off card says what is actually true.
// "hasn't landed yet" reads as a wait, and nothing is coming: the branch
// was kept on purpose, and `c` would delete exactly the thing the reader
// chose to keep.
func TestCleanUpRefusalOnAHandedOffCard(t *testing.T) {
	m := populatedShell(100, 30)
	m.sel = 4 // the verify row, which carries a worktree
	m.rows[m.sel].F.Stage = domain.StageDone
	m.rows[m.sel].F.HandedOffAt = time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC)
	m.rows[m.sel].Landed = false

	m.boardVerb("c")
	if !strings.Contains(m.notice.text, "handed off, not landed") {
		t.Errorf("clean-up refusal = %q, want it to name the hand-off", m.notice.text)
	}
	if !strings.Contains(m.notice.text, m.rows[m.sel].F.BranchName()) {
		t.Errorf("clean-up refusal = %q, want it to name the branch it would delete", m.notice.text)
	}
}

// Hand-off ends a card that finished. A card mid-flight has nothing to
// end, and the refusal says which stage it is actually at rather than
// leaving the reader to guess why the key did nothing.
func TestHandOffKeyRefusesBeforeVerify(t *testing.T) {
	m := populatedShell(100, 30)
	m.sel = 1 // implement, with a worktree
	m.boardVerb("h")
	if !strings.Contains(m.notice.text, "hand-off ends a verified card") {
		t.Errorf("notice = %q, want the stage refusal", m.notice.text)
	}
	if !strings.Contains(m.notice.text, string(domain.StageImplement)) {
		t.Errorf("notice = %q, want it to name the card's actual stage", m.notice.text)
	}
}

// A research card carries no branch: the key refuses with the same
// sentence every other branch verb gives, not with the stage refusal.
func TestHandOffKeyRefusesResearch(t *testing.T) {
	m := populatedShell(100, 30)
	m.sel = 4
	m.rows[m.sel].F.Kind = domain.KindResearch
	m.boardVerb("h")
	if !strings.Contains(m.notice.text, "no branch") {
		t.Errorf("notice = %q, want the no-branch refusal", m.notice.text)
	}
}
