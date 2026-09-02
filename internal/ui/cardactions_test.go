package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/ui/theme"
)

// idsOf flattens an action list to its id sequence for compact table
// expectations, mirroring nextsteps_test.go's keysOf.
func idsOf(acts []cardAction) string {
	ids := make([]string, len(acts))
	for i, a := range acts {
		ids[i] = a.id
	}
	return strings.Join(ids, " ")
}

// cardRow builds a minimal featureRow for a given kind/stage/landed/
// worktree combination — the bits cardActionsFor actually reads.
func cardRow(kind domain.Kind, stage domain.Stage, landed, hasWorktree bool) featureRow {
	return featureRow{
		F:           domain.Feature{Kind: kind, Stage: stage},
		Landed:      landed,
		HasWorktree: hasWorktree,
	}
}

func TestCardActionsForOrdering(t *testing.T) {
	// StageVerify with a failed check: nextActions ranks v, enter, b —
	// none of them canonically adjacent in the fixed action table, so a
	// literal reordering only happens if the "seed the ordering from
	// nextActions" contract actually holds.
	in := nextInput{
		stage:       domain.StageVerify,
		kind:        domain.KindFeature,
		attn:        attnGate,
		failedCheck: "lint",
	}
	r := cardRow(domain.KindFeature, domain.StageVerify, false, true)

	acts := cardActionsFor(in, r)
	got := idsOf(acts)
	// The promoted tier is exactly the three ranked entries in
	// nextActions' order; the fold holds the rest in board order. Inbox is
	// in there because attn is set: it is the surface the gate
	// recommendation (keyed i) lands on.
	want := "verify run bounce " +
		"deps spec diff advance envelope gate ask inbox attach rebase merge prlink duplicate delete"
	if got != want {
		t.Fatalf("order mismatch:\n got  %q\n want %q", got, want)
	}
	if gotFolded := idsOf(foldedOnly(acts, true)); gotFolded != "deps spec diff advance envelope gate ask inbox attach rebase merge prlink duplicate delete" {
		t.Fatalf("unexpected folded tail: %q", gotFolded)
	}
}

// foldedOnly filters an action list to one tier, so a test can assert
// what the card shows without expanding separately from what it holds.
func foldedOnly(acts []cardAction, folded bool) []cardAction {
	var out []cardAction
	for _, a := range acts {
		if a.folded == folded {
			out = append(out, a)
		}
	}
	return out
}

func TestCardActionsForFoldsTheTail(t *testing.T) {
	// The whole point of the fold: a mid-flow card offers a dozen-odd
	// actions, and only the handful nextActions ranks earn a row before
	// you ask for the rest.
	in := nextInput{stage: domain.StageVerify, kind: domain.KindFeature, attn: attnGate, verdict: verdictPass}
	r := cardRow(domain.KindFeature, domain.StageVerify, false, true)

	acts := cardActionsFor(in, r)
	promoted := foldedOnly(acts, false)
	if got, want := idsOf(promoted), "advance diff bounce"; got != want {
		t.Fatalf("promoted tier:\n got  %q\n want %q", got, want)
	}
	if n := len(foldedOnly(acts, true)); n < foldMin {
		t.Fatalf("expected a folded tail worth folding, got %d", n)
	}

	// and it is a fold, not a filter: every action is still in the list,
	// still carrying its accelerator, one enter away.
	l := newCardActionList(acts)
	if got, want := l.Len(), len(promoted)+1; got != want {
		t.Fatalf("collapsed list = %d rows, want %d (the promoted tier plus the fold row)", got, want)
	}
	l.expanded = true
	if got, want := l.Len(), len(acts)+1; got != want {
		t.Fatalf("expanded list = %d rows, want %d (every action plus the fold row)", got, want)
	}
}

