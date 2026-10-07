package engine

import (
	"slices"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
)

// routeSpec is a feature spec with one open human comment under each of
// the sections named, in template order.
func routeSpec(sections ...string) spec.Doc {
	var b strings.Builder
	b.WriteString("# Dark mode\n%% @user: a preamble note\n\n")
	for _, sec := range []string{"Problem", "Chosen approach", "Implementation notes", "Progress", "Review", "Verification plan"} {
		b.WriteString("## " + sec + "\n\nSome text.\n")
		if slices.Contains(sections, sec) {
			b.WriteString("%% @user(2026-09-29): about " + sec + "\n")
		}
		b.WriteString("\n")
	}
	return spec.Parse(b.String())
}

// A comment goes to the earliest stage that owns one of the open comments,
// over the graph's own rerun edges — and a note nobody owns stays where
// the card is, which is all request changes ever did before.
func TestRouteComments(t *testing.T) {
	feature := domain.CardType{Kind: domain.KindFeature}
	cases := []struct {
		name     string
		stage    domain.Stage
		sections []string
		diff     int
		target   domain.Stage
		path     []domain.Stage
		why      string
	}{
		{
			"design note at verify", domain.StageVerify,
			[]string{"Chosen approach"},
			0,
			domain.StagePlan,
			[]domain.Stage{domain.StageImplement, domain.StagePlan},
			"the comments on Chosen approach",
		},
		{
			"design note at implement", domain.StageImplement,
			[]string{"Problem", "Progress"},
			0,
			domain.StagePlan,
			[]domain.Stage{domain.StagePlan},
			"the comments on Problem",
		},
		{
			"progress note at verify", domain.StageVerify,
			[]string{"Progress"},
			0,
			domain.StageImplement,
			[]domain.Stage{domain.StageImplement},
			"the comments on Progress",
		},
		{
			"diff comments at verify", domain.StageVerify, nil, 2,
			domain.StageImplement,
			[]domain.Stage{domain.StageImplement},
			"the diff comments",
		},
		{
			"design note beats the diff", domain.StageVerify,
			[]string{"Implementation notes"},
			1,
			domain.StagePlan,
			[]domain.Stage{domain.StageImplement, domain.StagePlan},
			"the comments on Implementation notes",
		},
		{
			"verification note at verify stays", domain.StageVerify,
			[]string{"Verification plan", "Review"},
			0,
			domain.StageVerify, nil, "",
		},
		{
			"progress note at implement stays", domain.StageImplement,
			[]string{"Progress"},
			0,
			domain.StageImplement, nil, "",
		},
		{
			"a later stage's note at plan stays", domain.StagePlan,
			[]string{"Progress", "Chosen approach"},
			0,
			domain.StagePlan, nil, "",
		},
		{
			"diff comments at implement stay", domain.StageImplement, nil, 1,
			domain.StageImplement, nil, "",
		},
		{
			"done has no edge back", domain.StageDone,
			[]string{"Chosen approach"},
			0,
			domain.StageDone, nil, "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := RouteComments(feature, c.stage, routeSpec(c.sections...), c.diff)
			if r.Target != c.target || !slices.Equal(r.Path, c.path) || r.Why() != c.why {
				t.Errorf("route = %s via %v (%q), want %s via %v (%q)", r.Target, r.Path, r.Why(), c.target, c.path, c.why)
			}
			if r.Rewinds() != (len(c.path) > 0) {
				t.Errorf("Rewinds = %v with path %v", r.Rewinds(), r.Path)
			}
		})
	}
}

// Each kind's sections are owned by the stage its contract has write them;
// a heading the table does not know is nobody's.
func TestSectionOwner(t *testing.T) {
	cases := []struct {
		ct      domain.CardType
		section string
		want    domain.Stage
		ok      bool
	}{
		{domain.CardType{Kind: domain.KindFeature}, "Chosen approach", domain.StagePlan, true},
		{domain.CardType{Kind: domain.KindFeature}, "  progress ", domain.StageImplement, true},
		{domain.CardType{Kind: domain.KindFeature}, "Verification plan", "", false},
		{domain.CardType{Kind: domain.KindFeature}, "Review", "", false},
		{domain.CardType{Kind: domain.KindBug}, "Root cause", domain.StagePlan, true},
		{domain.CardType{Kind: domain.KindBug}, "Fix", domain.StageImplement, true},
		{domain.CardType{Kind: domain.KindResearch}, "Direction", domain.StagePlan, true},
		{domain.CardType{Kind: domain.KindResearch}, "Findings", domain.StageImplement, true},
		{domain.CardType{Kind: domain.KindResearch, Mode: domain.ModeDiagnosis}, spec.DiagSectionRepro, domain.StagePlan, true},
		{domain.CardType{Kind: domain.KindResearch, Mode: domain.ModeDiagnosis}, spec.DiagSectionEvidence, domain.StageImplement, true},
		{domain.CardType{Kind: domain.KindGoal}, "Objective", "", false},
		{domain.CardType{Kind: domain.KindFeature}, "", "", false},
	}
	for _, c := range cases {
		got, ok := SectionOwner(c.ct, c.section)
		if got != c.want || ok != c.ok {
			t.Errorf("SectionOwner(%v, %q) = %q, %v; want %q, %v", c.ct, c.section, got, ok, c.want, c.ok)
		}
	}
}

