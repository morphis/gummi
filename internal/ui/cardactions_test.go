package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

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

	got := idsOf(cardActionsFor(in, r))
	want := "verify run bounce deps spec diff advance envelope attach rebase merge duplicate delete"
	if got != want {
		t.Fatalf("order mismatch:\n got  %q\n want %q", got, want)
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
	dangerSeen := false
	for _, a := range acts {
		if a.danger {
			dangerSeen = true
			continue
		}
		if dangerSeen {
			t.Fatalf("non-danger action %q sorted after a danger action", a.id)
		}
	}
	if !dangerSeen {
		t.Fatal("expected at least one danger action on a landed card (clean, delete)")
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
	if got := l.View(theme.New(theme.GummiDark()), 40, true); got != "" {
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
	out := ansi.Strip(l.View(s, 40, true))
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

func TestCardActionListViewUnfocusedHidesMarker(t *testing.T) {
	s := theme.New(theme.GummiDark())
	l := newCardActionList([]cardAction{
		{id: "run", key: "enter", label: "run", why: "start the stage"},
		{id: "attach", key: "a", label: "attach", why: "raw-attach the agent CLI"},
	})
	out := ansi.Strip(l.View(s, 40, false))
	if strings.Contains(out, "▸") {
		t.Errorf("unfocused View() should carry no cursor marker, got %q", out)
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