func TestCardActionsForShortTailStaysUnfolded(t *testing.T) {
	// A todo card offers little enough that a fold row would cost a line
	// to save two: below foldMin the tail is shown inline instead.
	in := nextInput{stage: domain.StageTodo, kind: domain.KindFeature}
	r := cardRow(domain.KindFeature, domain.StageTodo, false, false)

	acts := cardActionsFor(in, r)
	if n := len(foldedOnly(acts, true)); n != 0 && n < foldMin {
		t.Fatalf("tail of %d folded, but foldMin is %d", n, foldMin)
	}
	l := newCardActionList(acts)
	if len(foldedOnly(acts, true)) == 0 && l.Len() != len(acts) {
		t.Fatalf("unfolded list grew a fold row: %d rows for %d actions", l.Len(), len(acts))
	}
}

func TestCardActionsForDangerLast(t *testing.T) {
	// A landed feature: nextActions recommends exactly "clean up" (key
	// c), which is danger:true — it must still sort after every
	// non-danger action, not jump to the front on its recommendation.
	in := nextInput{stage: domain.StageVerify, kind: domain.KindFeature, landed: true}
	r := cardRow(domain.KindFeature, domain.StageVerify, true, true)

	acts := cardActionsFor(in, r)
	if len(acts) == 0 {
		t.Fatal("expected at least one action on a landed card")
	}
	last := acts[len(acts)-1]
	if last.id != "delete" && last.id != "clean" {
		t.Fatalf("expected a danger action last, got %q", last.id)
	}
	// danger-last is an invariant of each tier, not of the concatenation:
	// "clean up" is the recommendation here, so it is promoted and sorts
	// after the promoted non-danger rows but before the whole folded tail.
	dangerSeen := false
	for _, tier := range [][]cardAction{foldedOnly(acts, false), foldedOnly(acts, true)} {
		tierDanger := false
		for _, a := range tier {
			if a.danger {
				dangerSeen, tierDanger = true, true
				continue
			}
			if tierDanger {
				t.Fatalf("non-danger action %q sorted after a danger action in its tier", a.id)
			}
		}
	}
	if !dangerSeen {
		t.Fatal("expected at least one danger action on a landed card (clean, delete)")
	}
	if a := foldedOnly(acts, false)[len(foldedOnly(acts, false))-1]; a.id != "clean" {
		t.Fatalf("expected the recommended clean-up promoted and last in its tier, got %q", a.id)
	}
}

func TestCardActionsForResearchExclusions(t *testing.T) {
	// Research cards carry no branch: diff/rebase/merge/clean must never
	// appear, matching keymap.go's status-bar/help filter — "not shown"
	// and "not available" are the same fact here.
	stages := []domain.Stage{
		domain.StageTodo, domain.StageInvestigate, domain.StageShape,
		domain.StageReview, domain.StageVerify, domain.StageDone,
	}
	excluded := []string{"diff", "rebase", "merge", "clean"}
	for _, stage := range stages {
		for _, landed := range []bool{false, true} {
			in := nextInput{stage: stage, kind: domain.KindResearch, landed: landed}
			// HasWorktree deliberately set true to prove the exclusion
			// comes from the kind (via workflow.NeedsWorktree), not from
			// an incidentally-false worktree flag.
			r := cardRow(domain.KindResearch, stage, landed, true)
			acts := cardActionsFor(in, r)
			for _, id := range excluded {
				for _, a := range acts {
					if a.id == id {
						t.Fatalf("research card in %s (landed=%v) offered %q, want it excluded", stage, landed, id)
					}
				}
			}
		}
	}
}

func TestCardActionsForLandedOffersCleanup(t *testing.T) {
	in := nextInput{stage: domain.StageVerify, kind: domain.KindFeature, landed: true}
	r := cardRow(domain.KindFeature, domain.StageVerify, true, true)
	acts := cardActionsFor(in, r)

	var clean, merge *cardAction
	for i := range acts {
		switch acts[i].id {
		case "clean":
			clean = &acts[i]
		case "merge":
			merge = &acts[i]
		}
	}
	if clean == nil {
		t.Fatal("expected a clean action on a landed feature card")
	}
	if !clean.danger {
		t.Error("clean should be marked danger")
	}
	if merge != nil {
		t.Error("merge should not be offered once a card has landed")
	}
}