// A stage's gate is held by every open comment but a later stage's; its
// writer is handed only its own and nobody's. Nothing reaches done with a
// comment open.
func TestSpecCommentsHoldingAndAnswered(t *testing.T) {
	feature := domain.CardType{Kind: domain.KindFeature}
	doc := routeSpec("Chosen approach", "Progress", "Verification plan")
	texts := func(ts []spec.Thread) []string {
		var out []string
		for _, th := range ts {
			out = append(out, strings.TrimPrefix(spec.UnresolvedUserMarker(th).Text, "about "))
		}
		return out
	}
	// the preamble note is nobody's, and so everybody's
	cases := []struct {
		stage             domain.Stage
		holding, answered []string
	}{
		{domain.StagePlan, []string{"a preamble note", "Chosen approach", "Verification plan"}, []string{"a preamble note", "Chosen approach", "Verification plan"}},
		{domain.StageImplement, []string{"a preamble note", "Chosen approach", "Progress", "Verification plan"}, []string{"a preamble note", "Progress", "Verification plan"}},
		{domain.StageVerify, []string{"a preamble note", "Chosen approach", "Progress", "Verification plan"}, []string{"a preamble note", "Verification plan"}},
	}
	for _, c := range cases {
		if got := texts(SpecCommentsHolding(feature, c.stage, doc)); !slices.Equal(got, c.holding) {
			t.Errorf("%s holding = %q, want %q", c.stage, got, c.holding)
		}
		if got := texts(SpecCommentsAnswered(feature, c.stage, doc)); !slices.Equal(got, c.answered) {
			t.Errorf("%s answered = %q, want %q", c.stage, got, c.answered)
		}
	}
	for st, want := range map[domain.Stage]bool{
		domain.StageTodo: false, domain.StagePlan: false, domain.StageImplement: true,
		domain.StageVerify: true, domain.StageDone: true, domain.StageOpen: true,
	} {
		if got := DiffCommentsHold(st); got != want {
			t.Errorf("DiffCommentsHold(%s) = %v, want %v", st, got, want)
		}
	}
}

// The confirmation names where the card goes, why, and what runs again.
func TestCommentRouteQuestion(t *testing.T) {
	r := RouteComments(domain.CardType{Kind: domain.KindFeature}, domain.StageVerify, routeSpec("Chosen approach"), 0)
	want := "send FD-001 back to plan? The comments on Chosen approach are plan's to answer — implement and verify run again after it"
	if got := r.Question("FD-001"); got != want {
		t.Errorf("question =\n%s\nwant\n%s", got, want)
	}
	if q := RouteComments(domain.CardType{Kind: domain.KindFeature}, domain.StageVerify, routeSpec("Review"), 0).Question("FD-001"); q != "" {
		t.Errorf("an in-place route asked %q", q)
	}
}

// The turn handed to a writer lists only what it answers, and asks it to
// resolve in its own name.
func TestCompileSpecCommentsForTheStage(t *testing.T) {
	doc := routeSpec("Chosen approach", "Progress")
	f := domain.Feature{Kind: domain.KindFeature, Stage: domain.StageImplement}
	turn := CompileSpecComments(f, doc)
	if strings.Contains(turn, "about Chosen approach") || !strings.Contains(turn, "about Progress") {
		t.Errorf("implement's turn carries the wrong comments:\n%s", turn)
	}
	if !strings.Contains(turn, "@implementer: resolved") || strings.Contains(turn, "@architect") {
		t.Errorf("implement's turn resolves in the wrong name:\n%s", turn)
	}
	f.Stage = domain.StagePlan
	if turn := CompileSpecComments(f, doc); !strings.Contains(turn, "about Chosen approach") || strings.Contains(turn, "about Progress") ||
		!strings.Contains(turn, "@architect: resolved") {
		t.Errorf("plan's turn:\n%s", turn)
	}
}
