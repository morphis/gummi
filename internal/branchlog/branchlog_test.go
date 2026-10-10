package branchlog

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/worktree"
)

func TestCheckpointSubjects(t *testing.T) {
	for s, want := range map[string]bool{
		"FD-009: implement checkpoint":      true,
		"FF-012: turn checkpoint":           true,
		"BG-001: final checkpoint":          true,
		"FD-004: dropped by its goal":       true,
		"final checkpoint":                  true,
		"feat(ui): add a log tab":           false,
		"FD-009: fix the checkpoint parser": false,
		"fix: checkpoint":                   false,
	} {
		if got := IsCheckpoint(s); got != want {
			t.Errorf("IsCheckpoint(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestRowsFlagAttribution(t *testing.T) {
	rows := Rows([]worktree.LogEntry{{SHA: "aaaaaaa1", Subject: "feat: x", Body: "Co-Authored-By: Claude <a@b>"}, {SHA: "bbbbbbb2", Subject: "feat: y"}})
	if rows[0].Warning == "" || rows[1].Warning != "" {
		t.Errorf("warnings = %q, %q", rows[0].Warning, rows[1].Warning)
	}
}

func TestRefusal(t *testing.T) {
	ok := domain.Feature{ID: "FD-001", Kind: domain.KindFeature, Stage: domain.StageImplement}
	if why := Refusal(ok, State{}); why != "" {
		t.Errorf("a plain card in implement is rewritable, got %q", why)
	}
	adopted := ok
	adopted.BranchScheme = domain.BranchSchemeAdopted
	adopted.Branch = "someone/else"
	for name, tc := range map[string]struct {
		f domain.Feature
		s State
	}{
		"todo":     {domain.Feature{ID: "FD-002", Kind: domain.KindFeature, Stage: domain.StageTodo}, State{}},
		"research": {domain.Feature{ID: "RS-001", Kind: domain.KindResearch, Stage: domain.StagePlan}, State{}},
		"goal":     {domain.Feature{ID: "GL-001", Kind: domain.KindGoal, Stage: domain.StageImplement}, State{}},
		"adopted":  {adopted, State{}},
		"landed":   {ok, State{Landed: true}},
		"busy":     {ok, State{Busy: true}},
	} {
		if Refusal(tc.f, tc.s) == "" {
			t.Errorf("%s: want a refusal", name)
		}
	}
	// a research card never gets a branch: not "yet"
	if why := Refusal(domain.Feature{ID: "RS-001", Kind: domain.KindResearch, Stage: domain.StagePlan}, State{}); strings.Contains(why, "yet") || !strings.Contains(why, "never") {
		t.Errorf("research refusal = %q", why)
	}
}

func TestPlanGroups(t *testing.T) {
	rows := []Row{{SHA: "a", Subject: "one"}, {SHA: "b", Subject: "two", Body: "why"}, {SHA: "c", Subject: "three"}}
	g := PlanGroups(rows, map[string]bool{"b": true}, nil)
	if len(g) != 2 || len(g[0].Commits) != 2 || g[0].Message != "one" || len(g[1].Commits) != 1 || g[1].Message != "" {
		t.Errorf("groups = %+v", g)
	}
	// the first commit cannot be folded; c folds into b's group, which
	// takes b's message; a's own edit stays a's
	g = PlanGroups(rows, map[string]bool{"a": true, "c": true}, map[string]string{"a": " new "})
	if len(g) != 2 || g[0].Message != "new" || len(g[1].Commits) != 2 || g[1].Message != "two\n\nwhy" {
		t.Errorf("groups = %+v", g)
	}
}

func TestSignable(t *testing.T) {
	mixed := []Row{{SHA: "a", Signed: true}, {SHA: "b"}}
	for _, tc := range []struct {
		name string
		l    Log
		want bool
	}{
		{"unsigned commits where git signs", Log{Rows: mixed, Signing: true}, true},
		{"nothing signs here", Log{Rows: mixed}, false},
		{"all signed", Log{Rows: mixed[:1], Signing: true}, false},
		{"not the card's to rewrite", Log{Rows: mixed, Signing: true, Why: "an agent is working"}, false},
	} {
		if got := tc.l.Signable(); got != tc.want {
			t.Errorf("%s: Signable = %v, want %v", tc.name, got, tc.want)
		}
	}
}