func TestCardActionsForNoWorktreeExcludesDiff(t *testing.T) {
	in := nextInput{stage: domain.StageImplement, kind: domain.KindFeature}
	r := cardRow(domain.KindFeature, domain.StageImplement, false, false)
	acts := cardActionsFor(in, r)
	for _, a := range acts {
		if a.id == "attach" {
			t.Error("attach should not be offered without a worktree")
		}
	}
	// diff is gated on stage (workflow.NeedsWorktree), which is
	// independent of featureRow.HasWorktree — todo/interactive stages
	// exclude it regardless of worktree presence.
	in2 := nextInput{stage: domain.StageTodo, kind: domain.KindFeature}
	r2 := cardRow(domain.KindFeature, domain.StageTodo, false, false)
	acts2 := cardActionsFor(in2, r2)
	for _, a := range acts2 {
		if a.id == "diff" {
			t.Error("diff should not be offered before a worktree stage exists")
		}
	}
}

func TestCardActionsForEmpty(t *testing.T) {
	l := newCardActionList(nil)
	if l.Len() != 0 {
		t.Fatalf("Len() = %d, want 0", l.Len())
	}
	if _, ok := l.Selected(); ok {
		t.Error("Selected() on an empty list should return false")
	}
	l.Move(1) // must not panic
	l.Move(-1)
	if got := l.View(theme.New(theme.GummiDark()), 40, 0, true); got != "" {
		t.Errorf("View() on an empty list = %q, want \"\"", got)
	}
}

func TestCardActionListMoveClamps(t *testing.T) {
	l := newCardActionList([]cardAction{
		{id: "a", key: "1", label: "one"},
		{id: "b", key: "2", label: "two"},
		{id: "c", key: "3", label: "three"},
	})
	l.Move(-5)
	if l.cursor != 0 {
		t.Fatalf("Move(-5) from start: cursor = %d, want 0", l.cursor)
	}
	l.Move(5)
	if l.cursor != 2 {
		t.Fatalf("Move(5): cursor = %d, want 2", l.cursor)
	}
	l.Move(5) // already at the end — must not overshoot
	if l.cursor != 2 {
		t.Fatalf("Move(5) past the end: cursor = %d, want 2", l.cursor)
	}
	l.Move(-1)
	if l.cursor != 1 {
		t.Fatalf("Move(-1): cursor = %d, want 1", l.cursor)
	}
}

func TestCardActionListSelected(t *testing.T) {
	l := newCardActionList([]cardAction{
		{id: "a", key: "1", label: "one"},
		{id: "b", key: "2", label: "two"},
	})
	l.Move(1)
	got, ok := l.Selected()
	if !ok || got.id != "b" {
		t.Fatalf("Selected() = %+v, %v; want id=b, true", got, ok)
	}
}

func TestCardActionListViewContainsKeyAndLabel(t *testing.T) {
	s := theme.New(theme.GummiDark())
	l := newCardActionList([]cardAction{
		{id: "run", key: "enter", label: "run", why: "start the stage"},
		{id: "attach", key: "a", label: "attach", why: "raw-attach the agent CLI"},
	})
	l.Move(1) // cursor -> attach
	out := ansi.Strip(l.View(s, 40, 0, true))
	lines := strings.Split(out, "\n")
	if len(lines) < 3 {
		t.Fatalf("expected at least 3 lines (2 rows + explainer), got %d: %q", len(lines), out)
	}
	if !strings.Contains(lines[0], "run") || !strings.Contains(lines[0], "enter") {
		t.Errorf("row 0 = %q, want it to contain label %q and key %q", lines[0], "run", "enter")
	}
	if !strings.Contains(lines[1], "attach") || !strings.Contains(lines[1], "a") {
		t.Errorf("row 1 = %q, want it to contain label %q and key %q", lines[1], "attach", "a")
	}
	// the marker belongs on the cursor's row (1), not the first row.
	if strings.Contains(lines[0], "▸") {
		t.Errorf("row 0 = %q, should not carry the cursor marker", lines[0])
	}
	if !strings.Contains(lines[1], "▸") {
		t.Errorf("row 1 = %q, should carry the cursor marker", lines[1])
	}
	// the trailing explainer describes the focused (cursor) row.
	if !strings.Contains(lines[2], "raw-attach the agent CLI") {
		t.Errorf("explainer line = %q, want it to describe the focused row", lines[2])
	}
}

