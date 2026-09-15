package domain

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/exp/golden"
)

func TestNewResearchID(t *testing.T) {
	cases := []struct {
		n    int
		want FeatureID
		err  bool
	}{
		{7, "RS-007", false},
		{1000, "RS-1000", false},
		{0, "", true},
	}
	for _, c := range cases {
		got, err := NewID(KindResearch, c.n)
		if (err != nil) != c.err {
			t.Errorf("NewID(research, %d) error = %v, want err=%v", c.n, err, c.err)
			continue
		}
		if got != c.want {
			t.Errorf("NewID(research, %d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func researchFeature() Feature {
	now := time.Now()
	return Feature{
		ID: "RS-007", Num: 7, Kind: KindResearch, Title: "Widget perf",
		OneLiner: "study", Slug: "my-topic", Stage: StagePlan,
		Profile: "thrifty", CreatedAt: now, UpdatedAt: now,
	}
}

func TestResearchArtifactPath(t *testing.T) {
	f := researchFeature()
	if got := f.ArtifactPath(); got != ".gummi/research/RS-007-my-topic.md" {
		t.Errorf("research ArtifactPath = %q", got)
	}
	if got := f.BranchName(); got != "gummi/RS-007-my-topic" {
		t.Errorf("research BranchName = %q", got)
	}
}

func TestResearchValidate(t *testing.T) {
	f := researchFeature()
	if err := f.Validate(); err != nil {
		t.Fatalf("valid research card rejected: %v", err)
	}
	mutations := map[string]func(*Feature){
		"unknown kind":    func(f *Feature) { f.Kind = "shipping" },
		"id/num mismatch": func(f *Feature) { f.Num = 99 },
		"empty title":     func(f *Feature) { f.Title = "  " },
		"bad slug":        func(f *Feature) { f.Slug = "../evil" },
		"invalid stage":   func(f *Feature) { f.Stage = "shipping" },
		"neg envelope":    func(f *Feature) { f.Budget.Envelope = -1 },
	}
	for name, mut := range mutations {
		g := researchFeature()
		mut(&g)
		if err := g.Validate(); err == nil {
			t.Errorf("%s: Validate() = nil, want error", name)
		}
	}
}

// TestResearchIDDistinct locks the shared monotonic counter: RS ids draw
// the same number space as FD/BG, so RS-NNN never collides with FD-NNN or
// BG-NNN for the same n.
func TestResearchIDDistinct(t *testing.T) {
	for _, n := range []int{1, 7, 42} {
		r, err1 := NewID(KindResearch, n)
		f, err2 := NewID(KindFeature, n)
		b, err3 := NewID(KindBug, n)
		if err1 != nil || err2 != nil || err3 != nil {
			t.Fatalf("NewID(%d) errored: %v %v %v", n, err1, err2, err3)
		}
		ids := []FeatureID{r, f, b}
		for i := 0; i < len(ids); i++ {
			for j := i + 1; j < len(ids); j++ {
				if ids[i] == ids[j] {
					t.Errorf("ids %q and %q collide for n=%d", ids[i], ids[j], n)
				}
			}
		}
	}
}

// renderResearchSurface renders a research card's domain surface so the
// new kind, prefix, artifact home, stage set, and superstate are reviewable
// as a golden.
func renderResearchSurface(f Feature) string {
	var b strings.Builder
	fmt.Fprintf(&b, "kind: %s\n", f.Kind)
	fmt.Fprintf(&b, "id: %s\n", f.ID)
	fmt.Fprintf(&b, "prefix: %s\n", f.ID.Kind().prefix())
	fmt.Fprintf(&b, "artifact: %s\n", f.ArtifactPath())
	fmt.Fprintf(&b, "stages: %s %s\n", StagePlan, StagePlan)
	fmt.Fprintf(&b, "superstate: %s\n", f.Stage.SuperState())
	return b.String()
}

func TestResearchKindSurface(t *testing.T) {
	f := researchFeature()
	golden.RequireEqual(t, []byte(renderResearchSurface(f)))
}

// TestResearchSeedShape locks the seed surface a creation form (FD-083)
// drives: exactly the two request-owned sections — Brief and Questions —
// and nothing the investigate/shape stages are supposed to own.
func TestResearchSeedShape(t *testing.T) {
	tp := reflect.TypeOf(ResearchSeed{})
	var fields []string
	for i := 0; i < tp.NumField(); i++ {
		fields = append(fields, tp.Field(i).Name)
	}
	want := []string{"Brief", "Questions"}
	if len(fields) != len(want) || !reflect.DeepEqual(fields, want) {
		t.Errorf("ResearchSeed fields = %v, want %v", fields, want)
	}
	got := ResearchSeed{Brief: "ask", Questions: []string{"why?"}}
	if got.Brief != "ask" || got.Questions[0] != "why?" {
		t.Errorf("ResearchSeed field round-trip failed: %+v", got)
	}
}

// TestParseCardType: the one name that is not a Kind resolves, every
// kind's own word resolves, and nothing else does.
func TestParseCardType(t *testing.T) {
	for name, want := range map[string]CardType{
		"feature":   {Kind: KindFeature},
		"bug":       {Kind: KindBug},
		"research":  {Kind: KindResearch},
		"diagnosis": {Kind: KindResearch, Mode: ModeDiagnosis},
		"goal":      {Kind: KindGoal},
		"  BUG  ":   {Kind: KindBug},
	} {
		got, ok := ParseCardType(name)
		if !ok || got != want {
			t.Errorf("ParseCardType(%q) = %+v, %v; want %+v", name, got, ok, want)
		}
	}
	for _, bad := range []string{"", "chore", "survey", "dx", "RS"} {
		if _, ok := ParseCardType(bad); ok {
			t.Errorf("ParseCardType(%q) should not resolve", bad)
		}
	}
}

// TestCardTypeValidIsMembership: a mode on a kind that has none is
// invalid even though both halves are — the combination is what a
// creation surface can produce by presetting one row and moving another.
func TestCardTypeValidIsMembership(t *testing.T) {
	if !(CardType{Kind: KindResearch, Mode: ModeDiagnosis}).Valid() {
		t.Error("diagnosis should be an offered type")
	}
	if (CardType{Kind: KindBug, Mode: ModeDiagnosis}).Valid() {
		t.Error("a mode on a bug names no offered type")
	}
	if (CardType{Kind: "chore"}).Valid() {
		t.Error("an unknown kind names no offered type")
	}
}

// TestCardTypeNamingAndPrefix: the two research modes are two names and
// one prefix, which is the whole shape of the decision.
func TestCardTypeNamingAndPrefix(t *testing.T) {
	survey := CardType{Kind: KindResearch}
	diag := CardType{Kind: KindResearch, Mode: ModeDiagnosis}
	if survey.Name() != "research" || diag.Name() != "diagnosis" {
		t.Errorf("names = %q / %q", survey.Name(), diag.Name())
	}
	if survey.Prefix() != "RS" || diag.Prefix() != "RS" {
		t.Errorf("prefixes = %q / %q, want RS for both", survey.Prefix(), diag.Prefix())
	}
}

// TestModeOnlyOnResearch: Validate refuses a mode stored on a kind that
// has no second contract, so no reader has to know to ignore one.
func TestModeOnlyOnResearch(t *testing.T) {
	diag := &Feature{
		ID: "RS-004", Num: 4, Kind: KindResearch, Mode: ModeDiagnosis,
		Title: "why", Slug: "why", Stage: StageTodo,
	}
	if err := diag.Validate(); err != nil {
		t.Fatalf("a diagnosis card should validate: %v", err)
	}
	if !diag.IsDiagnosis() {
		t.Error("IsDiagnosis should be true for a research card in diagnosis mode")
	}
	bug := &Feature{
		ID: "BG-004", Num: 4, Kind: KindBug, Mode: ModeDiagnosis,
		Title: "broken", Slug: "broken", Stage: StageTodo,
	}
	if err := bug.Validate(); err == nil {
		t.Error("a mode on a bug card should be refused")
	}
	bogus := &Feature{
		ID: "RS-005", Num: 5, Kind: KindResearch, Mode: "survey",
		Title: "why", Slug: "why", Stage: StageTodo,
	}
	if err := bogus.Validate(); err == nil {
		t.Error("an unknown research mode should be refused")
	}
}
