package workflow

import (
	"testing"

	"github.com/morphia/gummi/internal/domain"
)

// legal is the full expected transition table, keyed by skip-flag
// combination. Everything not listed is illegal — the test iterates
// every (from, to, flags) triple, so the table is exhaustive.
type edge struct{ from, to domain.Stage }

var alwaysLegal = []edge{
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

var skipBrainstormOnly = []edge{
	{domain.StageTodo, domain.StageSpec},
}

var skipPlanOnly = []edge{
	{domain.StageSpec, domain.StageImplement},
}

func legalFor(skip domain.SkipFlags) map[edge]bool {
	m := map[edge]bool{}
	for _, e := range alwaysLegal {
		m[e] = true
	}
	if skip.Brainstorm {
		for _, e := range skipBrainstormOnly {
			m[e] = true
		}
	}
	if skip.Plan {
		for _, e := range skipPlanOnly {
			m[e] = true
		}
	}
	return m
}

func allSkipCombos() []domain.SkipFlags {
	return []domain.SkipFlags{
		{},
		{Brainstorm: true},
		{Plan: true},
		{Brainstorm: true, Plan: true},
	}
}

func TestTransitionTableExhaustive(t *testing.T) {
	for _, skip := range allSkipCombos() {
		legal := legalFor(skip)
		for _, from := range domain.Stages {
			for _, to := range domain.Stages {
				err := CanTransition(from, to, skip)
				want := legal[edge{from, to}]
				if want && err != nil {
					t.Errorf("skip=%+v: %s → %s should be legal, got %v", skip, from, to, err)
				}
				if !want && err == nil {
					t.Errorf("skip=%+v: %s → %s should be illegal, got nil", skip, from, to)
				}
			}
		}
	}
}

func TestReviewAndVerifyNeverSkippable(t *testing.T) {
	// No skip-flag combination may open an edge that jumps over Review
	// or Verify: the only way into Verify is Review, the only way into
	// Done is Verify, and the only way out of Implement is Review.
	for _, skip := range allSkipCombos() {
		for _, from := range domain.Stages {
			if from != domain.StageVerify {
				if err := CanTransition(from, domain.StageDone, skip); err == nil {
					t.Errorf("skip=%+v: %s → done must be illegal", skip, from)
				}
			}
			if from != domain.StageReview {
				if err := CanTransition(from, domain.StageVerify, skip); err == nil {
					t.Errorf("skip=%+v: %s → verify must be illegal", skip, from)
				}
			}
		}
		for _, to := range domain.Stages {
			if to != domain.StageReview {
				if err := CanTransition(domain.StageImplement, to, skip); err == nil {
					t.Errorf("skip=%+v: implement → %s must be illegal", skip, to)
				}
			}
		}
	}
}

func TestUnknownStages(t *testing.T) {
	if err := CanTransition("bogus", domain.StageSpec, domain.SkipFlags{}); err == nil {
		t.Error("unknown from-stage accepted")
	}
	if err := CanTransition(domain.StageTodo, "bogus", domain.SkipFlags{}); err == nil {
		t.Error("unknown to-stage accepted")
	}
}

func TestNext(t *testing.T) {
	got := Next(domain.StageTodo, domain.SkipFlags{})
	if len(got) != 1 || got[0] != domain.StageBrainstorm {
		t.Errorf("Next(todo, none) = %v, want [brainstorm]", got)
	}
	got = Next(domain.StageTodo, domain.SkipFlags{Brainstorm: true})
	if len(got) != 2 || got[0] != domain.StageBrainstorm || got[1] != domain.StageSpec {
		t.Errorf("Next(todo, skip-brainstorm) = %v, want [brainstorm spec]", got)
	}
	got = Next(domain.StageReview, domain.SkipFlags{})
	if len(got) != 2 || got[0] != domain.StageVerify || got[1] != domain.StageImplement {
		t.Errorf("Next(review) = %v, want [verify implement]", got)
	}
	if got := Next(domain.StageDone, domain.SkipFlags{Brainstorm: true, Plan: true}); len(got) != 0 {
		t.Errorf("Next(done) = %v, want empty", got)
	}
}

func TestInitialAndTerminal(t *testing.T) {
	if Initial() != domain.StageTodo {
		t.Errorf("Initial() = %s, want todo", Initial())
	}
	for _, s := range domain.Stages {
		want := s == domain.StageDone
		if got := Terminal(s); got != want {
			t.Errorf("Terminal(%s) = %v, want %v", s, got, want)
		}
	}
}