func TestCardActionListViewAccountsForEveryRow(t *testing.T) {
	tests := []struct {
		name string
		in   nextInput
		row  featureRow
	}{
		{
			name: "mid-run",
			in:   nextInput{stage: domain.StageImplement, kind: domain.KindFeature, sess: engine.StateRunning},
			row:  cardRow(domain.KindFeature, domain.StageImplement, false, true),
		},
		{
			name: "gate",
			in:   nextInput{stage: domain.StagePlan, kind: domain.KindFeature, attn: attnGate},
			row:  cardRow(domain.KindFeature, domain.StagePlan, false, true),
		},
	}
	for _, tt := range tests {
		for _, width := range []int{40, 100} {
			t.Run(fmt.Sprintf("%s/%d", tt.name, width), func(t *testing.T) {
				l := newCardActionList(cardActionsFor(tt.in, tt.row))
				newCardActionsDialog("FD-042", l, func(int, bool) {}, func(cardAction) tea.Cmd { return nil })
				assertRenderedActionCount(t, l, width, 8)
			})
		}
	}
}

func assertRenderedActionCount(t *testing.T, l *cardActionList, width, maxRows int) {
	t.Helper()
	plain := ansi.Strip(l.View(theme.New(theme.GummiDark()), width, maxRows, true))
	shown, hidden := 0, 0
	for _, line := range strings.Split(plain, "\n") {
		switch {
		case strings.HasPrefix(line, sepPrefix), strings.HasPrefix(line, "  ↳"):
			continue
		case strings.HasPrefix(line, "  …"):
			if _, err := fmt.Sscanf(line, "  …%d more", &hidden); err != nil {
				t.Fatalf("parse hidden count from %q: %v", line, err)
			}
		default:
			shown++
		}
	}
	if got, want := shown+hidden, l.Len(); got != want {
		t.Fatalf("rendered %d rows + stated %d hidden = %d, want Len() %d\n%s", shown, hidden, got, want, plain)
	}
}

func TestCardActionsDialogWideGolden(t *testing.T) {
	in := nextInput{stage: domain.StageImplement, kind: domain.KindFeature, sess: engine.StateRunning}
	l := newCardActionList(cardActionsFor(in, cardRow(domain.KindFeature, domain.StageImplement, false, true)))
	d := newCardActionsDialog("FD-042", l, func(int, bool) {}, func(cardAction) tea.Cmd { return nil })
	golden.RequireEqual(t, []byte(d.View(theme.New(theme.GummiDark()), 100, 16)))
}

func TestCardActionsDialogNarrowGolden(t *testing.T) {
	in := nextInput{stage: domain.StagePlan, kind: domain.KindFeature, attn: attnGate}
	l := newCardActionList(cardActionsFor(in, cardRow(domain.KindFeature, domain.StagePlan, false, true)))
	d := newCardActionsDialog("FD-042", l, func(int, bool) {}, func(cardAction) tea.Cmd { return nil })
	golden.RequireEqual(t, []byte(d.View(theme.New(theme.GummiDark()), 40, 16)))
}

// TestCardActionListViewUnfocusedKeepsMarker: the trailing explainer
// describes the cursor row whether or not the list holds focus, so
// hiding the marker while unfocused left that explanation pointing at
// nothing. The marker stays and goes faint instead.
func TestCardActionListViewUnfocusedKeepsMarker(t *testing.T) {
	s := theme.New(theme.GummiDark())
	l := newCardActionList([]cardAction{
		{id: "run", key: "enter", label: "run", why: "start the stage"},
		{id: "attach", key: "a", label: "attach", why: "raw-attach the agent CLI"},
	})
	out := ansi.Strip(l.View(s, 40, 0, false))
	lines := strings.Split(out, "\n")
	if !strings.HasPrefix(lines[0], "\u25b8 ") {
		t.Errorf("cursor row = %q, want it to keep the marker while unfocused", lines[0])
	}
	if strings.HasPrefix(lines[1], "\u25b8 ") {
		t.Errorf("non-cursor row = %q, want no marker", lines[1])
	}
	if !strings.Contains(lines[2], "start the stage") {
		t.Errorf("explainer = %q, want it to describe the marked row", lines[2])
	}
}

