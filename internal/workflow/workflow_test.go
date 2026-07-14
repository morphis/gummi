package workflow

import (
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

// edge is one (from, to) pair; the tests build the full expected legal
// set per kind + skip-flag combination and check every (from, to) triple
// against domain.Stages, so the tables are exhaustive across both graphs.
type edge struct{ from, to domain.Stage }

// --- feature workflow expectations ---

var featureAlways = []edge{
	{domain.StageTodo, domain.StageBrainstorm},
	{domain.StageBrainstorm, domain.StageSpec},
	{domain.StageSpec, domain.StagePlan},
	{domain.StagePlan, domain.StageImplement},
	{domain.StageImplement, domain.StageReview},
	{domain.StageReview, domain.StageVerify},
	{domain.StageVerify, domain.StageDone},
	{domain.StageReview, domain.StageImplement},
	{domain.StageVerify, domain.StageImplement},
}

func legalForFeature(skip domain.SkipFlags) map[edge]bool {
	m := map[edge]bool{}
	for _, e := range featureAlways {
		m[e] = true
	}
	if skip.Brainstorm {
		m[edge{domain.StageTodo, domain.StageSpec}] = true
	}
	if skip.Plan {
		m[edge{domain.StageSpec, domain.StageImplement}] = true
	}
	return m
}

// --- bug workflow expectations ---

var bugAlways = []edge{
	{domain.StageTodo, domain.StageTriage},
	{domain.StageTriage, domain.StageDiagnose},
	{domain.StageDiagnose, domain.StageFix},
	{domain.StageFix, domain.StageReview},
	{domain.StageReview, domain.StageVerify},
	{domain.StageVerify, domain.StageDone},
	{domain.StageReview, domain.StageFix},
	{domain.StageVerify, domain.StageFix},
}

func legalForBug(skip domain.SkipFlags) map[edge]bool {
	m := map[edge]bool{}
	for _, e := range bugAlways {
		m[e] = true
	}
	if skip.Triage {
		m[edge{domain.StageTodo, domain.StageDiagnose}] = true
	}
	if skip.Diagnose {
		m[edge{domain.StageTriage, domain.StageFix}] = true
	}
	if skip.Triage && skip.Diagnose {
		m[edge{domain.StageTodo, domain.StageFix}] = true
	}
	return m
}

func allSkipCombos() []domain.SkipFlags {
	return []domain.SkipFlags{
		{},
		{Brainstorm: true},
		{Plan: true},
		{Brainstorm: true, Plan: true},
		{Triage: true},
		{Diagnose: true},
		{Triage: true, Diagnose: true},
	}
}

func TestTransitionTableExhaustive(t *testing.T) {
	kinds := []struct {
		kind  domain.Kind
		legal func(domain.SkipFlags) map[edge]bool
	}{
		{domain.KindFeature, legalForFeature},
		{domain.KindBug, legalForBug},
	}
	for _, k := range kinds {
		for _, skip := range allSkipCombos() {
			legal := k.legal(skip)
			for _, from := range domain.Stages {
				for _, to := range domain.Stages {
					err := CanTransition(k.kind, from, to, skip)
					want := legal[edge{from, to}]
					if want && err != nil {
						t.Errorf("%s skip=%+v: %s → %s should be legal, got %v", k.kind, skip, from, to, err)
					}
					if !want && err == nil {
						t.Errorf("%s skip=%+v: %s → %s should be illegal, got nil", k.kind, skip, from, to)
					}
				}
			}
		}
	}
}

func TestReviewAndVerifyNeverSkippable(t *testing.T) {
	// In BOTH workflows, the only way into Verify is Review, the only way
	// into Done is Verify, and the only way out of the work stage
	// (implement/fix) is Review — no skip combo may jump the quality floor.
	for _, kind := range []domain.Kind{domain.KindFeature, domain.KindBug} {
		work := WorkStage(kind)
		for _, skip := range allSkipCombos() {
			for _, from := range domain.Stages {
				if from != domain.StageVerify {
					if err := CanTransition(kind, from, domain.StageDone, skip); err == nil {
						t.Errorf("%s skip=%+v: %s → done must be illegal", kind, skip, from)
					}
				}
				if from != domain.StageReview {
					if err := CanTransition(kind, from, domain.StageVerify, skip); err == nil {
						t.Errorf("%s skip=%+v: %s → verify must be illegal", kind, skip, from)
					}
				}
			}
			for _, to := range domain.Stages {
				if to != domain.StageReview {
					if err := CanTransition(kind, work, to, skip); err == nil {
						t.Errorf("%s skip=%+v: %s → %s must be illegal", kind, skip, work, to)
					}
				}
			}
		}
	}
}

func TestUnknownStages(t *testing.T) {
	if err := CanTransition(domain.KindFeature, "bogus", domain.StageSpec, domain.SkipFlags{}); err == nil {
		t.Error("unknown from-stage accepted")
	}
	if err := CanTransition(domain.KindFeature, domain.StageTodo, "bogus", domain.SkipFlags{}); err == nil {
		t.Error("unknown to-stage accepted")
	}
}

func TestNextFeature(t *testing.T) {
	got := Next(domain.KindFeature, domain.StageTodo, domain.SkipFlags{})
	if len(got) != 1 || got[0] != domain.StageBrainstorm {
		t.Errorf("Next(todo, none) = %v, want [brainstorm]", got)
	}
	got = Next(domain.KindFeature, domain.StageTodo, domain.SkipFlags{Brainstorm: true})
	if len(got) != 2 || got[0] != domain.StageBrainstorm || got[1] != domain.StageSpec {
		t.Errorf("Next(todo, skip-brainstorm) = %v, want [brainstorm spec]", got)
	}
	got = Next(domain.KindFeature, domain.StageReview, domain.SkipFlags{})
	if len(got) != 2 || got[0] != domain.StageVerify || got[1] != domain.StageImplement {
		t.Errorf("Next(review) = %v, want [verify implement]", got)
	}
}

func TestNextBug(t *testing.T) {
	got := Next(domain.KindBug, domain.StageTodo, domain.SkipFlags{})
	if len(got) != 1 || got[0] != domain.StageTriage {
		t.Errorf("Next(bug todo) = %v, want [triage]", got)
	}
	// both skips open triage, plus the combined todo→fix bypass.
	got = Next(domain.KindBug, domain.StageTodo, domain.SkipFlags{Triage: true, Diagnose: true})
	if len(got) != 3 || got[0] != domain.StageTriage || got[1] != domain.StageDiagnose || got[2] != domain.StageFix {
		t.Errorf("Next(bug todo, skip both) = %v, want [triage diagnose fix]", got)
	}
	got = Next(domain.KindBug, domain.StageReview, domain.SkipFlags{})
	if len(got) != 2 || got[0] != domain.StageVerify || got[1] != domain.StageFix {
		t.Errorf("Next(bug review) = %v, want [verify fix]", got)
	}
}

func TestInitialTerminalAndWorkStage(t *testing.T) {
	for _, kind := range []domain.Kind{domain.KindFeature, domain.KindBug} {
		if Initial(kind) != domain.StageTodo {
			t.Errorf("Initial(%s) = %s, want todo", kind, Initial(kind))
		}
		if !Terminal(kind, domain.StageDone) {
			t.Errorf("Terminal(%s, done) should be true", kind)
		}
		if Terminal(kind, domain.StageTodo) {
			t.Errorf("Terminal(%s, todo) should be false", kind)
		}
	}
	if WorkStage(domain.KindFeature) != domain.StageImplement {
		t.Error("feature work stage should be implement")
	}
	if WorkStage(domain.KindBug) != domain.StageFix {
		t.Error("bug work stage should be fix")
	}
}