func TestCardActionsForAddPlanGate(t *testing.T) {
	f := domain.Feature{Kind: domain.KindFeature, Stage: domain.StageSpec}
	f.Skip.Plan = true
	r := featureRow{F: f}
	in := nextInput{stage: domain.StageSpec, kind: domain.KindFeature, quick: true}
	acts := cardActionsFor(in, r)
	found := false
	for _, a := range acts {
		if a.id == "addplan" {
			found = true
		}
	}
	if !found {
		t.Error("expected addplan on a spec-stage feature with Skip.Plan set")
	}

	// a bug never routes through plan at all — addplan must not appear
	// even with the flag set (routeViaPlan itself refuses on kind).
	fb := domain.Feature{Kind: domain.KindBug, Stage: domain.StageTriage}
	fb.Skip.Plan = true
	rb := featureRow{F: fb}
	inb := nextInput{stage: domain.StageTriage, kind: domain.KindBug}
	actsb := cardActionsFor(inb, rb)
	for _, a := range actsb {
		if a.id == "addplan" {
			t.Error("addplan should never appear on a bug")
		}
	}

	// past spec, the plan stage is already behind the feature.
	fp := domain.Feature{Kind: domain.KindFeature, Stage: domain.StageImplement}
	fp.Skip.Plan = true
	rp := featureRow{F: fp}
	inp := nextInput{stage: domain.StageImplement, kind: domain.KindFeature}
	actsp := cardActionsFor(inp, rp)
	for _, a := range actsp {
		if a.id == "addplan" {
			t.Error("addplan should not appear once a feature is past spec")
		}
	}
}

func TestCardActionsForSessionState(t *testing.T) {
	cases := []struct {
		name    string
		sess    engine.SessionState
		hasPaus bool // pause id expected
		hasDeps bool // deps id expected
	}{
		{"no session offers deps not pause", "", false, true},
		{"queued session offers pause not deps", engine.StateQueued, true, false},
		{"running session offers pause not deps", engine.StateRunning, true, false},
		{"paused session offers pause not deps", engine.StatePaused, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := nextInput{stage: domain.StageImplement, kind: domain.KindFeature, sess: c.sess}
			r := cardRow(domain.KindFeature, domain.StageImplement, false, true)
			acts := cardActionsFor(in, r)
			var gotPause, gotDeps bool
			for _, a := range acts {
				if a.id == "pause" {
					gotPause = true
				}
				if a.id == "deps" {
					gotDeps = true
				}
			}
			if gotPause != c.hasPaus {
				t.Errorf("pause present = %v, want %v", gotPause, c.hasPaus)
			}
			if gotDeps != c.hasDeps {
				t.Errorf("deps present = %v, want %v", gotDeps, c.hasDeps)
			}
		})
	}
}

func TestCardActionsForBounceGate(t *testing.T) {
	stages := []domain.Stage{
		domain.StageTodo, domain.StageBrainstorm, domain.StageSpec, domain.StagePlan,
		domain.StageImplement, domain.StageReview, domain.StageVerify, domain.StageDone,
	}
	for _, stage := range stages {
		in := nextInput{stage: stage, kind: domain.KindFeature}
		r := cardRow(domain.KindFeature, stage, false, true)
		acts := cardActionsFor(in, r)
		want := stage == domain.StageReview || stage == domain.StageVerify
		got := false
		for _, a := range acts {
			if a.id == "bounce" {
				got = true
			}
		}
		if got != want {
			t.Errorf("stage %s: bounce present = %v, want %v", stage, got, want)
		}
	}
}

func TestCardActionsForDoneStageExcludesAdvanceExceptResearch(t *testing.T) {
	f := cardRow(domain.KindFeature, domain.StageDone, false, false)
	in := nextInput{stage: domain.StageDone, kind: domain.KindFeature}
	for _, a := range cardActionsFor(in, f) {
		if a.id == "advance" {
			t.Error("a done feature should not offer advance")
		}
	}

	rs := cardRow(domain.KindResearch, domain.StageDone, false, false)
	inr := nextInput{stage: domain.StageDone, kind: domain.KindResearch}
	found := false
	for _, a := range cardActionsFor(inr, rs) {
		if a.id == "advance" {
			found = true
			if a.label != "decompose" {
				t.Errorf("a done research card's advance label = %q, want decompose", a.label)
			}
		}
	}
	if !found {
		t.Error("a done research card should still offer advance (decompose)")
	}
}

// TestCardActionsForPRLinkUnlinkPullGating spans DrivenAbroad, Landed,
// HasWorktree, and PullRequest.Empty() combinations, asserting the offered
// set matches the Chosen approach table exactly: prlink only before a link
// exists, on a card that could otherwise merge locally; prunlink and
// prpull only once linked; and none of the three on a card another gummi
// process is driving.
func TestCardActionsForPRLinkUnlinkPullGating(t *testing.T) {
	linked := domain.PullRequestRef{Repo: "o/r", Number: 1, URL: "https://github.com/o/r/pull/1"}

	has := func(acts []cardAction, id string) bool {
		for _, a := range acts {
			if a.id == id {
				return true
			}
		}
		return false
	}

	cases := []struct {
		name                           string
		landed, hasWT, linked, abroad  bool
		wantLink, wantUnlink, wantPull bool
	}{
		{"unlinked, worktree, not landed: link only", false, true, false, false, true, false, false},
		{"linked, worktree, not landed: unlink+pull, not link", false, true, true, false, false, true, true},
		{"linked, landed: unlink+pull still offered (m is what refuses, not these)", true, true, true, false, false, true, true},
		{"unlinked, landed: none — prlink needs !Landed", true, true, false, false, false, false, false},
		{"unlinked, no worktree row: none — prlink also needs r.HasWorktree", false, false, false, false, false, false, false},
		// prpull's own gate is `!Empty() && needsWT` — needsWT comes from
		// workflow.NeedsWorktree(kind, stage), not r.HasWorktree (whether a
		// worktree actually got created), so it stays offered here.
		{"linked, no worktree row: unlink+pull — prpull doesn't gate on r.HasWorktree", false, false, true, false, false, true, true},
		{"driven abroad, unlinked, worktree: none of the three", false, true, false, true, false, false, false},
		{"driven abroad, linked, worktree: none of the three", false, true, true, true, false, false, false},
	}
	for _, c := range cases {
		r := cardRow(domain.KindFeature, domain.StageImplement, c.landed, c.hasWT)
		if c.linked {
			r.F.PullRequest = linked
		}
		r.DrivenAbroad = c.abroad
		in := nextInput{stage: domain.StageImplement, kind: domain.KindFeature, landed: c.landed, pullRequest: r.F.PullRequest}

		acts := cardActionsFor(in, r)
		if got := has(acts, "prlink"); got != c.wantLink {
			t.Errorf("%s: prlink offered = %v, want %v (%s)", c.name, got, c.wantLink, idsOf(acts))
		}
		if got := has(acts, "prunlink"); got != c.wantUnlink {
			t.Errorf("%s: prunlink offered = %v, want %v (%s)", c.name, got, c.wantUnlink, idsOf(acts))
		}
		if got := has(acts, "prpull"); got != c.wantPull {
			t.Errorf("%s: prpull offered = %v, want %v (%s)", c.name, got, c.wantPull, idsOf(acts))
		}
	}
}

// TestCardActionsForPRActionsExcludedFromForeignSafe proves the omission
// from foreignSafeActions (not a special-cased id check) is what withholds
// all three on a driven-abroad card — each writes to the card, so a card
// another gummi process is driving withholds them like every other write
// verb.
func TestCardActionsForPRActionsExcludedFromForeignSafe(t *testing.T) {
	for _, id := range []string{"prlink", "prunlink", "prpull"} {
		if foreignSafeActions[id] {
			t.Errorf("%q must not be in foreignSafeActions — it writes to the card", id)
		}
	}
}
